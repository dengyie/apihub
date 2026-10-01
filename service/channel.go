package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

func shouldCloseActiveWebSocketsAfterDisable(channelId int) bool {
	channel, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to check channel status before closing active websockets: channel_id=%d, error=%v", channelId, err))
		return true
	}
	return channel.Status != common.ChannelStatusEnabled
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
		if shouldCloseActiveWebSocketsAfterDisable(channelError.ChannelId) {
			CloseActiveWebSocketsForChannel(channelError.ChannelId, ChannelDisabledCloseReason)
		}
		subject := fmt.Sprintf("通道「%s」（#%d）已被禁用", channelError.ChannelName, channelError.ChannelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, reason)
		NotifyRootUser(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content)
	}
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
	}
}

func ShouldDisableChannel(channelId int, err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	// 策略文件首次加载失败时，breaker_exempt 无从解析（IsBreakerExempt 会因
	// !Enabled() 一律返回 false），兜底渠道会被静默摘出豁免名单。这里选择
	// 「判据不可信就不动手」：熔断会自愈，自动禁用不会——要人工或渠道测活才
	// 回来，两者的代价不对称。故障期间少一次自动禁用，换兜底链路不会被
	// 一个 yaml 路径问题悄悄拆掉。
	if !loadbalancer.PolicyReliable() {
		return false
	}
	// 兜底渠道豁免：本机 CPA 一旦被自动禁用就彻底失去退路，与熔断豁免
	// 复用同一份判定（loadbalancer 的 breaker_exempt），避免两条路径口径漂移。
	if loadbalancer.IsBreakerExempt(channelId) {
		return false
	}
	if err == nil {
		return false
	}
	// 客户端断开与空流墙钟耗尽不是渠道故障：即使以后有人去掉 skipRetry，
	// 也不得据此自动禁用。空流本身靠连续计数熔断，不走自动禁用。
	if types.IsClientAbortedError(err) {
		return false
	}
	if loadbalancer.IsEmptyStream(err) || loadbalancer.IsEmptyStreamBudget(err) {
		return false
	}
	// 确定性失效优先于一切：模型映射失效与 OAuth 凭据刷新失效都不会自愈，
	// 留在池子里等于每次请求都白烧一轮换渠道重试。自动禁用状态码默认只有 401，
	// 覆盖不到 404「模型不存在」这类返回码。
	if loadbalancer.IsUpstreamModelUnavailableError(err) {
		return true
	}
	if types.IsChannelError(err) {
		return true
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
