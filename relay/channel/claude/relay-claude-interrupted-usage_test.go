package claude

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/constant"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude 流的正常收尾标记是 message_stop。缺了它，RequireTerminal 判下的 EOF
// 就是断流 → StreamBrokenError → 502 → 换渠道重试并熔断。
//
// 断流时 message_delta（带 output_tokens）通常也没到，所以 claudeInfo.Usage 里
// 只有 message_start 给的 input_tokens，输出侧是空的。此时 ClaudeStreamHandler
// 直接 `return nil, streamErr`，把已累积的用量丢掉；而 claude_handler 看到
// err != nil 就 return，走不到 PostTextConsumeQuota —— 客户端拿到了文本却不用
// 付钱，上游的账却已经记上了。
func claudeTruncatedStream(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *http.Response) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-5",
		RelayFormat:     types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-sonnet-4-5",
		},
	}

	// 有 message_start（给出 input_tokens）和一段正文，但没有 message_delta /
	// message_stop —— 典型的「上游吐到一半就断了」。
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":120,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"这是一段被上游中途掐断的输出，客户端已经完整收到了它。"}}` + "\n\n"

	return c, info, &http.Response{Body: io.NopCloser(bytes.NewReader([]byte(body)))}
}

// TestClaudeTruncatedStreamKeepsInterruptedUsage 断流已送达的内容必须照常结算。
func TestClaudeTruncatedStreamKeepsInterruptedUsage(t *testing.T) {
	c, info, resp := claudeTruncatedStream(t)

	usage, newAPIError := ClaudeStreamHandler(c, resp, info)

	require.NotNil(t, newAPIError, "本用例的前提就是流确实断了")
	require.NotNil(t, usage,
		"断流时必须把已累积的用量连同错误一起返回，由调用方转交 controller 结算，否则客户端白拿一次输出")
	assert.EqualValues(t, 120, usage.PromptTokens,
		"message_start 给的 input_tokens 必须保住")
	assert.Greater(t, usage.CompletionTokens, 0,
		"message_delta 没到，输出侧要从已累积的文本估算，不能留 0")
}

// TestClaudeEmptyFailedStreamIsNotBilled 一无所获的失败不该产生账单。
//
// 反向守卫：返回的 usage 只在客户端确实收到过东西时才非 nil。纯失败按 0
// 计费才对 —— 否则上游反复 5xx 拖到重试耗尽，反而凭空收用户钱。
func TestClaudeEmptyFailedStreamIsNotBilled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-5",
		RelayFormat:     types.RelayFormatClaude,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-5"},
	}

	usage, newAPIError := ClaudeStreamHandler(c, &http.Response{
		Body: io.NopCloser(bytes.NewReader(nil)),
	}, info)

	require.NotNil(t, newAPIError)
	assert.Nil(t, usage, "客户端什么都没收到，不该产生待结算用量")
}
