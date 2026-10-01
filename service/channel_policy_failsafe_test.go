package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"

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
