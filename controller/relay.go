package controller

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	taskdto "github.com/dengyie/apihub/dto"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/logger"
	"github.com/dengyie/apihub/middleware"
	"github.com/dengyie/apihub/model"
	pluginruntime "github.com/dengyie/apihub/pkg/jsplugin"
	perfmetrics "github.com/dengyie/apihub/pkg/perf_metrics"
	"github.com/dengyie/apihub/relay"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"

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

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
		// 提前声明，使终结 defer 能看到 GenRelayInfo 成功后的 relayInfo；
		// GenRelayInfo 失败时它为 nil，AppendRelayLogAdminInfo 会跳过。
		relayInfo *relaycommon.RelayInfo
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
		recordRequestErrorLogSafely(c, relayInfo, newAPIError)
		logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
		applyRelayTerminalStatusShield(c, newAPIError)
		newAPIError.SetMessage(common.MessageWithRequestId(clientFacingRelayMessage(newAPIError), requestId))
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

	relayInfo, err = relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}
	// 发布到 gin context：亲和性记录（distributor 在 c.Next() 之后调用的
	// RecordChannelAffinity）与终结路径之外的组件需要读流状态。WebSocket 路径
	// 已在 runCall 的 defer 里写同一个 key，这里补齐 HTTP 路径。
	common.SetContextKey(c, constant.ContextKeyRelayInfo, relayInfo)

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
		outcome := executeRelayAttempt(c, relayFormat, relayInfo, retryParam, requestBudgetCtx, &zeroByteDeadline)
		newAPIError = outcome.Err
		if outcome.Success || !outcome.WillRetry {
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
		// 重试轮选不出任何渠道：与首轮 SelectChannelForRequest 的 channel==nil
		// 分支同源，记一条 MODEL_EXHAUSTED 聚合证据。全部渠道在首轮之后才被
		// 熔断/打满的场景，此前在这里是安静的——「模型对外已不可用」没有任何
		// 可 grep 的信号，夜间全池挂掉只能等客户端报障。
		loadbalancer.RecordModelExhausted(info.OriginModelName, fmt.Sprintf("retry group=%s", selectGroup))
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
