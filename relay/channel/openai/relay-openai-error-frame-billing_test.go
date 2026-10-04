package openai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOaiErrorFrameOnlyIsNotBilled 上游只吐了一个错误帧就断了，**不能计费**。
//
// 错误是以**数据帧**形式送达的，所以 ReceivedResponseCount == 1；但客户端一个字
// 都没拿到。ResponseText2Usage 仍会按 GetEstimatePromptTokens() 估出整段 prompt，
// 于是这次失败的请求反过来收用户一遍全款。
//
// 判据必须是「客户端确实收到了产出内容」，不是「收到了一个帧」。
func TestOaiErrorFrameOnlyIsNotBilled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.1"},
	}
	info.SetEstimatePromptTokens(8000)

	body := "data: {\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.NotNil(t, newAPIError)
	assert.Nil(t, usage,
		"只收到一个上游错误帧、没有任何产出内容，不得按整段 prompt 计费")
}
