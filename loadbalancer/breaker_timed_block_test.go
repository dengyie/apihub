package loadbalancer

import (
	"testing"
	"time"
)

// TestTripBreakerDoesNotCancelActiveTimedBlock 锁住宵禁/限流避让不会被普通失败抹掉。
//
// blockedUntil 是绝对到期时间，常规熔断的 openedAt 是相对冷却。tripBreaker
// 过去无条件 blockedUntil.Store(0)，等于用一次失败把宵禁整段取消——而 End 的
// 失败分支不看 blockedUntil，宵禁期间累计到 failure_threshold 次在途失败就会
// 走到那里。生产上 IsCurfewError → TripBreakerUntil 是活的路径（宵禁到早 8 点），
// 渠道会在午夜重新进入轮转，正是宵禁要防的事。
func TestTripBreakerDoesNotCancelActiveTimedBlock(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9010

	tracker.TripBreakerUntil(id, testModel, time.Now().Add(2*time.Hour))
	before := blockedRemaining(t, tracker, id)
	if before <= 0 {
		t.Fatal("宵禁应设置 blockedUntil")
	}

	// 宵禁期间连续失败到阈值：End 的失败分支会调 tripBreaker
	for i := 0; i < 5; i++ {
		tracker.Begin(id, testModel).End(false, true)
	}

	if ok, reason := tracker.IsAvailable(id, testModel); ok || reason != ReasonCircuitBlockedUntil {
		t.Fatalf("宵禁期间失败不应解除避让，实际 IsAvailable = %v（%s）", ok, reason)
	}
	after := blockedRemaining(t, tracker, id)
	if after <= 0 {
		t.Fatal("blockedUntil 被普通失败清零了，宵禁/避让整段作废")
	}
	if delta := before - after; delta > 3*time.Second || delta < -3*time.Second {
		t.Fatalf("避让剩余时长从 %v 变成 %v，不应被改写", before, after)
	}
	// 这次失败是真的，递增计数照常累加（只是不该改写绝对到期时间）
	if tc := tripCountOf(t, tracker, id); tc != 5 {
		t.Fatalf("宵禁期间失败后 tripCount = %d，期望 5", tc)
	}
}

// TestTripBreakerAfterTimedBlockExpired 过期后的定时熔断不再挡路，常规熔断照常生效。
func TestTripBreakerAfterTimedBlockExpired(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9011

	// 已过期（过去 1 小时）的宵禁
	tracker.TripBreakerUntil(id, testModel, time.Now().Add(-time.Hour))
	// blockedRemaining 对已过期的定时熔断返回**负值**（剩余 = 到期 - 现在），
	// 这正是它「不再挡路」的信号，判据是 <= 0 而不是 == 0。
	if got := blockedRemaining(t, tracker, id); got > 0 {
		t.Fatalf("过期宵禁不应再有剩余避让时长，got = %v", got)
	}

	tracker.TripBreaker(id, testModel)
	if state := breakerState(statsFor(tracker, id).state.Load()); state != breakerOpen {
		t.Fatalf("过期后常规熔断应正常打开，状态 = %v", state)
	}
	if ok, reason := tracker.IsAvailable(id, testModel); ok || reason != ReasonCircuitOpen {
		t.Fatalf("常规冷却期内应不可用，实际 = %v（%s）", ok, reason)
	}
}

// TestTripBreakerStillEscalatesNormally 没有定时熔断时行为不变：递增计数与冷却照常。
func TestTripBreakerStillEscalatesNormally(t *testing.T) {
	usePolicy(t, newEscalationPolicy())

	tracker := newTestTracker()
	const id = 9012

	tracker.TripBreaker(id, testModel)
	first := cooldownRemaining(t, tracker, id)
	tracker.TripBreaker(id, testModel)
	second := cooldownRemaining(t, tracker, id)

	if second <= first {
		t.Fatalf("第二次熔断冷却 %v 应大于第一次 %v", second, first)
	}
	if delta := second - first; delta < 8*time.Second || delta > 12*time.Second {
		t.Fatalf("两次冷却差值 = %v，期望约 10s（10s → 20s）", delta)
	}
}

// TestPolicyReliableDefaultsTrue 未经过 Init 时判据可信，单测与编程式配置不受影响。
