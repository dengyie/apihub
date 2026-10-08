package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamChannelErrorStatusShielding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		useChannel     []string
		errorCode      types.ErrorCode
		statusCode     int
		errMessage     string
		expectedStatus int
	}{
		{
			name:           "upstream 403 group permission denied mapped to 502",
			useChannel:     []string{"165"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusForbidden,
			errMessage:     "无权访问 按量分组 分组 (request id: 202609280523093326619788268d9d6owlmt1Nf)",
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "upstream 404 TokenPlan model unsupported mapped to 502",
			useChannel:     []string{"150"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusNotFound,
			errMessage:     "deepseek-v4-flash is not supported by TokenPlan",
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "upstream 401 invalid key mapped to 502",
			useChannel:     []string{"120"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusUnauthorized,
			errMessage:     "Invalid API Key",
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "upstream 410 EOL model mapped to 502",
			useChannel:     []string{"56"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     410,
			errMessage:     "The model deepseek-ai/deepseek-v4-flash-0731 has reached its end of life",
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "downstream client insufficient quota remains 403",
			useChannel:     nil,
			errorCode:      types.ErrorCodeInsufficientUserQuota,
			statusCode:     http.StatusForbidden,
			errMessage:     "用户额度不足, 剩余额度: 0",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "upstream 400 parameter level max not supported mapped to 502",
			useChannel:     []string{"19"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusBadRequest,
			errMessage:     `level "max" not supported, valid levels: low, medium, high`,
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "upstream 400 thinking mode history error mapped to 502",
			useChannel:     []string{"24"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusBadRequest,
			errMessage:     "The `reasoning_content` in the thinking mode must be passed back to the API.",
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "downstream client malformed request remains 400",
			useChannel:     nil,
			errorCode:      types.ErrorCodeInvalidRequest,
			statusCode:     http.StatusBadRequest,
			errMessage:     "invalid json body",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if len(tc.useChannel) > 0 {
				c.Set("use_channel", tc.useChannel)
			}
			newAPIError := types.NewErrorWithStatusCode(
				errors.New(tc.errMessage),
				tc.errorCode,
				tc.statusCode,
			)

			// Execute the status code shielding logic as in Relay defer func
			isUpstreamChannelError := len(c.GetStringSlice("use_channel")) > 0
			if isUpstreamChannelError && newAPIError.GetErrorCode() != types.ErrorCodeInsufficientUserQuota {
				if loadbalancer.IsEOLError(newAPIError) ||
					loadbalancer.IsUpstreamPermissionError(newAPIError) ||
					loadbalancer.IsUpstreamQuotaError(newAPIError) ||
					loadbalancer.IsUpstreamRoutingError(newAPIError) ||
					loadbalancer.IsUpstreamRelayError(newAPIError) ||
					loadbalancer.IsThinkingModeHistoryError(newAPIError) ||
					newAPIError.StatusCode == http.StatusForbidden ||
					newAPIError.StatusCode == http.StatusUnauthorized ||
					newAPIError.StatusCode == http.StatusGone ||
					newAPIError.StatusCode == http.StatusBadRequest {
					newAPIError.StatusCode = http.StatusBadGateway
				}
			} else if loadbalancer.IsEOLError(newAPIError) {
				newAPIError.StatusCode = http.StatusBadGateway
			}

			assert.Equal(t, tc.expectedStatus, newAPIError.StatusCode)
		})
	}
}

func TestClientAbortedNotMappedByTerminalShield(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("use_channel", []string{"19"})
	err := types.NewClientAbortedError(errors.New("context canceled"))
	applyRelayTerminalStatusShield(c, err)
	assert.Equal(t, types.StatusClientClosedRequest, err.StatusCode)
	assert.Equal(t, types.ErrorCodeClientAborted, err.GetErrorCode())
}

func TestWriteRelayTerminalErrorEmitsClientAbortedJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := types.NewClientAbortedError(errors.New("context canceled"))
	writeRelayTerminalError(c, nil, types.RelayFormatOpenAI, err)
	assert.Equal(t, types.StatusClientClosedRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"client_aborted"`)
	assert.NotContains(t, w.Body.String(), `"code":500`)
}

func TestWriteRelayTerminalErrorSkipsSSEFrameWhenAlreadyWrittenAndAborted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	_, err := c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	require.NoError(t, err)
	before := w.Body.String()
	// abort + already written is handled by Relay defer, not writeRelayTerminalError.
	// Guard the helper itself still writes SSE for non-abort errors.
	streamErr := types.NewErrorWithStatusCode(
		&loadbalancer.StreamBrokenError{ChannelID: 1, Reason: "rst"},
		types.ErrorCodeBadResponseBody,
		http.StatusBadGateway,
	)
	writeRelayTerminalError(c, nil, types.RelayFormatOpenAI, streamErr)
	assert.True(t, strings.Contains(w.Body.String(), before))
	assert.Contains(t, w.Body.String(), `"code":502`)
}

func TestZeroByteRetryableAndStreamDeliveredContent(t *testing.T) {
	assert.False(t, streamDeliveredContent(nil))
	assert.False(t, streamDeliveredContent(&relaycommon.RelayInfo{}))
	assert.True(t, streamDeliveredContent(&relaycommon.RelayInfo{ReceivedResponseCount: 1}))
	assert.True(t, streamDeliveredContent(&relaycommon.RelayInfo{ReceivedContentBytes: 8}))

	assert.False(t, zeroByteRetryable(nil))
	assert.False(t, zeroByteRetryable(types.NewClientAbortedError(errors.New("canceled"))))
	assert.True(t, zeroByteRetryable(types.NewErrorWithStatusCode(
		&loadbalancer.EmptyStreamError{ChannelID: 1},
		types.ErrorCodeBadResponseBody,
		http.StatusBadGateway,
	)))
	assert.True(t, zeroByteRetryable(types.NewError(errors.New("dial"), types.ErrorCodeDoRequestFailed)))
	assert.False(t, zeroByteRetryable(types.NewErrorWithStatusCode(
		errors.New("bad json"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)))
}

func TestIsClientAbortUpstreamHang(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 下游已写出响应（流式或已有数据） -> 不是假死逼退
	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	_, _ = c1.Writer.Write([]byte("data: hi\n\n"))
	assert.False(t, isClientAbortUpstreamHang(c1, &relaycommon.RelayInfo{}, time.Now().Add(-60*time.Second)))

	// 2. relayInfo 记录已有响应帧 -> 不是假死逼退
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	assert.False(t, isClientAbortUpstreamHang(c2, &relaycommon.RelayInfo{ReceivedResponseCount: 2}, time.Now().Add(-60*time.Second)))

	// 3. relayInfo 记录已有字节 -> 不是假死逼退
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	assert.False(t, isClientAbortUpstreamHang(c3, &relaycommon.RelayInfo{ReceivedContentBytes: 100}, time.Now().Add(-60*time.Second)))

	// 4. 用户快速主动取消（耗时未达 30s，例如 5s） -> 正常交互取消，不计入假死逼退
	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	assert.False(t, isClientAbortUpstreamHang(c4, &relaycommon.RelayInfo{}, time.Now().Add(-5*time.Second)))

	// 5. 上游死挂逼退（零字节交付，且等待时间达 35s） -> 判定为上游死挂，计入渠道故障
	w5 := httptest.NewRecorder()
	c5, _ := gin.CreateTestContext(w5)
	assert.True(t, isClientAbortUpstreamHang(c5, &relaycommon.RelayInfo{}, time.Now().Add(-35*time.Second)))

	// 6. 渠道配置较短的 TTFT 阈值（如 10s）：下游等待 15s（未达 30s 默认但超过渠道阈值） -> 判定为上游死挂
	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Channels = map[int]loadbalancer.ChannelPolicy{
		88: {TTFTTimeoutMs: 10000},
	}
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	w6 := httptest.NewRecorder()
	c6, _ := gin.CreateTestContext(w6)
	info6 := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 88},
	}
	assert.True(t, isClientAbortUpstreamHang(c6, info6, time.Now().Add(-15*time.Second)))
}
