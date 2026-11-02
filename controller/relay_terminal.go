package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/logger"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// relayGatewayFaultStatus 是本网关回给客户端的「上游/网关侧故障」状态码。
//
// 不能用 502：Cloudflare 会把源站的 502 响应体整个替换成它自己的纯文本
// "error code: 502"，我们写进 body 的失败原因（哪个渠道、为什么失败）根本到不了
// 客户端，使用者只看到一句无从下手的通用错误。生产实测同一次请求直连源站能拿到
// {"error":{"message":"找不到可消费的工具调用状态：call_id=..."}}，经 CF 只剩
// "error code: 502"；而 503 及其响应体 CF 原样透传。两者同为 5xx，客户端 SDK 的
// 重试语义一致，也不会把网关故障误判成自己的凭证错误。
const relayGatewayFaultStatus = http.StatusServiceUnavailable

const upstreamToolCallStateLostMessage = "工具调用续接失败：当前渠道没有这次工具调用的会话记录，无法继续。" +
	"这通常不是你的请求或凭据有问题，而是网关侧的会话粘性记录失效（重启或发版后清空），" +
	"续接请求被路由到了当初产生该 call_id 的渠道之外。请重试；若反复失败，请新建会话重新开始。"

const upstreamReasoningHydrationErrorMessage = "跨账号推理思考密文校验失败：多轮会话中的加密思考项与上游账号不匹配。" +
	"网关已自动尝试剥离加密项并自愈重试，若重试耗尽仍失败，建议新建会话重新开始。"

// clientFacingGatewayStatus 把内部构造的 502 统一改写成 relayGatewayFaultStatus。
// 只在「回给客户端」的最后一跳改写：内部错误分类、熔断判定、日志仍按原始状态码进行。
func clientFacingGatewayStatus(statusCode int) int {
	if statusCode == http.StatusBadGateway {
		return relayGatewayFaultStatus
	}
	return statusCode
}

// clientFacingRelayMessage 决定客户端最终读到的错误正文。
//
// 默认原样透出上游/内部的报错，只改写「上游丢失工具调用会话状态」这一类：
// 上游原文（"找不到可消费的工具调用状态：call_id=..."）只描述现象，对使用者没有
// 任何可操作信息，而这条错误在生产里几乎全由本网关自己的渠道亲和失效引起
// （见 loadbalancer.IsUpstreamToolCallStateLostError）。说明成因与出路，
// 用户才知道该重试还是该新开会话。原始报错保留在句尾，便于对着日志排查。
func clientFacingRelayMessage(newAPIError *types.NewAPIError) string {
	if newAPIError == nil {
		return ""
	}
	if loadbalancer.IsUpstreamToolCallStateLostError(newAPIError) {
		return upstreamToolCallStateLostMessage + "上游原始报错：" + newAPIError.Error()
	}
	if loadbalancer.IsReasoningHydrationError(newAPIError) {
		return upstreamReasoningHydrationErrorMessage + "上游原始报错：" + newAPIError.Error()
	}
	return newAPIError.Error()
}

// applyRelayTerminalStatusShield 把上游渠道的 401/403/410/400 映射成网关侧故障码，
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
			loadbalancer.IsUpstreamModelUnavailableError(newAPIError) ||
			loadbalancer.IsUpstreamRelayError(newAPIError) ||
			loadbalancer.IsThinkingModeHistoryError(newAPIError) ||
			loadbalancer.IsUpstreamToolCallStateLostError(newAPIError) ||
			loadbalancer.IsReasoningHydrationError(newAPIError) ||
			newAPIError.StatusCode == http.StatusForbidden ||
			newAPIError.StatusCode == http.StatusUnauthorized ||
			newAPIError.StatusCode == http.StatusGone ||
			newAPIError.StatusCode == http.StatusBadRequest {
			newAPIError.StatusCode = relayGatewayFaultStatus
		}
	} else if loadbalancer.IsEOLError(newAPIError) {
		newAPIError.StatusCode = relayGatewayFaultStatus
	}
}

// clientFacingOpenAIError 构造向客户端透出的 OpenAIError。
//
// 核心原则：绝不能把渠道上游的额度耗尽（insufficient_quota）、鉴权失败（invalid_api_key）
// 或账号停用（account_deactivated）泄漏给客户端，除非本次请求确认是客户端用户自身的余额不足（ErrorCodeInsufficientUserQuota）。
// 很多 AI SDK（如 Vercel AI SDK、ZCode、Cursor）一旦在响应中看到 code="insufficient_quota"，
// 无论 HTTP 状态码是 429 还是 503，都会判定为不可恢复的账户欠费，直接终止执行并禁用后续重试（retryable: false）。
//
//  1. 若终结状态码被盾标改写为 5xx（如 503 relayGatewayFaultStatus），错误码必须对齐到该服务端状态码，
//     且 type 统一清洗为 upstream_error，确保客户端正确识别为服务端瞬时故障并正常重试。
//  2. 若走到了上游渠道（len(use_channel) > 0）且非用户自身欠费，即使状态码为 429（如聚合站 CPA 错报 code="insufficient_quota"），
//     也必须清洗渠道侧的误导性错误码，改写为 rate_limit_exceeded / rate_limit_error。
func clientFacingOpenAIError(c *gin.Context, newAPIError *types.NewAPIError, statusCode int) types.OpenAIError {
	openAIErr := newAPIError.ToOpenAIError()
	isUpstreamChannelError := c != nil && len(c.GetStringSlice("use_channel")) > 0
	isUserQuotaDepleted := newAPIError.GetErrorCode() == types.ErrorCodeInsufficientUserQuota

	if statusCode >= http.StatusInternalServerError {
		openAIErr.Code = statusCode
		typeStr := strings.ToLower(openAIErr.Type)
		if typeStr == "" || typeStr == "insufficient_quota" || typeStr == "invalid_request_error" ||
			strings.Contains(typeStr, "auth") || strings.Contains(typeStr, "permission") ||
			strings.Contains(typeStr, "key") || strings.Contains(typeStr, "quota") {
			openAIErr.Type = "upstream_error"
		}
	} else if isUpstreamChannelError && !isUserQuotaDepleted {
		codeStr := strings.ToLower(fmt.Sprintf("%v", openAIErr.Code))
		if strings.Contains(codeStr, "quota") || strings.Contains(codeStr, "insufficient") ||
			strings.Contains(codeStr, "invalid_api_key") || strings.Contains(codeStr, "account_deactivated") {
			if statusCode == http.StatusTooManyRequests || loadbalancer.IsUpstreamRateLimitError(newAPIError) {
				openAIErr.Code = "rate_limit_exceeded"
			} else {
				openAIErr.Code = statusCode
			}
		}
		if openAIErr.Type == "insufficient_quota" {
			if statusCode == http.StatusTooManyRequests || loadbalancer.IsUpstreamRateLimitError(newAPIError) {
				openAIErr.Type = "rate_limit_error"
			} else {
				openAIErr.Type = "upstream_error"
			}
		}
	}
	return openAIErr
}

// clientFacingClaudeError 构造向客户端透出的 ClaudeError。
//
// Claude 错误协议中没有独立的 code 字段，错误类别全靠 type（如 api_error、rate_limit_error、overloaded_error）。
// 同样必须防止渠道侧的 upstream 4xx 错误码（如 insufficient_quota、invalid_api_key）污染 type。
func clientFacingClaudeError(c *gin.Context, newAPIError *types.NewAPIError, statusCode int) types.ClaudeError {
	claudeErr := newAPIError.ToClaudeError()
	isUpstreamChannelError := c != nil && len(c.GetStringSlice("use_channel")) > 0
	isUserQuotaDepleted := newAPIError.GetErrorCode() == types.ErrorCodeInsufficientUserQuota

	if statusCode >= http.StatusInternalServerError {
		claudeErr.Type = "api_error"
	} else if isUpstreamChannelError && !isUserQuotaDepleted {
		typeStr := strings.ToLower(claudeErr.Type)
		if strings.Contains(typeStr, "quota") || strings.Contains(typeStr, "insufficient") ||
			strings.Contains(typeStr, "invalid_api_key") || strings.Contains(typeStr, "account_deactivated") {
			if statusCode == http.StatusTooManyRequests || loadbalancer.IsUpstreamRateLimitError(newAPIError) {
				claudeErr.Type = "rate_limit_error"
			} else {
				claudeErr.Type = "api_error"
			}
		}
	}
	return claudeErr
}

func writeRelayTerminalError(c *gin.Context, ws *websocket.Conn, relayFormat types.RelayFormat, newAPIError *types.NewAPIError) {
	if c == nil || newAPIError == nil {
		return
	}
	// 客户端读到的状态码在最后一跳统一改写，见 clientFacingGatewayStatus。
	statusCode := clientFacingGatewayStatus(newAPIError.StatusCode)
	openAIErr := clientFacingOpenAIError(c, newAPIError, statusCode)
	claudeErr := clientFacingClaudeError(c, newAPIError, statusCode)

	if !c.Writer.Written() {
		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			helper.WssError(c, ws, openAIErr)
		case types.RelayFormatClaude:
			c.JSON(statusCode, gin.H{
				"type":  "error",
				"error": claudeErr,
			})
		default:
			c.JSON(statusCode, gin.H{
				"error": openAIErr,
			})
		}
		return
	}
	// SSE 流已经输出部分数据（headers 已发送），无法再发送 HTTP 状态码或普通 JSON。
	// 必须通过 SSE 协议发送标准错误帧，通知客户端请求失败并携带错误码，
	// 这样客户端 SDK（如 ZCode、OpenAI SDK）能正确解析出错误并标记 retryable: true。
	var sseErrData string
	switch relayFormat {
	case types.RelayFormatClaude:
		errJSON, _ := common.Marshal(gin.H{
			"type":  "error",
			"error": claudeErr,
		})
		sseErrData = fmt.Sprintf("event: error\ndata: %s\n\n", string(errJSON))
	default:
		errJSON, _ := common.Marshal(gin.H{
			"error": openAIErr,
		})
		sseErrData = fmt.Sprintf("data: %s\n\n", string(errJSON))
	}
	_, _ = c.Writer.Write([]byte(sseErrData))
	c.Writer.Flush()
}

// recordRequestErrorLogSafely 把这次请求的失败补记成一条 type=5 行，否则通用日志页
// 只看得见成功的 type=2，请求级失败完全不可见。判开关、判 opt-out、判去重标记都在
// service.RecordRequestErrorLog 里面，这里只负责一件事：不让观测代码弄坏响应。
//
// HTTP 路径上这一行跑在终结 defer 里、writeRelayTerminalError 之前；WebSocket 路径上
// 它跑在逐轮终结的 middleware 里，而 responsesWSRequestEngine 是裸 gin.New()，全仓
// 唯一的 gin.CustomRecovery 挂在 main.go:258 的主 server 上，覆盖不到它。此处一旦
// panic 就会一路穿过 engine.ServeHTTP → s.runCall → ResponsesWebSocketHelper 掀掉整条
// WebSocket 连接。所以两条路径都必须过这一层包装，绝不能裸调 RecordRequestErrorLog。
//
// recover 只在本层自己 panic 时才拦得住：外层正在展开的 panic 传进来时，这里是
// 非 panic 帧，recover() 返回 nil，下面的 recover 分支不成立，外层 panic 照常继续
// 向上传播。反过来（HTTP 终结 defer 里本层自己 panic）也正是这里要拦的情况。
// 静默吞掉会让日志缺口变成无人知晓的缺口，所以 recover 之后留一条带定位信息的痕迹。
func recordRequestErrorLogSafely(c *gin.Context, relayInfo *relaycommon.RelayInfo, err *types.NewAPIError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// recover 存在的唯一意义就是事故时刻，所以这行必须自带定位信息：
			// 只有报错文本的话，值班的人还得先反查这次请求是谁、用什么模型、走的哪个渠道。
			logger.LogError(c, fmt.Sprintf("record request error log panicked: %v, user_id=%d, model=%s, channel_id=%d",
				recovered,
				c.GetInt("id"),
				common.GetContextKeyString(c, constant.ContextKeyOriginalModel),
				common.GetContextKeyInt(c, constant.ContextKeyChannelId)))
		}
	}()
	service.RecordRequestErrorLog(c, err, relayInfo)
}
