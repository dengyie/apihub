package openai

import (
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty effort remains empty",
			input:    "",
			expected: "",
		},
		{
			name:     "low remains low",
			input:    "low",
			expected: "low",
		},
		{
			name:     "medium remains medium",
			input:    "medium",
			expected: "medium",
		},
		{
			name:     "high remains high",
			input:    "high",
			expected: "high",
		},
		{
			name:     "max normalized to high",
			input:    "max",
			expected: "high",
		},
		{
			name:     "MAX with whitespace normalized to high",
			input:    "  MAX  ",
			expected: "high",
		},
		{
			name:     "xhigh normalized to high",
			input:    "xhigh",
			expected: "high",
		},
		{
			name:     "minimal remains minimal",
			input:    "minimal",
			expected: "minimal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &dto.GeneralOpenAIRequest{ReasoningEffort: tc.input}
			NormalizeReasoningEffort(req)
			assert.Equal(t, tc.expected, req.ReasoningEffort)
		})
	}
}

func TestStripOpenAIParam(t *testing.T) {
	t.Run("strip tools and reasoning_effort", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{
			ReasoningEffort: "high",
			Tools: []dto.ToolCallRequest{
				{Type: "function"},
			},
		}
		StripOpenAIParam(req, "tools")
		assert.Nil(t, req.Tools)
		assert.Equal(t, "high", req.ReasoningEffort)

		StripOpenAIParam(req, "reasoning_effort")
		assert.Empty(t, req.ReasoningEffort)
	})

	t.Run("strip max_completion_tokens fallback to max_tokens", func(t *testing.T) {
		var maxTokensVal uint = 4096
		req := &dto.GeneralOpenAIRequest{
			MaxCompletionTokens: &maxTokensVal,
		}
		StripOpenAIParam(req, "max_completion_tokens")
		assert.Nil(t, req.MaxCompletionTokens)
		require.NotNil(t, req.MaxTokens)
		assert.Equal(t, uint(4096), *req.MaxTokens)
	})

	t.Run("strip max_tokens fallback to max_completion_tokens", func(t *testing.T) {
		var maxTokensVal uint = 2048
		req := &dto.GeneralOpenAIRequest{
			MaxTokens: &maxTokensVal,
		}
		StripOpenAIParam(req, "max_tokens")
		assert.Nil(t, req.MaxTokens)
		require.NotNil(t, req.MaxCompletionTokens)
		assert.Equal(t, uint(2048), *req.MaxCompletionTokens)
	})

	t.Run("strip reasoning_content does not mutate original slice until modified", func(t *testing.T) {
		rc := "reasoning text"
		origMsgs := []dto.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "there", ReasoningContent: &rc},
		}
		req := &dto.GeneralOpenAIRequest{
			Messages: origMsgs,
		}
		StripOpenAIParam(req, "reasoning_content")
		assert.Nil(t, req.Messages[1].ReasoningContent)
		// Original array untouched
		assert.Equal(t, "reasoning text", *origMsgs[1].ReasoningContent)
	})
}

func TestSanitizeAssistantReasoningHistory(t *testing.T) {
	t.Run("AgentRouter channel injects placeholder reasoning_content if nil", func(t *testing.T) {
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer"},
			{Role: "user", Content: "next"},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelBaseUrl:    "https://agentrouter.org",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		assert.Equal(t, "...", *req.Messages[1].ReasoningContent)
		// Check that original slice is not mutated
		assert.Nil(t, origMsgs[1].ReasoningContent)
	})

	t.Run("AgentRouter channel replaces empty reasoning_content with placeholder", func(t *testing.T) {
		emptyStr := ""
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer", ReasoningContent: &emptyStr},
			{Role: "user", Content: "next"},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelName:       "cpa-agentrouter-1",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		assert.Equal(t, "...", *req.Messages[1].ReasoningContent)
		// Check that original slice is not mutated
		assert.Equal(t, "", *origMsgs[1].ReasoningContent)
	})

	t.Run("Other standard channels keep original empty string when nil and do not inject placeholder", func(t *testing.T) {
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer"}, // nil
			{Role: "user", Content: "next"},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelBaseUrl:    "https://api.siliconflow.cn",
				ChannelName:       "siliconflow-main",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		// Standard channels MUST get empty string "", NOT "..." placeholder
		assert.Equal(t, "", *req.Messages[1].ReasoningContent)
		// Check that original slice is not mutated
		assert.Nil(t, origMsgs[1].ReasoningContent)
	})

	t.Run("Other standard channels do NOT overwrite empty reasoning_content with placeholder", func(t *testing.T) {
		emptyStr := ""
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer", ReasoningContent: &emptyStr},
			{Role: "user", Content: "next"},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelBaseUrl:    "https://api.deepseek.com",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		// Standard channels MUST preserve empty string "", NOT replace with "..."
		assert.Equal(t, "", *req.Messages[1].ReasoningContent)
	})

	t.Run("DeepSeek model copies reasoning to reasoning_content if present", func(t *testing.T) {
		reasoningText := "thought process"
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer", Reasoning: &reasoningText},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-reasoner",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-r1",
				ChannelBaseUrl:    "https://agentrouter.org",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		assert.Equal(t, "thought process", *req.Messages[1].ReasoningContent)
	})

	t.Run("AgentRouter channel enabled via ParamOverride string or int", func(t *testing.T) {
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer"},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelBaseUrl:    "https://custom-gateway.internal",
				ChannelName:       "custom-proxy",
				ParamOverride: map[string]any{
					"agentrouter_thinking_placeholder": "true",
				},
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		assert.Equal(t, "...", *req.Messages[1].ReasoningContent)
	})

	t.Run("DeepSeek model preserves valid reasoning_content if already present", func(t *testing.T) {
		existing := "original thinking process"
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer", ReasoningContent: &existing},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "deepseek-chat",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-ai/DeepSeek-V3",
				ChannelBaseUrl:    "https://agentrouter.org",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		require.NotNil(t, req.Messages[1].ReasoningContent)
		assert.Equal(t, "original thinking process", *req.Messages[1].ReasoningContent)
	})

	t.Run("Non-DeepSeek model strips reasoning_content and reasoning", func(t *testing.T) {
		rc := "some reasoning"
		r := "another reasoning"
		origMsgs := []dto.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "answer", ReasoningContent: &rc, Reasoning: &r},
		}
		req := &dto.GeneralOpenAIRequest{
			Model:    "gpt-4o",
			Messages: origMsgs,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "gpt-4o",
				ChannelBaseUrl:    "https://api.openai.com",
			},
		}
		SanitizeAssistantReasoningHistory(req, info)
		assert.Nil(t, req.Messages[1].ReasoningContent)
		assert.Nil(t, req.Messages[1].Reasoning)
		// Original slice not mutated
		assert.NotNil(t, origMsgs[1].ReasoningContent)
	})
}

func TestSanitizeDeveloperRole(t *testing.T) {
	t.Run("changes system role to developer when enabled", func(t *testing.T) {
		origMsgs := []dto.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hello"},
		}
		req := &dto.GeneralOpenAIRequest{Messages: origMsgs}
		SanitizeDeveloperRole(req, true)
		assert.Equal(t, "developer", req.Messages[0].Role)
		assert.Equal(t, "system", origMsgs[0].Role)
	})

	t.Run("does nothing when disabled", func(t *testing.T) {
		origMsgs := []dto.Message{
			{Role: "system", Content: "be helpful"},
		}
		req := &dto.GeneralOpenAIRequest{Messages: origMsgs}
		SanitizeDeveloperRole(req, false)
		assert.Equal(t, "system", req.Messages[0].Role)
	})
}

func TestStripUnsupportedParams(t *testing.T) {
	channelID := 8888
	loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: true})
	loadbalancer.SetParamStripConfig(map[int][]string{
		channelID: {"tools", "reasoning_effort"},
	})

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         channelID,
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "claude-3-7-sonnet",
		},
		OriginModelName: "claude-3-7-sonnet",
	}

	req := &dto.GeneralOpenAIRequest{
		Model:           "claude-3-7-sonnet",
		ReasoningEffort: "high",
		Tools: []dto.ToolCallRequest{
			{Type: "function"},
		},
	}

	StripUnsupportedParams(req, channelID, info.OriginModelName, info)
	assert.Nil(t, req.Tools)
	assert.Empty(t, req.ReasoningEffort)
}

func TestSanitizeModelCapabilitiesAndLimits(t *testing.T) {
	t.Run("swaps MaxTokens to MaxCompletionTokens when capability requires", func(t *testing.T) {
		tokens := uint(2048)
		req := &dto.GeneralOpenAIRequest{
			MaxTokens: &tokens,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelId:         100,
				UpstreamModelName: "o1-mini",
			},
		}
		caps := dto.OpenAIChatCapabilities{
			UseMaxCompletionTokens: true,
			SupportsTemperature:    false,
			SupportsTopP:           false,
			SupportsLogProbs:       false,
		}
		temp := 0.7
		topP := 0.9
		logProbs := true
		req.Temperature = &temp
		req.TopP = &topP
		req.LogProbs = &logProbs

		SanitizeModelCapabilitiesAndLimits(req, info, caps)

		assert.Nil(t, req.MaxTokens)
		require.NotNil(t, req.MaxCompletionTokens)
		assert.Equal(t, uint(2048), *req.MaxCompletionTokens)
		assert.Nil(t, req.Temperature)
		assert.Nil(t, req.TopP)
		assert.Nil(t, req.LogProbs)
	})
}

func TestPostIntentSanitizeRequest(t *testing.T) {
	t.Run("cleans up capabilities, developer role, and assistant reasoning history", func(t *testing.T) {
		rc := "prior reasoning"
		msgs := []dto.Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "solve this"},
			{Role: "assistant", Content: "step 1", ReasoningContent: &rc},
		}
		temp := 0.5
		req := &dto.GeneralOpenAIRequest{
			Model:       "gpt-4o",
			Messages:    msgs,
			Temperature: &temp,
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelId:         200,
				ChannelType:       constant.ChannelTypeOpenAI,
				UpstreamModelName: "gpt-4o",
			},
			OriginModelName: "gpt-4o",
		}

		PostIntentSanitizeRequest(req, info)

		// Non-deepseek removes reasoning_content
		assert.Nil(t, req.Messages[2].ReasoningContent)
		// gpt-4o keeps system role (not developer) and supports temperature
		assert.Equal(t, "system", req.Messages[0].Role)
		require.NotNil(t, req.Temperature)
		assert.Equal(t, 0.5, *req.Temperature)
	})

	t.Run("strips stream_options for non-OpenAI non-Azure channels", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{
			Model: "claude-3-5-sonnet",
			StreamOptions: &dto.StreamOptions{
				IncludeUsage: true,
			},
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelId:         300,
				ChannelType:       constant.ChannelTypeAnthropic,
				UpstreamModelName: "claude-3-5-sonnet",
			},
			OriginModelName: "claude-3-5-sonnet",
		}

		PostIntentSanitizeRequest(req, info)
		assert.Nil(t, req.StreamOptions, "非原生 OpenAI / Azure 渠道必须裁剪 StreamOptions")
	})

	t.Run("preserves stream_options for OpenAI channel", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{
			Model: "gpt-4o",
			StreamOptions: &dto.StreamOptions{
				IncludeUsage: true,
			},
		}
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelId:         301,
				ChannelType:       constant.ChannelTypeOpenAI,
				UpstreamModelName: "gpt-4o",
			},
			OriginModelName: "gpt-4o",
		}

		PostIntentSanitizeRequest(req, info)
		require.NotNil(t, req.StreamOptions, "原生 OpenAI 渠道应保留 StreamOptions")
		assert.True(t, req.StreamOptions.IncludeUsage)
	})
}

func TestSanitizeProtocolStreamOptions(t *testing.T) {
	t.Run("nil request does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			SanitizeProtocolStreamOptions(nil, constant.ChannelTypeAnthropic)
		})
	})

	t.Run("clears stream_options for third party channel", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{
			StreamOptions: &dto.StreamOptions{IncludeUsage: true},
		}
		SanitizeProtocolStreamOptions(req, constant.ChannelTypeZhipu)
		assert.Nil(t, req.StreamOptions)
	})

	t.Run("keeps stream_options for Azure", func(t *testing.T) {
		req := &dto.GeneralOpenAIRequest{
			StreamOptions: &dto.StreamOptions{IncludeUsage: true},
		}
		SanitizeProtocolStreamOptions(req, constant.ChannelTypeAzure)
		assert.NotNil(t, req.StreamOptions)
	})
}
