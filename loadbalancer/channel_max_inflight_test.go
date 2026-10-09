package loadbalancer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChannelMaxInflightProviderPrecedence(t *testing.T) {
	policy := &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			MaxInflight: 5,
		},
		Channels: map[int]ChannelPolicy{
			101: {
				MaxInflight: 8,
			},
			102: {
				MaxInflight: 8,
			},
		},
	}

	// 初始状态：未注册 Provider 或返回 0 时，Resolve 分别采用 Channels[id] 或 Default
	SetChannelMaxInflightProvider(nil)
	assert.Equal(t, 8, policy.Resolve(101).MaxInflight, "未设置动态提供器时使用 Channels[101] 配置 (8)")
	assert.Equal(t, 5, policy.Resolve(999).MaxInflight, "未设置动态提供器时使用 Default 配置 (5)")

	// 注册动态 Provider：
	// 渠道 101 设置了动态 20（应覆盖 YAML 中的 8）
	// 渠道 999 未在 YAML 中定义，但动态设置了 15（应覆盖 Default 5）
	// 渠道 102 动态返回 0（应保留 YAML 中的 8）
	SetChannelMaxInflightProvider(func(channelID int) int {
		switch channelID {
		case 101:
			return 20
		case 999:
			return 15
		default:
			return 0
		}
	})
	defer SetChannelMaxInflightProvider(nil)

	assert.Equal(t, 20, policy.Resolve(101).MaxInflight, "动态渠道设置 (20) 优先于 YAML 中的 channels (8)")
	assert.Equal(t, 15, policy.Resolve(999).MaxInflight, "动态渠道设置 (15) 优先于 YAML 中的 default (5)")
	assert.Equal(t, 8, policy.Resolve(102).MaxInflight, "动态渠道返回 0 时回退到 YAML 中的 channels (8)")
	assert.Equal(t, 5, policy.Resolve(103).MaxInflight, "动态渠道返回 0 时回退到 YAML 中的 default (5)")
}
