package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	// 重试预算检查前置：负载类的可重试错误（超时/空流/断流）同样受预算约束
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	// 智能负载：首字超时视为可重试，触发切换到下一个渠道
	if loadbalancer.IsTTFTTimeout(err) {
		return PolicyDecision{Action: "retry", Reason: "ttft_timeout", Source: "loadbalancer"}
	}
	// 智能负载：空流视同渠道失败，换渠道重试（客户端尚未收到任何数据）
	if loadbalancer.IsEmptyStream(err) {
		return PolicyDecision{Action: "retry", Reason: "empty_stream", Source: "loadbalancer"}
	}
	// 智能负载：流中断（上游在首字节前断开）视同渠道失败，换渠道重试
	if loadbalancer.IsStreamBroken(err) {
		return PolicyDecision{Action: "retry", Reason: "stream_broken", Source: "loadbalancer"}
	}
	// 智能负载：上游额度耗尽（即使返回 400/402/403 等，属于渠道不可用，换渠道重试）
	if loadbalancer.IsUpstreamQuotaError(err) {
		return PolicyDecision{Action: "retry", Reason: "upstream_quota_exhausted", Source: "loadbalancer"}
	}
	// 智能负载：上游路由/会话头缺失（部分网关剥离 x-opencode-session 返回 400，换渠道重试）
	if loadbalancer.IsUpstreamRoutingError(err) {
		return PolicyDecision{Action: "retry", Reason: "upstream_routing_error", Source: "loadbalancer"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	// 智能负载：参数不支持的 400 错误换渠道重试（不同上游对参数的支持不同，
	// 如 "thinking" / "reasoning_effort" 等），但不计入熔断。
	if _, ok := loadbalancer.IsParamNotSupportedError(err); ok {
		return PolicyDecision{Action: "retry", Reason: "bad_request_retry", Source: "loadbalancer"}
	}
	// 智能负载：上游中继站报告代理异常（如 "来自上游渠道的报错: bad response status code 400"，换渠道重试）
	if loadbalancer.IsUpstreamRelayError(err) {
		return PolicyDecision{Action: "retry", Reason: "upstream_relay_error", Source: "loadbalancer"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode())))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode()
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
