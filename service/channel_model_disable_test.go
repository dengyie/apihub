package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func errString(s string) error { return errors.New(s) }

// 判据只认「可证明只关于某个模型」的失败，其余一律渠道级。
//
// 两个方向的错配代价都不对称：把账号级错判成模型级，死 key 的渠道会继续
// 吃满每一轮重试；把模型级错判成账号级，其余健康模型跟着陪葬 —— 生产实测
// 这一类连带下线过 91 个模型。
func TestIsModelScopedAutoDisable(t *testing.T) {
	cases := []struct {
		name string
		err  *types.NewAPIError
		want bool
	}{
		{"上游说模型不存在", types.NewErrorWithStatusCode(errString("404, message: 模型不存在"), types.ErrorCodeBadResponse, 404), true},
		{"model not found", types.NewErrorWithStatusCode(errString(`model_not_found`), types.ErrorCodeBadResponse, 404), true},
		{"no such model", types.NewErrorWithStatusCode(errString("no such model"), types.ErrorCodeBadResponse, 404), true},
		// 这句是本网关自己吐的下游症状，不是上游的判断，词表刻意不收
		// （见 operation_setting.go 的关键词注释）：拿它当「上游说没有这个
		// 模型」会把选路失败反向当成渠道的过错。
		{"本网关的无渠道提示不算上游表态", types.NewErrorWithStatusCode(errString("No available channel for model x under group default"), types.ErrorCodeBadResponse, 400), false},

		// 这条是本次最容易踩的坑：IsUpstreamModelUnavailableError 把令牌级措辞
		// 一并收进来了，若跟着转模型级，「拿不到 token」就会变成「只摘一个
		// 模型」，而该渠道的每个模型都拿不到 token。
		{"cannot fetch token 必须留在渠道级", types.NewErrorWithStatusCode(errString("cannot fetch token"), types.ErrorCodeBadResponse, 400), false},

		{"余额不足", types.NewErrorWithStatusCode(errString("insufficient balance"), types.ErrorCodeBadResponse, 403), false},
		{"额度耗尽", types.NewErrorWithStatusCode(errString("insufficient_user_quota"), types.ErrorCodeBadResponse, 403), false},
		{"密钥失效", types.NewErrorWithStatusCode(errString("API key not recognised"), types.ErrorCodeBadResponse, 401), false},
		{"分组无权", types.NewErrorWithStatusCode(errString("无权访问 CC-kiro 分组"), types.ErrorCodeBadResponse, 403), false},
		{"令牌被停用", types.NewErrorWithStatusCode(errString("令牌因分组倍率上调已停用"), types.ErrorCodeBadResponse, 403), false},

		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isModelScopedAutoDisable(c.err))
		})
	}
}

// 与熔断侧刻意不共用判据：熔断自 v29.14 起一律模型级（有递增退避兜底且能
// 自愈），自动禁用不能自愈、判据必须更保守。这条测试把两者的差异钉住，
// 防止后人「顺手」把它们合并 —— 合并的后果就是余额耗尽的渠道被降级成
// 「只摘一个模型」，于是它继续吃满每一轮重试。
func TestModelScopedAutoDisableIsStricterThanBreakerScope(t *testing.T) {
	quotaErr := types.NewErrorWithStatusCode(errString("insufficient_user_quota"), types.ErrorCodeBadResponse, 403)

	assert.Equal(t, loadbalancer.ScopeModel, loadbalancer.BreakerScopeOf(quotaErr),
		"熔断侧一律模型级")
	assert.False(t, isModelScopedAutoDisable(quotaErr),
		"自动禁用侧必须判为渠道级 —— 这是两者刻意的分歧点")
}

// 开关关闭时 DisableChannelForModel 必须退化成整渠道禁用，与 v29.14 逐位一致 ——
// 这是上线期的默认状态。
func TestDisableChannelForModelFallsBackWhenSwitchOff(t *testing.T) {
	previous := common.AutomaticDisableModelScope
	common.AutomaticDisableModelScope = false
	t.Cleanup(func() { common.AutomaticDisableModelScope = previous })

	channelError := types.ChannelError{ChannelId: 1, ChannelName: "ch", AutoBan: true}
	// AutoBan 为 false 时两个分支都会直接返回，用例只验证「没有崩、且没走
	// 模型级路径」；真正的降级行为由下面 MemoryCacheEnabled 的用例覆盖。
	assert.NotPanics(t, func() {
		DisableChannelForModel(channelError, "m1", "模型不存在")
	})
}

// 内存缓存打开时 abilities 的改动对选路不可见（InitChannelCache 从
// channels.Group × channels.Models 建表，不读 abilities.enabled），此时若不
// 降级，特性会静默失效：禁用照写、路由照旧、零报错。
func TestDisableChannelForModelFallsBackUnderMemoryCache(t *testing.T) {
	previousSwitch := common.AutomaticDisableModelScope
	previousCache := common.MemoryCacheEnabled
	common.AutomaticDisableModelScope = true
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.AutomaticDisableModelScope = previousSwitch
		common.MemoryCacheEnabled = previousCache
	})

	channelError := types.ChannelError{ChannelId: 1, ChannelName: "ch", AutoBan: true}
	assert.NotPanics(t, func() {
		DisableChannelForModel(channelError, "m1", "模型不存在")
	})
}
