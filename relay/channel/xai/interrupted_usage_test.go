package xai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// xAI 的 chat/completions 流和 OpenAI 同形：finish_reason（或 [DONE]）是收尾
// 标记，两者都没有就 EOF，在 RequireTerminal 下判成断流。
//
// xAIStreamHandler 原来在断流时 `return nil, streamErr`，把断流前已写给客户端的
// 正文丢掉 —— 用户拿到了整段输出却不付钱。这里锁住新契约：**有产出就随错误一起
// 返回用量，没有产出就返回 nil**。

func xaiCase(t *testing.T, body string) (*gin.Context, *relaycommon.RelayInfo, *http.Response) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "grok-4",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "grok-4"},
	}
	info.SetEstimatePromptTokens(8000)

	return c, info, &http.Response{Body: io.NopCloser(bytes.NewReader([]byte(body)))}
}

const xaiTruncatedBody = "data: {\"id\":\"c1\",\"model\":\"grok-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"上游说了一半就断了，\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"model\":\"grok-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"但客户端全收到了。\"}}]}\n\n"

func TestXaiTruncatedStreamReturnsDeliveredUsage(t *testing.T) {
	c, info, resp := xaiCase(t, xaiTruncatedBody)

	usage, newAPIError := xAIStreamHandler(c, info, resp)

	require.NotNil(t, newAPIError, "本用例的前提就是流确实断了")
	require.NotNil(t, usage,
		"断流已送达的内容必须随错误一起返回，否则客户端白拿一次输出")
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.Greater(t, usage.TotalTokens, 0)
}

// 反向守卫，也是上一版过收缺陷的形状：上游的错误是以**数据帧**送达的，
// ReceivedResponseCount 会 +1。只按「收到了帧」判就会把这次失败按整段 prompt
// （这里 8000）收一遍全款。零产出必须返回 nil。
func TestXaiErrorFrameOnlyIsNotBilled(t *testing.T) {
	body := "data: {\"id\":\"c1\",\"model\":\"grok-4\",\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n"
	c, info, resp := xaiCase(t, body)

	usage, newAPIError := xAIStreamHandler(c, info, resp)

	require.NotNil(t, newAPIError)
	assert.Nil(t, usage,
		"只收到一个错误帧 = 客户端什么都没拿到，不该按 8000 token 的 prompt 计费")
}
