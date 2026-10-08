package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequest_DoesNotMutateOriginalRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: true})

	// Configure channel 9999 to strip tools and reasoning_effort
	channelID := 9999
	loadbalancer.SetParamStripConfig(map[int][]string{
		channelID: {"tools", "reasoning_effort"},
	})

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         channelID,
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "gpt-4o",
		},
		OriginModelName: "gpt-4o",
	}

	origTools := []dto.ToolCallRequest{
		{
			Type: "function",
			Function: dto.FunctionRequest{
				Name: "get_weather",
			},
		},
	}
	origReasoning := "high"

	origRequest := &dto.GeneralOpenAIRequest{
		Model:           "gpt-4o",
		ReasoningEffort: origReasoning,
		Tools:           origTools,
	}

	adaptor := &Adaptor{}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, origRequest)
	require.NoError(t, err)
	require.NotNil(t, converted)

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)

	// Converted request should have stripped tools and reasoning_effort
	assert.Nil(t, convertedReq.Tools, "converted request tools should be stripped")
	assert.Empty(t, convertedReq.ReasoningEffort, "converted request reasoning_effort should be stripped")

	// Original request passed by caller MUST remain completely intact for cross-channel retries!
	assert.NotNil(t, origRequest.Tools, "original request tools must not be mutated")
	assert.Len(t, origRequest.Tools, 1, "original request tools must keep elements")
	assert.Equal(t, "high", origRequest.ReasoningEffort, "original reasoning_effort must not be mutated")
}

func TestConvertOpenAIRequest_InjectsReasoningContentForDeepSeekAssistantMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         120,
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "deepseek-v4-flash",
		},
		OriginModelName: "deepseek-v4-flash",
	}

	origMessages := []dto.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"}, // ReasoningContent is nil
		{Role: "user", Content: "what is 2+2?"},
	}

	origRequest := &dto.GeneralOpenAIRequest{
		Model:    "deepseek-v4-flash",
		Messages: origMessages,
	}

	adaptor := &Adaptor{}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, origRequest)
	require.NoError(t, err)
	require.NotNil(t, converted)

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)

	// Converted request must have non-nil empty string for assistant reasoning_content
	require.NotNil(t, convertedReq.Messages[1].ReasoningContent, "assistant message in converted request must have reasoning_content")
	assert.Equal(t, "", *convertedReq.Messages[1].ReasoningContent, "assistant reasoning_content should be empty string")

	// Original request must NOT be mutated
	assert.Nil(t, origRequest.Messages[1].ReasoningContent, "original request assistant message must remain nil")
}

func TestConvertOpenAIRequest_PreservesExplicitReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         120,
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "deepseek-v4-flash",
		},
		OriginModelName: "deepseek-v4-flash",
	}

	existingReasoning := "step by step thought process"
	origMessages := []dto.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ReasoningContent: &existingReasoning},
		{Role: "user", Content: "tell me more"},
	}

	origRequest := &dto.GeneralOpenAIRequest{
		Model:    "deepseek-v4-flash",
		Messages: origMessages,
	}

	adaptor := &Adaptor{}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, origRequest)
	require.NoError(t, err)

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, convertedReq.Messages[1].ReasoningContent)
	assert.Equal(t, "step by step thought process", *convertedReq.Messages[1].ReasoningContent)
}

func TestConvertOpenAIRequest_NonDeepSeekWithoutReasoningKeepsNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         1,
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "gpt-4o",
		},
		OriginModelName: "gpt-4o",
	}

	origMessages := []dto.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	origRequest := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4o",
		Messages: origMessages,
	}

	adaptor := &Adaptor{}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, origRequest)
	require.NoError(t, err)

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	assert.Nil(t, convertedReq.Messages[1].ReasoningContent, "non-deepseek request without reasoning should leave reasoning_content nil")
}

func TestConvertOpenAIRequest_NormalizesReasoningEffortMaxAndXHigh(t *testing.T) {
	for _, effort := range []string{"max", "MAX", "xhigh", "XHigh"} {
		t.Run(effort, func(t *testing.T) {
			origRequest := &dto.GeneralOpenAIRequest{
				Model:           "deepseek-v4-flash",
				ReasoningEffort: effort,
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelId:         19,
					ChannelType:       constant.ChannelTypeOpenAI,
					UpstreamModelName: "deepseek-v4-flash",
				},
				OriginModelName: "deepseek-v4-flash",
			}
			adaptor := &Adaptor{}
			converted, err := adaptor.ConvertOpenAIRequest(nil, info, origRequest)
			require.NoError(t, err)
			convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			assert.Equal(t, "high", convertedReq.ReasoningEffort)
			assert.Equal(t, effort, origRequest.ReasoningEffort, "original must not be mutated")
		})
	}
}
