package controller

import (
	"fmt"
	"time"

	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/logger"
	"github.com/dengyie/apihub/model"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"
	"github.com/gin-gonic/gin"
)

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	service.ProcessChannelError(c, channelError, err, relayInfo)
}

// applyUpstreamFailureEffects 在一轮尝试失败后，把这次失效记到正确的熔断
// 作用域上。熔断（本函数）与自动禁用（processChannelError）在此分流：熔断
// 可逆、按 (渠道, 模型) 粒度快速绕开；账号级整体下线由 AutomaticDisable
// 兜底，两者互不替代。lbAttempt 是本次尝试的句柄（必传）：常规冷却路径
// 依据它的 failureTripped 标记与 End 的阈值/半开熔断去重，一次失败至多
// 一次 tripCount 递增。
//
// 分支必须是严格的 if/else-if 链（或收敛的 switch），一次失败至多一次 tripBreaker。
// 原因有二：
//
//  1. 错误分类器之间存在有意的重叠：IsUpstreamRelayError 会把
//     permission / thinking 类也认下（中转站常把它们包成自己的报错格式），
//     宵禁的 403 同时命中 IsUpstreamPermissionError，上游 new-api 家族包装
//     的额度错误（type=new_api_error）也会命中 relay 分类。若用并列 if，
//     同一次失败会触发多次 tripBreaker，tripCount 一次涨 2-3——递增退避在
//     首次失败就跳到 2-3 倍冷却、cap 提前触顶，故障渠道退出轮转的速度
//     比设计快一倍以上（2026-10 review 实证：一次权限错误 tripCount=3）。
//  2. 并列结构无法对「一次失败恰好一次 trip」写回归测试，重叠面一旦变化
//     只能在生产上发现。
//
// 链序 = 特异性序：处置最特殊的在前（宵禁要绝对到期时间、429 要短避让、
// EOL/quota 是确定性失效），兜底的 relay 分类在最后。
//
// 每条熔断日志都必须打出作用域：按模型熔断之后，只打渠道 id 的日志无法
// 归因「到底是哪个模型把它熔了」。scopeOf 与 tracker.scopeKey 同源
// （loadbalancer.EffectiveModelName），per_model 关闭时标签自动落回渠道级。
func applyUpstreamFailureEffects(c *gin.Context, channel *model.Channel, relayInfo *relaycommon.RelayInfo, lbAttempt *loadbalancer.RequestHandle, newAPIError *types.NewAPIError) {
	if newAPIError == nil {
		return
	}
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
		// 常规冷却路径尊重句柄标记：End 的阈值/半开路径已为本次失败熔断过时，
		// 这里只留日志不重复递增 tripCount（一次失败至多一次熔断，见
		// RequestHandle.failureTripped）。blockedUntil 路径（宵禁）不查标记，
		// 它改写的是绝对到期时间，必须无条件执行。
		if until.IsZero() && lbAttempt.FailureTripped() {
			logger.LogInfo(c, fmt.Sprintf("渠道 #%d [%s] 本次失败已由连续失败阈值触发熔断，显式分类不重复递增退避", channel.Id, scopeLabel(effective)))
		} else {
			trip(effective)
			lbAttempt.MarkFailureTripped()
		}
		if effective == "" {
			return ""
		}
		for _, sibling := range loadbalancer.SiblingModelsFromMapping(channel.GetModelMapping(), modelName) {
			trip(sibling)
			logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 与 %s 映射到同一上游模型，一并熔断", channel.Id, scopeLabel(sibling), modelName))
		}
		return effective
	}

	// 智能负载：基于收敛的错误分类执行严格单次熔断
	effect, curfewUntil := service.ClassifyBreakerEffect(newAPIError)
	switch effect {
	case service.BreakerRateLimit:
		effective := scopeOf(newAPIError)
		loadbalancer.GlobalTracker().TripBreakerForRateLimit(channel.Id, effective)
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游限流/并发超限 (429/RPM/Concurrency)，已设置短期避让冷却: %s", channel.Id, scopeLabel(effective), newAPIError.Error()))
		for _, sibling := range loadbalancer.SiblingModelsFromMapping(channel.GetModelMapping(), modelName) {
			loadbalancer.GlobalTracker().TripBreakerForRateLimit(channel.Id, sibling)
			logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 与 %s 映射到同一上游模型，一并避让", channel.Id, scopeLabel(sibling), modelName))
		}
	case service.BreakerCurfew:
		tripped := tripModelScope(newAPIError, curfewUntil)
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 宵禁中，已熔断到 %s", channel.Id, scopeLabel(tripped), curfewUntil.Format("15:04")))
	case service.BreakerEOL:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游模型已 EOL/下线 (410)，已立即熔断", channel.Id, scopeLabel(tripped)))
	case service.BreakerStreamBroken:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游流传输中断 (%s)，已立即熔断并触发重试", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerQuota:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游额度已耗尽，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerRouting:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游会话路由失败 (缺失 x-opencode-session)，已立即熔断该渠道: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerThinkingHistory:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游思考模式历史消息不兼容 (reasoning_content must be passed back)，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerModelUnavailable:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游模型不可用/已禁用，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerPermission:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游权限受限/分组无权访问/模型不支持，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerRelay:
		tripped := tripModelScope(newAPIError, time.Time{})
		logger.LogWarn(c, fmt.Sprintf("渠道 #%d [%s] 上游中继代理异常 (bad response status code / 渠道出错)，已立即熔断: %s", channel.Id, scopeLabel(tripped), newAPIError.Error()))
	case service.BreakerNone:
	}
}
