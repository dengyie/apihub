package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
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
					newAPIError.StatusCode == http.StatusForbidden ||
					newAPIError.StatusCode == http.StatusUnauthorized ||
					newAPIError.StatusCode == http.StatusGone {
					newAPIError.StatusCode = http.StatusBadGateway
				}
			} else if loadbalancer.IsEOLError(newAPIError) {
				newAPIError.StatusCode = http.StatusBadGateway
			}

			assert.Equal(t, tc.expectedStatus, newAPIError.StatusCode)
		})
	}
}
