package loadbalancer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPolicyClientSafetyDeadlineAndEmptyStreamBudget(t *testing.T) {
	// 1. 默认策略：ClientSafetyDeadline 为 105s
	policy := DefaultPolicy()
	assert.Equal(t, 105*time.Second, policy.ClientSafetyDeadline())

	// 2. 自适应计算超大 TTFT (例如 100s) + 2 次重试，原本 needed = 300s，必须被截断到 105s
	policy.Default.TTFTTimeoutMs = 100000
	policy.MaxRetries = 2
	budget := policy.EmptyStreamRetryBudget()
	assert.Equal(t, 105*time.Second, budget, "即使 needed 达到 300s，也必须被 105s 客户端安全死线截断")

	// 3. 显式配置更小的 ClientSafetyDeadlineMs (例如 50s)
	customSafety := 50000
	policy.ClientSafetyDeadlineMs = &customSafety
	assert.Equal(t, 50*time.Second, policy.ClientSafetyDeadline())
	assert.Equal(t, 50*time.Second, policy.EmptyStreamRetryBudget(), "必须被显式配置的 safety deadline 截断")

	// 4. 关闭 ClientSafetyDeadline (显式 0)
	disabledSafety := 0
	policy.ClientSafetyDeadlineMs = &disabledSafety
	assert.Equal(t, time.Duration(0), policy.ClientSafetyDeadline())
	// 此时不受 safety 截断，但受 RequestTimeout (默认 180s) 截断
	assert.Equal(t, 180*time.Second, policy.EmptyStreamRetryBudget())
}

func TestChannelPolicyResolveHeaderTimeout(t *testing.T) {
	// 1. TTFT 较大 (60s)，HeaderTimeout 未配置：跟随 TTFT，不再固定 15s。
	//    线上就是这条：ttft_timeout_ms=100s 被未配置的 15s header 门限压回 15s。
	cp1 := ChannelPolicy{TTFTTimeoutMs: 60000}
	assert.Equal(t, 60*time.Second, cp1.ResolveHeaderTimeout())

	// 1b. 连 TTFT 都没配：回落到内置默认 TTFT，而不是另有一个更小的默认值。
	assert.Equal(t, time.Duration(DefaultTTFTTimeoutMs)*time.Millisecond,
		ChannelPolicy{}.ResolveHeaderTimeout())

	// 2. TTFT 较小 (8s)，HeaderTimeout 未配置：跟随 8s
	cp2 := ChannelPolicy{TTFTTimeoutMs: 8000}
	assert.Equal(t, 8*time.Second, cp2.ResolveHeaderTimeout())

	// 3. 显式配置 HeaderTimeoutMs (3s) 优先
	cp3 := ChannelPolicy{TTFTTimeoutMs: 60000, HeaderTimeoutMs: 3000}
	assert.Equal(t, 3*time.Second, cp3.ResolveHeaderTimeout())

	// 3b. 显式 HeaderTimeoutMs 大于 TTFT 时仍以 TTFT 为上限
	cp4 := ChannelPolicy{TTFTTimeoutMs: 20000, HeaderTimeoutMs: 90000}
	assert.Equal(t, 20*time.Second, cp4.ResolveHeaderTimeout())

	// 4. Policy Resolve 合并覆盖
	policy := Policy{
		Enabled: true,
		Default: ChannelPolicy{
			TTFTTimeoutMs:   30000,
			HeaderTimeoutMs: 10000,
		},
		Channels: map[int]ChannelPolicy{
			101: {HeaderTimeoutMs: 5000},
			102: {TTFTTimeoutMs: 4000},
		},
	}

	resolved101 := policy.Resolve(101)
	assert.Equal(t, 5*time.Second, resolved101.ResolveHeaderTimeout())

	resolved102 := policy.Resolve(102)
	assert.Equal(t, 4*time.Second, resolved102.ResolveHeaderTimeout())

	resolvedDefault := policy.Resolve(999)
	assert.Equal(t, 10*time.Second, resolvedDefault.ResolveHeaderTimeout())
}
