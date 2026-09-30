package loadbalancer

import "time"

// DefaultRequestTimeoutMs 非流式请求的整请求预算默认值（毫秒）。
//
// 非流式的上游响应是「算完才来」：TTFT 定时器只覆盖流式的首字等待，非流式
// 此前没有任何网关侧边界，单次尝试可以一直挂到上游或其前置网关（Cloudflare
// ~100s）判死，跨渠道重试叠加时总等待无上界。180s 对合法长生成足够宽松，
// 又能在系统性慢上游时把总等待封住。
const DefaultRequestTimeoutMs = 180000

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

// EmptyStreamRetryBudget 返回流式零字节换渠道重试墙钟；0 表示禁用。
func (p *Policy) EmptyStreamRetryBudget() time.Duration {
	if p == nil || p.EmptyStreamRetryBudgetMs == nil {
		return time.Duration(DefaultEmptyStreamRetryBudgetMs) * time.Millisecond
	}
	if *p.EmptyStreamRetryBudgetMs <= 0 {
		return 0
	}
	return time.Duration(*p.EmptyStreamRetryBudgetMs) * time.Millisecond
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

	// Breaker 熔断器参数
	Breaker BreakerPolicy `yaml:"breaker"`
}

// BreakerPolicy 熔断器策略
type BreakerPolicy struct {
	// FailureThreshold 连续慢请求/失败达到此次数后熔断
	FailureThreshold int `yaml:"failure_threshold"`
	// CooldownSeconds 熔断后冷却时间，之后进入半开状态允许探测
	CooldownSeconds int64 `yaml:"cooldown_seconds"`
	// RateLimitCooldownSeconds 遭遇 429 频次/并发限流时的短期避让冷却时间（秒），默认 30 秒
	RateLimitCooldownSeconds int64 `yaml:"rate_limit_cooldown_seconds"`
	// HalfOpenProbes 半开状态允许的探测请求数
	HalfOpenProbes int `yaml:"half_open_probes"`
}

// Resolve 将渠道级策略与默认策略合并，返回生效的完整策略
func (p *Policy) Resolve(channelID int) ChannelPolicy {
	if p == nil || !p.Enabled {
		return ChannelPolicy{}
	}
	cp, ok := p.Channels[channelID]
	if !ok {
		return p.Default
	}
	// 零值字段回退到 Default
	resolved := p.Default
	if cp.MaxInflight != 0 {
		resolved.MaxInflight = cp.MaxInflight
	}
	if cp.TTFTTimeoutMs != 0 {
		resolved.TTFTTimeoutMs = cp.TTFTTimeoutMs
	}
	if cp.Breaker.FailureThreshold != 0 {
		resolved.Breaker.FailureThreshold = cp.Breaker.FailureThreshold
	}
	if cp.Breaker.CooldownSeconds != 0 {
		resolved.Breaker.CooldownSeconds = cp.Breaker.CooldownSeconds
	}
	if cp.Breaker.RateLimitCooldownSeconds != 0 {
		resolved.Breaker.RateLimitCooldownSeconds = cp.Breaker.RateLimitCooldownSeconds
	}
	if cp.Breaker.HalfOpenProbes != 0 {
		resolved.Breaker.HalfOpenProbes = cp.Breaker.HalfOpenProbes
	}
	return resolved
}

// DefaultPolicy 返回内置默认策略（配置文件缺失时使用）
func DefaultPolicy() *Policy {
	return &Policy{
		Enabled:    false, // 默认关闭，显式开启才生效
		MaxRetries: 2,     // 默认失败后换渠道重试 2 次
		Default: ChannelPolicy{
			MaxInflight:   50,
			TTFTTimeoutMs: 15000,
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
