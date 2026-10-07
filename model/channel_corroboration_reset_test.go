package model

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 佐证计数必须挂在**写库入口**上，而不是各调用点。
//
// 「谁把 status 置为启用」有三条互不相干的路径：
//   1. service.EnableChannel（自动恢复、测活通过）
//   2. controller 的 UpdateChannelStatus（面板单条启用）—— 直接调本函数
//   3. controller 的 BatchUpdateChannelStatus / EnableChannelByTag（面板批量启用）
//
// 复位原先只写在第 1 条的 service 层，第 2、3 条完全绕过它。后果是
// 运维在面板上人工恢复一条渠道后，它此前攒的 2/3 仍然算数 —— 于是下一次
// 真实故障第一次就顶到阈值被摘掉。佐证退化成「一次即禁」，正是它存在的
// 理由的反面，而且发生在最需要它工作的时候（人工刚确认渠道回来了）。

func seedCorroboration(t *testing.T, channelID int, modelName, class string, n int) {
	t.Helper()
	p := loadbalancer.DefaultPolicy()
	p.Enabled = true
	p.Default.Breaker.AutoDisableCorroborationThreshold = 3
	p.Default.Breaker.AutoDisableCorroborationWindowSeconds = 600
	loadbalancer.SetPolicy(p)

	loadbalancer.ResetCorroborationForChannel(channelID)
	for i := 0; i < n; i++ {
		_, _ = loadbalancer.RecordAutoDisableSignal(channelID, modelName, class)
	}
	require.Equal(t, n, loadbalancer.CorroborationCount(channelID, modelName, class),
		"前置条件：计数已攒到位")
}

// TestUpdateChannelStatusResetsCorroborationOnEnable 面板启用渠道必须复位
// 该渠道所有模型的佐证计数。
func TestUpdateChannelStatusResetsCorroborationOnEnable(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{Name: "re-enable-resets", Key: "k", Status: common.ChannelStatusAutoDisabled}
	require.NoError(t, DB.Create(&channel).Error)
	seedCorroboration(t, channel.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	seedCorroboration(t, channel.Id, "claude-opus", loadbalancer.CorroborationClassChannelError, 2)

	assert.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusEnabled, ""))

	assert.Equal(t, 0, loadbalancer.CorroborationCount(channel.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode),
		"人工启用后旧信号必须作废：留着的下一次故障第一次就顶到阈值被摘")
	assert.Equal(t, 0, loadbalancer.CorroborationCount(channel.Id, "claude-opus", loadbalancer.CorroborationClassChannelError),
		"复位是渠道级的：人工确认的是整条渠道回来了，不是某一个模型")
	t.Cleanup(func() { loadbalancer.ResetCorroborationForChannel(channel.Id) })
}

// TestUpdateChannelStatusDoesNotResetOnDisable 禁用方向不得复位。
//
// 这条看着反直觉，但方向搞反同样有害：刚攒到 2/3 的一次自动禁用如果顺手
// 清零，佐证窗口就永远攒不到阈值 —— 机制静默失效，且完全没有报错。
func TestUpdateChannelStatusDoesNotResetOnDisable(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{Name: "disable-keeps-count", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	seedCorroboration(t, channel.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	t.Cleanup(func() { loadbalancer.ResetCorroborationForChannel(channel.Id) })

	assert.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "provider rejected key"))

	assert.Equal(t, 2, loadbalancer.CorroborationCount(channel.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode),
		"禁用时不得复位 —— 清零会让自动禁用永远攒不到阈值，且线上没有任何报错")
}

// TestUpdateChannelStatusLeavesOtherChannelsAlone 复位不得波及其它渠道。
func TestUpdateChannelStatusLeavesOtherChannelsAlone(t *testing.T) {
	setupChannelStatusTest(t)

	a := Channel{Name: "a", Key: "k", Status: common.ChannelStatusAutoDisabled}
	b := Channel{Name: "b", Key: "k", Status: common.ChannelStatusAutoDisabled}
	require.NoError(t, DB.Create(&a).Error)
	require.NoError(t, DB.Create(&b).Error)
	seedCorroboration(t, a.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	seedCorroboration(t, b.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	t.Cleanup(func() {
		loadbalancer.ResetCorroborationForChannel(a.Id)
		loadbalancer.ResetCorroborationForChannel(b.Id)
	})

	assert.True(t, UpdateChannelStatus(a.Id, "", common.ChannelStatusEnabled, ""))

	assert.Equal(t, 0, loadbalancer.CorroborationCount(a.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode))
	assert.Equal(t, 2, loadbalancer.CorroborationCount(b.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode),
		"别的渠道不受影响")
}

// TestEnableChannelByTagResetsCorroboration 面板的批量启用也必须复位。
//
// 这条路径与单条启用互不相干：它不进 UpdateChannelStatus，走的是一条
// 独立的 `UPDATE channels SET status = 1` —— 所以只在 UpdateChannelStatus
// 上挂复位对它无效。批量启用正是运维处理「一批渠道都挂了」后的标准动作，
// 复位漏在这里的后果与单条路径完全一样：人工确认过了，下一次故障第一次
// 就被摘掉。
func TestEnableChannelByTagResetsCorroboration(t *testing.T) {
	setupChannelStatusTest(t)

	tagged := Channel{Name: "tagged", Key: "k", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, DB.Create(&tagged).Error)
	tagged.Tag = common.GetPointer("ops")
	require.NoError(t, DB.Save(tagged).Error)

	other := Channel{Name: "untagged", Key: "k", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, DB.Create(&other).Error)

	seedCorroboration(t, tagged.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	seedCorroboration(t, other.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode, 2)
	t.Cleanup(func() {
		loadbalancer.ResetCorroborationForChannel(tagged.Id)
		loadbalancer.ResetCorroborationForChannel(other.Id)
	})

	require.NoError(t, EnableChannelByTag("ops"))

	assert.Equal(t, 0, loadbalancer.CorroborationCount(tagged.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode),
		"批量启用同样是一次恢复，旧佐证信号一律作废")
	assert.Equal(t, 2, loadbalancer.CorroborationCount(other.Id, "gpt-4o", loadbalancer.CorroborationClassStatusCode),
		"不在该 tag 下的渠道不受影响")
}
