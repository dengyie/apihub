package loadbalancer

import (
	"sync"
	"time"
)

// DefaultRequestTimeoutMs 非流式请求的整请求预算默认值（毫秒）。
//
// 非流式的上游响应是「算完才来」：TTFT 定时器只覆盖流式的首字等待，非流式
// 此前没有任何网关侧边界，单次尝试可以一直挂到上游或其前置网关（Cloudflare
// ~100s）判死，跨渠道重试叠加时总等待无上界。180s 对合法长生成足够宽松，
// 又能在系统性慢上游时把总等待封住。
const DefaultRequestTimeoutMs = 180000

// DefaultTTFTTimeoutMs 默认首字超时（毫秒）。
//
// 30s 而非更短：实测流式请求里首字超过 15s 的占 12.5%、超过 30s 的占 3.7%
// （长思考模型居多），15s 会把这些本来能出字的请求判成慢请求并换渠道。
// 同时 30s 仍远小于 105s 客户端安全死线，超时后仍留得下换渠道重试的窗口。
const DefaultTTFTTimeoutMs = 30000

// DefaultHeaderTimeoutMs 已废弃：响应头闸门未配置时改为跟随 TTFT 预期。
//
// 曾固定为 15s，但它与 TTFT 取 min 之后就成了实际生效的首字闸门：线上把
// ttft_timeout_ms 配成 100s 的运维意图被这个未配置的默认值静默压回 15s，
// 13 万 token 的长上下文请求被成批判成「响应头等待超时」换渠道，还会连带
// 喂给熔断器把健康渠道判死。响应头等待本就是首字等待的一部分
// （stream_scanner 的 TTFT 定时器会扣减已消耗的响应头时间），因此它不该比
// 运维显式配置的首字预期更短。保留常量只为外部引用兼容，内部不再使用。
const DefaultHeaderTimeoutMs = 15000

// DefaultClientSafetyDeadlineMs 默认下游客户端安全死线（毫秒）。
// 绝大多数现代 AI 客户端（如 ZCode、Claude Code、OpenAI SDK、Node.js 运行时）
// 底层 HTTP 请求超时为 120 秒左右。
// 流式请求在首字未出（零字节）状态下的总等待预算必须严格受控于此死线（保留至少 15s 缓冲），
// 避免超时撞车被下游掐线导致底层 Socket EPIPE，并使网关能够抢在客户端掐线前主动返回合法的 504 响应。
const DefaultClientSafetyDeadlineMs = 105000

// DefaultEmptyStreamRetryBudgetMs 流式请求在「客户端仍未收到任何内容字节」
// 时，换渠道重试的墙钟默认值（毫秒）。长流出字后立即作废，不套到
// request_timeout_ms 上。
const DefaultEmptyStreamRetryBudgetMs = 25000

// DefaultEmptyStreamTripThreshold 同一渠道连续空流达到此次数后熔断。
// 只熔断、不自动禁用。0 表示关闭。
const DefaultEmptyStreamTripThreshold = 3

// Policy 定义智能负载的全部策略参数。
// 策略支持热加载：修改 YAML 配置文件后自动生效，无需重启服务。
type Policy struct {
	Enabled  bool                  `yaml:"enabled"`
	Default  ChannelPolicy         `yaml:"default"`
	Channels map[int]ChannelPolicy `yaml:"channels"` // key: channel ID，覆盖默认策略
	// MaxRetries 单个请求失败后的最大重试次数（换渠道重试）。
	// 0 表示不重试。修改后热加载生效。
	MaxRetries int `yaml:"max_retries"`

	// RequestTimeoutMs 非流式请求的整请求预算（毫秒），所有重试尝试共享同一
	// 预算，到期后停止重试并以 504 返回。
	//
	// 指针三态：缺省（yaml 未写该键）用 DefaultRequestTimeoutMs；显式 0 表示
	// 关闭。流式请求不受它约束——首字等待归 ttft_timeout_ms，长流是合法形态。
	RequestTimeoutMs *int `yaml:"request_timeout_ms"`

	// EmptyStreamRetryBudgetMs 流式请求在客户端仍未收到任何内容字节时，
	// 换渠道重试的墙钟（毫秒）。指针三态：缺省 DefaultEmptyStreamRetryBudgetMs；
	// 显式 0 = 关闭。不作用于已开始出字的长流，也不套到 request context 上。
	EmptyStreamRetryBudgetMs *int `yaml:"empty_stream_retry_budget_ms"`

	// ClientSafetyDeadlineMs 下游客户端安全死线（毫秒）。
	// 截断流式零字节等待总预算。指针三态：缺省 DefaultClientSafetyDeadlineMs；显式 0 = 关闭。
	ClientSafetyDeadlineMs *int `yaml:"client_safety_deadline_ms"`

	// EmptyStreamTripThreshold 同一渠道连续空流达到此次数后熔断。
	// 指针三态：缺省 DefaultEmptyStreamTripThreshold；显式 0 = 关闭。
	EmptyStreamTripThreshold *int `yaml:"empty_stream_trip_threshold"`

	// StripThinkingChannels 需要裁剪 thinking 参数的渠道 ID 列表（兼容旧配置）。
	// 这些渠道的上游不支持 thinking 参数，转发时会自动去掉。
	// 新配置请用 StripParams，可指定任意参数。
	StripThinkingChannels []int `yaml:"strip_thinking_channels"`

	// StripParams 通用参数裁剪：channelID -> 需要去掉的参数名列表。
	// 例如：140: [thinking, reasoning_effort]
	// 不填则靠运行时自动学习（收到 400 参数不支持错误后自动标记）。
	StripParams map[int][]string `yaml:"strip_params"`
}

// RequestTimeout 返回非流式请求的整请求预算；0 表示禁用。
func (p *Policy) RequestTimeout() time.Duration {
	if p == nil {
		return time.Duration(DefaultRequestTimeoutMs) * time.Millisecond
	}
	if p.RequestTimeoutMs == nil {
		return time.Duration(DefaultRequestTimeoutMs) * time.Millisecond
	}
	if *p.RequestTimeoutMs <= 0 {
		return 0
	}
	return time.Duration(*p.RequestTimeoutMs) * time.Millisecond
}

// GetRequestTimeout 返回当前生效策略的非流式整请求预算。
func GetRequestTimeout() time.Duration {
	return GetPolicy().RequestTimeout()
}

// ClientSafetyDeadline 返回流式零字节安全死线；0 表示关闭。
func (p *Policy) ClientSafetyDeadline() time.Duration {
	if p == nil {
		return time.Duration(DefaultClientSafetyDeadlineMs) * time.Millisecond
	}
	if p.ClientSafetyDeadlineMs == nil {
		return time.Duration(DefaultClientSafetyDeadlineMs) * time.Millisecond
	}
	if *p.ClientSafetyDeadlineMs <= 0 {
		return 0
	}
	return time.Duration(*p.ClientSafetyDeadlineMs) * time.Millisecond
}

// MinRetryWindow 下一次换渠道尝试至少要留出的时间；剩余不足时不再重试。
const MinRetryWindow = 10 * time.Second

// retryReserve 还能重试时，从客户端安全死线里为下一次尝试预留的时间。
//
// 必须严格大于 MinRetryWindow：controller 的预算闸门要求「此刻 + MinRetryWindow」
// 早于死线，首字定时器触发后再走到闸门已过去若干毫秒，只预留 MinRetryWindow
// 的话闸门恒不通过，预留形同虚设。取两倍：线上单次成功流式的首字 95% 在 20s
// 内，换上的渠道扣掉选路与建连开销后仍大概率来得及出首字。
const retryReserve = 2 * MinRetryWindow

// AttemptFirstTokenDeadline 返回一次流式尝试等待首字（含响应头）的截止时刻；
// 零值表示不设截止。
//
// 首字窗口从本次尝试开始计时，前面失败的尝试不占用它。客户端安全死线是整次
// 请求共享的上限：还能重试时，本次截止在死线前 retryReserve，保证慢渠道超时
// 后仍有一次换渠道机会，同时把等待尽量留给慢而正常的上游（线上 ttft 100s 对
// 105s 死线：首轮可等 85s，单次成功里首字晚于 85s 的约 0.2%）。预留之后本次
// 剩不到 MinRetryWindow 时，本次就是最后机会，用满到死线。
func (p *Policy) AttemptFirstTokenDeadline(channelID int, requestStart, attemptStart time.Time, canRetry bool) time.Time {
	ttft := p.Resolve(channelID).TTFTTimeoutMs
	if ttft <= 0 {
		return time.Time{}
	}
	deadline := attemptStart.Add(time.Duration(ttft) * time.Millisecond)
	safety := p.ClientSafetyDeadline()
	if safety <= 0 || requestStart.IsZero() {
		return deadline
	}
	ceiling := requestStart.Add(safety)
	if reserved := ceiling.Add(-retryReserve); canRetry && reserved.Sub(attemptStart) >= MinRetryWindow {
		ceiling = reserved
	}
	if deadline.After(ceiling) {
		return ceiling
	}
	return deadline
}

// GetClientSafetyDeadline 返回当前生效策略的客户端安全死线。
func GetClientSafetyDeadline() time.Duration {
	return GetPolicy().ClientSafetyDeadline()
}

// EmptyStreamRetryBudget 返回流式零字节换渠道重试墙钟；0 表示禁用。
func (p *Policy) EmptyStreamRetryBudget() time.Duration {
	if p == nil {
		return time.Duration(DefaultEmptyStreamRetryBudgetMs) * time.Millisecond
	}
	safety := p.ClientSafetyDeadline()
	if p.EmptyStreamRetryBudgetMs != nil {
		if *p.EmptyStreamRetryBudgetMs <= 0 {
			return 0
		}
		configured := time.Duration(*p.EmptyStreamRetryBudgetMs) * time.Millisecond
		if reqTimeout := p.RequestTimeout(); reqTimeout > 0 && configured > reqTimeout {
			configured = reqTimeout
		}
		if safety > 0 && configured > safety {
			configured = safety
		}
		return configured
	}
	// 当未显式配置 empty_stream_retry_budget_ms 时，自适应计算动态预算：
	// 预算必须能够覆盖单次 TTFT 超时及后续重试窗口，避免「单次 TTFT 超时立即将整个重试掐死」的配置冲突死锁。
	maxTTFT := p.Default.TTFTTimeoutMs
	for _, ch := range p.Channels {
		if ch.TTFTTimeoutMs > maxTTFT {
			maxTTFT = ch.TTFTTimeoutMs
		}
	}
	baseBudget := time.Duration(DefaultEmptyStreamRetryBudgetMs) * time.Millisecond
	if maxTTFT > 0 {
		retries := p.MaxRetries
		if retries <= 0 {
			retries = 1
		}
		needed := time.Duration(int64(retries+1)*int64(maxTTFT)) * time.Millisecond
		if needed > baseBudget {
			baseBudget = needed
		}
	}
	if reqTimeout := p.RequestTimeout(); reqTimeout > 0 && baseBudget > reqTimeout {
		baseBudget = reqTimeout
	}
	if safety > 0 && baseBudget > safety {
		baseBudget = safety
	}
	return baseBudget
}

// GetEmptyStreamRetryBudget 返回当前生效策略的流式零字节墙钟。
func GetEmptyStreamRetryBudget() time.Duration {
	return GetPolicy().EmptyStreamRetryBudget()
}

// EmptyStreamTripLimit 返回连续空流熔断阈值；0 表示关闭。
func (p *Policy) EmptyStreamTripLimit() int {
	if p == nil || p.EmptyStreamTripThreshold == nil {
		return DefaultEmptyStreamTripThreshold
	}
	if *p.EmptyStreamTripThreshold <= 0 {
		return 0
	}
	return *p.EmptyStreamTripThreshold
}

// GetEmptyStreamTripThreshold 返回当前生效策略的连续空流熔断阈值。
func GetEmptyStreamTripThreshold() int {
	return GetPolicy().EmptyStreamTripLimit()
}

// ChannelPolicy 单个渠道的策略参数。
// 零值表示"未设置"，使用 Default 中的对应值。
type ChannelPolicy struct {
	// MaxInflight 单渠道最大并发请求数。超过后新请求跳过该渠道。
	// 0 表示不限制。
	MaxInflight int `yaml:"max_inflight"`

	// TTFTTimeoutMs 首字超时（毫秒）。流式请求超过此时长未收到首个 token
	// 则视为超时，触发熔断计数并允许重试到下一个渠道。
	// 0 表示不启用首字超时检测。
	TTFTTimeoutMs int64 `yaml:"ttft_timeout_ms"`

	// HeaderTimeoutMs 上游 HTTP 响应头等待超时（毫秒）。
	// 若超过此时长上游未返回响应头，判定为 Header 等待超时，快速熔断并触发换渠道重试。
	// 0 表示未设置：跟随该渠道生效的 TTFTTimeoutMs（见 ResolveHeaderTimeout）。
	HeaderTimeoutMs int64 `yaml:"header_timeout_ms"`

	// Breaker 熔断器参数
	Breaker BreakerPolicy `yaml:"breaker"`

	// BreakerExempt 豁免熔断与自动禁用。
	//
	// 用于本机 CPA（127.0.0.1:8317）这类兜底渠道：它们是最后一道防线，
	// 一旦因为上游抖动被熔断或被自动禁用，整条链路就没有退路了。
	// 豁免后该渠道不参与熔断统计、IsAvailable 恒为可用、自动禁用直接跳过。
	// 代价是 CPA 真的宕机时每个请求都会先撞一次快速失败的本地调用，
	// 但本机 loopback 失败是毫秒级的，远小于失去兜底的代价。
	BreakerExempt bool `yaml:"breaker_exempt"`
}

// ResolveHeaderTimeout 返回生效的 HTTP 响应头等待超时。
//
// HeaderTimeoutMs 显式配置时以它为上限，但绝不超过该渠道的 TTFT 预期；
// 未配置时直接跟随 TTFT。响应头等待是首字等待的前半段——不少上游直到第一个
// token 就绪才发响应头，用一个固定的小默认值会让显式配置的 TTFT 形同虚设
// （线上 ttft_timeout_ms 配了 100s，实际闸门仍是 15s，长上下文请求成批被误判
// 超时，还连带把健康渠道喂给熔断器）。
func (cp ChannelPolicy) ResolveHeaderTimeout() time.Duration {
	ttft := cp.TTFTTimeoutMs
	if ttft <= 0 {
		ttft = DefaultTTFTTimeoutMs
	}
	header := cp.HeaderTimeoutMs
	if header <= 0 {
		header = ttft
	}
	if ttft < header {
		header = ttft
	}
	return time.Duration(header) * time.Millisecond
}

// BreakerPolicy 熔断器策略
type BreakerPolicy struct {
	// FailureThreshold 连续慢请求/失败达到此次数后熔断
	FailureThreshold int `yaml:"failure_threshold"`
	// CooldownSeconds 熔断后冷却时间，之后进入半开状态允许探测
	CooldownSeconds int64 `yaml:"cooldown_seconds"`
	// RateLimitCooldownSeconds 遭遇 429 频次/并发限流时的短期避让冷却时间（秒），默认 30 秒
	RateLimitCooldownSeconds int64 `yaml:"rate_limit_cooldown_seconds"`
	// EscalationCap 连续熔断的冷却递增上限倍数。
	//
	// 第 n 次连续熔断的冷却 = cooldown_seconds × min(n, EscalationCap)：
	// 第 1 次 1 倍、第 2 次 2 倍、第 3 次 3 倍……一次成功即清零回到 1 倍。
	// 封顶是为了避免反复故障的渠道被冷却到数小时而彻底退出轮转。
	EscalationCap int64 `yaml:"escalation_cap"`
	// HalfOpenProbes 半开状态允许的探测请求数
	HalfOpenProbes int `yaml:"half_open_probes"`
	// PerModel 熔断粒度：true = 按 (渠道, 模型) 分键，一个模型坏不牵连
	// 同渠道其它模型；false = 按渠道，一失败全模型下线（v29.10 行为）。
	//
	// 用指针而非 bool：Resolve 是逐字段值合并，裸 bool 的零值 false 会在
	// 「某渠道只配了 failure_threshold」时把全局的 true 覆盖回 false，
	// 而且是静默的。*bool 才能区分「没配」（跟随全局）和「显式配 false」
	// （这个渠道就是要渠道级）。
	//
	// 默认 nil → 生效值 false，即新逻辑默认关闭。代码先以关闭状态上线，
	// 确认无误后改一行 YAML 打开，5 秒内热加载生效无需重启；发现不对改回
	// false 同样即时。这既是灰度手段也是回滚手段。
	PerModel *bool `yaml:"per_model"`
	// 以下五个字段（model_exhaustion_* 与 auto_disable_corroboration_*）是全局
	// 阈值，只读 default.breaker；写在 channels.<id>.breaker 下不生效，热加载时
	// 会打一行 ignored 日志提醒。

	// ModelExhaustionThreshold 「该模型选不出任何渠道」在窗口内累计到此次数
	// 即输出一条 MODEL_EXHAUSTION 告警行。0 = 关闭该检测。
	//
	// 这不是熔断，是**对外可用性的观测**：熔断只负责绕开坏渠道，而当一个模型
	// 的全部渠道同时熔断/打满/被禁用时，网关内部一片安静，客户端只看到 503。
	// 没有这条信号，模型全池挂掉只能等客户端来问。
	ModelExhaustionThreshold int `yaml:"model_exhaustion_threshold"`
	// ModelExhaustionWindowSeconds 上述计数窗口（秒），默认 300。
	ModelExhaustionWindowSeconds int64 `yaml:"model_exhaustion_window_seconds"`
	// ModelExhaustionAlertCooldownSeconds 同一模型两次告警的最小间隔（秒），
	// 默认 1800。必须有：客户端重试风暴会在几秒内打出上百次同类 503，
	// 不节流的日志只会把真正的信号淹掉。
	ModelExhaustionAlertCooldownSeconds int64 `yaml:"model_exhaustion_alert_cooldown_seconds"`
	// AutoDisableCorroborationThreshold 自动禁用所需的佐证次数，默认 3。
	//
	// 自动禁用**不可逆**（要靠测活或人工才回来），而判据是启发式的：四条
	// 判据里两条靠对上游自由文本做子串匹配。同一套系统里可逆的熔断要求
	// 连续 failure_threshold 次失败，不可逆的禁用却只要命中一次 —— 这个
	// 不对称是反的。阈值把「一次措辞巧合」挡在不可逆动作之外，代价只是
	// 多浪费几轮换渠道重试。
	//
	// 设为 1 即完全退回 v29.19 及更早的行为，用于出问题时即时止血。
	AutoDisableCorroborationThreshold int `yaml:"auto_disable_corroboration_threshold"`
	// AutoDisableCorroborationWindowSeconds 上述计数窗口（秒），默认 600。
	AutoDisableCorroborationWindowSeconds int64 `yaml:"auto_disable_corroboration_window_seconds"`
}

// AutoDisableCorroborationThresholdOrDefault 返回自动禁用的佐证阈值，默认 3。
func (b BreakerPolicy) AutoDisableCorroborationThresholdOrDefault() int {
	if b.AutoDisableCorroborationThreshold <= 0 {
		return 3
	}
	return b.AutoDisableCorroborationThreshold
}

// AutoDisableCorroborationWindowSecondsOrDefault 返回佐证计数窗口（秒），默认 600。
func (b BreakerPolicy) AutoDisableCorroborationWindowSecondsOrDefault() int64 {
	if b.AutoDisableCorroborationWindowSeconds <= 0 {
		return 600
	}
	return b.AutoDisableCorroborationWindowSeconds
}

// ModelExhaustionThresholdOrDefault 返回告警阈值，未配置时为 5。
func (b BreakerPolicy) ModelExhaustionThresholdOrDefault() int {
	if b.ModelExhaustionThreshold <= 0 {
		return 5
	}
	return b.ModelExhaustionThreshold
}

// ModelExhaustionWindowSecondsOrDefault 返回计数窗口（秒），未配置时为 300。
func (b BreakerPolicy) ModelExhaustionWindowSecondsOrDefault() int64 {
	if b.ModelExhaustionWindowSeconds <= 0 {
		return 300
	}
	return b.ModelExhaustionWindowSeconds
}

// ModelExhaustionAlertCooldownSecondsOrDefault 返回告警冷却（秒），未配置时为 1800。
func (b BreakerPolicy) ModelExhaustionAlertCooldownSecondsOrDefault() int64 {
	if b.ModelExhaustionAlertCooldownSeconds <= 0 {
		return 1800
	}
	return b.ModelExhaustionAlertCooldownSeconds
}

// PerModelOrDefault 返回生效的熔断粒度。未配置时为 false（渠道级 = v29.10 行为）。
func (b BreakerPolicy) PerModelOrDefault() bool {
	return b.PerModel != nil && *b.PerModel
}

// DefaultEscalationCap 连续熔断冷却倍数的内置上限。
const DefaultEscalationCap = 6

// EscalationCapOrDefault 返回生效的递增上限倍数。
func (b BreakerPolicy) EscalationCapOrDefault() int64 {
	if b.EscalationCap <= 0 {
		return DefaultEscalationCap
	}
	return b.EscalationCap
}

// ChannelMaxInflightProvider 动态提供渠道最大并发连接数的函数签名。
// 避免 loadbalancer 与 model 产生包循环依赖。
type ChannelMaxInflightProvider func(channelID int) int

var (
	channelMaxInflightProviderMu sync.RWMutex
	channelMaxInflightProvider   ChannelMaxInflightProvider
)

// SetChannelMaxInflightProvider 注册渠道动态最大并发连接数提供器。
func SetChannelMaxInflightProvider(fn ChannelMaxInflightProvider) {
	channelMaxInflightProviderMu.Lock()
	defer channelMaxInflightProviderMu.Unlock()
	channelMaxInflightProvider = fn
}

// GetChannelMaxInflight 返回渠道配置的动态最大并发数；未设置或返回 <=0 表示未指定。
func GetChannelMaxInflight(channelID int) int {
	channelMaxInflightProviderMu.RLock()
	fn := channelMaxInflightProvider
	channelMaxInflightProviderMu.RUnlock()
	if fn != nil && channelID > 0 {
		return fn(channelID)
	}
	return 0
}

// ChannelTimeouts 渠道在数据库里配置的超时（毫秒）。0 表示未配置、跟随 YAML。
type ChannelTimeouts struct {
	// TTFTTimeoutMs 流式首字超时。
	TTFTTimeoutMs int
	// HeaderTimeoutMs 上游响应头等待超时。
	HeaderTimeoutMs int
}

// ChannelTimeoutsProvider 动态提供渠道超时配置的函数签名。
// 与 ChannelMaxInflightProvider 同样用函数值注册，避免 loadbalancer <-> model 循环依赖。
type ChannelTimeoutsProvider func(channelID int) ChannelTimeouts

var (
	channelTimeoutsProviderMu sync.RWMutex
	channelTimeoutsProvider   ChannelTimeoutsProvider
)

// SetChannelTimeoutsProvider 注册渠道动态超时提供器。传 nil 注销。
func SetChannelTimeoutsProvider(fn ChannelTimeoutsProvider) {
	channelTimeoutsProviderMu.Lock()
	defer channelTimeoutsProviderMu.Unlock()
	channelTimeoutsProvider = fn
}

// GetChannelTimeouts 返回渠道在数据库里配置的超时；未注册或未配置时返回零值。
func GetChannelTimeouts(channelID int) ChannelTimeouts {
	channelTimeoutsProviderMu.RLock()
	fn := channelTimeoutsProvider
	channelTimeoutsProviderMu.RUnlock()
	if fn == nil || channelID <= 0 {
		return ChannelTimeouts{}
	}
	return fn(channelID)
}

// Resolve 将渠道级策略与默认策略合并，返回生效的完整策略
//
// 优先级（高 -> 低）：
//  1. 渠道在管理面板配置的 setting（ttft_timeout_ms / header_timeout_ms）
//  2. loadbalancer.yaml 的 channels[id]
//  3. loadbalancer.yaml 的 default
//
// 动态值必须是最高的那一层，否则「在后台改首字超时」对已经写进 YAML 的渠道
// 完全不起作用——而那恰恰是最需要单独调的渠道。0 表示未配置，逐字段回退。
func (p *Policy) Resolve(channelID int) ChannelPolicy {
	if p == nil || !p.Enabled {
		return ChannelPolicy{}
	}
	// 动态渠道设置优先于静态配置文件：渠道在 UI / 数据库中设置的 MaxInflight
	// 享有最高优先级，0 或未配置时回退到 loadbalancer.yaml 中的 channels[id] 或 default。
	dynamicMaxInflight := GetChannelMaxInflight(channelID)
	dynamicTimeouts := GetChannelTimeouts(channelID)
	cp, ok := p.Channels[channelID]
	if !ok {
		resolved := p.Default
		if dynamicMaxInflight > 0 {
			resolved.MaxInflight = dynamicMaxInflight
		}
		if dynamicTimeouts.TTFTTimeoutMs > 0 {
			resolved.TTFTTimeoutMs = int64(dynamicTimeouts.TTFTTimeoutMs)
		}
		if dynamicTimeouts.HeaderTimeoutMs > 0 {
			resolved.HeaderTimeoutMs = int64(dynamicTimeouts.HeaderTimeoutMs)
		}
		return resolved
	}
	// 零值字段回退到 Default
	resolved := p.Default
	if cp.MaxInflight != 0 {
		resolved.MaxInflight = cp.MaxInflight
	}
	if dynamicMaxInflight > 0 {
		resolved.MaxInflight = dynamicMaxInflight
	}
	if cp.TTFTTimeoutMs != 0 {
		resolved.TTFTTimeoutMs = cp.TTFTTimeoutMs
	}
	if cp.HeaderTimeoutMs != 0 {
		resolved.HeaderTimeoutMs = cp.HeaderTimeoutMs
	}
	// 动态值最后应用：它是 UI 上改出来的，要能盖过 YAML 里 channels[id] 的静态值。
	if dynamicTimeouts.TTFTTimeoutMs > 0 {
		resolved.TTFTTimeoutMs = int64(dynamicTimeouts.TTFTTimeoutMs)
	}
	if dynamicTimeouts.HeaderTimeoutMs > 0 {
		resolved.HeaderTimeoutMs = int64(dynamicTimeouts.HeaderTimeoutMs)
	}
	if cp.Breaker.FailureThreshold != 0 {
		resolved.Breaker.FailureThreshold = cp.Breaker.FailureThreshold
	}
	if cp.Breaker.CooldownSeconds != 0 {
		resolved.Breaker.CooldownSeconds = cp.Breaker.CooldownSeconds
	}
	if cp.Breaker.EscalationCap != 0 {
		resolved.Breaker.EscalationCap = cp.Breaker.EscalationCap
	}
	// 豁免只能显式打开：零值 false 与「未设置」不可区分，故只在为 true 时提升。
	if cp.BreakerExempt {
		resolved.BreakerExempt = true
	}
	if cp.Breaker.RateLimitCooldownSeconds != 0 {
		resolved.Breaker.RateLimitCooldownSeconds = cp.Breaker.RateLimitCooldownSeconds
	}
	if cp.Breaker.HalfOpenProbes != 0 {
		resolved.Breaker.HalfOpenProbes = cp.Breaker.HalfOpenProbes
	}
	// per_model 指针非 nil 才覆盖：nil 表示「本渠道未指定，跟随全局」。
	// 少了这一行，某渠道只配了 failure_threshold 就会把全局的 true
	// 静默覆盖成 false，熔断粒度在部分渠道上悄悄退回渠道级。
	if cp.Breaker.PerModel != nil {
		resolved.Breaker.PerModel = cp.Breaker.PerModel
	}
	return resolved
}

// IsBreakerExempt 报告该渠道是否豁免熔断与自动禁用。
// 供 tracker（熔断/可用性）与 service（自动禁用）共用同一份判定。
func IsBreakerExempt(channelID int) bool {
	if !Enabled() || channelID <= 0 {
		return false
	}
	return GetPolicy().Resolve(channelID).BreakerExempt
}

// DefaultPolicy 返回内置默认策略（配置文件缺失时使用）
func DefaultPolicy() *Policy {
	return &Policy{
		Enabled:    false, // 默认关闭，显式开启才生效
		MaxRetries: 2,     // 默认失败后换渠道重试 2 次
		Default: ChannelPolicy{
			MaxInflight:   50,
			TTFTTimeoutMs: DefaultTTFTTimeoutMs,
			Breaker: BreakerPolicy{
				FailureThreshold:         5,
				CooldownSeconds:          60,
				RateLimitCooldownSeconds: 30,
				HalfOpenProbes:           1,
			},
		},
		Channels: make(map[int]ChannelPolicy),
	}
}
