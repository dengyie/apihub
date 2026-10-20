package model

import (
	"testing"

	"github.com/dengyie/apihub/loadbalancer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParamStripRule 的落库后端是 loadbalancer.ParamStripStore 的实现。
// 这里钉住三件事：
//
//  1. 存进去能读回来（形状不丢：渠道、模型、参数名）；
//  2. 重复存同一���规则只有一行（幂等）；
//  3. 渠道被删除时规则跟着走（渠道 id 会被复用）。
//
// 最要紧的是「重启后规则还在」这件事本身：paramStripLearned 曾是纯内存
// map，网关一天重启 8 次，每次都把学到的规则清零，同一个渠道同一个模型的
// 同一个参数在每个新进程里都要先错一次再学一次（生产实测 2026-10-21
// 凌晨 #84 / #226 / #54 各重学了一次）。

func setupParamStripRuleTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Exec("DELETE FROM param_strip_rules").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM param_strip_rules")
	})
}

func TestParamStripStoreRoundTrip(t *testing.T) {
	setupParamStripRuleTest(t)

	store := &ParamStripStore{}
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 84,
		Model:     "gpt-5.3-codex",
		Params:    []string{"enable_thinking"},
	}))

	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, 84, rules[0].ChannelID)
	assert.Equal(t, "gpt-5.3-codex", rules[0].Model)
	assert.Equal(t, []string{"enable_thinking"}, rules[0].Params)
}

func TestParamStripStoreSaveIsIdempotent(t *testing.T) {
	setupParamStripRuleTest(t)

	store := &ParamStripStore{}
	rule := loadbalancer.ParamStripRule{
		ChannelID: 52,
		Model:     "gpt-6-luna",
		Params:    []string{"enable_thinking"},
	}
	// 同一个参数被反复遇到是最常见的情形：每次都写，但只能有一行。
	for i := 0; i < 5; i++ {
		require.NoError(t, store.SaveParamStripRule(rule))
	}

	var count int64
	require.NoError(t, DB.Model(&ParamStripRule{}).Where("channel_id = ? AND model = ?", 52, "gpt-6-luna").Count(&count).Error)
	assert.Equal(t, int64(1), count, "重复保存同一条规则必须只有一行")
}

func TestParamStripStoreMultipleParamsSamePair(t *testing.T) {
	setupParamStripRuleTest(t)

	store := &ParamStripStore{}
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 10,
		Model:     "deepseek-v4",
		Params:    []string{"reasoning_effort"},
	}))
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 10,
		Model:     "deepseek-v4",
		Params:    []string{"thinking"},
	}))

	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	require.Len(t, rules, 1, "同一 (渠道, 模型) 的多个参数应合并成一条规则")
	assert.ElementsMatch(t, []string{"reasoning_effort", "thinking"}, rules[0].Params)
}

func TestParamStripStoreDeleteForChannel(t *testing.T) {
	setupParamStripRuleTest(t)

	store := &ParamStripStore{}
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 272,
		Model:     "bge-m3",
		Params:    []string{"thinking"},
	}))
	require.NoError(t, store.DeleteParamStripRulesForChannel(272))

	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	assert.Empty(t, rules)
}

// TestParamStripRulesSurviveRestart 是本次修复的核心断言。
//
// 模拟一次进程重启：把规则写进库 → 把内存清空（等价于新进程的空 map）→
// 从库里读回 → 规则必须还在。
//
// 不做这一步的话，自动学习到的裁剪规则每次重启都要重新学一遍，而网关在
// 生产里一天要重启 8 次（蓝绿交接）。
func TestParamStripRulesSurviveRestart(t *testing.T) {
	setupParamStripRuleTest(t)

	// 第一次进程：学到两条规则并存进库。
	store := &ParamStripStore{}
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 84,
		Model:     "gpt-5.3-codex",
		Params:    []string{"enable_thinking"},
	}))
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: 226,
		Model:     "kimi-k3",
		Params:    []string{"thinking"},
	}))

	// 新进程：内存是空的，只有库。
	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	require.Len(t, rules, 2)

	byChannel := map[int]loadbalancer.ParamStripRule{}
	for _, r := range rules {
		byChannel[r.ChannelID] = r
	}
	assert.Equal(t, []string{"enable_thinking"}, byChannel[84].Params)
	assert.Equal(t, "gpt-5.3-codex", byChannel[84].Model)
	assert.Equal(t, []string{"thinking"}, byChannel[226].Params)
	assert.Equal(t, "kimi-k3", byChannel[226].Model)
}

// TestChannelDeleteClearsParamStripRules 钉住「渠道删除时规则跟着走」。
//
// 渠道 id 会被复用。不清规则的话，复用同一个 id 的新渠道会继承上一条渠道
// 学到的参数限制 —— 那些规则当初是为完全不同的上游模型观测到的。
func TestChannelDeleteClearsParamStripRules(t *testing.T) {
	setupParamStripRuleTest(t)

	channel := Channel{Name: "delete-clears-rules", Key: "k", Status: 1}
	require.NoError(t, DB.Create(&channel).Error)

	store := &ParamStripStore{}
	require.NoError(t, store.SaveParamStripRule(loadbalancer.ParamStripRule{
		ChannelID: channel.Id,
		Model:     "some-model",
		Params:    []string{"thinking"},
	}))

	require.NoError(t, channel.Delete())

	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	assert.Empty(t, rules, "渠道删除后它的裁剪规则必须一起清掉")
}
