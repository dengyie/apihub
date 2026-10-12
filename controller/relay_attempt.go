package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/logger"
	"github.com/dengyie/apihub/relay"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"
	"github.com/gin-gonic/gin"
)

type relayAttemptOutcome struct {
	Success   bool
	WillRetry bool
	Err       *types.NewAPIError
}

func armRequestBudget(c *gin.Context, isStream bool, relayFormat types.RelayFormat) (context.Context, context.CancelFunc) {
	if c == nil || c.Request == nil || isStream || relayFormat == types.RelayFormatOpenAIRealtime {
		return nil, nil
	}
	budget := loadbalancer.GetRequestTimeout()
	if budget <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), budget)
	c.Request = c.Request.WithContext(ctx)
	return ctx, cancel
}

func streamDeliveredContent(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil {
		return false
	}
	return relayInfo.ReceivedResponseCount > 0 || relayInfo.ReceivedContentBytes > 0
}

func isClientAbortUpstreamHang(c *gin.Context, relayInfo *relaycommon.RelayInfo, attemptStartTime time.Time) bool {
	if c != nil && c.Writer != nil && c.Writer.Written() {
		return false
	}
	if streamDeliveredContent(relayInfo) {
		return false
	}
	hangThreshold := 30 * time.Second
	if loadbalancer.Enabled() && relayInfo != nil {
		channelID := relayInfo.GetChannelID()
		lbPolicy := loadbalancer.GetPolicy().Resolve(channelID)
		if lbPolicy.TTFTTimeoutMs > 0 {
			hangThreshold = time.Duration(lbPolicy.TTFTTimeoutMs) * time.Millisecond
		}
	}
	return time.Since(attemptStartTime) >= hangThreshold
}

func zeroByteRetryable(err *types.NewAPIError) bool {
	if err == nil || types.IsClientAbortedError(err) {
		return false
	}
	if loadbalancer.IsEmptyStream(err) || loadbalancer.IsEmptyStreamBudget(err) ||
		loadbalancer.IsStreamBroken(err) || loadbalancer.IsTTFTTimeout(err) {
		return true
	}
	if err.GetErrorCode() == types.ErrorCodeDoRequestFailed {
		return true
	}
	if loadbalancer.IsUpstreamRelayError(err) {
		return true
	}
	switch err.StatusCode {
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// executeRelayAttempt 执行单次渠道尝试，封装了选路、资源占位、上游协议分发、
// 预算超时判定、流式状态机归因、以及基于 FailureDecision 的容灾自愈流程。
func executeRelayAttempt(
	c *gin.Context,
	relayFormat types.RelayFormat,
	relayInfo *relaycommon.RelayInfo,
	retryParam *service.RetryParam,
	requestBudgetCtx context.Context,
	zeroByteDeadline *time.Time,
) relayAttemptOutcome {
	// 客户端已主动断开：快速退出重试，避免无效的渠道选路与资源占用。
	// 必须严格检查 context.Canceled，不能误将网关整请求超时 (context.DeadlineExceeded) 当成下游取消。
	if c != nil && c.Request != nil && errors.Is(c.Request.Context().Err(), context.Canceled) {
		clientErr := types.NewClientAbortedError(c.Request.Context().Err())
		logger.LogInfo(c, "客户端已断开，停止重试")
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: clientErr}
	}

	relayInfo.StreamStatus = nil
	// think_tag_to_reasoning 的跨帧状态机按渠道开关生效，换渠道重试不得继承上一渠道的相位
	relayInfo.ThinkTagSplit = relaycommon.ThinkTagSplitState{}
	relayInfo.PerformanceBusinessRejection = false
	relayInfo.PerformanceOutputTokens = 0
	relayInfo.RetryIndex = retryParam.GetRetry()

	channel, channelErr := getChannel(c, relayInfo, retryParam)
	if channelErr != nil {
		logger.LogError(c, channelErr.Error())
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: channelErr}
	}

	// 智能负载：本轮尝试计入 inflight。原先只有流式路径在 scanner 里
	// Begin/End，非流式与任务请求对 max_inflight 完全不可见，并发上限形同
	// 虚设。句柄存入 context 供 StreamScannerHandler 复用同一个计数器。
	attemptStartTime := time.Now()
	lbAttempt := loadbalancer.GlobalTracker().Begin(channel.Id, relayInfo.OriginModelName)
	c.Set(loadbalancer.ContextKeyAttempt, lbAttempt)
	defer lbAttempt.EndCancelled()
	service.AppendUsedChannel(c, channel.Id)

	if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
		lbAttempt.EndCancelled()
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: billingErr}
	}

	bodyStorage, bodyErr := common.GetBodyStorage(c)
	if bodyErr != nil {
		var err *types.NewAPIError
		if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
			err = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			err = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		lbAttempt.EndCancelled()
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: err}
	}
	c.Request.Body = io.NopCloser(bodyStorage)

	var newAPIError *types.NewAPIError
	switch relayFormat {
	case types.RelayFormatOpenAIRealtime:
		newAPIError = relay.WssHelper(c, relayInfo)
	case types.RelayFormatClaude:
		newAPIError = relay.ClaudeHelper(c, relayInfo)
	case types.RelayFormatGemini:
		newAPIError = geminiRelayHandler(c, relayInfo)
	default:
		newAPIError = relayHandler(c, relayInfo)
	}

	// 智能负载：请求预算耗尽导致的失败，重写为可观测的网关超时语义。
	if requestBudgetCtx != nil && errors.Is(requestBudgetCtx.Err(), context.DeadlineExceeded) &&
		newAPIError != nil {
		newAPIError = types.NewErrorWithStatusCode(
			&loadbalancer.TTFTTimeoutError{ChannelID: channel.Id},
			types.ErrorCodeChannelResponseTimeExceeded,
			http.StatusGatewayTimeout)
	}

	if newAPIError == nil {
		if service.IsClientAbort(c, relayInfo, nil) {
			if !c.Writer.Written() {
				if isClientAbortUpstreamHang(c, relayInfo, attemptStartTime) {
					logger.LogError(c, fmt.Sprintf("渠道 #%d 上游死挂导致客户端等待后断开（耗时 %v），计入渠道故障",
						channel.Id, time.Since(attemptStartTime)))
					lbAttempt.End(true, true)
				} else {
					lbAttempt.EndCancelled()
				}
				abortErr := types.NewClientAbortedError(context.Canceled)
				relayInfo.LastError = abortErr
				logger.LogInfo(c, "客户端已断开，停止重试")
				return relayAttemptOutcome{Success: false, WillRetry: false, Err: abortErr}
			}
			lbAttempt.End(false, false)
			service.MarkRequestPolicySuccess(c, relayInfo.StreamStatus)
			loadbalancer.GlobalTracker().ClearEmptyStream(channel.Id, relayInfo.OriginModelName)
			relayInfo.LastError = nil
			return relayAttemptOutcome{Success: true, WillRetry: false, Err: nil}
		}

		if relayInfo.StreamStatus.IsUpstreamStreamFault() {
			var underlyingErr error
			var errCode types.ErrorCode
			statusCode := http.StatusBadGateway
			_, endErr := relayInfo.StreamStatus.EndState()
			if endErr != nil && loadbalancer.IsTTFTTimeout(endErr) {
				underlyingErr = endErr
				errCode = types.ErrorCodeChannelResponseTimeExceeded
				statusCode = http.StatusGatewayTimeout
			} else {
				underlyingErr = &loadbalancer.StreamBrokenError{
					ChannelID: channel.Id,
					Reason:    relayInfo.StreamStatus.Summary(),
					Err:       endErr,
				}
				errCode = types.ErrorCodeBadResponseBody
			}
			streamErr := types.NewErrorWithStatusCode(
				underlyingErr,
				errCode,
				statusCode,
			)
			relayInfo.LastError = streamErr
			newAPIError = streamErr
		} else {
			service.MarkRequestPolicySuccess(c, relayInfo.StreamStatus)
			loadbalancer.GlobalTracker().ClearEmptyStream(channel.Id, relayInfo.OriginModelName)
			lbAttempt.End(false, false)
			relayInfo.LastError = nil
			return relayAttemptOutcome{Success: true, WillRetry: false, Err: nil}
		}
	}

	newAPIError = service.NormalizeViolationFeeError(newAPIError)
	relayInfo.LastError = newAPIError

	if service.IsClientAbort(c, relayInfo, newAPIError) {
		if isClientAbortUpstreamHang(c, relayInfo, attemptStartTime) {
			logger.LogError(c, fmt.Sprintf("渠道 #%d 上游死挂导致客户端等待后断开（耗时 %v），计入渠道故障",
				channel.Id, time.Since(attemptStartTime)))
			lbAttempt.End(true, true)
		} else {
			lbAttempt.EndCancelled()
		}
		newAPIError = types.NewClientAbortedError(newAPIError)
		relayInfo.LastError = newAPIError
		logger.LogInfo(c, "客户端已断开，停止重试")
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: newAPIError}
	}

	if zeroByteDeadline != nil && !zeroByteDeadline.IsZero() && streamDeliveredContent(relayInfo) {
		*zeroByteDeadline = time.Time{}
	}
	wasEmptyStream := loadbalancer.IsEmptyStream(newAPIError)
	if wasEmptyStream {
		loadbalancer.GlobalTracker().RecordEmptyStream(lbAttempt, channel.Id, relayInfo.OriginModelName)
	}
	if zeroByteDeadline != nil && !zeroByteDeadline.IsZero() && !time.Now().Before(*zeroByteDeadline) &&
		zeroByteRetryable(newAPIError) {
		if loadbalancer.IsTTFTTimeout(newAPIError) &&
			c != nil && c.Request != nil && c.Request.Context().Err() == nil &&
			retryParam.GetRetry() < common.RetryTimes {
			channelTTFT := loadbalancer.GetPolicy().Resolve(channel.Id).TTFTTimeoutMs
			if channelTTFT <= 0 {
				channelTTFT = loadbalancer.DefaultTTFTTimeoutMs
			}
			*zeroByteDeadline = time.Now().Add(time.Duration(channelTTFT) * time.Millisecond)
		} else {
			newAPIError = types.NewErrorWithStatusCode(
				&loadbalancer.EmptyStreamBudgetError{ChannelID: channel.Id},
				types.ErrorCodeEmptyStreamBudgetExhausted,
				http.StatusBadGateway,
				types.ErrOptionWithSkipRetry(),
			)
			relayInfo.LastError = newAPIError
			logger.LogInfo(c, "空流零字节重试墙钟耗尽，停止重试")
			lbAttempt.EndCancelled()
			return relayAttemptOutcome{Success: false, WillRetry: false, Err: newAPIError}
		}
	}

	failureEval := service.EvaluateRelayFailure(c, newAPIError, common.RetryTimes-retryParam.GetRetry())
	decision := failureEval.PolicyDecision
	service.RecordPolicyFailure(c, channel.Id, newAPIError, decision)

	isPreStreamTTFT := relayInfo.StreamStatus == nil && loadbalancer.IsTTFTTimeout(newAPIError)
	isPreStreamNonTTFTFailure := relayInfo.StreamStatus == nil && failureEval.IsPreStreamNonTTFT
	isHardFailure := isPreStreamTTFT || isPreStreamNonTTFTFailure
	isTTFTSlow := loadbalancer.IsTTFTTimeout(newAPIError)
	if isHardFailure || isTTFTSlow {
		lbAttempt.End(isTTFTSlow, isHardFailure)
	} else {
		// 非硬故障 (如 400 参数格式问题、429 短暂限流等):
		// 不计入硬熔断计数器，但也绝不能走 End(false, false) 将历史连续失败和退避倍数洗白。
		lbAttempt.EndCancelled()
	}

	applyUpstreamFailureEffects(c, channel, relayInfo, lbAttempt, newAPIError)

	if failureEval.LearnedParam != "" {
		loadbalancer.MarkParamUnsupported(channel.Id, relayInfo.OriginModelName, failureEval.LearnedParam)
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 不支持 %s 参数，已标记自动裁剪", channel.Id, relayInfo.OriginModelName, failureEval.LearnedParam))
	}
	if failureEval.StripResponsesReasoning {
		common.SetContextKey(c, constant.ContextKeyStripResponsesReasoning, true)
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游推理水合失败 (Encrypted content cannot be decrypted)，已标记后续重试自动清洗加密项并自愈", channel.Id, relayInfo.OriginModelName))
	}
	processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError, relayInfo)

	if decision.Action != "retry" {
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: newAPIError}
	}

	if ctxErr := c.Request.Context().Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			logger.LogInfo(c, "请求预算耗尽（网关侧总超时），停止重试")
		}
		return relayAttemptOutcome{Success: false, WillRetry: false, Err: newAPIError}
	}

	return relayAttemptOutcome{Success: false, WillRetry: true, Err: newAPIError}
}
