package model

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 熔断状态的复位必须与佐证复位同点（UpdateChannelStatus 的启用分支），
// 理由同 channel_corroboration_reset_test.go：「谁把 status 置为启用」有三条
// 互不相干的路径，挂在写入口上才不会漏。这条测试钉住接线本身——若有人
// 重构掉 ResetChannelBreakers 调用，这里先红。

func TestUpdateChannelStatusResetsBreakersOnEnable(t *testing.T) {
	setupChannelStatusTest(t)

	p := loadbalancer.DefaultPolicy()
	p.Enabled = true
	prev := loadbalancer.GetPolicy()
	loadbalancer.SetPolicy(p)
	t.Cleanup(func() { loadbalancer.SetPolicy(prev) })

	channel := Channel{Name: "re-enable-resets-breaker", Key: "k", Status: common.ChannelStatusAutoDisabled}
	require.NoError(t, DB.Create(&channel).Error)

	tracker := loadbalancer.GlobalTracker()
	model := "gpt-4o"
	tracker.TripBreaker(channel.Id, model)
	ok, reason := tracker.IsAvailable(channel.Id, model)
	require.False(t, ok, "前置条件：熔断中渠道不可选（%s）", reason)

	assert.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusEnabled, ""))

	ok, reason = tracker.IsAvailable(channel.Id, model)
	assert.True(t, ok, "人工启用 = 验证过渠道回来了：熔断冷却必须同点复位（%s）", reason)

	t.Cleanup(func() { tracker.ResetChannelBreakers(channel.Id) })
}

func TestUpdateChannelStatusDoesNotResetBreakersOnDisable(t *testing.T) {
	setupChannelStatusTest(t)

	p := loadbalancer.DefaultPolicy()
	p.Enabled = true
	prev := loadbalancer.GetPolicy()
	loadbalancer.SetPolicy(p)
	t.Cleanup(func() { loadbalancer.SetPolicy(prev) })

	channel := Channel{Name: "disable-keeps-breaker", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)

	tracker := loadbalancer.GlobalTracker()
	model := "gpt-4o"
	tracker.TripBreaker(channel.Id, model)
	require.False(t, func() bool { ok, _ := tracker.IsAvailable(channel.Id, model); return ok }())

	// 禁用方向不改熔断状态：熔断是渠道健康度的内存事实，与面板的启用开关无关
	assert.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, ""))
	ok, _ := tracker.IsAvailable(channel.Id, model)
	assert.False(t, ok, "禁用操作不得顺带清掉熔断状态")

	t.Cleanup(func() { tracker.ResetChannelBreakers(channel.Id) })
}
