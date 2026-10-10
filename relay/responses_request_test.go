package relay

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPrepareResponsesRequestStripsReasoningOnRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	info := &relaycommon.RelayInfo{
		RetryIndex: 1, // Indicates retry attempt
	}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeOpenAI,
		ApiType:     constant.ChannelTypeOpenAI,
	}

	rawInput := `[
		{"role": "user", "content": "ping"},
		{"type": "reasoning", "id": "rs_123", "encrypted_content": "gAAAAAB..."},
		{"role": "assistant", "content": "pong"}
	]`
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-6-luna",
		Input: json.RawMessage(rawInput),
	}

	adaptor, body, closer, prepErr := PrepareResponsesRequest(c, info, req)
	require.Nil(t, prepErr)
	require.NotNil(t, adaptor)
	require.NotNil(t, body)
	defer closer.Close()

	bodyBytes, err := io.ReadAll(body)
	require.NoError(t, err)

	inputStr := gjson.GetBytes(bodyBytes, "input").Raw
	assert.NotContains(t, inputStr, "encrypted_content")
	assert.NotContains(t, inputStr, "rs_123")
	assert.Contains(t, inputStr, "ping")
	assert.Contains(t, inputStr, "pong")
}

func TestPrepareResponsesRequestStripsReasoningOnContextFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	common.SetContextKey(c, constant.ContextKeyStripResponsesReasoning, true)

	info := &relaycommon.RelayInfo{
		RetryIndex: 0, // Initial attempt but flag is set
	}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeOpenAI,
		ApiType:     constant.ChannelTypeOpenAI,
	}

	rawInput := `[
		{"role": "user", "content": "ping"},
		{"type": "reasoning", "id": "rs_123", "encrypted_content": "gAAAAAB..."},
		{"role": "assistant", "content": "pong"}
	]`
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-6-luna",
		Input: json.RawMessage(rawInput),
	}

	adaptor, body, closer, prepErr := PrepareResponsesRequest(c, info, req)
	require.Nil(t, prepErr)
	require.NotNil(t, adaptor)
	require.NotNil(t, body)
	defer closer.Close()

	bodyBytes, err := io.ReadAll(body)
	require.NoError(t, err)

	inputStr := gjson.GetBytes(bodyBytes, "input").Raw
	assert.NotContains(t, inputStr, "encrypted_content")
	assert.NotContains(t, inputStr, "rs_123")
	assert.Contains(t, inputStr, "ping")
	assert.Contains(t, inputStr, "pong")
}
