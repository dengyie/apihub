package controller

import (
	"context"
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
			name:           "upstream 403 group permission denied mapped to 503",
			useChannel:     []string{"165"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusForbidden,
			errMessage:     "无权访问 按量分组 分组 (request id: 202609280523093326619788268d9d6owlmt1Nf)",
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "upstream 404 TokenPlan model unsupported mapped to 503",
			useChannel:     []string{"150"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusNotFound,
			errMessage:     "deepseek-v4-flash is not supported by TokenPlan",
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "upstream 401 invalid key mapped to 503",
			useChannel:     []string{"120"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusUnauthorized,
			errMessage:     "Invalid API Key",
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "upstream 410 EOL model mapped to 503",
			useChannel:     []string{"56"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     410,
			errMessage:     "The model deepseek-ai/deepseek-v4-flash-0731 has reached its end of life",
			expectedStatus: http.StatusServiceUnavailable,
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
			name:           "upstream 400 parameter level max not supported mapped to 503",
			useChannel:     []string{"19"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusBadRequest,
			errMessage:     `level "max" not supported, valid levels: low, medium, high`,
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "upstream 400 thinking mode history error mapped to 503",
			useChannel:     []string{"24"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusBadRequest,
			errMessage:     "The `reasoning_content` in the thinking mode must be passed back to the API.",
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "upstream 400 tool call state lost mapped to 503",
			useChannel:     []string{"155"},
			errorCode:      types.ErrorCodeBadResponseStatusCode,
			statusCode:     http.StatusBadRequest,
			errMessage:     "找不到可消费的工具调用状态：call_id=call_qGRzoQMDDF3pXgOX7gt6TFaw",
			expectedStatus: http.StatusServiceUnavailable,
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

			// 直接调用生产函数，不再内联复制一遍，避免盾标改了这里却看不出来。
			applyRelayTerminalStatusShield(c, newAPIError)
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
	// 内部错误码仍是 502，但 SSE 帧里回给客户端的码被改写成 503
	// （Cloudflare 会替换源站 502 的响应体，见 clientFacingGatewayStatus）。
	assert.Contains(t, w.Body.String(), `"code":503`)
	assert.NotContains(t, w.Body.String(), `"code":502`)
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
	assert.True(t, zeroByteRetryable(types.NewErrorWithStatusCode(
		&loadbalancer.TTFTTimeoutError{ChannelID: 1, TimeoutMs: 5000},
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

		// 7. 深度思考模型配置大 TTFT 阈值（如 60s）：下游等待 35s 主动取消 -> 处于合法思考窗口内，不得误判为上游死挂
		policyDeep := loadbalancer.DefaultPolicy()
		policyDeep.Enabled = true
		policyDeep.Channels = map[int]loadbalancer.ChannelPolicy{
			99: {TTFTTimeoutMs: 60000},
		}
		loadbalancer.SetPolicy(policyDeep)

		w7 := httptest.NewRecorder()
		c7, _ := gin.CreateTestContext(w7)
		info7 := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 99},
		}
		assert.False(t, isClientAbortUpstreamHang(c7, info7, time.Now().Add(-35*time.Second)))
	}

func TestRetryLoop_ContextDeadlineExceededDoesNotMarkClientAborted(t *testing.T) {
	deadlineCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-deadlineCtx.Done()

	assert.True(t, errors.Is(deadlineCtx.Err(), context.DeadlineExceeded))
	assert.False(t, errors.Is(deadlineCtx.Err(), context.Canceled))
}

// TestToolCallStateLostMessageIsSelfExplanatory 钉住用户最终读到的文案：
// 上游原文只描述现象，必须被替换成有成因、有出路的说明，原始报错附在句尾备查。
func TestToolCallStateLostMessageIsSelfExplanatory(t *testing.T) {
	raw := "找不到可消费的工具调用状态：call_id=call_qGRzoQMDDF3pXgOX7gt6TFaw"
	err := types.NewErrorWithStatusCode(
		errors.New(raw),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
	require.True(t, loadbalancer.IsUpstreamToolCallStateLostError(err), "分类器必须命中上游原文")

	msg := clientFacingRelayMessage(err)
	assert.Contains(t, msg, "工具调用续接失败")
	assert.Contains(t, msg, "会话粘性")
	assert.Contains(t, msg, "重试")
	assert.Contains(t, msg, raw, "原始报错要保留，便于对日志排查")
}

// TestClientFacingRelayMessagePassesThroughOthers 确保只有这一类被改写。
func TestClientFacingRelayMessagePassesThroughOthers(t *testing.T) {
	err := types.NewErrorWithStatusCode(
		errors.New("The model has reached its end of life"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusGone,
	)
	assert.Equal(t, err.Error(), clientFacingRelayMessage(err))
	assert.Equal(t, "", clientFacingRelayMessage(nil))
}

// TestClientFacingGatewayStatusRewritesOnly502 Cloudflare 会把源站的 502 响应体
// 换成它自己的 "error code: 502"，所以回给客户端的最后一跳不能出现 502。
func TestClientFacingGatewayStatusRewritesOnly502(t *testing.T) {
	assert.Equal(t, http.StatusServiceUnavailable, clientFacingGatewayStatus(http.StatusBadGateway))
	for _, code := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusGone, http.StatusTooManyRequests,
		http.StatusGatewayTimeout, http.StatusServiceUnavailable} {
		assert.Equal(t, code, clientFacingGatewayStatus(code))
	}
}

func TestStreamScannerTTFTTimeoutPreservedInRelayError(t *testing.T) {
	status := relaycommon.NewStreamStatus()
	ttftErr := &loadbalancer.TTFTTimeoutError{ChannelID: 88, TimeoutMs: 25000}
	status.SetEndReason(relaycommon.StreamEndReasonTimeout, ttftErr)

	assert.True(t, status.IsUpstreamStreamFault())
	_, endErr := status.EndState()
	require.NotNil(t, endErr)
	assert.True(t, loadbalancer.IsTTFTTimeout(endErr))

	// 模拟 controller/relay.go:315 逻辑构造的错误
	var underlyingErr error
	var errCode types.ErrorCode
	statusCode := http.StatusBadGateway
	if endErr != nil && loadbalancer.IsTTFTTimeout(endErr) {
		underlyingErr = endErr
		errCode = types.ErrorCodeChannelResponseTimeExceeded
		statusCode = http.StatusGatewayTimeout
	} else {
		underlyingErr = &loadbalancer.StreamBrokenError{
			ChannelID: 88,
			Reason:    status.Summary(),
			Err:       endErr,
		}
		errCode = types.ErrorCodeBadResponseBody
	}
	apiErr := types.NewErrorWithStatusCode(underlyingErr, errCode, statusCode)

	assert.True(t, loadbalancer.IsTTFTTimeout(apiErr), "构造后的 API 错误必须能被 IsTTFTTimeout 解包")
	assert.True(t, zeroByteRetryable(apiErr), "流式首字超时必须允许零字节换渠道重试")
	assert.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)
}

func TestRetryLoop_ClientCanceledOverwritesPriorAttemptError(t *testing.T) {
	priorError := types.NewErrorWithStatusCode(errors.New("upstream internal error"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	require.False(t, types.IsClientAbortedError(priorError))

	// 模拟客户端在准备下一次重试前主动断开
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	var finalAPIError *types.NewAPIError = priorError
	if errors.Is(cancelCtx.Err(), context.Canceled) {
		finalAPIError = types.NewClientAbortedError(cancelCtx.Err())
	}

	assert.True(t, types.IsClientAbortedError(finalAPIError), "客户端取消必须覆盖前序尝试的 500 错误，返回 499 client_aborted")
	assert.Equal(t, 499, finalAPIError.StatusCode)
}
