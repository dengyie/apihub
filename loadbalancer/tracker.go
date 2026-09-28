package loadbalancer

import (
	"sync"
	"sync/atomic"
	"time"
)

// breakerState 熔断器状态
type breakerState int32

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// ChannelStats 单个渠道的实时状态。
// 所有字段通过原子操作或内部锁访问，支持高并发。
type ChannelStats struct {
	// inflight 当前进行中的请求数
	inflight atomic.Int32
	// consecutiveFailures 连续硬失败计数（达到阈值后硬熔断）
	consecutiveFailures atomic.Int32
	// state 熔断器状态
	state atomic.Int32
	// openedAt 熔断开启时间（Unix 秒）
	openedAt atomic.Int64
	// blockedUntil 定时熔断到期时间（Unix 秒），0=未设置。
	// 用于宵禁等需要熔断到指定时间点的场景，优先级高于常规冷却。
	blockedUntil atomic.Int64
	// degradedUntil 降级到期时间（Unix 秒），0=未设置。
	// 慢渠道的软降级：仍可用，但在选渠道时排最后。10 分钟后自动恢复。
	degradedUntil atomic.Int64
	// consecutiveSlowCount 连续慢请求计数（不含硬失败），用于触发降级
	consecutiveSlowCount atomic.Int32
	// halfOpenProbes 半开状态已放行的探测数
	halfOpenProbes atomic.Int32
	// ttftSamples 首字时间滑动窗口（毫秒），只保留最近 N 个
	mu          sync.Mutex
	ttftSamples []int64
}

const maxTTFTSamples = 100

// ContextKeyAttempt carries the inflight handle of the relay attempt in
// flight. The controller creates it at the start of every attempt so streams,
// non-stream replies and task submissions all count against max_inflight;
// StreamScannerHandler reuses it instead of opening a second one.
// RequestHandle.End is idempotent, so whichever layer reports first wins and
// the other is a no-op.
const ContextKeyAttempt = "loadbalancer_attempt"

// IsAvailable 拒绝渠道的原因，调用方按常量比较，避免裸字符串。
const (
	ReasonCircuitOpen            = "circuit_open"
	ReasonCircuitBlockedUntil    = "circuit_blocked_until"
	ReasonHalfOpenProbesExceeded = "circuit_half_open_probes_exhausted"
	ReasonOverloaded             = "overloaded"
)

// Tracker 跟踪所有渠道的实时状态
type Tracker struct {
	mu       sync.RWMutex
	channels map[int]*ChannelStats
}

var globalTracker = &Tracker{
	channels: make(map[int]*ChannelStats),
}

// GlobalTracker 返回全局跟踪器
func GlobalTracker() *Tracker {
	return globalTracker
}

func (t *Tracker) getOrCreate(channelID int) *ChannelStats {
	t.mu.RLock()
	s, ok := t.channels[channelID]
	t.mu.RUnlock()
	if ok {
		return s
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.channels[channelID]; ok {
		return s
	}
	s = &ChannelStats{}
	t.channels[channelID] = s
	return s
}

// TripBreakerUntil 定时熔断：将渠道熔断到指定时间点（如宵禁到早 8 点）。
// 立即打开熔断器，并设置 blockedUntil，到期前不参与选渠道。
func (t *Tracker) TripBreakerUntil(channelID int, until time.Time) {
	if !Enabled() || channelID <= 0 {
		return
	}
	s := t.getOrCreate(channelID)
	s.state.Store(int32(breakerOpen))
	s.openedAt.Store(time.Now().Unix())
	s.blockedUntil.Store(until.Unix())
	s.halfOpenProbes.Store(0)
}

// TripBreaker 立即熔断渠道（开启常规冷却周期）。
// 适用于明确的确定性或严重上游故障（如 410 EOL）。
func (t *Tracker) TripBreaker(channelID int) {
	if !Enabled() || channelID <= 0 {
		return
	}
	s := t.getOrCreate(channelID)
	s.state.Store(int32(breakerOpen))
	s.openedAt.Store(time.Now().Unix())
	s.halfOpenProbes.Store(0)
}

// IsDegraded 判断渠道是否处于降级状态（慢 3 次后的 10 分钟软降级）。
// 降级渠道仍可用，但在选渠道时排最后。
func (t *Tracker) IsDegraded(channelID int) bool {
	if !Enabled() || channelID <= 0 {
		return false
	}
	s := t.getOrCreate(channelID)
	if until := s.degradedUntil.Load(); until > 0 {
		if time.Now().Unix() < until {
			return true
		}
		// 到期自动清除
		s.degradedUntil.Store(0)
	}
	return false
}

// Begin 请求开始：inflight +1，返回一个用于 End 的句柄。
// 若 channelID <= 0（未确定渠道的占位调用），返回不污染统计的虚拟句柄。
func (t *Tracker) Begin(channelID int) *RequestHandle {
	if channelID <= 0 {
		return &RequestHandle{
			tracker:   t,
			stats:     &ChannelStats{},
			channelID: channelID,
			start:     time.Now(),
		}
	}
	s := t.getOrCreate(channelID)
	s.inflight.Add(1)
	return &RequestHandle{
		tracker:   t,
		stats:     s,
		channelID: channelID,
		start:     time.Now(),
	}
}

// Inflight 返回渠道当前进行中的请求数
func (t *Tracker) Inflight(channelID int) int {
	if channelID <= 0 {
		return 0
	}
	s := t.getOrCreate(channelID)
	return int(s.inflight.Load())
}

// AvgTTFT 返回渠道最近的平均首字时间（毫秒），无样本时返回 -1
func (t *Tracker) AvgTTFT(channelID int) int64 {
	if channelID <= 0 {
		return -1
	}
	s := t.getOrCreate(channelID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ttftSamples) == 0 {
		return -1
	}
	var sum int64
	for _, v := range s.ttftSamples {
		sum += v
	}
	return sum / int64(len(s.ttftSamples))
}

// RequestHandle 单次请求的跟踪句柄
type RequestHandle struct {
	tracker   *Tracker
	stats     *ChannelStats
	channelID int
	start     time.Time
	firstByte time.Time
	done      atomic.Bool
}

// MarkFirstByte 记录首字到达时间
func (h *RequestHandle) MarkFirstByte() {
	if h.firstByte.IsZero() {
		h.firstByte = time.Now()
	}
}

// TTFT 返回首字时间（毫秒），未收到首字时返回 -1
func (h *RequestHandle) TTFT() int64 {
	if h.firstByte.IsZero() {
		return -1
	}
	return h.firstByte.Sub(h.start).Milliseconds()
}

// End 请求结束：inflight -1，记录 TTFT 样本，更新熔断计数。
// slow 表示是否为慢请求（首字超时），failed 表示是否失败。
func (h *RequestHandle) End(slow, failed bool) {
	if h.done.Swap(true) {
		return
	}
	if h.channelID <= 0 {
		return
	}
	h.stats.inflight.Add(-1)

	policy := GetPolicy().Resolve(h.channelID)

	if ttft := h.TTFT(); ttft >= 0 {
		h.stats.mu.Lock()
		h.stats.ttftSamples = append(h.stats.ttftSamples, ttft)
		if len(h.stats.ttftSamples) > maxTTFTSamples {
			h.stats.ttftSamples = h.stats.ttftSamples[len(h.stats.ttftSamples)-maxTTFTSamples:]
		}
		h.stats.mu.Unlock()
	}

	if failed {
		// 硬失败：计入硬熔断计数器，达到阈值后熔断；
		// 半开探测失败时立即重新熔断，开启新冷却周期，避免卡在半开耗尽状态。
		n := h.stats.consecutiveFailures.Add(1)
		currentState := breakerState(h.stats.state.Load())
		if currentState == breakerHalfOpen || (policy.Breaker.FailureThreshold > 0 && int(n) >= policy.Breaker.FailureThreshold) {
			h.tripBreaker()
		}
		// 失败也重置慢计数（失败已硬处理，不再叠加软降级）
		h.stats.consecutiveSlowCount.Store(0)
	} else if slow {
		// 慢请求（非硬失败）：只软降级，不触发硬熔断。
		// 连续 3 次慢则降级 10 分钟，降级渠道仍可用（排最后），避免雪崩。
		if sn := h.stats.consecutiveSlowCount.Add(1); sn >= 3 {
			h.stats.degradedUntil.Store(time.Now().Add(10 * time.Minute).Unix())
			h.stats.consecutiveSlowCount.Store(0)
		}
	} else {
		h.stats.consecutiveFailures.Store(0)
		h.stats.consecutiveSlowCount.Store(0)
		// 半开探测成功，关闭熔断器并归还探测配额
		if breakerState(h.stats.state.Load()) == breakerHalfOpen {
			h.stats.state.Store(int32(breakerClosed))
			h.stats.halfOpenProbes.Store(0)
		}
	}
}

func (h *RequestHandle) tripBreaker() {
	// 无论之前是 closed 还是 half-open，只要判定熔断，无条件重置为 open 开启新冷却
	h.stats.state.Store(int32(breakerOpen))
	h.stats.openedAt.Store(time.Now().Unix())
	h.stats.halfOpenProbes.Store(0)
}

// IsAvailable 检查渠道当前是否可用（未过载、未熔断）
// 返回 false 时附带原因
func (t *Tracker) IsAvailable(channelID int) (bool, string) {
	if !Enabled() || channelID <= 0 {
		return true, ""
	}
	policy := GetPolicy().Resolve(channelID)
	s := t.getOrCreate(channelID)

	// 熔断器检查
	switch state := breakerState(s.state.Load()); state {
	case breakerOpen:
		// 定时熔断（如宵禁）：到期前一律不放行
		if until := s.blockedUntil.Load(); until > 0 {
			if time.Now().Unix() < until {
				return false, ReasonCircuitBlockedUntil
			}
			// 到期：清除定时，恢复常规冷却逻辑
			s.blockedUntil.Store(0)
		}
		cooldown := policy.Breaker.CooldownSeconds
		if cooldown <= 0 {
			cooldown = 60
		}
		if time.Now().Unix()-s.openedAt.Load() >= cooldown {
			// 进入半开状态
			if s.state.CompareAndSwap(int32(breakerOpen), int32(breakerHalfOpen)) {
				s.halfOpenProbes.Store(0)
			}
		} else {
			return false, ReasonCircuitOpen
		}
		// 进入半开后继续走下面的半开逻辑
		fallthrough
	case breakerHalfOpen:
		maxProbes := policy.Breaker.HalfOpenProbes
		if maxProbes <= 0 {
			maxProbes = 1
		}
		if s.halfOpenProbes.Add(1) > int32(maxProbes) {
			s.halfOpenProbes.Add(-1)
			return false, ReasonHalfOpenProbesExceeded
		}
		// 允许这一个探测请求通过，结束时 End 会根据结果关闭或重新熔断
	}

	// 并发上限检查
	if max := policy.MaxInflight; max > 0 && int(s.inflight.Load()) >= max {
		// 半开探测已占用的名额要归还
		if breakerState(s.state.Load()) == breakerHalfOpen {
			s.halfOpenProbes.Add(-1)
		}
		return false, ReasonOverloaded
	}

	return true, ""
}
