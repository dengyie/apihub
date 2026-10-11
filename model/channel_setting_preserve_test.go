package model

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 面板保存渠道会用它认识的字段整体重写 setting JSON；后端已认识但前端不渲染
// 的开关（dto.PanelHiddenSettingKeys，如 v29.31 的 reasoning_as_content）不在
// 面板字段里，会被静默抹掉。2026-10-11 生产实证：手工 json_set 的开关全部蒸
// 发，「思考被逐词切块」复发。这里钉住 Update() 的回填行为。

func TestUpdatePreservesHiddenSettingKeys(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{Name: "preserve-hidden-setting", Key: "k", Status: common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{"reasoning_as_content":true,"proxy":"http://old:1080"}`)}
	require.NoError(t, DB.Create(&channel).Error)

	// 模拟面板保存：前端不认识 reasoning_as_content，发来的 setting 里没有它
	panelUpdate := Channel{Id: channel.Id, Name: "preserve-hidden-setting", Key: "k",
		Status:  common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{"proxy":"http://new:1080"}`)}
	require.NoError(t, panelUpdate.Update())

	var row Channel
	require.NoError(t, DB.First(&row, "id = ?", channel.Id).Error)
	setting := row.GetSetting()
	assert.True(t, setting.ReasoningAsContent,
		"面板不渲染的后端开关不得被整行更新蒸发——这正是 2026-10-11 生产复发的原因")
	assert.Equal(t, "http://new:1080", setting.Proxy, "面板自己拥有的键照常更新")

	var raw map[string]any
	require.NoError(t, common.UnmarshalJsonStr(*row.Setting, &raw))
	assert.Contains(t, raw, "proxy")
}

func TestUpdateRespectsExplicitHiddenSettingValue(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{Name: "explicit-hidden-setting", Key: "k", Status: common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{"reasoning_as_content":true}`)}
	require.NoError(t, DB.Create(&channel).Error)

	// 显式提供新值（哪怕 false）必须以新值为准：API/SQL 之外的第三条改写路径
	panelUpdate := Channel{Id: channel.Id, Name: "explicit-hidden-setting", Key: "k",
		Status:  common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{"reasoning_as_content":false}`)}
	require.NoError(t, panelUpdate.Update())

	var row Channel
	require.NoError(t, DB.First(&row, "id = ?", channel.Id).Error)
	assert.False(t, row.GetSetting().ReasoningAsContent, "显式关闭必须生效，回填不得覆盖调用方的明确意图")
}

func TestUpdateSurvivesUnparseableStoredSetting(t *testing.T) {
	setupChannelStatusTest(t)

	// 库中旧值不可解析：合并放弃、新值照写，更新不得失败
	channel := Channel{Name: "broken-stored-setting", Key: "k", Status: common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{not-json`)}
	require.NoError(t, DB.Create(&channel).Error)

	panelUpdate := Channel{Id: channel.Id, Name: "broken-stored-setting", Key: "k",
		Status:  common.ChannelStatusEnabled,
		Setting: common.GetPointer(`{"proxy":"http://n:1080"}`)}
	require.NoError(t, panelUpdate.Update(), "旧值不可解析时不得阻断更新")

	var row Channel
	require.NoError(t, DB.First(&row, "id = ?", channel.Id).Error)
	assert.Contains(t, *row.Setting, "proxy")
}

// TestPanelHiddenSettingKeysAreRealFields 是登记表的守卫：列表里的键必须是
// dto.ChannelSettings 的真实 JSON 字段。列表与 struct 漂移（改名/拼错）时先红。
func TestPanelHiddenSettingKeysAreRealFields(t *testing.T) {
	for _, key := range dto.PanelHiddenSettingKeys {
		s := dto.ChannelSettings{}
		b, err := common.Marshal(s)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, common.UnmarshalJsonStr(string(b), &m))
		// omitempty 的零值字段不会出现在序列化结果里——用 set 一个非零值再序列化的方式验证
		require.True(t, settingKeyExistsInJSON(key),
			"PanelHiddenSettingKeys 里的键 %q 不是 dto.ChannelSettings 的已知 JSON 字段——struct 改名后登记表没跟上", key)
	}
}

func settingKeyExistsInJSON(key string) bool {
	s := dto.ChannelSettings{ReasoningAsContent: true, ForceFormat: true, ThinkingToContent: true,
		PassThroughBodyEnabled: true, ResponsesWebSocketEnabled: true, SystemPromptOverride: true,
		ThinkTagToReasoning: true}
	b, err := common.Marshal(s)
	if err != nil {
		return false
	}
	var m map[string]any
	if err := common.UnmarshalJsonStr(string(b), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
