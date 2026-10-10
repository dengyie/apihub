package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/setting/operation_setting"
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

// EnableChannel 把渠道恢复为启用态，返回是否真的落库。
//
// 返回值必须被计数方消费：报表里的「自动恢复 N 次」如果对着返回 true 之外也
// 计数，就会把没写进 DB 的也算成恢复（UpdateChannelStatus 可能因渠道并发消失、
// 状态已被人工改过等原因拒绝写入）—— 2026-10-02 对账自动恢复事件时，计数器
// 与 channels.status 的对不上正是这类问题的排障成本。
func EnableChannel(channelId int, usingKey string, channelName string) bool {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		// 佐证计数的复位由 model.UpdateChannelStatus 统一负责（那里才是 status
		// 真正落库的地方，面板的单条/批量/按标签三条路径都汇到它），这里不再
		// 重复调用 —— 之前复位只写在 service 这一层，面板人工恢复完全绕过了它。
		subject := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
	}
	return success
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
	// 「选不出渠道」是池状态、自愈，不该进 per-model 路径。与 ShouldDisableChannel
	// 里的同名守卫构成双保险：万一上游改了措辞让上面那张白名单命中了，这里
	// 仍然拦住，不会把「暂时没渠道」误记成「这个模型被摘了」。
	if loadbalancer.IsRoutingExhaustedError(err) {
		return false
	}
	if loadbalancer.IsUpstreamGatewayModelDisabled(err) {
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

// autoDisableVerdict 是一次「是否该把这条渠道永久摘出去」的判定结果。
type autoDisableVerdict struct {
	// Disable 为 true 时 Class 必有值，指明是哪条判据命中的。
	Disable bool
	Class   string
}

// classifyAutoDisable 是 ShouldDisableChannel 的本体，**纯函数**：不碰任何
// 全局状态、不计数、不写库。判定与计数必须分开，原因见 ShouldDisableChannel。
func classifyAutoDisable(channelId int, err *types.NewAPIError) autoDisableVerdict {
	if !common.AutomaticDisableChannelEnabled {
		return autoDisableVerdict{}
	}
	// 策略文件首次加载失败时，breaker_exempt 无从解析（IsBreakerExempt 会因
	// !Enabled() 一律返回 false），兜底渠道会被静默摘出豁免名单。这里选择
	// 「判据不可信就不动手」：熔断会自愈，自动禁用不会——要人工或渠道测活才
	// 回来，两者的代价不对称。故障期间少一次自动禁用，换兜底链路不会被
	// 一个 yaml 路径问题悄悄拆掉。
	if !loadbalancer.PolicyReliable() {
		return autoDisableVerdict{}
	}
	// 兜底渠道豁免：本机 CPA 一旦被自动禁用就彻底失去退路，与熔断豁免
	// 复用同一份判定（loadbalancer 的 breaker_exempt），避免两条路径口径漂移。
	if loadbalancer.IsBreakerExempt(channelId) {
		return autoDisableVerdict{}
	}
	if err == nil {
		return autoDisableVerdict{}
	}
	// 客户端断开与空流墙钟耗尽不是渠道故障：即使以后有人去掉 skipRetry，
	// 也不得据此自动禁用。空流本身靠连续计数熔断，不走自动禁用。
	if types.IsClientAbortedError(err) {
		return autoDisableVerdict{}
	}
	if loadbalancer.IsEmptyStream(err) || loadbalancer.IsEmptyStreamBudget(err) {
		return autoDisableVerdict{}
	}
	// TTFT 超时是**延迟**信号，不是凭据失效信号，而且熔断器早就明确把它排除在
	// 失败计数之外了（controller/relay.go 里 lbAttempt.End 传的是
	// !loadbalancer.IsTTFTTimeout(...)）。两道防线对同一个事件给出相反判断，
	// 而自动禁用这一侧的后果是渠道级的、不可逆的。
	//
	// 根因在 types.IsChannelError：它是纯粹的「错误码带 channel: 前缀即渠道级」
	// 总闸，而 TTFT 的错误码恰恰就叫 channel:response_time_exceeded
	//（relaykit/types/error.go）。于是纯延迟抖动攒够 3 次佐证，就能把一条
	// 只是慢的渠道整条摘掉 —— 这与 classifyAutoDisable 上面那些守卫要防的
	// 「把可自愈的问题当确定性失效」是同一类错误，只是走了另一条道进来。
	//
	// 延迟的正确处置是熔断：按 (渠道, 模型) 粒度、冷却后自愈，不留痕不可逆。
	if loadbalancer.IsTTFTTimeout(err) {
		return autoDisableVerdict{}
	}
	// 「选不出渠道」是路由池状态，不是「这条凭据没有这个模型」。显式排除，
	// 不依赖下面 IsUpstreamModelUnavailableError 的白名单恰好漏掉它 —— 那一层
	// 是子串匹配，日后谁往表里补一条同措辞，就会把一条 85% 可用的渠道整条
	// 摘掉，且线上不会有任何报错。生产实测 #144 40 分钟成功 39 次、失败 7 次，
	// 失败全是这一类。
	if loadbalancer.IsRoutingExhaustedError(err) {
		return autoDisableVerdict{}
	}
	// 上游网关临时禁用/暂不可用（如 "disabled on this gateway"）属于网关侧策略或路由池状态，
	// 触发重试与按模型熔断冷却，但不得触发不可逆的自动禁用（避免未开模型级禁用时整渠道下线）。
	if loadbalancer.IsUpstreamGatewayModelDisabled(err) {
		return autoDisableVerdict{}
	}
	// 参数不支持**在任何状态码下**都不得触发自动禁用，理由与它不进
	// IsUpstreamRelayError 熔断是同一条：这是请求形状问题，裁掉参数重发即可，
	// 渠道本身完全健康。摘掉它不但治不好，反而让一条好渠道永久退出轮转
	// （自动禁用不像熔断那样会自愈），且每个请求都还在持续带着那个参数打过来。
	//
	// 这道闸门原先不必存在：自动禁用状态码默认只有 401，而参数错误过去只可能是
	// 400，天然不相交。2026-10-06 把参数识别的状态码门槛放宽到 5xx（上游把
	// 校验报文包成 500，生产实测渠道 #238）之后，两者就此相交 —— 必须显式排除，
	// 不能指望「默认状态码列表里碰巧没有 500」，那正是把安全建立在配置巧合上。
	if _, isParamErr := loadbalancer.IsParamNotSupportedError(err); isParamErr {
		return autoDisableVerdict{}
	}
	// 推理水合（解密）失败属于跨账号会话密文不兼容，剥离密文重试即可自愈，不得触发自动禁用
	if loadbalancer.IsReasoningHydrationError(err) {
		return autoDisableVerdict{}
	}
	// 确定性失效优先于一切：模型映射失效与 OAuth 凭据刷新失效都不会自愈，
	// 留在池子里等于每次请求都白烧一轮换渠道重试。自动禁用状态码默认只有 401，
	// 覆盖不到 404「模型不存在」这类返回码。
	if loadbalancer.IsUpstreamModelUnavailableError(err) {
		return autoDisableVerdict{Disable: true, Class: loadbalancer.CorroborationClassModelUnavailable}
	}
	if types.IsChannelError(err) {
		return autoDisableVerdict{Disable: true, Class: loadbalancer.CorroborationClassChannelError}
	}
	if types.IsSkipRetryError(err) {
		return autoDisableVerdict{}
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return autoDisableVerdict{Disable: true, Class: loadbalancer.CorroborationClassStatusCode}
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	if search {
		return autoDisableVerdict{Disable: true, Class: loadbalancer.CorroborationClassKeyword}
	}
	return autoDisableVerdict{}
}

// ShouldDisableChannel 只回答「这类错误该不该自动禁用渠道」，**不做计数**。
//
// 刻意保持纯函数：同一次上游失败在一条请求里会被评估两次 ——
// controller/relay.go 先 RecordPolicyFailure（内含一次判定，用来写
// RequestPolicy 的事件流），随后 processChannelError 又判定一次（真正执行
// 禁用）。把佐证计数塞进这里，一次失败会被记成两次，阈值形同虚设。
//
// 真正的计数与阈值闸门在 ShouldDisableChannelCorroborated，且只允许从
// service.ProcessChannelError 这一个咽喉点调用 —— 四条禁用路径
// （relay.go 同步/任务提交、channel-test.go 测活、responses_websocket.go）
// 全部汇流于此，在上游各判一次必然重复计数。
func ShouldDisableChannel(channelId int, err *types.NewAPIError) bool {
	return classifyAutoDisable(channelId, err).Disable
}

// ShouldDisableChannelCorroborated 在 ShouldDisableChannel 判「该禁」之后，
// 再要求同一 (渠道, 模型, 判据类别) 在窗口内重复到阈值，才承认这是
// 确定性失效。理由与阈值见 loadbalancer/corroboration.go。
//
// 未达阈值时返回 false —— 调用方**不得**因此把渠道摘出去。熔断器仍在按
// FailureThreshold 熔这条渠道，所以没到阈值的窗口不是「完全不管」，
// 只是把不可逆动作换成可自愈的那个。
//
// 只允许从 ProcessChannelError 调用：该函数是四条禁用路径的唯一汇流点，
// 在别处计数都会与它重复（原因见 ShouldDisableChannel 的注释）。
func ShouldDisableChannelCorroborated(channelId int, modelName string, err *types.NewAPIError) bool {
	verdict := classifyAutoDisable(channelId, err)
	if !verdict.Disable {
		return false
	}
	ready, count := loadbalancer.RecordAutoDisableSignal(channelId, modelName, verdict.Class)
	threshold := loadbalancer.GetPolicy().Default.Breaker.AutoDisableCorroborationThresholdOrDefault()
	if !ready && count == 1 {
		// 只记首次：同一个模型连续失败几百次时，日志里出现一条就够定位，
		// 几百条会把真正的信号淹掉（与 ModelExhaustion 告警同一个理由）。
		common.SysLog(fmt.Sprintf("channel #%d model %q hit auto-disable rule (%s) 1/%d, holding back disable; breaker still applies",
			channelId, modelName, verdict.Class, threshold))
	} else if ready && count == threshold {
		// 闸门打开的那一刻必须留痕。此前只有 count==1 那一条，而它恰恰是
		// 「没禁用」；于是从 1/3 涨到 3/3、真正执行不可逆摘除的那一刻，
		// 日志上什么都没有 —— 唯一的信号是下游那条「已被禁用」，看不出
		// 证据攒了几次、攒了多久。不可逆动作之前的那一步是排障的关键。
		//
		// 用 == 而不是 >=：达标之后每次失败仍然返回 true（渠道在被真正摘掉
		// 之前会一直重试），>= 会把「42/3、43/3 …」逐次刷出来 —— 一条稳定
		// 故障的渠道每来一个请求就产出一行，正是上面那条限定 count==1
		// 要避免的淹没。== 只在跨越阈值的那一次成立。
		common.SysLog(fmt.Sprintf("channel #%d model %q corroborated auto-disable rule (%s) %d/%d within window, proceeding with disable",
			channelId, modelName, verdict.Class, count, threshold))
	}
	return ready
}
