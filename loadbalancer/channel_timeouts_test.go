package loadbalancer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 管理面板里配的首字 / 响应头超时必须压过 YAML：否则「在后台单独调某个渠道」
// 对那些已经写进 loadbalancer.yaml 的渠道完全无效，而它们恰恰是最需要单独调的。
func TestChannelTimeoutsProviderPrecedence(t *testing.T) {
	policy := &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			TTFTTimeoutMs:   100000,
			HeaderTimeoutMs: 15000,
		},
		Channels: map[int]ChannelPolicy{
			101: {TTFTTimeoutMs: 180000, HeaderTimeoutMs: 180000},
			102: {TTFTTimeoutMs: 180000, HeaderTimeoutMs: 180000},
			103: {},
		},
	}

	// 未注册 Provider：完全按 YAML 解析
	SetChannelTimeoutsProvider(nil)
	assert.Equal(t, int64(180000), policy.Resolve(101).TTFTTimeoutMs)
	assert.Equal(t, int64(100000), policy.Resolve(999).TTFTTimeoutMs)
	assert.Equal(t, int64(15000), policy.Resolve(999).HeaderTimeoutMs)

	SetChannelTimeoutsProvider(func(channelID int) ChannelTimeouts {
		switch channelID {
		case 101:
			return ChannelTimeouts{TTFTTimeoutMs: 30000, HeaderTimeoutMs: 20000}
		case 999:
			return ChannelTimeouts{TTFTTimeoutMs: 60000}
		default:
			return ChannelTimeouts{}
		}
	})
	defer SetChannelTimeoutsProvider(nil)

	// YAML 里有显式值的渠道：动态值覆盖
	resolved101 := policy.Resolve(101)
	assert.Equal(t, int64(30000), resolved101.TTFTTimeoutMs, "后台配置应覆盖 channels[101] 的 180s")
	assert.Equal(t, int64(20000), resolved101.HeaderTimeoutMs)
	assert.Equal(t, 20*time.Second, resolved101.ResolveHeaderTimeout())

	// YAML 里没有的渠道：动态值覆盖 default，未配置的字段仍继承 default
	resolved999 := policy.Resolve(999)
	assert.Equal(t, int64(60000), resolved999.TTFTTimeoutMs)
	assert.Equal(t, int64(15000), resolved999.HeaderTimeoutMs, "只配 TTFT 时 header 继续继承 default")

	// 动态返回 0 表示未配置：回退 YAML
	assert.Equal(t, int64(180000), policy.Resolve(102).TTFTTimeoutMs)
	assert.Equal(t, int64(180000), policy.Resolve(102).HeaderTimeoutMs)

	// 只配 TTFT 会把响应头闸门一起压到 TTFT 以下（ResolveHeaderTimeout 取 min）
	SetChannelTimeoutsProvider(func(int) ChannelTimeouts {
		return ChannelTimeouts{TTFTTimeoutMs: 8000}
	})
	assert.Equal(t, 8*time.Second, policy.Resolve(103).ResolveHeaderTimeout(),
		"响应头等待取 min(header, ttft)，配 8s 首字就不该再等 180s 响应头")
}

func TestChannelTimeoutsProviderIgnoresInvalidChannelID(t *testing.T) {
	SetChannelTimeoutsProvider(func(int) ChannelTimeouts {
		return ChannelTimeouts{TTFTTimeoutMs: 30000}
	})
	defer SetChannelTimeoutsProvider(nil)

	assert.Equal(t, ChannelTimeouts{}, GetChannelTimeouts(0))
	assert.Equal(t, ChannelTimeouts{}, GetChannelTimeouts(-1))
	assert.Equal(t, 30000, GetChannelTimeouts(7).TTFTTimeoutMs)
}

// 策略关闭时 Resolve 直接返回零值，动态超时也不该被应用——否则 enabled:false
// 的部署里 stream_scanner 会拿到一个非零 TTFT 却又不该启用任何闸门。
func TestChannelTimeoutsIgnoredWhenPolicyDisabled(t *testing.T) {
	SetChannelTimeoutsProvider(func(int) ChannelTimeouts {
		return ChannelTimeouts{TTFTTimeoutMs: 30000}
	})
	defer SetChannelTimeoutsProvider(nil)

	policy := &Policy{Enabled: false, Default: ChannelPolicy{TTFTTimeoutMs: 100000}}
	assert.Equal(t, ChannelPolicy{}, policy.Resolve(1))
}
