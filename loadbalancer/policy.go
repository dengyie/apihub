package loadbalancer

// Policy 定义智能负载的全部策略参数。
// 策略支持热加载：修改 YAML 配置文件后自动生效，无需重启服务。
type Policy struct {
	Enabled  bool                  `yaml:"enabled"`
	Default  ChannelPolicy         `yaml:"default"`
	Channels map[int]ChannelPolicy `yaml:"channels"` // key: channel ID，覆盖默认策略
	// MaxRetries 单个请求失败后的最大重试次数（换渠道重试）。
	// 0 表示不重试。修改后热加载生效。
	MaxRetries int `yaml:"max_retries"`

	// StripThinkingChannels 需要裁剪 thinking 参数的渠道 ID 列表（兼容旧配置）。
	// 这些渠道的上游不支持 thinking 参数，转发时会自动去掉。
	// 新配置请用 StripParams，可指定任意参数。
	StripThinkingChannels []int `yaml:"strip_thinking_channels"`

	// StripParams 通用参数裁剪：channelID -> 需要去掉的参数名列表。
	// 例如：140: [thinking, reasoning_effort]
	// 不填则靠运行时自动学习（收到 400 参数不支持错误后自动标记）。
	StripParams map[int][]string `yaml:"strip_params"`
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
				FailureThreshold: 5,
				CooldownSeconds:  60,
				HalfOpenProbes:   1,
			},
		},
		Channels: make(map[int]ChannelPolicy),
	}
}
