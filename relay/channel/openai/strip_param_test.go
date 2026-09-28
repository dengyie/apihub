package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/loadbalancer"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
