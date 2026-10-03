package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	case relayconstant.RelayModeAlphaSearch:
		err = relay.AlphaSearchHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError == nil {
			return
		}
		clientAborted := types.IsClientAbortedError(newAPIError)
		if clientAborted {
			// 取消不是上游故障：不记渠道失败、不映射成 502，已出字则不再补 SSE 错误帧。
			if c.Writer.Written() {
				return
			}
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			writeRelayTerminalError(c, ws, relayFormat, newAPIError)
			return
		}
		service.RecordRequestPolicyTermination(c, newAPIError)
		logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
		applyRelayTerminalStatusShield(c, newAPIError)
		newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
		writeRelayTerminalError(c, ws, relayFormat, newAPIError)
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	// 记账必须用**父 context**，不能用 c.Request.Context()。
	//
	// armRequestBudget 会把 c.Request 换成带超时的预算 context，而它的 cancel
	// 注册得比下面这个 defer 晚 —— Go 的 defer 是 LIFO，cancelRequestBudget()
	// 先跑，RecordRelayResult 再跑时看到的必然是 context.Canceled。那是我们
	// 自己释放预算造成的，不是客户端断开。
	//
	// ClassifyRelayOutcome 里有一条 `ctx.Err() == context.Canceled → OutcomeIgnored`，
	// 本意是排除「客户端自己掐掉」的请求（那不该记进渠道健康度），但在这里会把
	// **每一个**非流式请求都误判成客户端取消：生产策略 request_timeout_ms=180000 > 0，
	// 所有非流式请求都会套上预算 context，于是它们的 perf 指标全部静默丢失。
	// 流式不受影响 —— armRequestBudget 对流式直接返回 nil，这也正是该缺陷
	// 长期没被发现的原因：只有非流式路径是黑的，而那部分没人查。
	accountingCtx := c.Request.Context()

	defer func() {
		recovered := recover()
		resultErr := newAPIError
		if recovered != nil {
			resultErr = types.NewError(fmt.Errorf("relay panic: %v", recovered), types.ErrorCodeBadResponse)
		}
		if relayFormat != types.RelayFormatOpenAIRealtime {
			perfmetrics.RecordRelayResult(accountingCtx, relayInfo, resultErr)
		}
		if recovered != nil {
			panic(recovered)
		}
	}()

	if newAPIError = relay.PrepareRequestBilling(c, relayInfo); newAPIError != nil {
		return
	}
	defer func() {
		newAPIError = relay.RefundFailedRequestBilling(c, relayInfo, newAPIError)
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
		// 智能负载：prompt 前缀一致性 hash，同样的 prompt 走同一渠道，提高上游 cache 命中率。
		// 渠道失败重试时忽略 sticky，走普通选择。
		StickyKey: loadbalancer.StickyKeyFromRequest(request),
	}
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil

	// 智能负载：非流式请求的整请求预算。
	//
	// 非流式的上游响应是「算完才来」：TTFT 定时器只覆盖流式的首字等待，非流式
	// 此前没有任何网关侧边界，单次尝试可以一直挂到上游或其前置网关判死，跨渠道
	// 重试叠加时总等待无上界（v29.4 review 遗留 P2 的根子）。出站请求继承本
	// request 的 context（api_request.go 用 c.Request.Context() 构造上游请求），
	// 所以在这里给 context 套上预算，所有重试尝试共享同一个总额。
	// 流式与 realtime 不受约束：首字等待归 ttft_timeout_ms，长流是合法形态。
	requestBudgetCtx, cancelRequestBudget := armRequestBudget(c, relayInfo.IsStream, relayFormat)
	if cancelRequestBudget != nil {
		defer cancelRequestBudget()
	}

	// 流式零字节墙钟：只约束「客户端仍未收到任何内容」的换渠道重试。
	// 用单调时钟在控制器里比，不套到 request context 上——那会把合法长流
	// 和取消/预算搅在一起。一出字即作废。
	zeroByteDeadline := time.Time{}
	if relayInfo.IsStream {
		if budget := loadbalancer.GetEmptyStreamRetryBudget(); budget > 0 {
			zeroByteDeadline = time.Now().Add(budget)
		}
	}

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		relayInfo.StreamStatus = nil
		relayInfo.PerformanceBusinessRejection = false
		relayInfo.PerformanceOutputTokens = 0
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel, channelErr := getChannel(c, relayInfo, retryParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			newAPIError = channelErr
			break
		}
		// 智能负载：本轮尝试计入 inflight。原先只有流式路径在 scanner 里
		// Begin/End，非流式与任务请求对 max_inflight 完全不可见，并发上限形同
		// 虚设。句柄存入 context 供 StreamScannerHandler 复用同一个计数器。
		lbAttempt := loadbalancer.GlobalTracker().Begin(channel.Id, relayInfo.OriginModelName)
		c.Set(loadbalancer.ContextKeyAttempt, lbAttempt)
		// panic 兜底：保证 inflight 一定归还。
		//
		// defer 写在 for 循环里通常是函数级、看着没用，但 panic 展开本身就是
		// 一次函数退出——所有迭代注册的 defer 都会按 LIFO 执行，其中自然包含
		// 正在 panic 的那次尝试。End 自身用 done.Swap 幂等，所以正常路径上这
		// 若干次调用全是空操作，不会双记熔断计数。
		//
		// 没有这条兜底时，panic 会跳过下面所有显式 End 而直接冲到外层 recover
		// （文本路径）或 gin 的 Recovery 中间件（任务路径），inflight 就此不归
		// 还：该渠道的 MaxInflight 永久少一个槽，直到进程结束为止。
		defer lbAttempt.End(false, false)
		service.AppendUsedChannel(c, channel.Id)
		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			// 计费准备失败发生在请求上游之前，渠道本身无过错
			lbAttempt.End(false, false)
			newAPIError = billingErr
			break
		}

		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			// 读请求体失败同样发生在请求上游之前
			lbAttempt.End(false, false)
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

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
		// 预算到期时出站请求以 context.DeadlineExceeded 失败（客户端断开产生的
		// 是 context.Canceled，二者可区分）。判据以「预算 ctx 是否已到期」为准而
		// 不是 errors.Is(newAPIError.Err, DeadlineExceeded)：OpenAIError 构造路径
		// 只保留 err.Error() 字符串、不保留 Unwrap 链，依赖错误链会在包装处静默
		// 失效；预算到期是请求级事实，且尝试返回与本次检查之间只有微秒级窗口，
		// 误判代价仅是终端错误文案。复用 TTFT 超时类型：语义相同（预算内没等到
		// 可用响应——非流式下响应整体即「首字」），且天然接入既有分类——不计入
		// 熔断硬失败（慢≠不健康），按 ttft_timeout 换渠道重试。
		if requestBudgetCtx != nil && errors.Is(requestBudgetCtx.Err(), context.DeadlineExceeded) &&
			newAPIError != nil {
			newAPIError = types.NewErrorWithStatusCode(
				&loadbalancer.TTFTTimeoutError{ChannelID: channel.Id},
				types.ErrorCodeChannelResponseTimeExceeded,
				http.StatusGatewayTimeout)
		}

		if newAPIError == nil {
			if service.IsClientAbort(c, relayInfo, nil) {
				// 扫描器在客户端断开时返回 nil。尚未写出响应体则补 499；
				// 已经出字则按部分成功收尾，不再发伪装上游故障的错误帧。
				lbAttempt.End(false, false)
				if !c.Writer.Written() {
					newAPIError = types.NewClientAbortedError(context.Canceled)
					relayInfo.LastError = newAPIError
					logger.LogInfo(c, "客户端已断开，停止重试")
					break
				}
				service.MarkRequestPolicySuccess(c, relayInfo.StreamStatus)
				loadbalancer.GlobalTracker().ClearEmptyStream(channel.Id, relayInfo.OriginModelName)
				relayInfo.LastError = nil
				return
			}
			// 客户端主动断开不是上游渠道故障：不得据此熔断渠道，也不得为一个
			// 已经离开的接收方继续重试。判据统一走 StreamStatus.IsUpstreamStreamFault。
			if relayInfo.StreamStatus.IsUpstreamStreamFault() {
				streamErr := types.NewErrorWithStatusCode(
					&loadbalancer.StreamBrokenError{
						ChannelID: channel.Id,
						Reason:    relayInfo.StreamStatus.Summary(),
					},
					types.ErrorCodeBadResponseBody,
					http.StatusBadGateway,
				)
				relayInfo.LastError = streamErr
				newAPIError = streamErr
			} else {
				service.MarkRequestPolicySuccess(c, relayInfo.StreamStatus)
				loadbalancer.GlobalTracker().ClearEmptyStream(channel.Id, relayInfo.OriginModelName)
				// 流式路径已由 StreamScannerHandler 上报（幂等），这里收尾非流式
				lbAttempt.End(false, false)
				relayInfo.LastError = nil
				return
			}
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		// 客户端主动断开：先改写再记账。取消是下游行为，不是渠道故障。
		// 判据用请求级事实（context.Canceled / StreamStatus.IsClientAbort），
		// 不依赖错误链——OpenAIError 构造经常丢掉 Unwrap。
		if service.IsClientAbort(c, relayInfo, newAPIError) {
			newAPIError = types.NewClientAbortedError(newAPIError)
			relayInfo.LastError = newAPIError
			logger.LogInfo(c, "客户端已断开，停止重试")
			lbAttempt.End(false, false)
			break
		}

		// 流式仍零字节且墙钟耗尽：改写 502，停止换渠道。出字后墙钟已作废。
		if !zeroByteDeadline.IsZero() && streamDeliveredContent(relayInfo) {
			zeroByteDeadline = time.Time{}
		}
		wasEmptyStream := loadbalancer.IsEmptyStream(newAPIError)
		if wasEmptyStream {
			loadbalancer.GlobalTracker().RecordEmptyStream(channel.Id, relayInfo.OriginModelName)
		}
		if !zeroByteDeadline.IsZero() && !time.Now().Before(zeroByteDeadline) &&
			zeroByteRetryable(newAPIError) {
			newAPIError = types.NewErrorWithStatusCode(
				&loadbalancer.EmptyStreamBudgetError{ChannelID: channel.Id},
				types.ErrorCodeEmptyStreamBudgetExhausted,
				http.StatusBadGateway,
				types.ErrOptionWithSkipRetry(),
			)
			relayInfo.LastError = newAPIError
			logger.LogInfo(c, "空流零字节重试墙钟耗尽，停止重试")
			lbAttempt.End(false, false)
			break
		}

		decision := service.DecideRelayRetry(c, newAPIError, common.RetryTimes-retryParam.GetRetry())
		service.RecordPolicyFailure(c, channel.Id, newAPIError, decision)
		// 智能负载：本轮尝试收尾——归还 inflight，并决定是否计入熔断计数。
		// 流式失败的计数已由 StreamScannerHandler 用同一个（幂等）句柄上报，
		// 这里的 End 不会双记；只有没有流状态的失败（首字节前的错误、非流式）
		// 才由本层补记。400 不计入（参数不支持不代表渠道不健康）。
		// 无论是否计入都必须 End，否则 inflight 不归还，并发上限会被永久占满。
		lbAttempt.End(false,
			relayInfo.StreamStatus == nil &&
				newAPIError.StatusCode != 400 &&
				!loadbalancer.IsTTFTTimeout(newAPIError) &&
				!loadbalancer.IsEmptyStream(newAPIError) &&
				!loadbalancer.IsEmptyStreamBudget(newAPIError) &&
				!loadbalancer.IsStreamBroken(newAPIError) &&
				!loadbalancer.IsUpstreamRateLimitError(newAPIError) &&
				!types.IsClientAbortedError(newAPIError))
		// 熔断作用域：只有可证明是账号/密钥/中继级的问题才熔整个渠道，
		// 其余只熔 (渠道, 模型) 这一对——一个模型 404 不该让该渠道
		// 上百个健康模型一起退出轮转。判据见 loadbalancer.BreakerScopeOf。
		//
		// 下面每条熔断日志都必须打出作用域：按模型熔断之后，只打渠道
		// id 的日志无法归因「到底是哪个模型把它熔了」。
		modelName := relayInfo.OriginModelName
		scopeOf := func(err *types.NewAPIError) string {
			return loadbalancer.EffectiveModelName(channel.Id, modelName, err)
		}
		scopeLabel := func(model string) string {
			if model == "" {
				return "全部模型"
			}
			return model
		}
		// tripModelScope 熔断「实际作用域为模型级」的那一把键，并连带熔掉
		// 映射到同一上游模型的兄弟客户端模型。
		//
		// until 传零值表示走常规冷却（相对 openedAt 递增退避），非零走定时
		// 熔断（绝对 blockedUntil，如宵禁）——两者语义不同，不能互相替代。
		//
		// 返回实际生效的模型名（空串=渠道级）。开关关闭时 effective 为空，
		// 键已折叠成渠道级，兄弟一并被渠道级熔断覆盖，这里直接返回空即可。
		tripModelScope := func(err *types.NewAPIError, until time.Time) string {
			effective := scopeOf(err)
			trip := func(m string) {
				if until.IsZero() {
					loadbalancer.GlobalTracker().TripBreaker(channel.Id, m)
				} else {
					loadbalancer.GlobalTracker().TripBreakerUntil(channel.Id, m, until)
				}
			}
			trip(effective)
			if effective == "" {
				return ""
			}
			for _, sibling := range loadbalancer.SiblingModelsFromMapping(channel.GetModelMapping(), modelName) {
				trip(sibling)
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 与 %s 映射到同一上游模型，一并熔断", channel.Id, scopeLabel(sibling), modelName))
			}
			return effective
		}
		// 智能负载：上游限流或并发超限（429 / RPM / 并发限制），采用短周期熔断冷却（默认 30 秒），避免整池因瞬时限流假死
		if loadbalancer.IsUpstreamRateLimitError(newAPIError) {
			// v29.14：429 一律按 (渠道, 模型) 避让，不再整渠道退出轮转。
			// 限流通常是账号级 RPM 造成的，改为模型级后每个模型各自累计频次，
			// 但冷却仍只有 30s，代价远小于「一次 429 让上百个健康模型消失」。
			effective := scopeOf(newAPIError)
			loadbalancer.GlobalTracker().TripBreakerForRateLimit(channel.Id, effective)
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游限流/并发超限 (429/RPM/Concurrency)，已设置短期避让冷却: %s", channel.Id, scopeLabel(effective), newAPIError.Error()))
			for _, sibling := range loadbalancer.SiblingModelsFromMapping(channel.GetModelMapping(), modelName) {
				loadbalancer.GlobalTracker().TripBreakerForRateLimit(channel.Id, sibling)
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 与 %s 映射到同一上游模型，一并避让", channel.Id, scopeLabel(sibling), modelName))
			}
		} else {
			// 智能负载：模型 EOL (410 等) 属于该模型自身的确定性失效，只熔这一个模型。
			if loadbalancer.IsEOLError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游模型已 EOL/下线 (410)，已立即熔断", channel.Id, scopeLabel(tripped)))
			}
			// 智能负载：上游流传输中断（RST_STREAM / connection reset 等）。这是单次请求的
			// 传输故障而非账号失效，只熔该模型；若它其实是系统性的，每个模型会各自
			// 熔断并各自升级退避，最终收敛到全渠道退出，只是慢 N 步。
			if loadbalancer.IsStreamBroken(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游流传输中断 (%s)，已立即熔断并触发重试", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
			// 智能负载：上游额度耗尽。v29.14 起熔断按模型粒度；账号级的整体下线
			// 仍由自动下线（AutomaticDisableChannelEnabled）兜底。
			if loadbalancer.IsUpstreamQuotaError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游额度已耗尽，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
			// 智能负载：会话路由失败是上游网关自身的问题，与具体哪个模型无关，熔整个渠道。
			if loadbalancer.IsUpstreamRoutingError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游会话路由失败 (缺失 x-opencode-session)，已立即熔断该渠道: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
			// 智能负载：上游思考模式历史消息不兼容，是这个模型的请求形状问题，只熔该模型。
			if loadbalancer.IsThinkingModeHistoryError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游思考模式历史消息不兼容 (reasoning_content must be passed back)，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
			// 智能负载：上游中继代理异常（bad response status code / 来自上游渠道的报错）。
			// v29.14 起按模型粒度：IsUpstreamRelayError 的关键词表含
			// `no available channel for model`，而那句话说的是「上游没有这个模型」，
			// 不是代理坏了。原先这里硬编码 `TripBreaker(id, "")` 整渠道退出，
			// 导致 BreakerScopeOf 里已修好的模型级判据在这条路径上从未生效。
			if loadbalancer.IsUpstreamRelayError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游中继代理异常 (bad response status code / 渠道出错)，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
			// 智能负载：上游渠道权限受限/分组无权访问/TokenPlan不支持。
			// v29.14 起同样按模型粒度，令牌整体失效由自动下线兜底。
			if loadbalancer.IsUpstreamPermissionError(newAPIError) {
				tripped := tripModelScope(newAPIError, time.Time{})
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 上游权限受限/分组无权访问/模型不支持，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
			}
		}
		// 智能负载参数裁剪：上游明确说不支持某参数时，
		// 标记该渠道，后续请求（包括重试）自动裁剪该参数后再发。
		if param, ok := loadbalancer.IsParamNotSupportedError(newAPIError); ok {
			loadbalancer.MarkParamUnsupported(channel.Id, param)
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 不支持 %s 参数，已标记自动裁剪", channel.Id, scopeLabel(modelName), param))
		}
		// 智能负载宵禁处理：00:00-8:00 服务不可用的渠道，熔断到早 8 点。
		// 403 本身已会触发换渠道重试，这里加的是超长熔断。
		// v29.14 起按模型粒度：宵禁是账号的时段限制，但把它摊到每个模型上
		// 最多让上游多接 failure_threshold 次无效请求，而整渠道退出期间
		// 该渠道所有模型都不可用，代价大得多。
		if loadbalancer.IsCurfewError(newAPIError) {
			until := loadbalancer.CurfewEndTime(time.Now())
			tripped := tripModelScope(newAPIError, until)
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("渠道 #%d [%s] 宵禁中，已熔断到 %s", channel.Id, scopeLabel(tripped), until.Format("15:04")))
		}
		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError, relayInfo)

		if decision.Action != "retry" {
			break
		}
		// 请求预算耗尽：取消已在记账前改写并 break，这里只处理 DeadlineExceeded。
		if ctxErr := c.Request.Context().Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				logger.LogInfo(c, "请求预算耗尽（网关侧总超时），停止重试")
			}
			break
		}
	}

	// 整轮重试都失败时，结算最后一次中断流的部分产出。客户端已经收到了这份输出，
	// 不结算就等于中断的流全部免费。放在循环外而不是 handler 里，是为了保证同一
	// 请求只结算一次：任何一轮重试成功时，成功那轮的结算已经覆盖整次请求，
	// 这里直接跳过（BillingSession 的 settled 守卫会把迟到的第二次结算静默吞掉，
	// 那会变成少收钱，同样不能放任发生）。
	if newAPIError != nil && relayInfo.InterruptedStreamUsage != nil {
		relay.ConsumeResponsesQuota(c, relayInfo, relayInfo.InterruptedStreamUsage)
		relayInfo.InterruptedStreamUsage = nil
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
}

// CountClaudeTokens implements Anthropic's token-counting utility endpoint.
// It deliberately skips upstream generation and billing; callers use this
// endpoint to size prompts before creating a Message.
func CountClaudeTokens(c *gin.Context) {
	request, err := helper.GetAndValidateClaudeRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": common.MessageWithRequestId(err.Error(), c.GetString(common.RequestIdKey)),
			},
		})
		return
	}

	info := relaycommon.GenRelayInfoClaude(c, request)
	inputTokens, err := service.CountRequestToken(c, request.GetTokenCountMeta(), info)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "api_error",
				"message": common.MessageWithRequestId(err.Error(), c.GetString(common.RequestIdKey)),
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": inputTokens})
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime", "responses"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

// armRequestBudget 为非流式请求套上整请求预算（所有重试尝试共享同一总额）。
//
// 预算取自策略 request_timeout_ms（缺省 DefaultRequestTimeoutMs，显式 0 关闭）。
// 出站请求用 c.Request.Context() 构造（见 api_request.go），所以替换 request 的
// context 即可让预算传导到上游调用；父 context 的取值与取消（客户端断开）全部
// 保留。流式与 realtime 返回 (nil, nil)——它们各有自己的时间语义，长流是合法
// 形态，绝不能被整请求预算误杀。
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

// applyRelayTerminalStatusShield 把上游渠道的 401/403/410/400 映射成 502，
// 避免客户端 SDK 把网关故障当成自己的凭证错误。客户端断开不得映射。
func applyRelayTerminalStatusShield(c *gin.Context, newAPIError *types.NewAPIError) {
	if newAPIError == nil || types.IsClientAbortedError(newAPIError) {
		return
	}
	isUpstreamChannelError := c != nil && len(c.GetStringSlice("use_channel")) > 0
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
}

func writeRelayTerminalError(c *gin.Context, ws *websocket.Conn, relayFormat types.RelayFormat, newAPIError *types.NewAPIError) {
	if c == nil || newAPIError == nil {
		return
	}
	if !c.Writer.Written() {
		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			helper.WssError(c, ws, newAPIError.ToOpenAIError())
		case types.RelayFormatClaude:
			c.JSON(newAPIError.StatusCode, gin.H{
				"type":  "error",
				"error": newAPIError.ToClaudeError(),
			})
		default:
			c.JSON(newAPIError.StatusCode, gin.H{
				"error": newAPIError.ToOpenAIError(),
			})
		}
		return
	}
	// SSE 流已经输出部分数据（headers 已发送），无法再发送 HTTP 状态码或普通 JSON。
	// 必须通过 SSE 协议发送标准错误帧，通知客户端请求失败并携带错误码，
	// 这样客户端 SDK（如 ZCode、OpenAI SDK）能正确解析出 502/错误并标记 retryable: true。
	var sseErrData string
	switch relayFormat {
	case types.RelayFormatClaude:
		errJSON, _ := common.Marshal(gin.H{
			"type":  "error",
			"error": newAPIError.ToClaudeError(),
		})
		sseErrData = fmt.Sprintf("event: error\ndata: %s\n\n", string(errJSON))
	default:
		openAIErr := newAPIError.ToOpenAIError()
		openAIErr.Code = newAPIError.StatusCode
		errJSON, _ := common.Marshal(gin.H{
			"error": openAIErr,
		})
		sseErrData = fmt.Sprintf("data: %s\n\n", string(errJSON))
	}
	_, _ = c.Writer.Write([]byte(sseErrData))
	c.Writer.Flush()
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		channel := &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}
		service.RequestPolicy(c).BeginAttempt(channel, info.UsingGroup)
		return channel, nil
	}
	channel, selectGroup, err := service.SelectRetryChannel(retryParam)
	if err != nil {
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	service.RequestPolicy(c).BeginAttempt(channel, selectGroup)
	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
	if newAPIError != nil {
		return nil, newAPIError
	}
	return channel, nil
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	service.ProcessChannelError(c, channelError, err, relayInfo)
}

func RelayMidjourney(c *gin.Context) {
	policy := service.RequestPolicy(c)
	defer func() {
		if policy.Attempts > 0 && !policy.Successful {
			service.RecordRequestPolicyTermination(c, types.NewErrorWithStatusCode(errors.New("Midjourney submission failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway, types.ErrOptionWithSkipRetry()))
		}
	}()
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *taskdto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		policy.Successful = false
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	// The web fallback may already have applied static-asset cache headers.
	// A missing API or asset can appear after an upgrade; never cache its 404.
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

// RelayTaskPluginEndpoint keeps unclaimed shared-endpoint traffic on its
// existing handler while claimed requests enter the generation-pinned
// host-owned protocol bridge.
func RelayTaskPluginEndpoint(c *gin.Context, fallback gin.HandlerFunc) {
	pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint)
	if !exists {
		fallback(c)
		return
	}
	pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint)
	if !ok || pinned.Plugin == nil || pinned.Generation == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Task protocol request failed",
				"type":    "new_api_error",
				"code":    "task_protocol_error",
			},
		})
		return
	}
	switch pinned.Protocol {
	case "openai_responses":
		serveTaskPluginProtocol(c, pinned, defaultPluginProtocolBridgeDeps())
	case pluginruntime.ProtocolOpenAIImage:
		serveTaskPluginImageProtocol(c, pinned, defaultPluginProtocolBridgeDeps())
	default:
		fallback(c)
	}
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

type taskSubmissionOutcome struct {
	Result    *relay.TaskSubmitResult
	Task      *model.Task
	RelayInfo *relaycommon.RelayInfo
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		respondTaskSubmissionError(c, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if action := c.GetString("task_action"); action != "" {
		relayInfo.Action = action
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}
	if taskErr := relay.ApplyOriginTaskAffinity(c, relayInfo); taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}

	outcome, taskErr := executeTaskSubmission(c, relayInfo)
	if taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}
	presentTaskSubmission(c, outcome)
}

// executeTaskSubmission owns the retry, billing, and persistence lifecycle.
// It deliberately performs no client response writes so JSON and protocol
// presenters share the same durable task barrier. Its cancellation semantics
// come from c.Request.Context: native task endpoints use the client context,
// while the Responses bridge supplies an independently bounded context.
func executeTaskSubmission(c *gin.Context, relayInfo *relaycommon.RelayInfo) (*taskSubmissionOutcome, *taskdto.TaskError) {
	return executeTaskSubmissionWith(c, relayInfo, relay.RelayTaskSubmit)
}

type taskSubmitAttempt func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *taskdto.TaskError)

func executeTaskSubmissionWith(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	submit taskSubmitAttempt,
) (*taskSubmissionOutcome, *taskdto.TaskError) {
	policy := service.RequestPolicy(c)
	diagnostics := newTaskPluginSubmitDiagnostics(c)
	diagnostics.start(relayInfo)
	var result *relay.TaskSubmitResult
	var taskErr *taskdto.TaskError
	durable := false
	stage := "start"
	defer func() {
		if !durable && relayInfo.Billing != nil {
			diagnostics.refund(stage)
			relayInfo.Billing.Refund(c)
		}
	}()
	stage = "before_attempt"
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_attempt", 0)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		stage = "select_channel"
		if requestErr := c.Request.Context().Err(); requestErr != nil {
			diagnostics.cancelled("before_attempt", retryParam.GetRetry()+1)
			taskErr = service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
			break
		}
		var channel *model.Channel

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			policy.BeginAttempt(channel, relayInfo.UsingGroup)
			if retryParam.GetRetry() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", channelErr.StatusCode)
				break
			}
		}
		diagnostics.attempt(retryParam.GetRetry()+1, channel, relayInfo.LockedChannel != nil)

		// 智能负载：任务提交同样计入 inflight（见 Relay 中的同名注释）
		lbAttempt := loadbalancer.GlobalTracker().Begin(channel.Id, relayInfo.OriginModelName)
		c.Set(loadbalancer.ContextKeyAttempt, lbAttempt)
		// panic 兜底，理由见 Relay 中的同名注释。任务提交路径没有自己的
		// recover，panic 由 gin 的 Recovery 中间件接住，所以这条更必要。
		defer lbAttempt.End(false, false)
		service.AppendUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			stage = "read_body"
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			lbAttempt.End(false, false)
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		stage = "submit"
		result, taskErr = submit(c, relayInfo)
		if requestErr := c.Request.Context().Err(); requestErr != nil {
			diagnostics.cancelled("after_submit", retryParam.GetRetry()+1)
			taskErr = service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
			lbAttempt.End(false, false)
			break
		}
		if taskErr == nil {
			diagnostics.attemptSucceeded(retryParam.GetRetry()+1, result)
			lbAttempt.End(false, false)
			break
		}

		taskAPIError := taskSubmissionAPIError(taskErr)
		relayInfo.LastError = taskAPIError
		decision := decideTaskRetry(c, taskErr, common.RetryTimes-retryParam.GetRetry())
		service.RecordPolicyFailure(c, channel.Id, taskAPIError, decision)
		// 智能负载：渠道失败计入熔断器（400 参数问题不计入）。
		// 任务提交不经过流式扫描器，失败一律由本层上报。
		lbAttempt.End(false, taskAPIError.StatusCode != 400)
		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				taskAPIError,
				relayInfo)
		}

		willRetry := decision.Action == "retry"
		diagnostics.attemptFailed(retryParam.GetRetry()+1, channel, taskErr, willRetry)
		if !willRetry {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	if taskErr != nil {
		diagnostics.failed(stage, "task_error", taskErr, false)
		return nil, taskErr
	}
	if result == nil {
		taskErr = service.TaskErrorWrapperLocal(errors.New("task submission returned no result"), "task_submit_failed", http.StatusInternalServerError)
		diagnostics.failed("submit", "missing_result", taskErr, false)
		return nil, taskErr
	}
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_reserve", retryParam.GetRetry()+1)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	// Reserve any submit-time upward billing adjustment before persistence.
	// This keeps insertion failures fully refundable while ensuring settlement
	// after the barrier normally has a zero positive delta.
	if relayInfo.Billing != nil {
		stage = "reserve"
		diagnostics.reserve("reserve_start", result.Quota)
		if reserveErr := relayInfo.Billing.Reserve(result.Quota); reserveErr != nil {
			common.SysError("reserve adjusted task billing error: " + reserveErr.Error())
			taskErr = service.TaskErrorWrapperLocal(errors.New("insufficient quota for adjusted task cost"), string(types.ErrorCodeInsufficientUserQuota), http.StatusForbidden)
			diagnostics.failed("reserve", "insufficient_quota", taskErr, false)
			return nil, taskErr
		}
		diagnostics.reserve("reserve_complete", result.Quota)
	}
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_insert", retryParam.GetRetry()+1)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	stage = "insert"
	task := model.InitTask(result.Platform, relayInfo)
	task.PrivateData.Execution = service.TaskExecutionSnapshotFromContext(c)
	task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
	task.PrivateData.BillingSource = relayInfo.BillingSource
	task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
	task.PrivateData.TokenId = relayInfo.TokenId
	task.PrivateData.NodeName = common.NodeName
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		ModelPrice:      relayInfo.PriceData.ModelPrice,
		GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		ModelRatio:      relayInfo.PriceData.ModelRatio,
		OtherRatios:     relayInfo.PriceData.OtherRatios(),
		OriginModelName: relayInfo.OriginModelName,
		PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		TieredSnapshot:  relayInfo.TieredBillingSnapshot,
	}
	task.Quota = result.Quota
	task.Data = result.TaskData
	if len(result.PluginState) > 0 {
		task.PrivateData.PluginState = result.PluginState
	}
	task.Action = relayInfo.Action
	if immediate := result.Immediate; immediate != nil {
		task.Status = model.TaskStatus(immediate.Status)
		task.Progress = immediate.Progress
		if immediate.Status == model.TaskStatusSuccess || immediate.Status == model.TaskStatusFailure {
			task.FinishTime = time.Now().Unix()
		}
		if immediate.Status == model.TaskStatusFailure {
			task.FailReason = immediate.Reason
		}
		if immediate.Url != "" {
			task.PrivateData.ResultURL = immediate.Url
		} else if immediate.Status == model.TaskStatusSuccess {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
	}
	// A native submit route may declare retainResult: false. It applies only
	// to immediate terminal results: the client receives the complete response
	// once, the upstream snapshot is never persisted, and the task is not
	// retrievable afterwards. An asynchronous result on such a route keeps its
	// snapshot because polling and retrieval need it. The OpenAI Images
	// protocol delivers its images inline in the same HTTP response, so its
	// immediate results follow the same rule; an asynchronous image task is
	// expected there and is polled inside the request.
	var insertOmits []string
	immediateTerminal := result.Immediate != nil && (result.Immediate.Status == model.TaskStatusSuccess || result.Immediate.Status == model.TaskStatusFailure)
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedRoute); exists {
		pinned, ok := pinnedValue.(pluginruntime.PinnedRoute)
		if ok && pinned.Route.RetainResult != nil && !*pinned.Route.RetainResult {
			if immediateTerminal {
				task.PrivateData.ResultDiscarded = true
				insertOmits = append(insertOmits, "data")
			} else {
				logger.LogWarn(c, fmt.Sprintf("task plugin route %s %s declares retainResult: false but returned an asynchronous result; retaining task %s", pinned.Route.Method, pinned.Route.Path, task.TaskID))
			}
		}
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint); exists && immediateTerminal {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint); ok && pinned.Protocol == pluginruntime.ProtocolOpenAIImage {
			task.PrivateData.ResultDiscarded = true
			insertOmits = append(insertOmits, "data")
		}
	}
	diagnostics.insertStart(task)
	if insertErr := task.InsertWithContext(c.Request.Context(), insertOmits...); insertErr != nil {
		common.SysError("insert task error: " + insertErr.Error())
		taskErr = service.TaskErrorWrapperLocal(errors.New("failed to persist task"), "task_insert_failed", http.StatusInternalServerError)
		diagnostics.failed("insert", "database_error", taskErr, false)
		return nil, taskErr
	}
	durable = true
	stage = "settle"
	diagnostics.durable(task)
	diagnostics.settleStart(task, result.Quota)

	if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
		common.SysError("settle task billing error: " + settleErr.Error())
		taskErr = service.TaskErrorWrapperLocal(errors.New("failed to settle task billing"), "task_billing_settlement_failed", http.StatusInternalServerError)
		diagnostics.failed("settle", "billing_error", taskErr, true)
		return nil, taskErr
	}
	if task.Status != model.TaskStatusFailure {
		service.MarkRequestPolicySuccess(c, nil)
	} else {
		policy.AddEvent(service.PolicyEvent{Decision: service.PolicyDecision{Action: "stop", Reason: "task_failed", Source: "upstream"}})
	}
	service.LogTaskConsumption(c, relayInfo, task)
	diagnostics.complete(task, result.Quota)

	return &taskSubmissionOutcome{Result: result, Task: task, RelayInfo: relayInfo}, nil
}

func presentTaskSubmission(c *gin.Context, outcome *taskSubmissionOutcome) {
	diagnostics := newTaskPluginSubmitDiagnostics(c)
	otherRatios := outcome.RelayInfo.PriceData.OtherRatios()
	if otherRatios == nil {
		otherRatios = map[string]float64{}
	}
	if ratiosJSON, err := common.Marshal(otherRatios); err == nil {
		c.Header("X-New-Api-Other-Ratios", string(ratiosJSON))
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedRoute); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedRoute); ok && pinned.Plugin != nil && pinned.Route.Render != "" {
			view, err := service.BuildTaskPluginView(outcome.Task)
			requestValue, _ := c.Get(pluginruntime.ContextKeyRouteRequest)
			requestContext, _ := requestValue.(pluginruntime.RouteRequestContext)
			if err == nil {
				viewValue, valueErr := taskPluginProtocolJSONValue(view)
				if valueErr == nil {
					if body, callErr := pinned.Plugin.Engine.CallPath(c.Request.Context(), "native", []string{pinned.Route.Render}, requestContext.JSValue(), viewValue); callErr == nil {
						diagnostics.present(outcome.Task, "native_presenter")
						c.JSON(http.StatusOK, body)
						return
					} else {
						logger.LogError(c, "task plugin native submit presenter failed: "+callErr.Error())
					}
				} else {
					logger.LogError(c, "encode task plugin native submit view failed: "+valueErr.Error())
				}
			} else {
				logger.LogError(c, "build task plugin native submit view failed: "+err.Error())
			}
		}
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint); ok && pinned.Protocol == "openai_video" && pinned.Operation.Name == "create" {
			diagnostics.present(outcome.Task, "openai_video_create")
			c.JSON(http.StatusOK, outcome.Task.ToOpenAIVideo())
			return
		}
	}
	createdAt := outcome.Task.CreatedAt
	if createdAt == 0 {
		createdAt = outcome.Task.SubmitTime
	}
	diagnostics.present(outcome.Task, "host_fallback")
	c.JSON(http.StatusOK, map[string]any{
		"id":         outcome.Task.TaskID,
		"task_id":    outcome.Task.TaskID,
		"status":     "queued",
		"model":      outcome.RelayInfo.OriginModelName,
		"created_at": createdAt,
	})
}

func respondTaskSubmissionError(c *gin.Context, taskErr *taskdto.TaskError) {
	service.RecordRequestPolicyTermination(c, taskSubmissionAPIError(taskErr))
	newTaskPluginSubmitDiagnostics(c).presentError(taskErr)
	if middleware.RespondTaskPluginError(c, taskErr) {
		return
	}
	respondTaskError(c, taskErr)
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *taskdto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

// taskSubmissionAPIError adapts a task error for the shared relay error paths.
// TaskError.Error is nil for many local rejections, so fall back to the message.
func taskSubmissionAPIError(taskErr *taskdto.TaskError) *types.NewAPIError {
	err := taskErr.Error
	if err == nil {
		err = errors.New(taskErr.Message)
	}
	return types.NewOpenAIError(err, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode)
}

// decideTaskRetry is the single retry decision for task submissions. The
// reason is recorded in the request policy decision events of the log details.
func decideTaskRetry(c *gin.Context, taskErr *taskdto.TaskError, retryTimes int) service.PolicyDecision {
	stop := service.PolicyDecision{Action: "stop", Source: "system"}
	retry := service.PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "system"}
	switch {
	// 宵禁 403 换渠道重试（不能直接返回给客户端），但仍受重试预算约束，
	// 避免所有渠道都宵禁时无限循环。
	case taskErr != nil && taskErr.StatusCode == 403 &&
		(strings.Contains(taskErr.Message, "system_curfew") || strings.Contains(taskErr.Message, "宵禁")):
		if retryTimes <= 0 {
			stop.Reason, stop.Source = "attempt_budget_exhausted", "global"
			return stop
		}
		retry.Reason = "curfew_retry"
		return retry
	case taskErr == nil:
		stop.Reason = "request_completed"
	case taskErr.NoRetry:
		stop.Reason = "task_accepted"
	case service.ShouldSkipRetryAfterChannelAffinityFailure(c):
		stop.Reason, stop.Source = "strict_session", "session_rule"
		if source := service.RequestPolicy(c).SessionModeSource; source != "" {
			stop.Source = source
		}
	case retryTimes <= 0:
		stop.Reason, stop.Source = "attempt_budget_exhausted", "global"
	case service.GetChannelConstraints(c).SuppressesRetry():
		stop.Reason, stop.Source = "pinned_channel", "channel_constraint"
	case taskErr.StatusCode == http.StatusTooManyRequests, taskErr.StatusCode == 307:
		return retry
	case taskErr.StatusCode/100 == 5:
		// 5xx 全部换渠道重试。必跳过清单（v29.4 起）默认为空——504/524 网关
		// 超时同样重试；下面这个 if 保留作运行期开关，管理员可通过选项把
		// 特定状态码重新拉回「必跳过」。
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			stop.Reason = "system_retry_exclusion"
			break
		}
		return retry
	case taskErr.StatusCode == http.StatusBadRequest, taskErr.StatusCode == 408:
		// azure处理超时不重试
		stop.Reason = "status_not_retryable"
	case taskErr.LocalError:
		stop.Reason = "local_rejection"
	case taskErr.StatusCode/100 == 2:
		stop.Reason = "system_retry_exclusion"
	default:
		return retry
	}
	return stop
}
