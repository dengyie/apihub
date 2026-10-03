package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamBrokenRetryDecisionAndBreakerTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// SetPolicy 与 GlobalTracker 都是包级全局状态，不还原就留给后续所有用例。
	//
	// 这个测试打开的 Enabled 会让 selectResponsesWSChannel 走进「选不出渠道」
	// 的错误分支，那条分支调 i18n.T；在没 Init 过 i18n 的测试环境里那会空指针
	// panic，panic 被 recover 成 error 事件，于是后面几个 WebSocket 用例连环
	// 失败，最后卡在一个无超时的 `<-targets` 上 —— 整包测试挂死 40 分钟。
	// 2026-10-03 追查这个 hang 时定位到的根因就是这里缺失的还原。
	previousPolicy := loadbalancer.GetPolicy()
	t.Cleanup(func() { loadbalancer.SetPolicy(previousPolicy) })
	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	loadbalancer.SetPolicy(policy)

	channelID := 46
	tracker := loadbalancer.GlobalTracker()
	// Start from a healthy channel: a successful attempt resets the counters
	// and closes a half-open breaker.
	tracker.Begin(channelID, testModel).End(false, false)
	// 同理，下面 TripBreaker 留下的开路状态也必须抹掉，否则后续用例会
	// 继承一个「熔断中」的渠道。
	t.Cleanup(func() { tracker.Begin(channelID, testModel).End(false, false) })

	brokenErr := &loadbalancer.StreamBrokenError{
		ChannelID: channelID,
		Reason:    "stream error: stream ID 1; INTERNAL_ERROR; received from peer",
	}
	newAPIErr := types.NewErrorWithStatusCode(brokenErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway)

	// 1. Verify DecideRelayRetry returns Action: "retry" and Reason: "stream_broken"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	decision := service.DecideRelayRetry(c, newAPIErr, 3)
	assert.Equal(t, "retry", decision.Action)
	assert.Equal(t, "stream_broken", decision.Reason)

	// 2. Verify breaker trip on StreamBrokenError
	assert.True(t, loadbalancer.IsStreamBroken(newAPIErr))
	tracker.TripBreaker(channelID, testModel)
	available, reason := tracker.IsAvailable(channelID, testModel)
	assert.False(t, available)
	assert.Equal(t, "circuit_open", reason)
}

func TestWrittenStreamTerminalSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\\n\\n"))
	require.NoError(t, err)
	assert.True(t, c.Writer.Written())

	newAPIError := types.NewErrorWithStatusCode(
		&loadbalancer.StreamBrokenError{
			ChannelID: 46,
			Reason:    "stream error: stream ID 1; INTERNAL_ERROR; received from peer",
		},
		types.ErrorCodeBadResponseBody,
		http.StatusBadGateway,
	)

	openAIErr := newAPIError.ToOpenAIError()
	openAIErr.Code = newAPIError.StatusCode
	errJSON, err := common.Marshal(gin.H{
		"error": openAIErr,
	})
	require.NoError(t, err)
	sseErrData := fmt.Sprintf("data: %s\n\n", string(errJSON))
	_, err = c.Writer.Write([]byte(sseErrData))
	require.NoError(t, err)

	body := w.Body.String()
	assert.True(t, strings.Contains(body, "thinking..."))
	assert.True(t, strings.Contains(body, "\"code\":502"))
	assert.True(t, strings.Contains(body, "INTERNAL_ERROR"))
}

// TestArmRequestBudget pins which requests get the gateway-side budget.
//
// The budget is the root fix for the v29.4 review's residual risk: a
// non-stream upstream response "arrives all at once", the TTFT timer only
// bounds stream first-byte waits, and cross-channel retries stacked the
// unbounded per-attempt hangs without limit. Streaming and realtime sessions
// must NEVER be bounded by it (a long stream is legitimate); everything else
// gets a deadline the outbound request inherits, because the relay builds
// upstream requests from c.Request.Context() (api_request.go).
func TestArmRequestBudget(t *testing.T) {
	old := loadbalancer.GetPolicy()
	policy := loadbalancer.DefaultPolicy()
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(old)

	gin.SetMode(gin.TestMode)
	newCtx := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c
	}

	// 非流式：context 被替换为带 deadline 的子 context，父链保留。
	c := newCtx()
	parent := c.Request.Context()
	budgetCtx, cancel := armRequestBudget(c, false, types.RelayFormatOpenAI)
	require.NotNil(t, cancel)
	defer cancel()
	_, hasDeadline := budgetCtx.Deadline()
	require.True(t, hasDeadline, "non-stream request must carry the gateway budget deadline")
	require.True(t, c.Request.Context() == budgetCtx, "c.Request must be swapped so the outbound call inherits the budget")
	require.NotNil(t, parent, "父 context 链保留：出站请求同时继承预算与客户端断开信号")

	// 流式：绝不加预算。
	c2 := newCtx()
	before := c2.Request
	budgetCtx2, cancel2 := armRequestBudget(c2, true, types.RelayFormatOpenAI)
	require.Nil(t, budgetCtx2)
	require.Nil(t, cancel2)
	require.True(t, c2.Request == before, "stream request context must stay untouched")

	// realtime：绝不加预算（长会话）。
	c3 := newCtx()
	budgetCtx3, cancel3 := armRequestBudget(c3, false, types.RelayFormatOpenAIRealtime)
	require.Nil(t, budgetCtx3)
	require.Nil(t, cancel3)

	// 预算显式关闭（request_timeout_ms: 0）：不加。
	off := 0
	policy.RequestTimeoutMs = &off
	c4 := newCtx()
	budgetCtx4, cancel4 := armRequestBudget(c4, false, types.RelayFormatOpenAI)
	require.Nil(t, budgetCtx4)
	require.Nil(t, cancel4)
}

func TestEmptyStreamRetryBudgetDoesNotArmRequestContext(t *testing.T) {
	old := loadbalancer.GetPolicy()
	policy := loadbalancer.DefaultPolicy()
	one := 1
	policy.EmptyStreamRetryBudgetMs = &one
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(old)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	before := c.Request
	budgetCtx, cancel := armRequestBudget(c, true, types.RelayFormatOpenAI)
	require.Nil(t, budgetCtx)
	require.Nil(t, cancel)
	require.True(t, c.Request == before, "stream request context must stay untouched even with a zero-byte wall clock")
	assert.Equal(t, time.Millisecond, loadbalancer.GetEmptyStreamRetryBudget())
}

// testModel 测试里统一用的模型名。per_model 默认关闭，模型名会被 scopeKey
// 折叠成渠道级，所以这些用例的语义与 v29.10 一致。
const testModel = "gpt-4o"
