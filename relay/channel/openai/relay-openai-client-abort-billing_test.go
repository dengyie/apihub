package openai

import (
	"bytes"
	"context"
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

// 客户端在首字到达前放弃请求时，scanner 把它记成 client_gone 并**返回 nil**
// （见 stream_scanner.go 的 isClientGone 分支）——客户端主动放弃不是渠道故障，
// 不该触发换渠道重试。于是 handler 走的是成功路径，而成功路径的兜底在没有上游
// usage 帧时按 GetEstimatePromptTokens() 收整段 prompt。
//
// 后果是：上游一个 token 都没生成（生产记录里 frt 全是 -1000，即首字从未到达），
// 网关却按 3.6 万~12 万 token 的本地估算收了用户的钱。24 小时内 19 条。
//
// 这条用例锁住修复：零交付的放弃请求结算为 0。
func TestOaiClientAbortBeforeFirstTokenIsNotBilled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cancel() // 客户端已经走了
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.1"},
	}
	info.SetEstimatePromptTokens(36861)

	// 上游一个字都没吐就断了。
	usage, newAPIError := OaiStreamHandler(c, info, &http.Response{
		Body: io.NopCloser(bytes.NewReader(nil)),
	})

	require.Nil(t, newAPIError, "客户端主动放弃不算渠道故障，不应报错")
	require.NotNil(t, usage)
	assert.EqualValues(t, 0, usage.PromptTokens,
		"首字前放弃 = 上游没生成也没计费，不能按本地估算收用户 36861 token 的钱")
	assert.EqualValues(t, 0, usage.TotalTokens)
}

// 「读到一半才断开」的反向守卫放在 service 层的
// TestDeliveredTextUsageStillBillsPartialContentOnAbort —— 那里直接驱动判定逻辑，
// 没有「帧有没有赶上取消」这种竞态，集成层写只会 flaky。
