package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

// TestShouldDisableChannelFailSafeOnPolicyLoadFailure 是 P2 的接线级守卫：
// 策略文件首次加载失败时，自动禁用必须整体停摆，而不是无声地拆掉兜底渠道。
//
// 故障链：loadbalancer.Init 加载 data/loadbalancer.yaml 失败 → currentPolicy
// 退化为内置 DefaultPolicy（Enabled=false、Channels 为空）→ IsBreakerExempt
// 因 !Enabled() 一律返回 false → 本机 CPA 兜底渠道（生产上 1/2/3/4/5/8）
// 被静默摘出豁免名单。这些渠道一旦再被自动禁用，整条链路就没有退路了，
// 而且不会有任何一行日志指向真正的原因。
//
// 判据不可信时选择「不自动禁用」而不是「照常禁用」：熔断会自愈（冷却到期
// 就回到池子里），自动禁用不会（要人工或渠道测活才回来），两者代价不对称。
func TestShouldDisableChannelFailSafeOnPolicyLoadFailure(t *testing.T) {
	restoreAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = restoreAutoDisable })

	// 401 在默认自动禁用状态码内，正常情况下一定判 true（下面 Test…Enabled 路径对照）
	authErr := types.NewErrorWithStatusCode(errors.New("invalid api key"), types.ErrorCodeBadResponseBody, 401)

	require.True(t, ShouldDisableChannel(1, authErr),
		"对照组：判据可信时 401 仍应判自动禁用")

	// 制造「首次加载失败」状态
	restore := loadbalancer.MarkPolicyLoadFailedForTest(true)
	require.False(t, loadbalancer.PolicyReliable(), "前置条件：判据应标记为不可信")

	require.False(t, ShouldDisableChannel(1, authErr),
		"策略加载失败时不得自动禁用任何渠道，否则兜底渠道会被静默摘出豁免名单")
	require.False(t, ShouldDisableChannel(8, authErr),
		"兜底渠道（生产 CPA #8）在判据不可信时同样不得被自动禁用")

	restore()
	require.True(t, loadbalancer.PolicyReliable(), "恢复后判据应重新可信")
}

// TestShouldDisableChannelStillRespectsManualDisableSwitch 关掉总开关时，
// 判据不可信与否都不影响「不自动禁用」。
func TestShouldDisableChannelStillRespectsManualDisableSwitch(t *testing.T) {
	restore := loadbalancer.MarkPolicyLoadFailedForTest(true)
	defer restore()

	restoreAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = false
	defer func() { common.AutomaticDisableChannelEnabled = restoreAutoDisable }()

	authErr := types.NewErrorWithStatusCode(errors.New("invalid api key"), types.ErrorCodeBadResponseBody, 401)
	require.False(t, ShouldDisableChannel(1, authErr), "总开关关闭时一律不自动禁用")
}

// TestShouldDisableChannelExcludesParamError 钉住「参数不支持」在任何状态码下
// 都不自动禁用渠道。
//
// 这条闸门是为 2026-10-06 的状态码门槛放宽而加的：原先参数错误只可能是 400、
// 自动禁用状态码默认只有 401，两者天然不相交；放宽到 5xx 后（上游把校验报文
// 包成 500，生产实测渠道 #238）两者相交。参数错误是请求形状问题，裁掉参数
// 重发即可，渠道本身健康 —— 自动禁用不像熔断会自愈，摘掉就是永久退出轮转。
//
// 测试同时覆盖 400 与 500：前者证明这道闸门没有把既有行为改坏，后者证明它
// 真的接住了新相交的那一半。
func TestShouldDisableChannelExcludesParamError(t *testing.T) {
	restoreAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = restoreAutoDisable })

	restore := loadbalancer.MarkPolicyLoadFailedForTest(false)
	t.Cleanup(restore)

	// 把 5xx 显式加进自动禁用状态码，否则这道闸门在默认配置下是**测不出来的**：
	// 默认只有 401，500 参数错误本来就不会被判禁用，删掉闸门测试照样绿。
	// 而闸门存在的意义恰恰是防住「日后有人把 5xx 加进自动禁用列表」这一种变化 ——
	// 那是配置巧合，不是代码保证。这里把巧合改成显式配置，闸门才真正被覆盖。
	origCodes := operation_setting.AutomaticDisableStatusCodesToString()
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401,500-599"))
	t.Cleanup(func() {
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(origCodes))
	})

	// 前置条件自证：此刻普通 500 确实会被判自动禁用，闸门不是空转。
	plain500 := types.NewErrorWithStatusCode(
		errors.New("internal server error"), types.ErrorCodeBadResponseBody, 500)
	require.True(t, ShouldDisableChannel(238, plain500),
		"前置条件：5xx 已在自动禁用列表内时，普通 500 应判禁用")

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"400 参数校验", 400},
		{"422 pydantic 原生码", 422},
		{"500 中转层包装（#238 生产形态）", 500},
	} {
		err := types.NewErrorWithStatusCode(
			errors.New("Validation: Unsupported parameter(s): `enable_thinking`"),
			types.ErrorCodeBadResponseBody, tc.status)
		require.False(t, ShouldDisableChannel(238, err),
			"%s：参数错误不得自动禁用渠道", tc.name)
	}

	// 对照组：凭据失效仍应判自动禁用，证明上面的 false 不是被总开关或
	// 策略可信度一起关掉了。
	authErr := types.NewErrorWithStatusCode(errors.New("invalid api key"), types.ErrorCodeBadResponseBody, 401)
	require.True(t, ShouldDisableChannel(238, authErr),
		"对照组：401 凭据失效仍应判自动禁用")
}
