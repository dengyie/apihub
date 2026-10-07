package openai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/constant"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chat/completions 流的收尾标记是 choices[].finish_reason（或 data: [DONE]）。
// 两者都没有就 EOF，在本函数声明的 RequireTerminal 下就是断流 → StreamBrokenError。
//
// 断流时最后一帧的 usage 常常也没到，OaiStreamHandler 于是按已累积的正文估算
// 一个 usage，**连同错误一起返回**；调用方拿到后转交 controller 结算。若这里
// 返回 nil，compatible_handler 看到 err != nil 直接 return，走不到
// PostTextConsumeQuota —— 客户端拿到了整段输出却不用付钱。
func oaiTruncatedStream(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *http.Response) {
	t.Helper()
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
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.1",
		},
	}

	// 两帧正文，没有 finish_reason、没有 [DONE]、没有 usage —— 典型的中途断流。
	body := "data: {\"id\":\"c1\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"这是一段被上游中途掐断的输出，\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"客户端已经完整收到了它。\"}}]}\n\n"

	return c, info, &http.Response{Body: io.NopCloser(bytes.NewReader([]byte(body)))}
}

// TestOaiTruncatedStreamKeepsInterruptedUsage 断流已送达的内容必须照常结算。
func TestOaiTruncatedStreamKeepsInterruptedUsage(t *testing.T) {
	c, info, resp := oaiTruncatedStream(t)

	usage, newAPIError := OaiStreamHandler(c, info, resp)

	require.NotNil(t, newAPIError, "本用例的前提就是流确实断了")
	require.NotNil(t, usage,
		"断流时必须把已累积的用量连同错误一起返回，由调用方转交 controller 结算，否则客户端白拿一次输出")
	assert.Greater(t, usage.CompletionTokens, 0,
		"已经送达客户端的文本量必须体现在结算里")
	assert.Greater(t, usage.TotalTokens, 0)
}

// TestOaiEmptyFailedStreamIsNotBilled 一无所获的失败不该产生账单。
//
// 反向守卫：返回的 usage 只在客户端确实收到过东西时才非 nil。纯失败按 0
// 计费才对 —— 否则上游反复 5xx 拖到重试耗尽，反而凭空收用户钱。
func TestOaiEmptyFailedStreamIsNotBilled(t *testing.T) {
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

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader(nil)),
	})

	require.NotNil(t, newAPIError)
	assert.Nil(t, usage, "客户端什么都没收到，不该产生待结算用量")
}
