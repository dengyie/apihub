package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这组测试锁定「测活多模型 / 单模型失败不连坐整条渠道」这条不变式。
//
// 背景是生产实测 #145：渠道声明 10 个模型，测活每轮只测 models[0]，那一次
// 返回 "The model service rejected this request"（401），整条渠道被摘，
// 另外 9 个从未被测过的模型陪葬。只测一个模型时，「这个模型坏了」与
// 「这条渠道的凭据失效」在证据上完全无法区分 —— 而两者的正确处置范围
// 相差整整一条渠道。

// channel.Models 在 DB 里是逗号分隔的字符串，不是数组 —— 这里照真实存储形状
// 构造，否则测试会绕过 GetModels 的切分逻辑，测出一条线上不存在的路径。
func channelWithModels(models ...string) *model.Channel {
	c := &model.Channel{}
	c.Models = strings.Join(models, ",")
	return c
}

func TestHealthCheckFollowupModel(t *testing.T) {
	cases := []struct {
		name      string
		models    []string
		firstTest string
		wantModel string
		wantOK    bool
	}{
		{
			name:      "多模型：换一个不同的模型复核",
			models:    []string{"gpt-4o", "gpt-4o-mini", "claude-3-5"},
			firstTest: "gpt-4o",
			wantModel: "gpt-4o-mini",
			wantOK:    true,
		},
		{
			name:      "单模型渠道：没有可复核的对象",
			models:    []string{"gpt-4o"},
			firstTest: "gpt-4o",
			wantOK:    false,
		},
		{
			name:      "零模型渠道：不复核",
			models:    nil,
			firstTest: "",
			wantOK:    false,
		},
		{
			name:      "跳过空白项",
			models:    []string{"gpt-4o", "  ", "gpt-4o-mini"},
			firstTest: "gpt-4o",
			wantModel: "gpt-4o-mini",
			wantOK:    true,
		},
		{
			name:      "跳过与首测相同的模型",
			models:    []string{"gpt-4o", "gpt-4o", "gpt-4o-mini"},
			firstTest: "gpt-4o",
			wantModel: "gpt-4o-mini",
			wantOK:    true,
		},
		{
			name:      "全是重复的首测模型：不复核",
			models:    []string{"gpt-4o", "gpt-4o"},
			firstTest: "gpt-4o",
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := healthCheckFollowupModel(channelWithModels(tc.models...), tc.firstTest)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantModel, got)
		})
	}
}

// TestHealthCheckFollowupSkipsFirstTestModel 单独钉住一条：复核模型绝不能
// 与首测模型相同。同一条渠道、同一个模型、几乎同一时刻的第二次探测不构成
// 新证据，只会把探针成本翻倍却换不来任何信息 —— 恰恰是它在制造「已复核」
// 的假象。
func TestHealthCheckFollowupSkipsFirstTestModel(t *testing.T) {
	got, ok := healthCheckFollowupModel(
		channelWithModels("gpt-4o", "gpt-4o", "gpt-4o"),
		"gpt-4o",
	)
	assert.False(t, ok, "没有真正的第二个模型时不该复核")
	assert.Empty(t, got)

	got, ok = healthCheckFollowupModel(
		channelWithModels("gpt-4o", "gpt-4o-mini"),
		"gpt-4o",
	)
	require.True(t, ok)
	assert.NotEqual(t, "gpt-4o", got, "复核模型必须与首测不同")
}

// TestHealthCheckFollowupTrimsWhitespace 复核模型名会被当作 testModel 传进
// testChannel，那里虽会 TrimSpace，但带空白的名字会让「首测模型」的比较在
// 这里就判不等，从而选出一个其实测过的模型。
func TestHealthCheckFollowupTrimsWhitespace(t *testing.T) {
	got, ok := healthCheckFollowupModel(
		channelWithModels("gpt-4o", " gpt-4o-mini "),
		"gpt-4o",
	)
	require.True(t, ok)
	assert.Equal(t, "gpt-4o-mini", got, "复核模型名必须已去空白")
}

// TestHealthCheckFollowupPicksDeterministicallySame 每轮必须选出同一个复核
// 模型，否则「这条渠道的第二个模型一直有病」会被测成忽好忽坏，两边都不摘，
// 等于把 #145 那种慢性失效率变成没有任何告警的静默问题。
func TestHealthCheckFollowupPicksDeterministicallySame(t *testing.T) {
	c := channelWithModels("gpt-4o", "gpt-4o-mini", "claude-3-5", "gemini-2.0")
	first, ok := healthCheckFollowupModel(c, "gpt-4o")
	require.True(t, ok)
	for i := 0; i < 5; i++ {
		again, ok := healthCheckFollowupModel(c, "gpt-4o")
		require.True(t, ok)
		assert.Equal(t, first, again, "复核模型的选择必须是确定的")
	}
}