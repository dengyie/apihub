package loadbalancer

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// usePolicy 装载测试策略，返回还原函数。
func usePolicy(t *testing.T, p *Policy) {
	t.Helper()
	old := currentPolicy.Load()
	currentPolicy.Store(p)
	t.Cleanup(func() { currentPolicy.Store(old) })
}

func limitErr(msg string) *types.NewAPIError {
	return types.NewError(errors.New(msg), types.ErrorCodeBadResponseBody)
}

// newEscalationPolicy 构造一个递增退避行为确定的策略：基础冷却 10 秒、封顶 3 倍。
func newEscalationPolicy() *Policy {
	return &Policy{
		Enabled:    true,
		MaxRetries: 2,
		Default: ChannelPolicy{
			MaxInflight:   50,
			TTFTTimeoutMs: 15000,
			Breaker: BreakerPolicy{
				FailureThreshold:         1,
				CooldownSeconds:          10,
				RateLimitCooldownSeconds: 30,
				EscalationCap:            3,
				HalfOpenProbes:           1,
			},
		},
		Channels: make(map[int]ChannelPolicy),
	}
}

func tripCountOf(t *testing.T, tracker *Tracker, id int) int {
	t.Helper()
	return int(statsFor(tracker, id).tripCount.Load())
}

func blockedRemaining(t *testing.T, tracker *Tracker, id int) time.Duration {
	t.Helper()
	until := statsFor(tracker, id).blockedUntil.Load()
	if until <= 0 {
		return 0
	}
	return time.Until(time.Unix(until, 0))
}

// cooldownRemaining 计算递增退避下这一轮熔断的剩余冷却。
// 倍数从 tripCount 现算，与 IsAvailable 的到期判定口径保持一致。
func cooldownRemaining(t *testing.T, tracker *Tracker, id int) time.Duration {
	t.Helper()
	s := statsFor(tracker, id)
	if breakerState(s.state.Load()) != breakerOpen {
		return 0
	}
	breaker := GetPolicy().Resolve(id).Breaker
	deadline := s.openedAt.Load() + breaker.CooldownSeconds*s.escalationMultiplier(breaker)
	return time.Until(time.Unix(deadline, 0))
}

// TestTripBreakerEscalatesCooldown 验证递增退避：连续熔断的冷却按 1x/2x/3x 增长并在封顶处截断。
func TestTripBreakerEscalatesCooldown(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9001

	want := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, expect := range want {
		tracker.TripBreaker(id, testModel)
		got := cooldownRemaining(t, tracker, id)
		// 允许 2 秒调度抖动
		if delta := got - expect; delta > 2*time.Second || delta < -2*time.Second {
			t.Fatalf("第 %d 次熔断冷却 = %v，期望约 %v", i+1, got, expect)
		}
		if tc := tripCountOf(t, tracker, id); tc != i+1 {
			t.Fatalf("第 %d 次熔断后 tripCount = %d，期望 %d", i+1, tc, i+1)
		}
	}
}

// TestTripBreakerEscalationResetsOnSuccess 验证「成功一次即救活」：成功后计数清零，冷却回到 1 倍。
func TestTripBreakerEscalationResetsOnSuccess(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9002

	tracker.TripBreaker(id, testModel)
	tracker.TripBreaker(id, testModel)
	if tc := tripCountOf(t, tracker, id); tc != 2 {
		t.Fatalf("连续熔断 2 次后 tripCount = %d，期望 2", tc)
	}

	// 一次成功请求
	handle := &RequestHandle{tracker: tracker, stats: statsFor(tracker, id),
		inflight: tracker.getInflight(id), channelID: id}
	handle.End(false, false)

	if tc := tripCountOf(t, tracker, id); tc != 0 {
		t.Fatalf("成功后 tripCount = %d，期望清零为 0", tc)
	}
	if state := breakerState(statsFor(tracker, id).state.Load()); state != breakerClosed {
		t.Fatalf("成功后熔断状态 = %v，期望 closed", state)
	}

	// 再次熔断应回到 1 倍
	tracker.TripBreaker(id, testModel)
	if got := cooldownRemaining(t, tracker, id); got > 12*time.Second {
		t.Fatalf("救活后再次熔断冷却 = %v，期望回到约 10s（1 倍）", got)
	}
}

// TestBreakerExemptChannelNeverTrips 验证豁免渠道既不熔断也不被自动禁用判据拦下。
func TestBreakerExemptChannelNeverTrips(t *testing.T) {
	policy := newEscalationPolicy()
	policy.Channels[9003] = ChannelPolicy{BreakerExempt: true}
	usePolicy(t, policy)

	tracker := newTestTracker()
	const id = 9003

	for i := 0; i < 5; i++ {
		tracker.TripBreaker(id, testModel)
	}
	tracker.RecordEmptyStream(id, testModel)
	tracker.RecordEmptyStream(id, testModel)
	tracker.RecordEmptyStream(id, testModel)

	if state := breakerState(statsFor(tracker, id).state.Load()); state != breakerClosed {
		t.Fatalf("豁免渠道熔断状态 = %v，期望始终 closed", state)
	}
	if tc := tripCountOf(t, tracker, id); tc != 0 {
		t.Fatalf("豁免渠道 tripCount = %d，期望 0", tc)
	}
	if ok, reason := tracker.IsAvailable(id, testModel); !ok {
		t.Fatalf("豁免渠道 IsAvailable = false（%s），期望恒可用", reason)
	}
	if !IsBreakerExempt(id) {
		t.Fatal("IsBreakerExempt = false，期望 true")
	}
}

// TestBreakerExemptStillRespectsMaxInflight 豁免只针对熔断，不应顺带取消并发上限。
func TestBreakerExemptStillRespectsMaxInflight(t *testing.T) {
	policy := newEscalationPolicy()
	policy.Channels[9004] = ChannelPolicy{BreakerExempt: true, MaxInflight: 1}
	usePolicy(t, policy)

	tracker := newTestTracker()
	const id = 9004

	if ok, _ := tracker.IsAvailable(id, testModel); !ok {
		t.Fatal("首次检查应可用")
	}
	tracker.getInflight(id).Store(1)
	ok, reason := tracker.IsAvailable(id, testModel)
	if ok || reason != ReasonOverloaded {
		t.Fatalf("并发打满后 IsAvailable = %v（%s），期望 false/%s", ok, reason, ReasonOverloaded)
	}
}

// TestRateLimitCooldownDoesNotEscalate 限流短避让是独立机制，不应被递增退避放大。
func TestRateLimitCooldownDoesNotEscalate(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9005

	tracker.TripBreakerForRateLimit(id, testModel)
	first := blockedRemaining(t, tracker, id)
	if first > 31*time.Second || first < 29*time.Second {
		t.Fatalf("限流避让 = %v，期望约 30s", first)
	}
	if tc := tripCountOf(t, tracker, id); tc != 0 {
		t.Fatalf("限流避让后 tripCount = %d，期望不计入递增（0）", tc)
	}
}

// TestParseAndClampMaxCompletionTokens 覆盖上游 400 自述上限的解析、学习与出站钳制。
func TestParseAndClampMaxCompletionTokens(t *testing.T) {
	_, ok := ParseMaxCompletionTokensLimit(limitErr("max_completion_tokens is too large: 384000. This model supports at most 262144 completion tokens."))
	if !ok {
		t.Fatal("应能从上游报文解析出上限")
	}
	if _, ok := ParseMaxCompletionTokensLimit(limitErr("some unrelated upstream failure")); ok {
		t.Fatal("无关错误不应被误判为 token 上限")
	}

	const id = 9100
	const model = "test-model"
	if got := ClampMaxCompletionTokens(id, model, 384000); got != 384000 {
		t.Fatalf("未学到上限时不应钳制，got = %d", got)
	}

	RecordMaxCompletionTokensLimit(id, model, 262144)
	if got := ClampMaxCompletionTokens(id, model, 384000); got != 262144 {
		t.Fatalf("超限请求应被钳到 262144，got = %d", got)
	}
	if got := ClampMaxCompletionTokens(id, model, 1000); got != 1000 {
		t.Fatalf("未超限请求不应被改动，got = %d", got)
	}
	// 只收紧不放松：更大的上限不得覆盖已学到的更小值
	RecordMaxCompletionTokensLimit(id, model, 999999)
	if got := ClampMaxCompletionTokens(id, model, 384000); got != 262144 {
		t.Fatalf("上限只应收紧，got = %d", got)
	}
	// 其它模型不受影响
	if got := ClampMaxCompletionTokens(id, "other-model", 384000); got != 384000 {
		t.Fatalf("其它模型不应被钳制，got = %d", got)
	}
}
