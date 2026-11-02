package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiPreCommitErrorStopsAndTriggersRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-1-flash-1017"},
	}

	// Chunk 0 has an unparseable malformed JSON payload that cannot be unmarshaled
	body := "data: {malformed json chunk}\n\ndata: [DONE]\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.NotNil(t, newAPIError, "malformed chunk 0 should return error")
	assert.Nil(t, usage)

	// Invariant: Because chunk 0 failed before commit, nothing was flushed to client
	assert.False(t, c.Writer.Written(), "c.Writer.Written() must remain false so retry can engage")
	assert.True(t, loadbalancer.IsStreamBroken(newAPIError), "error must be classified as StreamBroken")

	// Retry decision must be "retry"
	decision := service.DecideRelayRetry(c, newAPIError, 3)
	assert.Equal(t, "retry", decision.Action)
	assert.Equal(t, "stream_broken", decision.Reason)
}

func TestOaiStreamHandler_ArrayThinkingNormalized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-1-flash-1017"},
	}

	// Real incident chunk from DeepSeek upstream
	chunk0 := `data: {"id":"953d65ca8d6043b3aef2e91783888f48","object":"chat.completion.chunk","created":1793563373,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"All"}],"closed":true}]},"finish_reason":null}]}`
	chunk1 := `data: {"id":"953d65ca8d6043b3aef2e91783888f48","object":"chat.completion.chunk","created":1793563374,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"content":" done"},"finish_reason":null}]}`
	chunk2 := `data: {"id":"953d65ca8d6043b3aef2e91783888f48","object":"chat.completion.chunk","created":1793563375,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	chunkDone := `data: [DONE]`

	body := strings.Join([]string{chunk0, chunk1, chunk2, chunkDone}, "\n\n") + "\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.Nil(t, newAPIError, "normalized stream should process without error")
	require.NotNil(t, usage)

	output := w.Body.String()
	assert.Contains(t, output, `"reasoning_content":"All"`)
	assert.Contains(t, output, `"content":" done"`)

	// Ensure no array content was leaked to client
	assert.NotContains(t, output, `"content":[{"type":"thinking"`)

	// Ensure each chunk in the output parses as valid JSON with string/null content
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") || strings.Contains(line, "[DONE]") {
			continue
		}
		jsonStr := strings.TrimSpace(line[5:])
		var parsed struct {
			Choices []struct {
				Delta struct {
					Content          any `json:"content"`
					ReasoningContent any `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		err := json.Unmarshal([]byte(jsonStr), &parsed)
		require.NoError(t, err, "output chunk must be valid JSON: %s", jsonStr)
		for _, choice := range parsed.Choices {
			if choice.Delta.Content != nil {
				_, isString := choice.Delta.Content.(string)
				assert.True(t, isString, "delta.content must be string, not array")
			}
			if choice.Delta.ReasoningContent != nil {
				_, isString := choice.Delta.ReasoningContent.(string)
				assert.True(t, isString, "delta.reasoning_content must be string")
			}
		}
	}
}

func TestOaiStreamHandler_EmptyStructFrameTriggersSafeFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-1-flash-1017"},
	}

	// Exact incident stream where upstream hung and then emitted a zero-valued struct before [DONE]
	body := "data: {\"id\":\"\",\"object\":\"\",\"created\":0,\"model\":\"\",\"system_fingerprint\":null,\"choices\":null,\"usage\":null}\n\ndata: [DONE]\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.NotNil(t, newAPIError, "empty stream must return error to trigger failover")
	assert.Nil(t, usage)

	// Invariant 1: Nothing was flushed to client, response was not committed
	assert.False(t, c.Writer.Written(), "c.Writer.Written() must remain false so load balancer failover can engage")

	// Invariant 2: Client output body must not contain the junk chunk
	output := w.Body.String()
	assert.NotContains(t, output, `"choices":null`)

	// Invariant 3: Retry decision engages
	decision := service.DecideRelayRetry(c, newAPIError, 3)
	assert.Equal(t, "retry", decision.Action)
	assert.Equal(t, "empty_stream", decision.Reason)
}

// 流中错误帧（post-commit，后继还有数据帧时）会经 HandleStreamFormat 透传给客户端：
// code/type 必须在透传前被清洗，否则 insufficient_quota 直达客户端 SDK，触发
// retryable:false 甚至禁用账户。message 必须原样保留，它既是给用户的原始信息，
// 也是 loadbalancer 限流分类（IsUpstreamRateLimitError）的依据。
func TestOaiStreamHandler_MidStreamErrorFrameSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-1-flash-1017"},
	}

	// 内容 → 错误帧（CPA 限流错配 code: insufficient_quota）→ 内容 → [DONE]。
	// 错误帧位于流中，必然被转发给客户端。
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"error":{"message":"inference exceeds tpm/rpm limit","type":"rate_limit_error","code":"insufficient_quota"}}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.Nil(t, newAPIError, "post-commit 软错误不得中断流收尾")
	require.NotNil(t, usage)

	output := w.Body.String()
	assert.NotContains(t, output, `"insufficient_quota"`, "流中错误帧的误导性 code 绝不能透传给客户端")
	assert.Contains(t, output, `"rate_limit_exceeded"`, "带限流措辞的错误帧必须清洗为 rate_limit_exceeded")
	assert.Contains(t, output, "inference exceeds tpm/rpm limit", "message 必须原样保留")
	assert.Contains(t, output, `"content":"hello"`, "错误帧之前的正文必须已送达")
}

// 错误帧作为流的最后一帧时同样必须送达客户端（清洗后）：不能按「无 choices 内容」
// 抑制，否则流以错误收尾时客户端看到的是一次看似完整、实则截断的回答，SDK 无从重试。
// 这与流中错误帧（后继还有数据帧、经下一次回调透传）形成一致行为。
func TestOaiStreamHandler_LastFrameErrorDelivered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-1-flash-1017"},
	}

	// 内容 → 错误帧（最后一帧）→ [DONE]
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial answer"}}]}`,
		`data: {"error":{"message":"upstream exploded mid-answer","type":"server_error","code":"insufficient_quota"}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	})

	require.Nil(t, newAPIError)
	require.NotNil(t, usage)

	output := w.Body.String()
	assert.NotContains(t, output, `"insufficient_quota"`, "末帧错误帧的误导性 code 同样不能透传")
	assert.Contains(t, output, `"upstream_error"`, "末帧错误帧必须清洗后送达客户端")
	assert.Contains(t, output, "upstream exploded mid-answer", "message 必须原样保留")
	assert.Contains(t, output, `"content":"partial answer"`, "错误前的正文必须已送达")
}
