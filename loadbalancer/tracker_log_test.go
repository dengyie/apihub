package loadbalancer

import (
	"bytes"
	"log"

	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 熔断跳闸此前完全没有日志。这批用例钉的是「可观测性」而非「熔断行为」——
// 熔断该发生还是发生，TestBreaker* 系列已经覆盖；这里覆盖的是运维能不能
// 知道它发生了。
//
// 缺口的具体形状：controller 里有 11 处显式 Trip* 调用，都带 WARN 日志；
// 唯独「连续失败计数到阈值自动跳闸」这条路是哑的，而它恰好是生产里最常走
// 的一条。渠道被熔掉的那 5 分钟里，日志上唯一的线索是「它没流量了」——
// 而熔断和上游就是没人用，在日志里长得一模一样。

// captureLog 把标准 logger 的输出重定向到 buf，返回还原函数。
func captureLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	return buf, func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}

// TestBreakerTripLogsOnThreshold 阈值驱动的熔断必须留痕，且写清三件事：
// 哪条渠道、哪把键、攒了几次。
func TestBreakerTripLogsOnThreshold(t *testing.T) {
	installPolicy(t, perModelPolicy(10))
	tr := newTestTracker()
	buf, restore := captureLog(t)
	defer restore()

	const channelID = 4242
	for i := 0; i < 3; i++ {
		h := tr.Begin(channelID, "gpt-4o")
		h.End(false, true)
	}

	out := buf.String()
	require.Contains(t, out, "breaker tripped",
		"阈值触发的熔断此前完全没有日志：被摘掉的那几分钟里日志上什么都看不到")
	assert.Contains(t, out, "channel #4242", "必须指出是哪条渠道")
	assert.Contains(t, out, "gpt-4o", "必须指出熔的是哪个模型 —— 整条熔与单模型熔要能区分开")
	assert.Contains(t, out, "3 consecutive failures", "必须写出攒了几次")
}

// TestNoBreakerLogBelowThreshold 未达阈值不得打日志。
//
// 这条日志的价值在于「不可逆/不可用动作发生了」；逐次失败都刷一遍的话，
// 一个稳定故障的渠道会每秒产出一行，淹没真正的信号。
func TestNoBreakerLogBelowThreshold(t *testing.T) {
	installPolicy(t, perModelPolicy(10))
	tr := newTestTracker()
	buf, restore := captureLog(t)
	defer restore()

	for i := 0; i < 2; i++ {
		h := tr.Begin(4243, "gpt-4o")
		h.End(false, true)
	}

	assert.NotContains(t, buf.String(), "breaker tripped",
		"未达阈值不得打熔断日志，否则真正的跳闸会被日常失败淹没")
}

// TestBreakerTripLogsModelScopedAndChannelScopedLabels per_model 关闭时
// 熔断落在整条渠道上，日志必须写出来。
//
// 两件事在日志上长得一样就等于没记：事后无从判断「熔了 #228 的
// claude-opus-4-8」和「整条 #228 都不用了」的影响面差了几个数量级。
func TestBreakerTripLogsModelScopedAndChannelScopedLabels(t *testing.T) {
	p := perModelPolicy(10)
	no := false
	p.Default.Breaker.PerModel = &no
	installPolicy(t, p)
	tr := newTestTracker()
	buf, restore := captureLog(t)
	defer restore()

	for i := 0; i < 3; i++ {
		h := tr.Begin(4244, "some-model")
		h.End(false, true)
	}

	out := buf.String()
	require.Contains(t, out, "breaker tripped")
	assert.Contains(t, out, "[全部模型]",
		"per_model 关闭时熔断落在整条渠道上，日志必须这样写出来")
}

// TestBreakerTripLogCooldownMatchesPolicy 日志里报的冷却时长必须与实际
// 拒绝选择的时长是同一份算式。
//
// 排障时最需要对得上的两个数就是「它多久回来」和「日志说它多久回来」；
// 两处各写一遍算式的话，日志报 60s 而实际挡 300s，结论会完全跑偏。
// 这条用例在递增退避（第 2 次熔断 = 基础 × 2）下比对，确保抽出来的
// cooldownSecondsFor 与 checkBreaker 用的是同一份。
func TestBreakerTripLogCooldownMatchesPolicy(t *testing.T) {
	installPolicy(t, perModelPolicy(10))
	tr := newTestTracker()
	buf, restore := captureLog(t)
	defer restore()

	// 第 1 次熔断：tripCount=1 → 倍数 1 → 冷却 60s
	for i := 0; i < 3; i++ {
		h := tr.Begin(4245, "gpt-4o")
		h.End(false, true)
	}
	// 冷却到期，重新进入闭合并再次攒够 3 次
	s := tr.getBreaker(scopeKey(4245, "gpt-4o"))
	s.openedAt.Store(time.Now().Unix() - 61)
	s.state.Store(int32(breakerHalfOpen))
	s.halfOpenProbes.Store(0)

	for i := 0; i < 3; i++ {
		h := tr.Begin(4245, "gpt-4o")
		h.End(false, true)
	}

	out := buf.String()
	require.Contains(t, out, "breaker tripped")
	// 第 2 次熔断 tripCount=2 → 倍数 2 → 冷却 120s
	assert.Contains(t, out, "cooling down for 120s",
		"日志里的冷却时长必须走递增退避，否则报出来的数与实际不一致")

	// 与 checkBreaker 用的算式对拍：同样是 stats + 同一个 BreakerPolicy
	s2 := &ChannelStats{}
	s2.tripCount.Store(2)
	assert.EqualValues(t, 120, cooldownSecondsFor(s2, perModelPolicy(10).Default.Breaker),
		"cooldownSecondsFor 必须是基础冷却 × 退避倍数")
}

// TestBreakerLogKeyMatchesEntryItTripped 熔断日志标的键必须与失败真正落进的那把
// 键一致，即使 per_model 在这三次请求之间被热加载翻动过。
//
// 根因：句柄不保留模型名，若 End 为写日志重调 scopeKey，它读到的是那一刻的
// policy.per_model。翻动之后，一次按模型熔断会被标成「[全部模型]」（或反过来）。
// 熔断行为不受影响，但日志会说反影响面 —— 而影响面正是排障要判断的第一件事。
func TestBreakerLogKeyMatchesEntryItTripped(t *testing.T) {
	installPolicy(t, perModelPolicy(10))
	tr := newTestTracker()
	buf, restore := captureLog(t)
	defer restore()

	// 三次请求都在 per_model 打开时 Begin，于是三笔失败落在同一把按模型的键上。
	handles := make([]*RequestHandle, 0, 3)
	for i := 0; i < 3; i++ {
		handles = append(handles, tr.Begin(4246, "gpt-4o"))
	}

	// 请求在途期间热加载把 per_model 关掉
	no := false
	p2 := perModelPolicy(10)
	p2.Default.Breaker.PerModel = &no
	installPolicy(t, p2)

	// 第 3 笔攒够阈值 → 熔断，落点是 Begin 时定下的那把按模型键
	for _, h := range handles {
		h.End(false, true)
	}

	out := buf.String()
	require.Contains(t, out, "breaker tripped")
	assert.Contains(t, out, "gpt-4o",
		"日志必须标出失败真正落进的那把键；若按 End 时刻的 per_model 重算，会错标成 [全部模型]")
	assert.NotContains(t, out, "[全部模型]",
		"这三笔失败都落在 per_model 打开时建的那把按模型键上")
}
