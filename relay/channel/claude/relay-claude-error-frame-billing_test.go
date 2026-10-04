package claude

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

// TestClaudeErrorFrameOnlyIsNotBilled 上游只吐了一个错误事件就断了，**不能计费**。
//
// 这个形状是 RecordInterruptedUsage 守卫的真实反面：上游错误是以**数据帧**形式
// 送达的（`{"type":"error",...}`），所以 ReceivedResponseCount == 1。但客户端
// 一个字都没拿到，ResponseText2Usage 仍会按 GetEstimatePromptTokens() 估出**整段
// prompt** —— 于是这次失败的请求反过来收了用户一遍全款。
//
// 判据必须是「客户端确实收到了产出的内容」，不是「收到了一个帧」。这也与非流式
// 路径一致：非流式出错同样不计费。
func TestClaudeErrorFrameOnlyIsNotBilled(t *testing.T) {
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
	// 生产里 prompt 一定是从请求体解析出来的。这里补上，才能看出「按整段 prompt
	// 收费」的真实后果，而不是一个碰巧全 0 的 usage。
	info.SetEstimatePromptTokens(8000)

	body := "event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"

	_, newAPIError := ClaudeStreamHandler(c, &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	}, info)

	require.NotNil(t, newAPIError)
	assert.Nil(t, info.InterruptedStreamUsage,
		"只收到一个上游错误帧、没有任何产出内容，不得按整段 prompt 计费")
}
