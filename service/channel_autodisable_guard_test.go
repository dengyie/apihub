package service

import (
	"bytes"
	stdErrors "errors"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSysLog 接管 common.SysLog 的落点。
//
// SysLog 写的是 gin.DefaultWriter（不是标准 log），所以抓日志必须换这个
// 缓冲 —— 用 log.SetOutput 会一条都抓不到，表现为断言「没打过这条日志」，
// 而实际上它一直在打。这类「测的是错误的输出流」的失败最容易被误读成
// 生产缺陷。
func captureSysLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := gin.DefaultWriter
	gin.DefaultWriter = buf
	return buf, func() { gin.DefaultWriter = prev }
}

// 这批用例钉的是「不可逆的自动禁用」在什么情况下**不得**执行。
//
// 代价的不对称性是这套代码里最该被守住的一条：熔断错了，冷却 5 分钟后
// 自愈；自动禁用错了，渠道要靠测活或人工才回来，而且现场没有留下足够信息
// 让人判断它该不该被摘。所以每一条「看起来像渠道故障」的信号，都必须先问
// 「它自愈吗」，再决定要不要记进佐证。

// ttftTimeoutError 复现 controller/relay.go:272 与 relay/helper/stream_scanner.go
// 构造的那个错误：错误码是 channel:response_time_exceeded（带 channel: 前缀），
// 而底层 Err 是 TTFTTimeoutError。
func ttftTimeoutError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		&loadbalancer.TTFTTimeoutError{ChannelID: 4242, TimeoutMs: 5000},
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusGatewayTimeout)
}

// TestTTFTTimeoutNeverAutoDisables 是本文件最关键的一条。
//
// TTFT 超时是**延迟**信号，不是凭据失效信号。熔断器早就这么认了：
// controller/relay.go 里 lbAttempt.End 传的是 !loadbalancer.IsTTFTTimeout(...)，
// 明确把它排除在失败计数之外。而自动禁用这一侧经由 types.IsChannelError 判它 ——
// 那是个纯粹的前缀总闸（strings.HasPrefix(code, "channel:")），TTFT 的错误码
// 恰好就叫 channel:response_time_exceeded，于是被扫进来了。
//
// 两道防线对同一个事件给出相反判断，而自动禁用那一侧的后果是渠道级的、
// 不可逆的。生产上表现为：一条只是慢的渠道攒够 3 次佐证后被整条摘掉，
// 而摘它的证据只是「它慢了三次」。
func TestTTFTTimeoutNeverAutoDisables(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9201
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	err := ttftTimeoutError()
	require.True(t, types.IsChannelError(err),
		"前置条件：IsChannelError 确实把这个错误当成渠道级 —— 这正是缺陷的根")

	for i := 0; i < 20; i++ {
		assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", err),
			"TTFT 超时攒多少次都不该摘掉整条渠道：慢≠失效")
	}

	assert.Equal(t, 0, loadbalancer.CorroborationCount(channelID, "gpt-4o", loadbalancer.CorroborationClassChannelError),
		"更不能进佐证计数 —— 进了就等于替它攒证据")
	assert.False(t, classifyAutoDisable(channelID, err).Disable)
}

// TestTTFTTimeoutAgreesWithBreaker 两套机制对同一个事件必须给同一个答案。
//
// 不一致本身就是缺陷：熔断器说「这只是慢，不计失败」，自动禁用说
// 「渠道级失效，攒够就摘」。谁对谁错不重要，重要的是**同一个信号在两处
// 得到相反结论**，于是「熔断没动」会被误读成「系统认为它没问题」。
func TestTTFTTimeoutAgreesWithBreaker(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9202
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	err := ttftTimeoutError()
	// 熔断侧的口径：IsTTFTTimeout 为真 → End 传 failed=false → 不计失败
	notCountedByBreaker := loadbalancer.IsTTFTTimeout(err.Err)
	// 自动禁用侧的口径：classifyAutoDisable 必须给出一致的结论
	verdict := classifyAutoDisable(channelID, err)

	assert.True(t, notCountedByBreaker)
	assert.False(t, verdict.Disable,
		"熔断器不计的失败，自动禁用侧也不该拿它当确定性失效的证据")
}

// TestCorroborationGateLogsWhenItOpens 闸门打开的那一刻必须留痕。
//
// 此前只有 count==1 那一条日志，而它恰恰是「没禁用」的那次。于是从 1/3 涨到
// 3/3、真正执行不可逆摘除的那一刻，日志上什么都没有 —— 唯一的线索是下游那条
// 「已被禁用」，看不出证据攒了几次、攒了多久。不可逆动作之前的那一步是排障的
// 关键：正是它需要回答「系统当时凭什么认为它坏了」。
func TestCorroborationGateLogsWhenItOpens(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9203
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	buf, restore := captureSysLog(t)
	defer restore()

	err := authError()
	assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", err))
	assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", err))
	require.True(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", err))

	out := buf.String()
	require.Contains(t, out, "corroborated auto-disable rule",
		"真正执行不可逆禁用之前必须留痕，否则现场无从判断证据攒了几次")
	assert.Contains(t, out, "3/3", "日志要写出达标次数与阈值")
	assert.Contains(t, out, "channel #9203", "日志要指出是哪条渠道")
	assert.Contains(t, out, "gpt-4o", "日志要指出是哪个模型")
	assert.NotContains(t, out, "[全部模型]")
}

// TestCorroborationGateDoesNotLogEveryHit 达标之后不得逐次刷日志。
//
// 一条稳定故障的渠道在冷却前会持续失败，每次都打一行的话，真正的信号会被
// 日常噪声淹没。首次那条（holding back）与达标那条是全部需要的量。
func TestCorroborationGateDoesNotLogEveryHit(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9204
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	buf, restore := captureSysLog(t)
	defer restore()

	err := authError()
	for i := 0; i < 3; i++ {
		ShouldDisableChannelCorroborated(channelID, "gpt-4o", err)
	}
	firstPhase := strings.Count(buf.String(), "corroborated auto-disable rule")

	for i := 0; i < 50; i++ {
		ShouldDisableChannelCorroborated(channelID, "gpt-4o", err)
	}
	total := strings.Count(buf.String(), "corroborated auto-disable rule")

	assert.Equal(t, 1, firstPhase, "达标那一刻打一条")
	assert.Equal(t, firstPhase, total,
		"达标之后不得逐次刷日志 —— 稳定故障会每秒产出一行，把真正的信号淹掉")
}

// TestCorroborationGateFirstHitStillLogged 首条（被挡下的那次）必须继续打。
//
// 这条是 v29.19 就有的行为：整个机制在没有它的情况下是静默的 —— 运维看到的是
// 「渠道还在、日志里有错误」，猜不到中间还隔着一道佐证闸门。新增达标那条不得
// 以任何方式挤掉它。
func TestCorroborationGateFirstHitStillLogged(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9205
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	buf, restore := captureSysLog(t)
	defer restore()

	assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", authError()))

	out := buf.String()
	require.Contains(t, out, "holding back disable",
		"首次被闸门挡下必须留痕，否则整个佐证机制是静默的")
	assert.Contains(t, out, "1/3")
}

// TestEmptyStreamNeverAutoDisables 空流同样不得触发自动禁用（既有守卫的回归）。
//
// 空流靠连续计数熔断，不走自动禁用：上游正常结束了流但一个有效内容块都没发，
// 这通常是模型侧或网关侧的问题，不是凭据失效。摘掉整条渠道代价过大。
func TestEmptyStreamNeverAutoDisables(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9206
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	empty := types.NewErrorWithStatusCode(
		&loadbalancer.EmptyStreamError{ChannelID: channelID},
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusGatewayTimeout)

	for i := 0; i < 20; i++ {
		assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", empty),
			"空流由连续计数熔断处理，不得走不可逆的自动禁用")
	}
}

// TestRoutingExhaustedNeverAutoDisables 「选不出渠道」是路由池状态，
// 不是「这条凭据没有这个模型」（既有守卫的回归）。
//
// 生产实测 #144 40 分钟成功 39 次、失败 7 次，失败全是这一类。把它当成
// 确定性失效会把一条 85% 可用的渠道整条摘掉，且线上不会有任何报错。
func TestRoutingExhaustedNeverAutoDisables(t *testing.T) {
	newCorroborationFixture(t, 3)
	const channelID = 9207
	defer loadbalancer.ResetCorroborationForChannel(channelID)

	exhausted := types.NewErrorWithStatusCode(
		stdErrors.New("no available channel for model gpt-4o"),
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusServiceUnavailable)
	require.True(t, loadbalancer.IsRoutingExhaustedError(exhausted),
		"前置条件：这个措辞确实被识别为路由耗尽")

	for i := 0; i < 20; i++ {
		assert.False(t, ShouldDisableChannelCorroborated(channelID, "gpt-4o", exhausted))
	}
}
