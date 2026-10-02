package service

import (
	"errors"
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

// isModelScopedAutoDisable 判定一次自动禁用该摘多大范围。
//
// 判据只有一条：**这条错误是否可证明只关于某个模型**。可证明的只摘
// (渠道, 模型) 这一对，其余一律摘整条渠道 —— 因为摘错的代价不对称：把健康
// 模型连坐下线，是几十个模型一起消失；而该摘没摘，只是多烧几轮重试。
//
// 所以这里刻意不与 loadbalancer.BreakerScopeOf 共用判据：熔断侧自 v29.14 起
// 一律按模型级（少熔有递增退避兜着，且熔断能自愈），自动禁用不能自愈、要靠
// 人工或渠道测活才回来，判据必须更保守。
//
// 当前唯一命中的是「上游说这个模型不存在」这一类。它早该是模型级 —— 词表
// setting/operation_setting/operation_setting.go 的注释里早就写明
// 「No available channel for model …」是模型级、渠道本身还服务其它模型，
// 词表也确实没收它；但 ShouldDisableChannel 对同一类错误返回了 true 并走整渠道
// 禁用。两处自相矛盾的代价是实测出来的：#28/#70/#84 因单个模型 404 被整条摘掉。
func isModelScopedAutoDisable(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	// IsUpstreamModelUnavailableError 混进了 "cannot fetch token"，那是令牌级
	// 问题：只摘一个模型毫无意义，其余模型照样拿不到 token。必须拆出来。
	if strings.Contains(strings.ToLower(err.Error()), "cannot fetch token") {
		return false
	}
	return loadbalancer.IsUpstreamModelUnavailableError(err)
}

// DisableChannelForModel 把单个 (渠道, 模型) 退出轮转，渠道本身保持启用。
//
// 以下四种情况一律降级回整渠道禁用，宁可多熔不可假装生效：
//
//  1. 开关 AutomaticDisableModelScope 关闭 —— 上线期的默认状态；
//  2. MemoryCacheEnabled 打开 —— 内存缓存的 group2model2channels 是从
//     channels.Group × channels.Models 建的、不读 abilities.enabled
//     （model/channel_cache.go 的 InitChannelCache），此时改 abilities 对选路
//     完全不可见。若不兜底，特性会静默失效：禁用照写、路由照旧、零报错，
//     这是最难查的一类故障；
//  3. modelName 为空 —— 没有模型名就没有降级的依据；
//  4. 该渠道不服务这个模型 —— 翻 abilities 无意义。
func DisableChannelForModel(channelError types.ChannelError, modelName string, reason string) {
	if !common.AutomaticDisableModelScope {
		DisableChannel(channelError, reason)
		return
	}
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}
	if modelName == "" {
		common.SysLog(fmt.Sprintf("渠道「%s」（#%d）模型级禁用缺少模型名，降级为整渠道禁用", channelError.ChannelName, channelError.ChannelId))
		DisableChannel(channelError, reason)
		return
	}
	if common.MemoryCacheEnabled {
		common.SysLog(fmt.Sprintf("渠道「%s」（#%d）内存缓存模式下 abilities 改动对选路不可见，模型级禁用降级为整渠道禁用", channelError.ChannelName, channelError.ChannelId))
		DisableChannel(channelError, reason)
		return
	}

	needsChannelDisable, err := model.DisableChannelModel(channelError.ChannelId, modelName, reason)
	switch {
	case errors.Is(err, model.ErrChannelModelNotServed):
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）不服务模型「%s」，降级为整渠道禁用", channelError.ChannelName, channelError.ChannelId, modelName))
		DisableChannel(channelError, reason)
		return
	case err != nil:
		common.SysLog(fmt.Sprintf("渠道「%s」（#%d）模型「%s」禁用失败: %v，降级为整渠道禁用", channelError.ChannelName, channelError.ChannelId, modelName, err))
		DisableChannel(channelError, reason)
		return
	}

	if needsChannelDisable {
		// 摘完这个模型，该渠道已经没有任何可用的模型了。留一条 status=1 却在
		// 选路里永远选不中的僵尸渠道，比直接禁掉更难排查。
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）摘除模型「%s」后已无任何可用模型，升级为整渠道禁用", channelError.ChannelName, channelError.ChannelId, modelName))
		DisableChannel(channelError, reason)
		return
	}

	subject := fmt.Sprintf("通道「%s」（#%d）的模型「%s」已被禁用", channelError.ChannelName, channelError.ChannelId, modelName)
	content := fmt.Sprintf("通道「%s」（#%d）的模型「%s」已被禁用，原因：%s（该渠道其余模型不受影响）", channelError.ChannelName, channelError.ChannelId, modelName, reason)
	// 通知 type 必须与整渠道禁用区分开。CheckNotificationLimit 按
	// (userId, notifyType, hour) 计数、默认每小时 2 条；沿用整渠道的 type 会让
	// per-model 通知把该渠道的整渠道禁用通知挤掉 —— 而 per-model 禁用是每请求
	// 都可能触发的，撞上限流的机会远高于整渠道禁用，被挤掉的恰好总是更严重
	// 的那一类。带上模型名还顺带按模型分别计数，避免多个模型共用一个桶。
	NotifyRootUser(fmt.Sprintf("%s_model_%s", formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), modelName), subject, content)
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
