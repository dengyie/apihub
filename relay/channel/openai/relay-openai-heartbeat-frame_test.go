package openai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/constant"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 兼容上游把 Anthropic 协议的保活心跳 `{"type":"ping"}` 混进 OpenAI 兼容流时，
// 网关绝不能原样透传给 chat.completions 客户端。
//
// 线上事故：严格校验的客户端（ZCode / Vercel AI SDK）把每个 data 帧都按
// 「choices 数组 or error 对象」的联合类型解析，`{"type":"ping"}` 两者都不满足，
// 整个 Turn 直接被判成 retryable=false 的失败：
//
//	Type validation failed: Value: {"type":"ping"} invalid_union
//
// 心跳是纯保活信号，没有任何可计费或可展示的语义，丢弃是唯一正确的处置：
// 既不能当正文计数（会把空流伪装成有内容的流，跳过换渠道重试），也不能转发。
func TestOaiStreamHandler_AnthropicPingFrameNotRelayed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3.8-flash-high",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.8-flash-high"},
	}

	// 内容 → 心跳 → 内容 → 收尾 → [DONE]：心跳夹在流中间，命中透传路径。
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"type":"ping"}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.Nil(t, newAPIError, "心跳帧不得中断流")
	require.NotNil(t, usage)

	output := w.Body.String()
	assert.NotContains(t, output, `"type":"ping"`, "Anthropic 心跳帧绝不能透传给 OpenAI 客户端")
	assert.Contains(t, output, `"content":"hello"`, "心跳之前的正文必须已送达")
	assert.Contains(t, output, `"content":" world"`, "心跳之后的正文必须已送达")
	assert.Contains(t, output, `"finish_reason":"stop"`, "收尾帧必须已送达")
}
