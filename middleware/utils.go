package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/dto"
	"github.com/dengyie/apihub/i18n"
	"github.com/dengyie/apihub/logger"
	pluginruntime "github.com/dengyie/apihub/pkg/jsplugin"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
)

// isRequestBodyAbort reports whether err stems from the client's upload dying
// mid-body (transport abort) rather than from a fully received but malformed
// payload. A canceled request context counts: the client is gone either way.
func isRequestBodyAbort(c *gin.Context, err error) bool {
	if common.IsRequestBodyIncompleteError(err) {
		return true
	}
	return c != nil && c.Request != nil && c.Request.Context().Err() == context.Canceled
}

// abortRequestBodyReadFailure classifies a body read/parse failure at the
// middleware exit. Transport aborts get an honest 408 + client_aborted (400
// invalid_request made ZCode treat the client's own ~120s upload deadline as
// a permanent request defect); genuine malformed bodies keep 400. The WARN
// line carries the received/declared byte counts embedded by
// common.WrapRequestBodyReadError so one log line identifies the case.
func abortRequestBodyReadFailure(c *gin.Context, err error) {
	if isRequestBodyAbort(c, err) {
		logger.LogWarn(c, fmt.Sprintf("event=request_body_incomplete reason=client_upload_aborted err=%q path=%s content_length=%d", err.Error(), c.Request.URL.Path, c.Request.ContentLength))
		abortWithOpenAiMessage(c, http.StatusRequestTimeout, i18n.T(c, i18n.MsgDistributorRequestBodyIncomplete, map[string]any{"Error": err.Error()}), types.ErrorCodeClientAborted)
		return
	}
	abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}), types.ErrorCodeInvalidRequest)
}

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	userId := c.GetInt("id")
	_, preparedPluginRoute := c.Get(pluginruntime.ContextKeyRouteRequest)
	if !preparedPluginRoute || !RespondTaskPluginError(c, &dto.TaskError{
		Code:       codeStr,
		Message:    message,
		StatusCode: statusCode,
	}) {
		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
				"type":    "new_api_error",
				"code":    codeStr,
			},
		})
	}
	c.Abort()
	logger.LogError(c.Request.Context(), fmt.Sprintf("user %d | %s", userId, message))
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}
