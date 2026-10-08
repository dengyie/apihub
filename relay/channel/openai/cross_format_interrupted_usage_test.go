package openai

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 跨格式的两个流式 handler（responses↔chat）以前在断流时一律 `return nil`，
// 于是断流前已转发给客户端的正文全部白送。它们现在按统一契约返回已交付部分的
// 用量，判据与其它 handler 一致：**累积到了正文才返回，否则 nil**。

func crossFormatTestSetup(t *testing.T) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
}

// responses → chat：只发了 delta、没有 response.completed 也没有 [DONE]。
func TestOaiResponsesToChatTruncatedStreamReturnsDeliveredUsage(t *testing.T) {
	crossFormatTestSetup(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"上游说了一半就断了，"}`,
		`data: {"type":"response.output_text.delta","delta":"但客户端全收到了。"}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.SetEstimatePromptTokens(8000)

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, err, "本用例的前提就是流确实断了")
	require.NotNil(t, usage, "断流已转发的正文必须随错误一起返回，否则客户端白拿一次输出")
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.Contains(t, recorder.Body.String(), "但客户端全收到了。", "前提：内容确实已经写给客户端")
}

// 反向守卫，也是上一版过收缺陷的形状：上游的错误同样以**数据帧**送达。只按
// 「收到了帧」判就会把这次失败按整段 prompt（这里 8000）收一遍全款。
func TestOaiResponsesToChatErrorEventOnlyIsNotBilled(t *testing.T) {
	crossFormatTestSetup(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"error","code":"server_error","message":"upstream overloaded"}`,
		``,
	}, "\n")

	c, _, resp, info := newResponsesChatTestContext(t, body, true)
	info.SetEstimatePromptTokens(8000)

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, err)
	assert.Nil(t, usage, "只收到一个 error 事件 = 客户端什么都没拿到，不该按 8000 token 计费")
}

// chat → responses：同样的两条。
func TestOaiChatToResponsesTruncatedStreamReturnsDeliveredUsage(t *testing.T) {
	crossFormatTestSetup(t)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"上游说了一半就断了，"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"但客户端全收到了。"},"finish_reason":null}]}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.SetEstimatePromptTokens(8000)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)

	require.NotNil(t, err, "本用例的前提就是流确实断了")
	require.NotNil(t, usage, "断流已转发的正文必须随错误一起返回，否则客户端白拿一次输出")
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.Contains(t, recorder.Body.String(), "但客户端全收到了。", "前提：内容确实已经写给客户端")
}

func TestOaiChatToResponsesErrorChunkOnlyIsNotBilled(t *testing.T) {
	crossFormatTestSetup(t)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"error":{"message":"upstream overloaded","type":"server_error"}}`,
		``,
	}, "\n")

	c, _, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.SetEstimatePromptTokens(8000)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)

	require.NotNil(t, err)
	assert.Nil(t, usage, "只收到一个 error 块 = 客户端什么都没拿到，不该按 8000 token 计费")
}
