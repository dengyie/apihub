package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
