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

// 按标签批量启用走独立的 UPDATE，不经 UpdateChannelStatus：复位必须在
// EnableChannelByTag 自己身上再挂一次，否则「一批渠道刚恢复」后它们仍空转
// 剩余冷却，批量恢复形同未恢复。
func TestEnableChannelByTagResetsBreakers(t *testing.T) {
	setupChannelStatusTest(t)

	p := loadbalancer.DefaultPolicy()
	p.Enabled = true
	prev := loadbalancer.GetPolicy()
	loadbalancer.SetPolicy(p)
	t.Cleanup(func() { loadbalancer.SetPolicy(prev) })

	tagged := Channel{Name: "tag-enable-resets-breaker", Key: "k", Status: common.ChannelStatusManuallyDisabled, Tag: common.GetPointer("ops")}
	require.NoError(t, DB.Create(&tagged).Error)
	other := Channel{Name: "untagged-keeps-breaker", Key: "k", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, DB.Create(&other).Error)

	tracker := loadbalancer.GlobalTracker()
	model := "gpt-4o"
	tracker.TripBreaker(tagged.Id, model)
	tracker.TripBreaker(other.Id, model)
	t.Cleanup(func() {
		tracker.ResetChannelBreakers(tagged.Id)
		tracker.ResetChannelBreakers(other.Id)
	})

	require.NoError(t, EnableChannelByTag("ops"))

	ok, reason := tracker.IsAvailable(tagged.Id, model)
	assert.True(t, ok, "批量启用同样是一次恢复，熔断冷却必须复位（%s）", reason)
	ok, _ = tracker.IsAvailable(other.Id, model)
	assert.False(t, ok, "不在该 tag 下的渠道不受影响")
}
