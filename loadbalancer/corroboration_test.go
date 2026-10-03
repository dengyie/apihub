package loadbalancer

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetCorroborationForTest 清空佐证表并返回还原函数。
// 包级全局状态，测试之间必须隔离 —— 否则一个用例攒下的计数会让下一个
// 用例「第一次就达阈值」，失败原因指向错误的地方。
func resetCorroborationForTest() func() {
	corroborationRegistry.mu.Lock()
	prev := corroborationRegistry.m
	corroborationRegistry.m = nil
	corroborationRegistry.mu.Unlock()
	return func() {
		corroborationRegistry.mu.Lock()
		corroborationRegistry.m = prev
		corroborationRegistry.mu.Unlock()
	}
}

func corroborationPolicyForTest(threshold int, windowSeconds int64) *Policy {
	p := DefaultPolicy()
	p.Enabled = true
	p.Default.Breaker.AutoDisableCorroborationThreshold = threshold
	p.Default.Breaker.AutoDisableCorroborationWindowSeconds = windowSeconds
	return p
}

// TestRecordAutoDisableSignalRequiresRepeatedEvidence 钉住这个特性的核心不变式：
// 未达阈值不得放行。自动禁用不可逆，一次措辞巧合就摘掉一条健康渠道，
// 而线上没有任何报错 —— 这是它与熔断（可自愈、要连续 FailureThreshold 次）
// 最本质的差别。
func TestRecordAutoDisableSignalRequiresRepeatedEvidence(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	ready, count := RecordAutoDisableSignal(1, "gpt-4o", CorroborationClassStatusCode)
	assert.False(t, ready, "第 1 次信号不得放行不可逆的禁用")
	assert.Equal(t, 1, count)

	ready, count = RecordAutoDisableSignal(1, "gpt-4o", CorroborationClassStatusCode)
	assert.False(t, ready, "第 2 次信号仍不得放行")
	assert.Equal(t, 2, count)

	ready, count = RecordAutoDisableSignal(1, "gpt-4o", CorroborationClassStatusCode)
	assert.True(t, ready, "达到阈值才放行")
	assert.Equal(t, 3, count)
}

// TestCorroborationKeyedByChannelModelClass 是这个设计里最容易做错的一处。
// 三条正交性各自钉死：不同渠道、不同模型、不同判据类别都不该互相佐证。
// 合起来的意思是「必须是**同一条渠道、同一个模型、同一种失效**重复发生」。
func TestCorroborationKeyedByChannelModelClass(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(2, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	// 渠道维度
	ready, _ := RecordAutoDisableSignal(1, "m", CorroborationClassChannelError)
	require.False(t, ready)
	ready, _ = RecordAutoDisableSignal(2, "m", CorroborationClassChannelError)
	assert.False(t, ready, "别的渠道的失败不算这条渠道的佐证")

	// 模型维度
	ready, _ = RecordAutoDisableSignal(1, "other", CorroborationClassChannelError)
	assert.False(t, ready, "别的模型的失败不算这个模型的佐证")

	// 类别维度
	ready, _ = RecordAutoDisableSignal(1, "m", CorroborationClassKeyword)
	assert.False(t, ready, "「凭据失效」与「模型不存在」是两回事，不互相印证")

	// 回到同一条：此时应为 1 次，第 2 次才达阈值
	ready, count := RecordAutoDisableSignal(1, "m", CorroborationClassChannelError)
	assert.Equal(t, 2, count)
	assert.True(t, ready)
}

// TestCorroborationIgnoresWordingVariants 锁住「不按报文原文分桶」这个决定。
//
// 上游对同一失效的措辞会变（"insufficient balance" / "Insufficient account
// balance"）。若按原文分桶，两种措辞永远凑不齐佐证，等于要求上游每次都用
// 完全相同的措辞渠道才可能被摘掉 —— 那不是更保守，是让机制基本失效。
func TestCorroborationIgnoresWordingVariants(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(2, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	ready, _ := RecordAutoDisableSignal(1, "m", CorroborationClassKeyword)
	require.False(t, ready)
	// 同类别、不同措辞：调用方只传类别，不传原文，所以必然同桶
	ready, _ = RecordAutoDisableSignal(1, "m", CorroborationClassKeyword)
	assert.True(t, ready, "同类别即互相佐证，与上游措辞无关")
}

// TestCorroborationThresholdOneRestoresOldBehavior 是止血档位的回归防护。
// 阈值 1 必须完全退回 v29.19 的语义（一次即禁），否则出问题时没有回退手段。
func TestCorroborationThresholdOneRestoresOldBehavior(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(1, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	ready, _ := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	assert.True(t, ready, "阈值为 1 时第一次即放行")

	// 0 / 未配置 走默认值 3，而不是 1（0 会被当成「没配」）
	SetPolicy(corroborationPolicyForTest(0, 0))
	ready, _ = RecordAutoDisableSignal(2, "m", CorroborationClassStatusCode)
	assert.False(t, ready, "未配置时用默认阈值 3，不是 1")
	_, _ = RecordAutoDisableSignal(2, "m", CorroborationClassStatusCode)
	ready, _ = RecordAutoDisableSignal(2, "m", CorroborationClassStatusCode)
	assert.True(t, ready)
}

// TestCorroborationWindowExpiry 钉住窗口语义：佐证要的是「持续」，不是「累计」。
// 隔得再久的两��失败不构成互相印证 —— 那更可能是两次独立的偶发。
func TestCorroborationWindowExpiry(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(2, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	ready, _ := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	require.False(t, ready)
	ready, count := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	require.True(t, ready)
	require.Equal(t, 2, count)

	// 窗口边界：恰好等于窗口长度时算过期（st.count 在 now-window 之后才重置）
	st := &corroborationState{count: 1, windowStart: time.Now().Add(-601 * time.Second)}
	corroborationRegistry.mu.Lock()
	corroborationRegistry.m = map[string]*corroborationState{
		"9|m|" + CorroborationClassStatusCode: st,
	}
	corroborationRegistry.mu.Unlock()

	ready, count = RecordAutoDisableSignal(9, "m", CorroborationClassStatusCode)
	assert.Equal(t, 1, count, "窗口外的旧信号不该续命，应从 1 重新计")
	assert.False(t, ready)
}

// TestResetCorroborationOnRecovery 钉住恢复后必须清零。
//
// 不清的话：一条渠道攒到 2/3 失败、测活通过恢复、随后第一次真实故障就顶到
// 阈值被摘掉 —— 佐证退化成「一次即禁」，恰好是它要防的失败模式。
func TestResetCorroborationOnRecovery(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	_, _ = RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	require.Equal(t, 2, CorroborationCount(1, "m", CorroborationClassStatusCode))

	ResetCorroboration(1, "m")
	assert.Equal(t, 0, CorroborationCount(1, "m", CorroborationClassStatusCode),
		"测活通过后必须清零，否则下一次故障第一次就顶到阈值")

	ready, count := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	assert.Equal(t, 1, count)
	assert.False(t, ready)
}

// TestResetCorroborationForChannelIsModelWide 覆盖渠道级恢复（EnableChannel
// 拿不到模型名，只能清整条渠道）。必须清干净、且不误伤别的渠道。
func TestResetCorroborationForChannelIsModelWide(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	_, _ = RecordAutoDisableSignal(1, "m1", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(1, "m2", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(2, "m1", CorroborationClassStatusCode)

	ResetCorroborationForChannel(1)
	assert.Equal(t, 0, CorroborationCount(1, "m1", CorroborationClassStatusCode))
	assert.Equal(t, 0, CorroborationCount(1, "m2", CorroborationClassStatusCode))
	assert.Equal(t, 1, CorroborationCount(2, "m1", CorroborationClassStatusCode),
		"别的渠道不受影响 —— 前缀 1| 与 2| 不该互相误删")
}

// TestPeekAutoDisableCorroborationDoesNotCount 是 ReadOnly 契约的守卫。
//
// RecordPolicyFailure 跑在 ProcessChannelError **之前**，靠 Peek 提前知道
// 结论好让事件流里的标签与事实一致。Peek 一旦偷偷计数，同一次失败就会被
// 记两次，阈值形同虚设 —— 而症状是「阈值调多大都不生效」，极难排查。
func TestPeekAutoDisableCorroborationDoesNotCount(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	// 连 peek 若干次
	for i := 0; i < 10; i++ {
		assert.False(t, PeekAutoDisableCorroboration(1, "m", CorroborationClassStatusCode))
	}
	assert.Equal(t, 0, CorroborationCount(1, "m", CorroborationClassStatusCode),
		"peek 绝不能计数")

	// 计数路径仍从 1 开始
	ready, count := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	assert.Equal(t, 1, count)
	assert.False(t, ready)
}

// TestPeekMatchesRecordOutcome 钉住 peek 的预测必须与真正的记录结果一致 ——
// 否则事件流里的标签又变成新的假话。
func TestPeekMatchesRecordOutcome(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	// 第 3 次：peek 预测会放行，record 确认放行
	for i := 1; i <= 3; i++ {
		peeked := PeekAutoDisableCorroboration(1, "m", CorroborationClassStatusCode)
		ready, count := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
		assert.Equal(t, peeked, ready, "第 %d 次：peek 与 record 必须一致", i)
		assert.Equal(t, i, count)
	}
}

// TestCorroborationDisabledPolicySkipsGate 覆盖策略未加载时的退化行为。
// 生产首次加载失败时 currentPolicy 为 nil，此时不应因为「拿不到阈值」而
// 把所有自动禁用都挡下 —— 那会让兜底链路失效，也是 v29.x 一直在防的方向。
func TestCorroborationDisabledPolicySkipsGate(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(&Policy{Enabled: false})
	t.Cleanup(func() { SetPolicy(prev) })

	ready, _ := RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	assert.True(t, ready, "策略未启用时不拦自动禁用")

	// 且不得留下计数
	assert.Equal(t, 0, CorroborationCount(1, "m", CorroborationClassStatusCode))
	assert.True(t, PeekAutoDisableCorroboration(1, "m", CorroborationClassStatusCode))
}

// TestCorroborationRegistryBounded 验证无界模型名撑不爆这张表。
// 模型名来自客户端，是无界输入；不淘汰就是别人用随机模型名把内存吃光。
func TestCorroborationRegistryBounded(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = RecordAutoDisableSignal(1, string(rune('a'+i%26))+"-random-model-name", CorroborationClassStatusCode)
		}(i)
	}
	wg.Wait()

	corroborationRegistry.mu.Lock()
	size := len(corroborationRegistry.m)
	corroborationRegistry.mu.Unlock()
	assert.LessOrEqual(t, size, 4096, "佐证表必须有界")
}
// 佐证表的键含客户端给的模型名。已有并发用例只覆盖了「有界」这个结论，
// 没有覆盖触发它的机制：并发下每条 key 各不相同，靠的是过期清扫那条路径。
// 这里钉住第二道闸门 —— 一个窗口之内涌入的互不相同的键一条也过期不了，
// 过期清扫等于没做，必须有总量硬上限兜底。
func TestCorroborationRegistryHardCap(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	// 阈值调高，让所有信号都停在「未达阈值」，从而不会互相干扰计数
	SetPolicy(corroborationPolicyForTest(1000000, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	flood := corroborationCap + 2000
	for i := 0; i < flood; i++ {
		RecordAutoDisableSignal(1, fmt.Sprintf("flood-model-%d", i), CorroborationClassStatusCode)
	}

	corroborationRegistry.mu.Lock()
	size := len(corroborationRegistry.m)
	corroborationRegistry.mu.Unlock()
	// 清扫发生在本条记录写入之前，稳态上限是 cap+1，理由同 exhaustion。
	assert.LessOrEqual(t, size, corroborationCap+1,
		"未过期的键也必须有硬上限，否则随机模型名可以把佐证表撑爆")
	assert.Greater(t, size, corroborationCap/2,
		"硬上限不应误伤正常规模的条目")
}
