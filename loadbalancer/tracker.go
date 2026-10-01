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
	// consecutiveEmptyStreams 连续空流计数。达到 empty_stream_trip_threshold
	// 后熔断该渠道（不自动禁用）；一次非空流成功清零。
	consecutiveEmptyStreams atomic.Int32
	// tripCount 连续熔断次数。用于冷却递增退避：第 n 次熔断的冷却为
	// 基础冷却 × min(n, escalation_cap)。一次成功请求清零。
	tripCount atomic.Int32
	// halfOpenProbes 半开状态已放行的探测数
	halfOpenProbes atomic.Int32
	// halfOpenSince 本轮半开的起始时刻（Unix 纳秒），0=未进入过半开。
	// 用于探测配额租约回收，见 halfOpenProbeLease。
	halfOpenSince atomic.Int64
	// ttftSamples 首字时间滑动窗口（毫秒），只保留最近 N 个
	mu          sync.Mutex
	ttftSamples []int64
}

const maxTTFTSamples = 100

// halfOpenProbeLease 是半开探测配额的租约时长。
//
// IsAvailable 名为「检查」，却在半开状态下以副作用方式预占一个探测配额；
// 该配额只有真正发出请求并走到 RequestHandle.End 才会归还。现实中存在大量
// 「检查通过但请求从未发出」的路径：客户端在选路后立刻断开、计费准备失败、
// Responses WebSocket 中继根本不调用 End。没有租约时，一次泄漏就会让渠道
// 永久停在半开耗尽状态——熔断器再也回不到 closed，该渠道彻底死掉且无任何
// 日志。租约让「没有结论的半开轮次」在有限时间后自动作废重来。
//
// 取值与熔断冷却同量级：远大于任何一次真实探测的时长（首字超时默认 5s），
// 不会误伤正常探测；又足够短，泄漏后渠道能在一分钟内自愈。
const halfOpenProbeLease = 60 * time.Second

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
// 豁免渠道（breaker_exempt）直接返回，兜底链路不做定时熔断。
func (t *Tracker) TripBreakerUntil(channelID int, until time.Time) {
	if !Enabled() || channelID <= 0 {
		return
	}
	if IsBreakerExempt(channelID) {
		return
	}
	s := t.getOrCreate(channelID)
	s.state.Store(int32(breakerOpen))
	s.openedAt.Store(time.Now().Unix())
	s.blockedUntil.Store(until.Unix())
	s.halfOpenProbes.Store(0)
	s.halfOpenSince.Store(0)
}

// RecordEmptyStream 记一次该渠道的空流。连续达到阈值则熔断（只熔断不禁用）。
// 阈值 ≤0 时关闭。成功一次非空流应调用 ClearEmptyStream。
func (t *Tracker) RecordEmptyStream(channelID int) {
	if !Enabled() || channelID <= 0 {
		return
	}
	if IsBreakerExempt(channelID) {
		return
	}
	threshold := GetEmptyStreamTripThreshold()
	if threshold <= 0 {
		return
	}
	s := t.getOrCreate(channelID)
	n := s.consecutiveEmptyStreams.Add(1)
	if int(n) >= threshold {
		t.TripBreaker(channelID)
		s.consecutiveEmptyStreams.Store(0)
	}
}

// ClearEmptyStream 渠道一次非空流成功后清零连续空流计数。
func (t *Tracker) ClearEmptyStream(channelID int) {
	if channelID <= 0 {
		return
	}
	t.getOrCreate(channelID).consecutiveEmptyStreams.Store(0)
}

// EmptyStreamStreak 返回渠道当前连续空流次数（测试用）。
func (t *Tracker) EmptyStreamStreak(channelID int) int {
	if channelID <= 0 {
		return 0
	}
	return int(t.getOrCreate(channelID).consecutiveEmptyStreams.Load())
}

// TripBreaker 立即熔断渠道（开启常规冷却周期）。
// 适用于明确的确定性或严重上游故障（如 410 EOL）。
// 冷却时长按连续熔断次数递增，见 tripBreaker。
func (t *Tracker) TripBreaker(channelID int) {
	if !Enabled() || channelID <= 0 {
		return
	}
	if IsBreakerExempt(channelID) {
		return
	}
	t.getOrCreate(channelID).tripBreaker()
}

// TripBreakerForRateLimit 针对上游瞬时限流或并发超限（429、RPM 等）的短期避让熔断。
// 使用配置的 RateLimitCooldownSeconds（默认 30 秒），
// 短暂避让后自动进入半开探测，防止常规长冷却（如 300 秒）导致全渠道假死。
func (t *Tracker) TripBreakerForRateLimit(channelID int) {
	if !Enabled() || channelID <= 0 {
		return
	}
	policy := GetPolicy().Resolve(channelID)
	cooldown := policy.Breaker.RateLimitCooldownSeconds
	if cooldown <= 0 {
		cooldown = 30
	}
	t.TripBreakerUntil(channelID, time.Now().Add(time.Duration(cooldown)*time.Second))
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
		// BreakerExempt 渠道只记失败数，不推进熔断状态机（IsAvailable 也跳过
		// 熔断判断，两端保持一致，避免留下永远读不到的死状态）。
		if !policy.BreakerExempt &&
			(currentState == breakerHalfOpen || (policy.Breaker.FailureThreshold > 0 && int(n) >= policy.Breaker.FailureThreshold)) {
			h.stats.tripBreaker()
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
		// 任何一次成功都证明渠道已恢复：递增退避计数清零，
		// 下次熔断重新从 1 倍基础冷却开始。
		h.stats.tripCount.Store(0)
		// 成功即救活：半开探测成功关闭熔断器；开熔断期间仍在途的请求成功
		// 同样说明渠道已恢复（旧逻辑只认半开，会让递增退避的渠道在冷却期内
		// 无人问津，只能靠时间自然过期）。
		//
		// 但定时熔断（限流短避让 / 宵禁）不在此列：它走 blockedUntil 分支，
		// 一旦把状态改成 closed，IsAvailable 就再也不读 blockedUntil，剩余的
		// 避让时长被整段作废。渠道正被限流时其它在途请求成功是常态，那样等于
		// 限流避让形同虚设，「避让 → 立刻重入 → 再 429」会一直抖。
		switch breakerState(h.stats.state.Load()) {
		case breakerHalfOpen, breakerOpen:
			if h.stats.blockedUntil.Load() > 0 {
				break
			}
			h.stats.state.Store(int32(breakerClosed))
			h.stats.halfOpenProbes.Store(0)
			h.stats.halfOpenSince.Store(0)
		}
	}
}

// tripBreaker 打开熔断器并开启一轮递增冷却。
//
// 递增退避：第 n 次连续熔断的冷却 = cooldown_seconds × min(n, escalation_cap)
// （第 1 次 1 倍、第 2 次 2 倍、第 3 次 3 倍……），一次成功请求即清零计数。
// 动机是生产上观察到的「同一个渠道被反复熔断、每次冷却结束又立刻被同一个
// 故障打回」：固定 300 秒既没有惩罚递增的复发，也没有让重试更快找到别处。
// 封顶（默认 6 倍）避免反复故障的渠道被冷却到数小时而彻底退出轮转。
//
// 冷却长度仍按 openedAt + cooldown_seconds × 倍数 计算（不落绝对到期时间），
// 这样定时熔断（宵禁，blockedUntil）的优先级语义和既有到期判定路径都不变。
func (s *ChannelStats) tripBreaker() {
	s.tripCount.Add(1)
	// 已经在定时熔断中就整体让路：blockedUntil 是绝对到期时间（宵禁到早 8 点、
	// 限流避让到 +30s），常规熔断的 openedAt 是相对冷却。把 blockedUntil 清零
	// 等于用一次失败把宵禁/避让整段抹掉——而 End 的失败分支不看 blockedUntil，
	// 宵禁期间累计到 failure_threshold 次在途失败就会走到这里，渠道于是在午夜
	// 重新进入轮转，正是宵禁要防的事。
	//
	// tripCount 照常递增：这次失败是真的，只是不该拿它改写已有的绝对到期时间。
	if until := s.blockedUntil.Load(); until > 0 && time.Now().Unix() < until {
		return
	}
	// 无论之前是 closed 还是 half-open，只要判定熔断，无条件重置为 open 开启新冷却
	s.state.Store(int32(breakerOpen))
	s.openedAt.Store(time.Now().Unix())
	s.blockedUntil.Store(0)
	s.halfOpenProbes.Store(0)
	s.halfOpenSince.Store(0)
}

// escalationMultiplier 本轮熔断的冷却倍数：第 n 次连续熔断为 n 倍，在封顶处截断。
func (s *ChannelStats) escalationMultiplier(breaker BreakerPolicy) int64 {
	mult := int64(s.tripCount.Load())
	if cap := breaker.EscalationCapOrDefault(); mult > cap {
		mult = cap
	}
	if mult < 1 {
		mult = 1
	}
	return mult
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
	// BreakerExempt 只豁免熔断状态机，不豁免并发上限：兜底渠道（CPA）本身
	// 仍可能被并发打满，此时继续放行只会把过载原样透传给兜底链路。
	reservedProbe := false
	if !policy.BreakerExempt {
		switch state := breakerState(s.state.Load()); state {
		case breakerOpen:
			// 定时熔断（如宵禁或限流短冷却）优先级高于常规冷却：到期前一律不放行
			if until := s.blockedUntil.Load(); until > 0 {
				if time.Now().Unix() < until {
					return false, ReasonCircuitBlockedUntil
				}
				// 到期：清除定时，直接进入半开状态允许探测
				s.blockedUntil.Store(0)
				if s.state.CompareAndSwap(int32(breakerOpen), int32(breakerHalfOpen)) {
					s.halfOpenProbes.Store(0)
					s.halfOpenSince.Store(time.Now().UnixNano())
				}
			} else {
				cooldown := policy.Breaker.CooldownSeconds
				if cooldown <= 0 {
					cooldown = 60
				}
				// 递增退避：连续第 n 次熔断的冷却 = 基础冷却 × min(n, escalation_cap)
				cooldown *= s.escalationMultiplier(policy.Breaker)
				if time.Now().Unix()-s.openedAt.Load() >= cooldown {
					// 进入半开状态
					if s.state.CompareAndSwap(int32(breakerOpen), int32(breakerHalfOpen)) {
						s.halfOpenProbes.Store(0)
						s.halfOpenSince.Store(time.Now().UnixNano())
					}
				} else {
					return false, ReasonCircuitOpen
				}
			}
			// 进入半开后继续走下面的半开逻辑
			fallthrough
		case breakerHalfOpen:
			maxProbes := policy.Breaker.HalfOpenProbes
			if maxProbes <= 0 {
				maxProbes = 1
			}
			s.reclaimExpiredProbes()
			if s.halfOpenProbes.Add(1) > int32(maxProbes) {
				s.halfOpenProbes.Add(-1)
				return false, ReasonHalfOpenProbesExceeded
			}
			reservedProbe = true
			// 允许这一个探测请求通过，结束时 End 会根据结果关闭或重新熔断
		}
	}

	// 并发上限检查
	if max := policy.MaxInflight; max > 0 && int(s.inflight.Load()) >= max {
		// 归还本次调用自己预占的探测配额。用局部标志而不是重读熔断状态：
		// 状态可能在两步之间被并发的 End 改成 open，那时重读会漏还或多还。
		if reservedProbe {
			s.halfOpenProbes.Add(-1)
		}
		return false, ReasonOverloaded
	}

	return true, ""
}

// reclaimExpiredProbes 回收「已预占但从未归还」的半开探测配额。
//
// 上一轮半开在租约内没有给出任何结论，说明放行的探测请求没有走到 End
// （客户端断开、计费准备失败、Responses WebSocket 中继根本不调用 End）。
// 与其让渠道永久停在耗尽状态，不如把这轮作废、重新放行探测。
func (s *ChannelStats) reclaimExpiredProbes() {
	if s.halfOpenProbes.Load() <= 0 {
		return
	}
	since := s.halfOpenSince.Load()
	if since <= 0 {
		return
	}
	if time.Since(time.Unix(0, since)) > halfOpenProbeLease {
		// 同时把租约时钟拨回现在：这一轮从此刻重新计时。否则时钟停留在过期值上，
		// 之后每一次 IsAvailable 都会再次触发回收，探测配额上限形同虚设——
		// 本该被限流的半开渠道会被无限放行。
		s.halfOpenProbes.Store(0)
		s.halfOpenSince.Store(time.Now().UnixNano())
	}
}
