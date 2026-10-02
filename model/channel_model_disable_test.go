package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
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

// 清单只收「渠道启用、但这个模型被关着」的组合；渠道级禁用会把该渠道所有
// abilities 一起置灰，混进来就分不清两种完全不同的故障了。
func TestListChannelModelDisabledExcludesChannelLevelDisable(t *testing.T) {
	setupChannelStatusTest(t)
	perModel := seedChannelWithModels(t, "per-model", []string{"m1", "m2"}, true)
	_, err := DisableChannelModel(perModel.Id, "m1", "模型不存在")
	require.NoError(t, err)

	wholeChannel := seedChannelWithModels(t, "whole", []string{"m1"}, true)
	// 走真实的整渠道禁用路径：status 与 abilities 是一起改的，只翻 abilities
	// 造出来的状态在生产里并不存在，那样测的就不是筛选逻辑本身了。
	require.True(t, UpdateChannelStatus(wholeChannel.Id, "", common.ChannelStatusAutoDisabled, "余额不足"))

	list, err := ListChannelModelDisabled()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, perModel.Id, list[0].ChannelId)
	assert.Equal(t, "m1", list[0].Model)
	assert.Equal(t, "per-model", list[0].ChannelName)
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
