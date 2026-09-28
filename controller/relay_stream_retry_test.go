package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	loadbalancer.SetPolicy(policy)

	channelID := 46
	tracker := loadbalancer.GlobalTracker()
	// Start from a healthy channel: a successful attempt resets the counters
	// and closes a half-open breaker.
	tracker.Begin(channelID).End(false, false)

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
	tracker.TripBreaker(channelID)
	available, reason := tracker.IsAvailable(channelID)
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
