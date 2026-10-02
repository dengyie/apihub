package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedChannelWithModels 建一条渠道并写入给定的 (模型, 启用) 组合。
func seedChannelWithModels(t *testing.T, name string, models []string, enabled bool) *Channel {
	t.Helper()
	channel := Channel{
		Name:   name,
		Key:    "sk-test",
		Group:  "default",
		Models: joinModels(models),
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, DB.Create(&channel).Error)
	for _, m := range models {
		require.NoError(t, DB.Create(&Ability{
			Group:     "default",
			Model:     m,
			ChannelId: channel.Id,
			Enabled:   enabled,
		}).Error)
	}
	return &channel
}

func joinModels(models []string) string {
	out := ""
	for i, m := range models {
		if i > 0 {
			out += ","
		}
		out += m
	}
	return out
}

func abilityEnabled(t *testing.T, channelID int, model string) bool {
	t.Helper()
	var a Ability
	require.NoError(t, DB.Where("channel_id = ? and model = ?", channelID, model).First(&a).Error)
	return a.Enabled
}

// 摘掉一个模型，其它模型必须原封不动，且 channels.status 保持启用 —— 这是整个
// 特性存在的理由：一次「某模型 404」不该让其余健康模型陪葬。
func TestDisableChannelModelKeepsChannelAndSiblingsEnabled(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "per-model", []string{"m1", "m2", "m3"}, true)

	needsChannelDisable, err := DisableChannelModel(channel.Id, "m2", "404 模型不存在")
	require.NoError(t, err)
	assert.False(t, needsChannelDisable, "还有 m1/m3 可用，不该升级为整渠道禁用")

	assert.False(t, abilityEnabled(t, channel.Id, "m2"), "m2 应退出轮转")
	assert.True(t, abilityEnabled(t, channel.Id, "m1"), "m1 不该受影响")
	assert.True(t, abilityEnabled(t, channel.Id, "m3"), "m3 不该受影响")

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "per-model 禁用不得改渠道状态")
	assert.Equal(t, "404 模型不存在", stored.ChannelInfo.ModelDisabledReason["m2"])
	assert.NotZero(t, stored.ChannelInfo.ModelDisabledTime["m2"])
}

// 摘掉最后一个可用模型后必须升级：否则留下一条 status=1 却永远选不中的僵尸
// 渠道，比不禁用更难排查。
func TestDisableChannelModelEscalatesWhenNoModelLeft(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "last-model", []string{"only"}, true)

	needsChannelDisable, err := DisableChannelModel(channel.Id, "only", "模型不存在")
	require.NoError(t, err)
	assert.True(t, needsChannelDisable, "摘完就没有任何可用模型，必须让调用方升级")
}

// 渠道不服务这个模型时不能凭空造出一行禁用。
func TestDisableChannelModelRejectsUnservedModel(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "unserved", []string{"m1"}, true)

	_, err := DisableChannelModel(channel.Id, "not-mine", "模型不存在")
	require.ErrorIs(t, err, ErrChannelModelNotServed)
	assert.True(t, abilityEnabled(t, channel.Id, "m1"), "失败的调用不得留下副作用")
}

// 重复禁用同一个模型是幂等的：第二次没有 enabled 行可翻，必须报未服务而不是
// 静默成功，否则上游连续两次 404 会把两次失败记成两次独立禁用。
func TestDisableChannelModelIsIdempotent(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "twice", []string{"m1", "m2"}, true)

	_, err := DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.NoError(t, err)
	_, err = DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.ErrorIs(t, err, ErrChannelModelNotServed)
	assert.True(t, abilityEnabled(t, channel.Id, "m2"))
}

func TestEnableChannelModelRestoresOnlyRecordedModel(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "restore", []string{"m1", "m2"}, true)

	_, err := DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.NoError(t, err)

	cleared, err := EnableChannelModel(channel.Id, "m1")
	require.NoError(t, err)
	assert.True(t, cleared)
	assert.True(t, abilityEnabled(t, channel.Id, "m1"), "m1 应回到轮转")

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Empty(t, stored.ChannelInfo.ModelDisabledReason, "留痕必须一并清掉")
}

// 从未被 per-model 禁用的模型，测活通过时不得碰 abilities —— 否则一次普通的
// 渠道测活会把因渠道级禁用而失效的行错误复活。
func TestEnableChannelModelIgnoresUnrecordedModel(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "untouched", []string{"m1"}, true)
	require.NoError(t, DB.Model(&Ability{}).
		Where("channel_id = ? and model = ?", channel.Id, "m1").
		Update("enabled", false).Error)

	cleared, err := EnableChannelModel(channel.Id, "m1")
	require.NoError(t, err)
	assert.False(t, cleared, "没有禁用留痕就不该动")
	assert.False(t, abilityEnabled(t, channel.Id, "m1"), "没有留痕的行必须保持原状")
}

// 整渠道禁用必须清掉 per-model 留痕，否则恢复时两张表会互相矛盾。
//
// UpdateAbilityStatus 是无差别覆写：恢复渠道会把所有 ability 置回 true，包括
// 被 per-model 禁用摘掉的那几个。若留痕不清，abilities 说模型在架、channel_info
// 说它已下线，而留痕正是 EnableChannelModel 唯一的准入凭据 —— 矛盾会让手动
// 测活去改一条本来就没被禁用的 ability，日报也会把同一模型报成两种状态。
// 这是自动恢复链路的常规动作（每小时一轮），不是边缘情况。
func TestWholeChannelDisableClearsPerModelRecords(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "recover", []string{"m1", "m2"}, true)

	_, err := DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.NoError(t, err)
	require.False(t, abilityEnabled(t, channel.Id, "m1"), "前置：m1 已退出轮转")

	// 渠道因账号级原因整条被自动禁用（余额不足等）
	require.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "余额不足"))

	var afterDisable Channel
	require.NoError(t, DB.First(&afterDisable, channel.Id).Error)
	assert.Empty(t, afterDisable.ChannelInfo.ModelDisabledReason,
		"整渠道禁用后 per-model 留痕必须一并清掉，否则恢复时会与 abilities 矛盾")
	assert.Empty(t, afterDisable.ChannelInfo.ModelDisabledTime)

	// 自动恢复测通、整条恢复
	require.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusEnabled, ""))
	assert.True(t, abilityEnabled(t, channel.Id, "m1"),
		"留痕已清，恢复后 m1 回到轮转是预期行为 —— 它会再失败一次并被重新禁用")

	// 关键：清掉留痕后，EnableChannelModel 不该把 m1 当成「曾被 per-model 禁用」
	var afterRecovery Channel
	require.NoError(t, DB.First(&afterRecovery, channel.Id).Error)
	cleared, err := EnableChannelModel(channel.Id, "m1")
	require.NoError(t, err)
	assert.False(t, cleared, "没有留痕就不该动 abilities —— 这正是矛盾状态会触发的误改")
}

// 多 key 渠道停用单把 key 时，渠道本身仍是启用态、abilities 原封不动，
// per-model 留痕必须保留。
//
// 清留痕的判据若读「传入的 status」而不是「handlerMultiKeyUpdate 跑完之后
// 实际的 channel.Status」，这条路径就会误清 —— 留痕没了、ability 却还关着，
// 变成与本次修复目标完全相反的反向矛盾。
func TestPerModelRecordsSurviveSingleKeyDisable(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-per-model",
		Key:    "key-a\nkey-b",
		Group:  "default",
		Models: "m1,m2",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModePolling,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)
	for _, m := range []string{"m1", "m2"} {
		require.NoError(t, DB.Create(&Ability{
			Group: "default", Model: m, ChannelId: channel.Id, Enabled: true,
		}).Error)
	}

	_, err := DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.NoError(t, err)

	// 只停用 key-a：渠道仍是启用态
	require.True(t, UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "上游拒绝该 key"))

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "只停用一把 key 不该改变渠道状态")
	assert.Contains(t, stored.ChannelInfo.ModelDisabledReason, "m1",
		"渠道仍启用，留痕必须保留 —— 清掉它会造出「留痕没了但 ability 还关着」的反向矛盾")
	assert.False(t, abilityEnabled(t, channel.Id, "m1"), "m1 仍应退出轮转")
	assert.True(t, abilityEnabled(t, channel.Id, "m2"), "m2 不该受影响")
}

// 渠道一直健康（从未整渠道禁用）时，留痕必须原样保留：恢复路径之外的任何
// 状态变更都不该动它。
func TestPerModelRecordsSurviveUnrelatedStatusChanges(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "healthy", []string{"m1", "m2"}, true)

	_, err := DisableChannelModel(channel.Id, "m1", "模型不存在")
	require.NoError(t, err)

	// 重复设置同一个状态：早退路径，不该清留痕
	require.False(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusEnabled, ""))

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Contains(t, stored.ChannelInfo.ModelDisabledReason, "m1",
		"渠道本就启用、无整渠道状态变更，留痕必须保留")
}

func TestPerModelDisableRejectsDegenerateInput(t *testing.T) {
	setupChannelStatusTest(t)
	channel := seedChannelWithModels(t, "degenerate", []string{"m1"}, true)

	for _, tc := range []struct {
		name      string
		channelID int
		model     string
	}{
		{"渠道 id 为 0", 0, "m1"},
		{"渠道 id 为负", -1, "m1"},
		{"模型名为空", channel.Id, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DisableChannelModel(tc.channelID, tc.model, "x")
			require.ErrorIs(t, err, ErrChannelModelNotServed)
		})
	}

	cleared, err := EnableChannelModel(0, "m1")
	require.NoError(t, err)
	assert.False(t, cleared)
}
