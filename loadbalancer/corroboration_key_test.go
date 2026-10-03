package loadbalancer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这批用例钉的是 ResetCorroboration 的**键精度**，不是它的语义。
//
// 语义（恢复后必须清零）由 TestResetCorroborationOnRecovery 覆盖。
// 这里覆盖的是「清谁」——原实现按 "渠道ID|模型名|" 前缀扫全表，
// 而模型名是客户端提供的无界输入，所以复位可能顺带删掉不相干的桶。
//
// 后果不是理论上的：删掉兄弟桶等于替那条 (渠道, 模型) 免了一次佐证。
// 一个反复故障的模型只要名字里带 "|"，且同渠道下存在以它为前缀的模型名，
// 它每次失败在复位时都会被抹掉，佐证永远攒不到阈值 —— 不可逆的自动禁用
// 对它静默失效，和「阈值调到 1」是同一种故障，只是更难察觉。

// TestResetCorroborationDoesNotTouchSiblingModels 复位 "a" 不得动 "a|b"。
//
// 这是本条缺陷的最小复现：前缀 "1|a|" 匹配 "1|a|b|status_code"，
// 所以旧实现在复位 "a" 时把 "a|b" 的计数一起清了。
func TestResetCorroborationDoesNotTouchSiblingModels(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	_, _ = RecordAutoDisableSignal(1, "a", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(1, "a", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(1, "a|b", CorroborationClassStatusCode)
	require.Equal(t, 2, CorroborationCount(1, "a", CorroborationClassStatusCode))
	require.Equal(t, 1, CorroborationCount(1, "a|b", CorroborationClassStatusCode))

	ResetCorroboration(1, "a")

	assert.Equal(t, 0, CorroborationCount(1, "a", CorroborationClassStatusCode),
		"被复位的模型必须真的清零")
	assert.Equal(t, 1, CorroborationCount(1, "a|b", CorroborationClassStatusCode),
		"名字以被复位模型为前缀的模型不受影响 —— 它们是两个独立的桶")
}

// TestResetCorroborationClearsAllClasses 复位必须覆盖四个判据类别。
//
// 类别是封闭集合，复位按精确键逐个删；漏掉任何一类都意味着「凭据失效」
// 这类信号能在恢复后残留。不过这条用例同样能抓住「只删了当前命中类别」
// 的实现退化 —— 那种实现在测试里看不出类别差异，因为每次只记了一个类别。
func TestResetCorroborationClearsAllClasses(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	for _, class := range corroborationClasses {
		_, _ = RecordAutoDisableSignal(1, "m", class)
		_, _ = RecordAutoDisableSignal(1, "m", class)
	}

	ResetCorroboration(1, "m")

	for _, class := range corroborationClasses {
		assert.Equalf(t, 0, CorroborationCount(1, "m", class),
			"类别 %q 的计数必须在复位时一并清零", class)
	}
}

// TestResetCorroborationIsNotConfusedByOtherChannels 复位 "m" 不得动别的渠道。
//
// 与前一条同源：旧的前缀扫是按 "渠道ID|模型名|" 匹配的，渠道 ID 进了前缀，
// 所以这条在旧实现下本来就能过 —— 保留它是为了把「精度」钉在模型名这一维上，
// 将来谁把实现换成「只按模型名删」，它会先红。
func TestResetCorroborationIsNotConfusedByOtherChannels(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	_, _ = RecordAutoDisableSignal(1, "m", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(11, "m", CorroborationClassStatusCode)
	_, _ = RecordAutoDisableSignal(1, "m2", CorroborationClassStatusCode)

	ResetCorroboration(1, "m")

	assert.Equal(t, 0, CorroborationCount(1, "m", CorroborationClassStatusCode))
	assert.Equal(t, 1, CorroborationCount(11, "m", CorroborationClassStatusCode),
		"别的渠道不受影响 —— 前缀 1| 与 11| 不该互相误删")
	assert.Equal(t, 1, CorroborationCount(1, "m2", CorroborationClassStatusCode),
		"同渠道的其它模型不受影响")
}

// TestCorroborationKeyRoundTrip 确认键的构造与查表用的是同一处实现。
//
// RecordAutoDisableSignal / CorroborationCount / ResetCorroboration 三处
// 各自拼一遍键是这类注册表的经典缺陷来源：改了一处另两处不跟着改，
// 于是「记进去了但读不到」，表现为佐证永远差一次。构造只有一处就没有
// 这个失配面，这条用例守住它。
func TestCorroborationKeyRoundTrip(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	for _, class := range corroborationClasses {
		_, count := RecordAutoDisableSignal(7, "weird|model|name", class)
		assert.Equal(t, 1, count)
		assert.Equal(t, 1, CorroborationCount(7, "weird|model|name", class),
			"写入与读取必须落到同一把键（类别 %q）", class)
	}
}

// TestResetCorroborationHandlesPipeInModelNameRoundTrip 端到端版：模型名里
// 带 "|" 时，复位后计数必须真的归零（而不是复位了个寂寞）。
func TestResetCorroborationHandlesPipeInModelNameRoundTrip(t *testing.T) {
	defer resetCorroborationForTest()()
	prev := GetPolicy()
	SetPolicy(corroborationPolicyForTest(3, 600))
	t.Cleanup(func() { SetPolicy(prev) })

	const model = "vendor|model/v1"
	_, _ = RecordAutoDisableSignal(3, model, CorroborationClassChannelError)
	_, _ = RecordAutoDisableSignal(3, model, CorroborationClassChannelError)
	require.Equal(t, 2, CorroborationCount(3, model, CorroborationClassChannelError))

	ResetCorroboration(3, model)
	assert.Equal(t, 0, CorroborationCount(3, model, CorroborationClassChannelError),
		"带 | 的模型名同样必须被精确复位")
}
