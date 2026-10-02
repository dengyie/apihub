package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 上游反测活检测的触发特征是真实流量里不存在的形状：超短 prompt + max_tokens=16。
// 这组测试锁定「探针必须与真实流量同分布」，防止后来人把 "hi" 加回去。

const probeMinUserRunes = 120 // 生产 prompt p10 = 163 token，取一个保守的字符数下限

func TestProbePromptsAreRealistic(t *testing.T) {
	require.NotEmpty(t, probeUserPrompts)
	require.NotEmpty(t, probeSystemPrompts)
	seen := make(map[string]bool, len(probeUserPrompts))
	for _, p := range probeUserPrompts {
		trimmed := strings.TrimSpace(p)
		require.Greater(t, len([]rune(trimmed)), probeMinUserRunes,
			"探针 user prompt 过短，会被上游反测活识别: %q", p)
		seen[trimmed] = true
	}
	// 池内不允许出现历史上的占位词
	for banned := range seen {
		lower := strings.ToLower(banned)
		assert.NotEqual(t, "hi", lower)
		assert.NotEqual(t, "hello world", lower)
	}
	for _, s := range probeSystemPrompts {
		assert.Greater(t, len([]rune(s)), 20, "system prompt 过短: %q", s)
	}
}

func TestProbePromptsRotate(t *testing.T) {
	// 连续抽取必须覆盖到不止一条 prompt，否则多次探测指纹固定
	uniq := make(map[string]bool)
	for range 40 {
		uniq[probePick(probeUserPrompts)] = true
	}
	assert.Greater(t, len(uniq), 1, "40 次抽取只命中一条 prompt，随机化失效")
}

func TestProbeMaxTokensForModel(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  uint
	}{
		{"gpt-4.1", probeMaxTokens},
		{"deepseek-v4-flash", probeMaxTokens},
		{"gemini-3.8-flash-high", probeReasoningMaxTokens}, // 思考后缀要留足思考配额
		{"qwen3-max-thinking", probeReasoningMaxTokens},
		{"o3-mini", probeReasoningMaxTokens},
		{"o1-preview", probeReasoningMaxTokens},
	} {
		assert.Equalf(t, tt.want, probeMaxTokensForModel(tt.model), "model=%s", tt.model)
	}
}

// 所有聊天族端点的探针都必须满足：有 system、user 是真实长度、completion 上限不是 16。
func TestBuildRealisticChatProbeAllEndpoints(t *testing.T) {
	const model = "test-probe-model"
	for _, tt := range []struct {
		name    string
		ep      constant.EndpointType
		assertf func(t *testing.T, req dto.Request)
	}{
		{"anthropic", constant.EndpointTypeAnthropic, func(t *testing.T, req dto.Request) {
			r, ok := req.(*dto.ClaudeRequest)
			require.True(t, ok)
			sys, ok := r.System.(string)
			require.True(t, ok, "system 应为字符串")
			assert.Contains(t, probeSystemPrompts, sys)
			require.Len(t, r.Messages, 1)
			assert.Equal(t, "user", r.Messages[0].Role)
			assert.Greater(t, len([]rune(r.Messages[0].GetStringContent())), probeMinUserRunes)
			require.NotNil(t, r.MaxTokens)
			assert.NotEqual(t, uint(16), *r.MaxTokens)
		}},
		{"gemini", constant.EndpointTypeGemini, func(t *testing.T, req dto.Request) {
			r, ok := req.(*dto.GeminiChatRequest)
			require.True(t, ok)
			require.NotNil(t, r.SystemInstructions, "gemini 探针必须带 systemInstruction")
			require.Len(t, r.Contents, 1)
			assert.Equal(t, "user", r.Contents[0].Role)
			require.Len(t, r.Contents[0].Parts, 1)
			assert.Greater(t, len([]rune(r.Contents[0].Parts[0].Text)), probeMinUserRunes)
		}},
		{"openai responses", constant.EndpointTypeOpenAIResponse, func(t *testing.T, req dto.Request) {
			r, ok := req.(*dto.OpenAIResponsesRequest)
			require.True(t, ok)
			var msgs []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			require.NoError(t, json.Unmarshal(r.Input, &msgs))
			require.Len(t, msgs, 2)
			assert.Equal(t, "system", msgs[0].Role)
			assert.Equal(t, "user", msgs[1].Role)
			assert.Greater(t, len([]rune(msgs[1].Content)), probeMinUserRunes)
		}},
		{"openai chat", constant.EndpointTypeOpenAI, func(t *testing.T, req dto.Request) {
			r, ok := req.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			require.Len(t, r.Messages, 2)
			assert.Equal(t, "system", r.Messages[0].Role)
			assert.Equal(t, "user", r.Messages[1].Role)
			assert.Greater(t, len([]rune(r.Messages[1].StringContent())), probeMinUserRunes)
			require.NotNil(t, r.MaxTokens)
			assert.NotEqual(t, uint(16), *r.MaxTokens)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, isStream := range []bool{false, true} {
				req := buildRealisticChatProbe(model, tt.ep, isStream)
				require.NotNil(t, req)
				tt.assertf(t, req)
			}
		})
	}
}

// 非聊天端点的占位内容也必须是真实内容。
func TestNonChatProbePayloads(t *testing.T) {
	assert.Greater(t, len([]rune(probeEmbeddingInput)), 80)
	assert.Greater(t, len([]rune(probeImagePrompt)), 40)
	assert.Greater(t, len([]rune(probeRerankQuery)), 20)
	require.NotEmpty(t, probeRerankDocs)
	for _, d := range probeRerankDocs {
		s, ok := d.(string)
		require.True(t, ok)
		assert.Greater(t, len([]rune(s)), 40)
	}

	req := buildTestRequest("text-embedding-3-small", string(constant.EndpointTypeEmbeddings), nil, false)
	er, ok := req.(*dto.EmbeddingRequest)
	require.True(t, ok)
	assert.Equal(t, []any{probeEmbeddingInput}, er.Input)
}
