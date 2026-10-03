package loadbalancer

import (
	"testing"
	"time"
)

// TestInFlightSuccessDoesNotCancelTimedBlock 锁住 P2 的根因修复。
//
// 定时熔断（限流短避让 / 宵禁）走 blockedUntil 绝对到期时间。修复前的
// 「成功即救活」分支不看 blockedUntil，只要冷却期内有一个在途请求成功，就把
// 状态直接改回 closed；而 IsAvailable 的 open 分支只在 state==open 时才读
// blockedUntil，状态一旦变成 closed，剩余避让时长被整段作废。
//
// 生产上渠道正被限流时，其它在途请求成功是常态，于是「避让 → 立刻重入 →
// 再 429」会一直抖，限流避让形同虚设。
func TestInFlightSuccessDoesNotCancelTimedBlock(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9006

	tracker.TripBreakerForRateLimit(id, testModel)
	before := blockedRemaining(t, tracker, id)
	if before <= 0 {
		t.Fatal("限流避让应设置 blockedUntil")
	}

	// 冷却期内有一个在途请求成功
	tracker.Begin(id, testModel).End(false, false)

	if state := breakerState(statsFor(tracker, id).state.Load()); state != breakerOpen {
		t.Fatalf("定时熔断期间成功后状态 = %v，期望仍为 open（不能被救活分支改写）", state)
	}
	if ok, reason := tracker.IsAvailable(id, testModel); ok || reason != ReasonCircuitBlockedUntil {
		t.Fatalf("定时熔断期间成功后 IsAvailable = %v（%s），期望 false/%s", ok, reason, ReasonCircuitBlockedUntil)
	}
	if after := blockedRemaining(t, tracker, id); after <= 0 {
		t.Fatal("避让剩余时长不应被成功请求清零")
	} else if delta := before - after; delta > 3*time.Second || delta < -3*time.Second {
		t.Fatalf("避让剩余时长从 %v 变成 %v，不应被大幅改写", before, after)
	}
}

// TestInFlightSuccessStillRescuesRegularCooldown 反向守卫：上面的守卫不能把
// 「成功一次即救活」这条需求一起关掉。常规冷却 blockedUntil==0，成功仍然
// 必须关闭熔断器并清零递增计数。
func TestInFlightSuccessStillRescuesRegularCooldown(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9007

	tracker.TripBreaker(id, testModel)
	tracker.TripBreaker(id, testModel)
	if got := blockedRemaining(t, tracker, id); got != 0 {
		t.Fatalf("常规熔断不应设置 blockedUntil，实际 %v", got)
	}
	if ok, reason := tracker.IsAvailable(id, testModel); ok {
		t.Fatalf("常规冷却期内应不可用，实际可用（%s）", reason)
	}

	tracker.Begin(id, testModel).End(false, false)

	if state := breakerState(statsFor(tracker, id).state.Load()); state != breakerClosed {
		t.Fatalf("成功后熔断状态 = %v，期望 closed（成功一次即救活）", state)
	}
	if ok, reason := tracker.IsAvailable(id, testModel); !ok {
		t.Fatalf("成功后 IsAvailable = false（%s），期望立即恢复可用", reason)
	}
	if tc := tripCountOf(t, tracker, id); tc != 0 {
		t.Fatalf("成功后 tripCount = %d，期望清零", tc)
	}
}

// TestMaxCompletionTokensKeyMustBeUpstreamModelName 锁住 P1 的根因修复。
//
// 上限是「上游模型」的属性。渠道 #111 配了 model_mapping，把客户端请求名
// deepseek-v4-flash 映射成上游名 Deepseek-v4-flash。记录侧与读取侧一旦用了
// 不同字符串（例如记录用客户端名、读取用上游名），查询永远命中不了，钳制
// 静默退化成空操作——上线后没有任何报错，只有 max_completion_tokens 的 400
// 原样继续出现。这个测试用真实的分叉名字证明键是对齐的。
func TestMaxCompletionTokensKeyMustBeUpstreamModelName(t *testing.T) {
	const id = 9008
	const clientModelName = "deepseek-v4-flash"
	const upstreamModelName = "Deepseek-v4-flash"

	RecordMaxCompletionTokensLimit(id, upstreamModelName, 262144)

	if got := ClampMaxCompletionTokens(id, upstreamModelName, 384000); got != 262144 {
		t.Fatalf("按上游模型名读取应命中已学上限，got = %d，期望 262144", got)
	}
	// 客户端请求名查不到上限：正因为如此，记录侧也必须用上游名，
	// 否则这个 384000 会一路飞到上游变成 400。
	if got := ClampMaxCompletionTokens(id, clientModelName, 384000); got != 384000 {
		t.Fatalf("未记录过的模型名不应被钳制，got = %d，期望原样 384000", got)
	}
	if got := GetMaxCompletionTokensLimit(id, upstreamModelName); got != 262144 {
		t.Fatalf("按上游模型名查询上限 = %d，期望 262144", got)
	}
}

// TestRecordMaxCompletionTokensLimitIsMonotonic 上限只应收紧：上游偶尔给出的
// 更大数字不得把已学到的更小值顶开，否则一次误报就能解除钳制。
func TestRecordMaxCompletionTokensLimitIsMonotonic(t *testing.T) {
	const id = 9009
	RecordMaxCompletionTokensLimit(id, "monotonic-model", 1000)
	RecordMaxCompletionTokensLimit(id, "monotonic-model", 5000)
	if got := GetMaxCompletionTokensLimit(id, "monotonic-model"); got != 1000 {
		t.Fatalf("更大的上限不得覆盖已学到的更小值，got = %d，期望 1000", got)
	}
	RecordMaxCompletionTokensLimit(id, "monotonic-model", 800)
	if got := GetMaxCompletionTokensLimit(id, "monotonic-model"); got != 800 {
		t.Fatalf("更小的上限应收紧生效，got = %d，期望 800", got)
	}
}
