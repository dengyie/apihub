package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clampLimitMsg = "max_completion_tokens is too large: 384000. This model supports at most 262144 completion tokens."

func clampTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// clampRelayInfo 造一个「客户端请求名 != 上游模型名」的 relayInfo，
// 复现渠道 #111 配了 model_mapping 的真实形态。
func clampRelayInfo(channelID int, clientName, upstreamName string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         channelID,
			UpstreamModelName: upstreamName,
			IsModelMapped:     clientName != upstreamName,
		},
		OriginModelName: clientName,
	}
}

// TestProcessChannelErrorLearnsLimitUnderUpstreamModelName 是 P1 的接线级守卫：
// 上限必须记在「实际发出去的那个模型名」上。
//
// 生产证据：渠道 #111 配了 model_mapping，把客户端请求名 deepseek-v4-flash
// 映射成上游名 Deepseek-v4-flash，日志里 98 条 max_completion_tokens 400 全部
// 来自该渠道。此前记录侧写的是客户端请求名，而 openai adaptor 的出站钳制读
// 的是 UpstreamModelName——两个名字分叉，钳制永远命中不了，功能等于没上线，
// 而且不会有任何报错。
func TestProcessChannelErrorLearnsLimitUnderUpstreamModelName(t *testing.T) {
	const id = 999111
	info := clampRelayInfo(id, "deepseek-v4-flash", "Deepseek-v4-flash")
	apiErr := types.NewError(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody)

	ProcessChannelError(clampTestContext(), types.ChannelError{ChannelId: id, AutoBan: false}, apiErr, info)

	if got := loadbalancer.GetMaxCompletionTokensLimit(id, info.UpstreamModelName); got != 262144 {
		t.Fatalf("上限未按上游模型名学到：got = %d，期望 262144", got)
	}
	if got := loadbalancer.ClampMaxCompletionTokens(id, info.UpstreamModelName, 384000); got != 262144 {
		t.Fatalf("按上游模型名出站钳制未生效：got = %d，期望 262144", got)
	}
	// 反向断言：客户端请求名查不到上限。正因如此记录侧也必须用上游名，
	// 否则这个 384000 会一路飞到上游变成 400。
	if got := loadbalancer.GetMaxCompletionTokensLimit(id, info.OriginModelName); got != 0 {
		t.Fatalf("上限不应记到客户端请求名下：got = %d，期望 0", got)
	}
}

// TestProcessChannelErrorLearnsLimitEvenWhenRetryBudgetExhausted 是 P2 的接线级
// 守卫：学习是「观测到事实」的副作用，必须与「这次要不要重试」解耦。
//
// 写在 DecideRelayRetry 里时，它会被 client_aborted / skipRetry /
// retryTimes<=0 三道闸门挡掉，而恰恰是反复失败到预算耗尽的请求最需要把上限
// 学到手——学不到就永远重试到死。
func TestProcessChannelErrorLearnsLimitEvenWhenRetryBudgetExhausted(t *testing.T) {
	const id = 999112
	info := clampRelayInfo(id, "m", "mapped-m")
	apiErr := types.NewError(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody)

	c := clampTestContext()
	ProcessChannelError(c, types.ChannelError{ChannelId: id, AutoBan: false}, apiErr, info)

	// 这次请求本身不再重试了
	decision := DecideRelayRetry(c, apiErr, 0)
	require.Equal(t, "stop", decision.Action, "预算耗尽时本轮不应重试")

	// 但上限已经学到手，下一个请求就能一次成功
	if got := loadbalancer.GetMaxCompletionTokensLimit(id, info.UpstreamModelName); got != 262144 {
		t.Fatalf("重试预算耗尽时上限未学到手：got = %d，期望 262144", got)
	}
	if got := loadbalancer.ClampMaxCompletionTokens(id, info.UpstreamModelName, 384000); got != 262144 {
		t.Fatalf("重试预算耗尽后出站钳制未生效：got = %d，期望 262144", got)
	}
}

// TestDecideRelayRetryClampBranchIsLive 锁住 P1 的真正修复：这个分支曾经是死代码。
//
// 钳制分支排在 alwaysSkip / retryRanges 两道闸门**之后**时，它永远走不到：
//  1. defaultStatusCodeRules 的 alwaysSkipCodes 默认含 ErrorCodeBadResponseBody，
//     而上游 400 绝大多数落在这个错误码上 → 闸门直接 stop；
//  2. 即便错误码不在其中，默认 retryRanges 明确排除 400 → 落到
//     status_not_retryable。
//
// 死代码的代价不是少一个 reason：controller 的重试循环是
// 「processChannelError（学到上限）→ 继续下一次尝试」，下一轮 ConvertOpenAIRequest
// 出站前就会用刚学到的上限钳制并当场成功。分支死了等于第一次请求必定把 400
// 打给客户端，只有客户端重试第二次才受益——而这类请求（超长回复）代价最大。
//
// 这里用真实的默认规则配置，直接断言 Action 是 retry 且原因落在钳制分支上。
func TestDecideRelayRetryClampBranchIsLive(t *testing.T) {
	apiErr := types.NewErrorWithStatusCode(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody, http.StatusBadRequest)

	// 前置条件本身就是要锁住的「陷阱」：默认规则下这个错误码免重试、400 不在
	// 重试区间内。也就是说，只要钳制分支排在下面任何一道闸门之后，它就必然
	// 走不到——这正是回归的成因，必须写进测试而不是靠注释。
	require.True(t, operation_setting.IsAlwaysSkipRetryCode(apiErr.GetErrorCode()),
		"前置条件：默认 alwaysSkipCodes 含 ErrorCodeBadResponseBody")
	require.False(t, operation_setting.ShouldRetryByStatusCode(http.StatusBadRequest),
		"前置条件：默认 retryRanges 排除 400")

	decision := DecideRelayRetry(clampTestContext(), apiErr, 3)

	require.Equal(t, "retry", decision.Action,
		"钳制分支不得被 alwaysSkip/retryRanges 挡成死代码，否则首个请求必定 400")
	require.Equal(t, "max_completion_tokens_clamped", decision.Reason)
	require.Equal(t, "loadbalancer", decision.Source)
}

// TestDecideRelayRetryDoesNotWriteLimits DecideRelayRetry 只做分类，
// 不得再夹带写状态的副作用，否则「决策」与「学习」两件事永远绑在一起调不开。
func TestDecideRelayRetryDoesNotWriteLimits(t *testing.T) {
	const id = 999113
	apiErr := types.NewErrorWithStatusCode(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody, http.StatusBadRequest)

	DecideRelayRetry(clampTestContext(), apiErr, 3)

	assert.Equal(t, uint(0), loadbalancer.GetMaxCompletionTokensLimit(id, "m"),
		"仅做分类的调用不应写入任何上限")
}

// TestDecideRelayRetryClampStillRespectsHardGates 钳制分支前移不应把重试闸门一起
// 绕过去：客户端断开、skipRetry 标记、重试预算耗尽仍然必须 stop。
//
// 三道闸门都排在钳制分支**之前**，前移不能越过它们——否则「先学上限再换渠道」
// 就变成了「无视取消信号继续烧上游」。
func TestDecideRelayRetryClampStillRespectsHardGates(t *testing.T) {
	c := clampTestContext()

	clampErr := func() *types.NewAPIError {
		return types.NewErrorWithStatusCode(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody, http.StatusBadRequest)
	}

	// 预算耗尽：上限已在 ProcessChannelError 学到，本轮不再重试
	require.Equal(t, "attempt_budget_exhausted",
		DecideRelayRetry(c, clampErr(), 0).Reason)

	// 客户端断开（判据是错误码 ErrorCodeClientAborted，见 relaykit/types/error.go）
	aborted := types.NewErrorWithStatusCode(errors.New("context canceled"),
		types.ErrorCodeClientAborted, 499, types.ErrOptionWithSkipRetry())
	require.Equal(t, "client_aborted",
		DecideRelayRetry(c, aborted, 3).Reason)

	// skipRetry 显式标记
	skip := types.NewErrorWithStatusCode(errors.New(clampLimitMsg),
		types.ErrorCodeBadResponseBody, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	require.Equal(t, "non_retryable_error",
		DecideRelayRetry(c, skip, 3).Reason)
}

// TestProcessChannelErrorTolerateDegradedRelayInfo 守住两个降级边界：
//
//   - relayInfo 为 nil（service/relay_error_test.go 的既有用例就传 nil）
//   - relayInfo 非 nil 但 ChannelMeta 为 nil——UpstreamModelName 是从内嵌的
//     **指针** *ChannelMeta 提升上来的字段，渠道选定之前就失败时正是这个状态，
//     直接取字段会空指针 panic，把一次正常记账的失败变成 500。
//   - err 为 nil 时直接返回，不解引用。
func TestProcessChannelErrorTolerateDegradedRelayInfo(t *testing.T) {
	apiErr := types.NewError(errors.New(clampLimitMsg), types.ErrorCodeBadResponseBody)

	assert.NotPanics(t, func() {
		ProcessChannelError(clampTestContext(), types.ChannelError{ChannelId: 999114, AutoBan: false},
			apiErr, nil)
	})
	assert.NotPanics(t, func() {
		ProcessChannelError(clampTestContext(), types.ChannelError{ChannelId: 999116, AutoBan: false},
			apiErr, &relaycommon.RelayInfo{OriginModelName: "m"})
	})
	assert.NotPanics(t, func() {
		ProcessChannelError(clampTestContext(), types.ChannelError{ChannelId: 999115}, nil,
			clampRelayInfo(999115, "m", "m"))
	})

	assert.Equal(t, uint(0), loadbalancer.GetMaxCompletionTokensLimit(999114, "m"),
		"relayInfo 为 nil 时不得记录上限（否则会把上限记到空模型名下）")
	assert.Equal(t, uint(0), loadbalancer.GetMaxCompletionTokensLimit(999116, ""),
		"ChannelMeta 为 nil 时不得记录上限")
}
