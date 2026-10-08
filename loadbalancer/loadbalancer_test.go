package loadbalancer

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dengyie/apihub/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPolicy() *Policy {
	return &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			MaxInflight:   2,
			TTFTTimeoutMs: 5000,
			Breaker: BreakerPolicy{
				FailureThreshold: 3,
				CooldownSeconds:  60,
				HalfOpenProbes:   1,
			},
		},
		Channels: map[int]ChannelPolicy{
			123: {MaxInflight: 10},
		},
	}
}

func TestPolicyResolve(t *testing.T) {
	p := testPolicy()

	// 未覆盖的渠道用默认
	r := p.Resolve(999)
	assert.Equal(t, 2, r.MaxInflight)
	assert.Equal(t, int64(5000), r.TTFTTimeoutMs)

	// 覆盖的渠道：设置的字段生效，未设置的回退默认
	r = p.Resolve(123)
	assert.Equal(t, 10, r.MaxInflight)
	assert.Equal(t, int64(5000), r.TTFTTimeoutMs)
	assert.Equal(t, 3, r.Breaker.FailureThreshold)

	// 关闭时返回空策略
	p.Enabled = false
	r = p.Resolve(123)
	assert.Equal(t, 0, r.MaxInflight)
}

func TestTrackerInflight(t *testing.T) {
	tr := newTestTracker()

	h1 := tr.Begin(1, testModel)
	h2 := tr.Begin(1, testModel)
	assert.Equal(t, 2, tr.Inflight(1))

	h1.End(false, false)
	assert.Equal(t, 1, tr.Inflight(1))

	h2.End(false, false)
	assert.Equal(t, 0, tr.Inflight(1))

	// 重复 End 只计一次
	h2.End(false, false)
	assert.Equal(t, 0, tr.Inflight(1))
}

func TestBreakerTripAndRecover(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// 连续 3 次慢请求（非失败）只触发软降级，不硬熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(7, testModel)
		h.End(true, false)
	}
	ok, _ := tr.IsAvailable(7, testModel)
	assert.True(t, ok, "channel 7 should stay available after 3 slow requests (soft degrade only)")
	assert.True(t, tr.IsDegraded(7, testModel), "channel 7 should be degraded after 3 slow requests")

	// 连续 3 次硬失败触发熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(8, testModel)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(8, testModel)
	assert.False(t, ok, "channel 8 should be unavailable after 3 hard failures")
	assert.Equal(t, "circuit_open", reason)

	// 手动把 openedAt 拨到冷却期之前，模拟冷却结束
	s := statsFor(tr, 8)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 半开：允许 1 个探测
	ok, _ = tr.IsAvailable(8, testModel)
	assert.True(t, ok, "channel 8 should allow 1 probe in half-open")
	// 第 2 个被拒绝
	ok, _ = tr.IsAvailable(8, testModel)
	assert.False(t, ok, "channel 8 should reject 2nd probe in half-open")

	// 探测成功后熔断器关闭
	h := tr.Begin(8, testModel)
	h.End(false, false)
	ok, _ = tr.IsAvailable(8, testModel)
	assert.True(t, ok, "channel 8 should be available after successful probe")
}

func TestBreakerHalfOpenProbeFailure(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// 1. 触发硬熔断（3次失败）
	for i := 0; i < 3; i++ {
		h := tr.Begin(100, testModel)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(100, testModel)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 2. 冷却结束，进入半开
	s := statsFor(tr, 100)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 3. 放行 1 个探测
	ok, _ = tr.IsAvailable(100, testModel)
	assert.True(t, ok)

	// 4. 探测失败！此时应立即切回 circuit_open，并开启新的冷却时间
	h := tr.Begin(100, testModel)
	h.End(false, true)

	assert.Equal(t, int32(breakerOpen), s.state.Load(), "state should revert to breakerOpen after failed probe")
	assert.Equal(t, int32(0), s.halfOpenProbes.Load(), "halfOpenProbes should be reset to 0")

	// 此时冷却未结束（刚熔断），不应可用
	ok, reason = tr.IsAvailable(100, testModel)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 模拟过了新的一轮冷却时间后，应再次允许探测（不会永久死锁）。
	// v29.8 起冷却按连续熔断次数递增：这里探测失败是第 2 次熔断，冷却已翻倍，
	// 必须按实际倍数拨回，否则会误判成「仍被锁住」而掩盖真正的死锁回归。
	breaker := GetPolicy().Resolve(100).Breaker
	s.openedAt.Store(time.Now().Unix() - breaker.CooldownSeconds*s.escalationMultiplier(breaker) - 1)
	ok, _ = tr.IsAvailable(100, testModel)
	assert.True(t, ok, "channel should allow probe again after next cooldown")
}

func TestTrackerBreakerHalfOpenProbeSuccessClosesBreaker(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// 1. 触发硬熔断
	for i := 0; i < 3; i++ {
		tr.Begin(200, testModel).End(false, true)
	}
	s := statsFor(tr, 200)
	assert.Equal(t, int32(breakerOpen), s.state.Load())

	// 2. 冷却结束，进入半开
	s.openedAt.Store(time.Now().Unix() - 61)

	// 3. 放行 1 个探测请求
	ok, _ := tr.IsAvailable(200, testModel)
	assert.True(t, ok)
	assert.Equal(t, int32(breakerHalfOpen), s.state.Load())
	assert.Equal(t, int32(1), s.halfOpenProbes.Load())

	// 4. 探测请求成功结束
	tr.Begin(200, testModel).End(false, false)

	// 5. 验证熔断器成功闭合，且 halfOpenProbes 重置为 0
	assert.Equal(t, int32(breakerClosed), s.state.Load(), "state should close to breakerClosed after a successful probe")
	assert.Equal(t, int32(0), s.halfOpenProbes.Load(), "halfOpenProbes should be reset to 0")
	assert.Equal(t, int32(0), s.consecutiveFailures.Load(), "consecutiveFailures should be reset to 0")

	// 6. 后续请求应该完全可用，不会出现 circuit_half_open_probes_exhausted
	ok, reason := tr.IsAvailable(200, testModel)
	assert.True(t, ok, "channel should be available after breaker closed")
	assert.Empty(t, reason)
}

func TestTrackerIgnoreZeroChannel(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// channel <= 0 不应崩溃且不应污染统计
	h := tr.Begin(0, testModel)
	assert.NotNil(t, h)
	h.MarkFirstByte()
	h.End(false, true)

	assert.Equal(t, 0, tr.Inflight(0))
	assert.Equal(t, int64(-1), tr.AvgTTFT(0, testModel))
	assert.False(t, tr.IsDegraded(0, testModel))
	ok, _ := tr.IsAvailable(0, testModel)
	assert.True(t, ok)
}

func TestOverloadSkip(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// MaxInflight=2，打满
	_ = tr.Begin(9, testModel)
	_ = tr.Begin(9, testModel)

	ok, reason := tr.IsAvailable(9, testModel)
	assert.False(t, ok, "channel 9 should be unavailable when overloaded")
	assert.Equal(t, "overloaded", reason)
}

func TestPolicyHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lb.yaml")

	write := func(maxInflight int) {
		data := []byte("enabled: true\ndefault:\n  max_inflight: " + strconv.Itoa(maxInflight) + "\n")
		require.NoError(t, os.WriteFile(path, data, 0644))
	}

	// 备份全局状态
	oldPath, oldPolicy, oldMtime := policyPath, currentPolicy.Load(), policyMtime
	defer func() {
		policyPath, policyMtime = oldPath, oldMtime
		currentPolicy.Store(oldPolicy)
	}()

	policyPath = path
	write(50)
	require.NoError(t, reload())
	assert.Equal(t, 50, GetPolicy().Default.MaxInflight)

	// 修改文件后重新加载生效
	write(100)
	require.NoError(t, reload())
	assert.Equal(t, 100, GetPolicy().Default.MaxInflight)
}

func TestTTFTTimeoutError(t *testing.T) {
	err := &TTFTTimeoutError{ChannelID: 1, TimeoutMs: 5000}
	assert.True(t, IsTTFTTimeout(err))
	assert.False(t, IsTTFTTimeout(nil))
	assert.False(t, IsTTFTTimeout(errors.New("other")))
}

func TestNormalizeParamName(t *testing.T) {
	assert.Equal(t, "reasoning_effort", normalizeParamName("ReasoningEffort"))
	assert.Equal(t, "reasoning_effort", normalizeParamName("reasoning_effort"))
	assert.Equal(t, "stream_options", normalizeParamName("streamOptions"))
	assert.Equal(t, "stream_options", normalizeParamName("StreamOptions"))
	assert.Equal(t, "thinking", normalizeParamName("thinking"))
	assert.Equal(t, "thinking", normalizeParamName("THINKING"))
	assert.Equal(t, "response_format", normalizeParamName("ResponseFormat"))
}

func TestIsParamNotSupportedError(t *testing.T) {
	// SenseNova 风格：field ReasoningEffort invalid, should be one of:...
	err1 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("status_code=400, field ReasoningEffort invalid, should be one of: low, medium, high, xhigh, none"),
	}
	p1, ok1 := IsParamNotSupportedError(err1)
	assert.True(t, ok1)
	assert.Equal(t, "reasoning_effort", p1)

	// OpenAI 风格：Unsupported parameter: 'reasoning_effort'
	err2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("Unsupported parameter: 'reasoning_effort'"),
	}
	p2, ok2 := IsParamNotSupportedError(err2)
	assert.True(t, ok2)
	assert.Equal(t, "reasoning_effort", p2)

	// Thinking 风格
	err3 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("'thinking' is not supported on /v1/chat/completions"),
	}
	p3, ok3 := IsParamNotSupportedError(err3)
	assert.True(t, ok3)
	assert.Equal(t, "thinking", p3)

	// Level not supported 风格（如 cpa-xkool / OpenAI-compatible 拒绝 max/xhigh 级别）：
	errLevel := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("status_code=400, level \"max\" not supported, valid levels: low, medium, high"),
	}
	pLevel, okLevel := IsParamNotSupportedError(errLevel)
	assert.True(t, okLevel)
	assert.Equal(t, "reasoning_effort", pLevel)

	errLevel2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("level 'xhigh' not supported"),
	}
	pLevel2, okLevel2 := IsParamNotSupportedError(errLevel2)
	assert.True(t, okLevel2)
	assert.Equal(t, "reasoning_effort", pLevel2)

	// huan666 / pydantic 风格：复数 (s) + 反引号包裹（渠道 #68 生产实测报文）。
	// 这是渠道 #68 每请求白烧一轮 400 的根因：老的 "parameter:" 字面量匹配不到 "parameter(s):"。
	errParen := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("status_code=400, Validation: Unsupported parameter(s): `enable_thinking`"),
	}
	pParen, okParen := IsParamNotSupportedError(errParen)
	assert.True(t, okParen, "复数 parameter(s) 必须被识别")
	assert.Equal(t, "enable_thinking", pParen)

	// 复数无 (s)、无引号包裹
	errPlainPlural := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("Unsupported parameters: top_k"),
	}
	pPlainPlural, okPlainPlural := IsParamNotSupportedError(errPlainPlural)
	assert.True(t, okPlainPlural)
	assert.Equal(t, "top_k", pPlainPlural)

	// 500 遇到**散文式**措辞不得触发参数裁剪：`field X invalid` 不在严格子集内。
	// 它没有引号也没有校验框架，"field name invalid" 这类 5xx 报文会误命中。
	err4 := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("field ReasoningEffort invalid"),
	}
	_, ok4 := IsParamNotSupportedError(err4)
	assert.False(t, ok4)
}

// TestIsParamNotSupportedErrorNon400 钉住「判据是报文措辞、不是状态码」。
//
// 背景：ilovecat520-cc（渠道 #238）把 pydantic 的参数校验报文包在 **500** 里
// 返回，而老实现第一行就写死 `StatusCode != 400` 直接返回，于是自动学习与
// 裁剪链路在该渠道上静默失效——每个请求白烧一轮 500 且永不自愈。
func TestIsParamNotSupportedErrorNon400(t *testing.T) {
	// 生产实测报文（直连 api.ilovecat520.me，带 enable_thinking 回 500、不带回 200）。
	err500 := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("status_code=500, Validation: Unsupported parameter(s): `enable_thinking`"),
	}
	p500, ok500 := IsParamNotSupportedError(err500)
	assert.True(t, ok500, "500 的参数校验报文必须被识别，否则裁剪链路永不触发")
	assert.Equal(t, "enable_thinking", p500)

	// pydantic 原生码是 422，同样必须识别。
	err422 := &types.NewAPIError{
		StatusCode: 422,
		Err:        errors.New("Validation: Unsupported parameter(s): `enable_thinking`"),
	}
	p422, ok422 := IsParamNotSupportedError(err422)
	assert.True(t, ok422, "422 是 pydantic 的原生校验码")
	assert.Equal(t, "enable_thinking", p422)

	// 其余 5xx 变体：只有「明确点名参数」的措辞才算。
	for _, tc := range []struct{ msg, want string }{
		{"Unsupported parameter: 'top_k'", "top_k"},
		{"Unrecognized request argument supplied: top_k", "top_k"},
		{"Additional properties are not allowed ('top_k' was unexpected)", "top_k"},
		{"unknown parameter: top_k", "top_k"},
		{"invalid parameter: top_k", "top_k"},
		{"parameter 'top_k' is not supported", "top_k"},
		{"'top_k' is invalid", "top_k"},
		// loc 是路径数组，末元素才是真正被拒的字段；抓到首元素 body 会让裁剪
		// 变成空操作（stripOpenAIParam 无 body 分支），该渠道就永远不自愈。
		{"extra fields not permitted: loc: ['body', 'top_k']", "top_k"},
		// 单元素 loc 没有逗号，必须由括号那条兜住，否则这一族会从「学错名字」
		// 变成「完全不识别」。
		{"extra fields not permitted: loc: ['top_k']", "top_k"},
		{"'thinking' is not supported on /v1/chat/completions", "thinking"},
	} {
		e := &types.NewAPIError{StatusCode: 502, Err: errors.New(tc.msg)}
		p, ok := IsParamNotSupportedError(e)
		assert.True(t, ok, "严格子集必须识别: %s", tc.msg)
		assert.Equal(t, tc.want, p, "参数名提取错误: %s", tc.msg)
	}

	// 5xx 但**没有**参数校验语义：绝不能误判成参数问题。
	// 误判的代价是双向的：既会裁掉一个上游其实支持的参数，
	// 又会让 IsUpstreamRelayError 提前 return false 而不熔断真正坏掉的渠道。
	for _, msg := range []string{
		"internal server error",
		"no active accounts available: total=2 active=0 cooldown=0 expired=2",
		"模型 glm-5.3-flash 的上游暂时无法使用，系统会定期自动重新检查",
		"field name invalid",        // 散文式，不在严格子集
		"does not support STARTTLS", // 散文式：会提取出 "STARTTLS"
		"task plugin \"gw-a\" does not support a New API upstream",
		"value is invalid", // 无引号参数名
		"connection reset by peer",
		"<html><body>502 Bad Gateway</body></html>",
	} {
		e := &types.NewAPIError{StatusCode: 500, Err: errors.New(msg)}
		_, ok := IsParamNotSupportedError(e)
		assert.False(t, ok, "非参数语义的 5xx 不得误判: %s", msg)
	}

	// 仍被排除的状态码：凭据/权限/路由类，报文里即使出现措辞也不认。
	// 这些码下「不支持某参数」的真实原因几乎必然是凭据或权限，裁剪有害无益。
	for _, code := range []int{401, 403, 404, 429} {
		e := &types.NewAPIError{
			StatusCode: code,
			Err:        errors.New("Validation: Unsupported parameter(s): `enable_thinking`"),
		}
		_, ok := IsParamNotSupportedError(e)
		assert.False(t, ok, "%d 不应触发参数裁剪", code)
	}

	// nil 防御
	_, okNil := IsParamNotSupportedError(nil)
	assert.False(t, okNil)
}

// TestIsParamNotSupportedError400Unchanged 钉住 400 路径未被本次改动收窄。
// 那 71 条生产 400 正是靠它自愈的，任何收紧都是回归。
func TestIsParamNotSupportedError400Unchanged(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want string
	}{
		{"Unsupported parameter: 'reasoning_effort'", "reasoning_effort"},
		{"Unsupported parameters: top_k", "top_k"},
		{"'thinking' is not supported on /v1/chat/completions", "thinking"},
		{"Unrecognized request argument supplied: stream_options", "stream_options"},
		{"Additional properties are not allowed ('top_k' was unexpected)", "top_k"},
		{"does not support parameter \"bar\"", "bar"},
		{"field ReasoningEffort invalid, should be one of: low, medium, high", "reasoning_effort"},
		{"parameter 'top_k' is invalid", "top_k"},
		{"unknown parameter: top_k", "top_k"},
		{"invalid parameter: top_k", "top_k"},
		{"parameter top_k is not supported", "top_k"},
		{"extra fields not permitted: loc: ['body', 'top_k']", "top_k"},
		{"extra fields not permitted: loc: ['top_k']", "top_k"},
		{"level \"max\" not supported, valid levels: low, medium, high", "reasoning_effort"},
	} {
		e := &types.NewAPIError{StatusCode: 400, Err: errors.New(tc.msg)}
		p, ok := IsParamNotSupportedError(e)
		assert.True(t, ok, "400 路径必须保持识别能力: %s", tc.msg)
		assert.Equal(t, tc.want, p, "400 路径参数名提取被改动: %s", tc.msg)
	}
}

func TestIsEOLError(t *testing.T) {
	err410 := &types.NewAPIError{
		StatusCode: 410,
		Err:        errors.New("The model 'deepseek-ai/deepseek-v4-flash-0731' has reached its end of life on 2026-09-21T08:00:00Z and is no longer available."),
	}
	assert.True(t, IsEOLError(err410))

	errOtherCodeWithEOL := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("Model has reached its end of life"),
	}
	assert.True(t, IsEOLError(errOtherCodeWithEOL))

	errNormal := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("Internal server error"),
	}
	assert.False(t, IsEOLError(errNormal))
	assert.False(t, IsEOLError(nil))
}

func TestTripBreaker(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()

	// 初始可用
	ok, _ := tr.IsAvailable(55, testModel)
	assert.True(t, ok)

	// 立即熔断
	tr.TripBreaker(55, testModel)
	ok, reason := tr.IsAvailable(55, testModel)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)
}

func TestIsUpstreamQuotaError(t *testing.T) {
	err1 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("credit insufficient balance: balance=0 required=9952 (request id: 20260928030525807440091c955d568n4BrhQ9s)"),
	}
	assert.True(t, IsUpstreamQuotaError(err1))

	err2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("insufficient_user_quota: out of balance"),
	}
	assert.True(t, IsUpstreamQuotaError(err2))

	err3 := &types.NewAPIError{
		StatusCode: 403,
		Err:        errors.New("You exceeded your current quota, please check your plan and billing details."),
	}
	assert.True(t, IsUpstreamQuotaError(err3))

	errNormal400 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("invalid_request_error: max_tokens must be positive"),
	}
	assert.False(t, IsUpstreamQuotaError(errNormal400))
	assert.False(t, IsUpstreamQuotaError(nil))
}

func TestIsUpstreamRoutingError(t *testing.T) {
	err1 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("Request is missing x-opencode-session and cannot be routed efficiently. Please see https://***.***.ai/***/***"),
	}
	assert.True(t, IsUpstreamRoutingError(err1))

	err2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("MissingSessionID: session header required"),
	}
	assert.True(t, IsUpstreamRoutingError(err2))

	errNormal400 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("invalid json body"),
	}
	assert.False(t, IsUpstreamRoutingError(errNormal400))
	assert.False(t, IsUpstreamRoutingError(nil))
}

func TestIsUpstreamRelayError(t *testing.T) {
	err1 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("来自上游渠道的报错: bad response status code 400 (request id: 202609281247566269176407PebRmdX)"),
	}
	assert.True(t, IsUpstreamRelayError(err1))

	err2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("unknown provider for model deepseek-v4-flash"),
	}
	assert.True(t, IsUpstreamRelayError(err2))

	err3 := &types.NewAPIError{
		StatusCode: 502,
		Err:        errors.New("upstream request failed"),
	}
	assert.True(t, IsUpstreamRelayError(err3))

	// thinking 不支持错误应优先由参数裁剪处理，不被判定为中继失效
	errThinking := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New(`来自上游渠道的报错: "thinking" is not supported on /v1/chat/completions and was not applied. Use "reasoning_effort" (or "***.effort") to control thinking.`),
	}
	assert.False(t, IsUpstreamRelayError(errThinking))

	errNormal400 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("invalid json payload: unexpected EOF"),
	}
	assert.False(t, IsUpstreamRelayError(errNormal400))
	assert.False(t, IsUpstreamRelayError(nil))

	// 思考模式历史 reasoning_content 缺失的 400 错误也归入上游中继失效与即时熔断
	errReasoning := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("The `reasoning_content` in the thinking mode must be passed back to the API. (request_id: 3392e26e-fd8c-4a6d-ba03-2982501fdef1)"),
	}
	assert.True(t, IsUpstreamRelayError(errReasoning))

	// 上游网关模型禁用与暂不可用等 400/503 报错必须识别为中继失效并触发重试
	errGatewayDisabled := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("model is disabled on this gateway: deepseek/deepseek-v4.1-flash"),
	}
	assert.True(t, IsUpstreamRelayError(errGatewayDisabled))

	errModelTempUnavailable := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("status_code=400, 模型 'deepseek-v4-pro-free' 暂不可用，请稍后重试。"),
	}
	assert.True(t, IsUpstreamRelayError(errModelTempUnavailable))

	errNoAvailableChannel := &types.NewAPIError{
		StatusCode: 503,
		Err:        errors.New("当前分组无可用渠道服务该模型"),
	}
	assert.True(t, IsUpstreamRelayError(errNoAvailableChannel))
}

// TestIsUpstreamRelayErrorExcludesParamErrorOn5xx 钉住 5xx 参数错误**不**熔断。
//
// 这是本次放宽状态码门槛后最需要盯住的一条交互：IsUpstreamRelayError 开头就用
// IsParamNotSupportedError 提前 return false，把参数类错误排除在「中继代理异常」
// 熔断之外。语义是对的 —— 裁掉参数就能过，不是渠道坏了，熔断它反而是把一条
// 健康渠道踢出池子。但放宽到 5xx 之后，这条「不熔断」也一并覆盖到了 5xx，
// 必须钉住，否则日后有人收紧某个模式就会让 #238 那类错误重新开始熔断。
func TestIsUpstreamRelayErrorExcludesParamErrorOn5xx(t *testing.T) {
	paramErr := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("status_code=500, Validation: Unsupported parameter(s): `enable_thinking`"),
	}
	assert.False(t, IsUpstreamRelayError(paramErr),
		"5xx 参数错误是请求形状问题，裁剪即可，不应熔断渠道")

	// 反例：同样是 500，但没有参数语义 → 仍应按中继失效熔断。
	// 若这条也变成 false，说明判据被放宽过头了。
	relayErr := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("upstream request failed"),
	}
	assert.True(t, IsUpstreamRelayError(relayErr),
		"普通 5xx 仍必须熔断，否则真故障渠道不再被隔离")
}

func TestIsThinkingModeHistoryError(t *testing.T) {
	err1 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("The `reasoning_content` in the thinking mode must be passed back to the API. (request_id: 3392e26e-fd8c-4a6d-ba03-2982501fdef1)"),
	}
	assert.True(t, IsThinkingModeHistoryError(err1))

	err2 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("The content[].thinking in the thinking mode must be passed back to the API."),
	}
	assert.True(t, IsThinkingModeHistoryError(err2))

	errNormal400 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("invalid json payload"),
	}
	assert.False(t, IsThinkingModeHistoryError(errNormal400))
	assert.False(t, IsThinkingModeHistoryError(nil))
}

func TestIsUpstreamPermissionError(t *testing.T) {
	err1 := &types.NewAPIError{
		StatusCode: 403,
		Err:        errors.New("无权访问 按量分组 分组 (request id: 202609280523093326619788268d9d6owlmt1Nf)"),
	}
	assert.True(t, IsUpstreamPermissionError(err1))

	err2 := &types.NewAPIError{
		StatusCode: 404,
		Err:        errors.New("deepseek-v4-flash is not supported by TokenPlan"),
	}
	assert.True(t, IsUpstreamPermissionError(err2))

	err3 := &types.NewAPIError{
		StatusCode: 403,
		Err:        errors.New("user_group_no_permission"),
	}
	assert.True(t, IsUpstreamPermissionError(err3))

	err4 := &types.NewAPIError{
		StatusCode: 403,
		Err:        errors.New("当前分组本时段不可调用"),
	}
	assert.True(t, IsUpstreamPermissionError(err4))

	err5 := &types.NewAPIError{
		StatusCode: 403,
		Err:        errors.New("Forbidden"),
	}
	assert.True(t, IsUpstreamPermissionError(err5))

	errRelay := &types.NewAPIError{
		StatusCode: 400,
		RelayError: types.OpenAIError{
			Code:    "user_group_no_permission",
			Message: "not authorized for this group",
		},
		Err: errors.New("upstream error"),
	}
	assert.True(t, IsUpstreamPermissionError(errRelay))

	errNormal400 := &types.NewAPIError{
		StatusCode: 400,
		Err:        errors.New("invalid json payload"),
	}
	assert.False(t, IsUpstreamPermissionError(errNormal400))
	assert.False(t, IsUpstreamPermissionError(nil))
}

func TestIsUpstreamRelayError_Excludes429(t *testing.T) {
	// 429 属于上游频次/并发限流，即使带有 new_api_error 类型，也不应判定为中继失效
	err429 := &types.NewAPIError{
		StatusCode: 429,
		RelayError: types.OpenAIError{
			Type:    "new_api_error",
			Message: "您已达到并发请求数限制：最多同时处理1个请求",
		},
		Err: errors.New("channel error (channel #83, status code: 429): status_code=429, 您已达到并发请求数限制：最多同时处理1个请求"),
	}
	assert.False(t, IsUpstreamRelayError(err429))

	// 非 429（如 400）但带有限流信息的聚合中继报错，也必须被互斥排除，不能误判为上游中继代理崩溃
	err400RateLimit := &types.NewAPIError{
		StatusCode: 400,
		RelayError: types.OpenAIError{
			Type:    "new_api_error",
			Message: "您已达到并发请求数限制：最多同时处理1个请求",
		},
		Err: errors.New("channel error: 您已达到并发请求数限制：最多同时处理1个请求"),
	}
	assert.True(t, IsUpstreamRateLimitError(err400RateLimit))
	assert.False(t, IsUpstreamRelayError(err400RateLimit))
}

func TestIsUpstreamRateLimitError(t *testing.T) {
	// 并发超限
	errConcurrency := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("您已达到并发请求数限制：最多同时处理1个请求"),
	}
	assert.True(t, IsUpstreamRateLimitError(errConcurrency))

	// RPM / TPM 限流
	errRPM := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("您已达到请求数限制：1分钟内最多请求5次"),
	}
	assert.True(t, IsUpstreamRateLimitError(errRPM))

	errTPM := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("inference exceeds tpm/rpm limit"),
	}
	assert.True(t, IsUpstreamRateLimitError(errTPM))

	// 临时超额冻结
	errFrozen := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("API 已因用量超额临时冻结，请稍后再试"),
	}
	assert.True(t, IsUpstreamRateLimitError(errFrozen))

	// 额度耗尽（应属于 QuotaError，不属于瞬时 RateLimitError）
	errQuota := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("API key 额度已用完"),
	}
	assert.False(t, IsUpstreamRateLimitError(errQuota))
	assert.True(t, IsUpstreamQuotaError(errQuota))

	// 客户端 API Key 已达到用量上限（应属于 QuotaError）
	errQuotaLimit := &types.NewAPIError{
		StatusCode: 429,
		Err:        errors.New("客户端 API Key 已达到用量上限"),
	}
	assert.False(t, IsUpstreamRateLimitError(errQuotaLimit))
	assert.True(t, IsUpstreamQuotaError(errQuotaLimit))

	// 正常 200/500/400 不算
	assert.False(t, IsUpstreamRateLimitError(nil))
	assert.False(t, IsUpstreamRateLimitError(&types.NewAPIError{StatusCode: 500, Err: errors.New("server error")}))
	assert.False(t, IsUpstreamRateLimitError(&types.NewAPIError{StatusCode: 400, Err: errors.New("bad request")}))
}

func TestRateLimitBreakerShortCooldown(t *testing.T) {
	policy := &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			Breaker: BreakerPolicy{
				CooldownSeconds:          300, // 常规故障 300 秒
				RateLimitCooldownSeconds: 2,   // 429 短冷却 2 秒
				HalfOpenProbes:           1,
			},
		},
		Channels: make(map[int]ChannelPolicy),
	}
	SetPolicy(policy)
	tracker := newTestTracker()
	const chID = 888

	// 触发针对限流的短期熔断
	tracker.TripBreakerForRateLimit(chID, testModel)

	// 熔断中：被 blockedUntil 阻拦
	ok, reason := tracker.IsAvailable(chID, testModel)
	assert.False(t, ok)
	assert.Equal(t, ReasonCircuitBlockedUntil, reason)

	// 等待 2.1 秒后到期
	time.Sleep(2100 * time.Millisecond)

	// 到期后应当立即进入半开状态并允许 1 次探测请求，绝不能继续等待 300 秒常规冷却！
	ok, reason = tracker.IsAvailable(chID, testModel)
	assert.True(t, ok, "短冷却到期后应直接允许探测，当前原因: %s", reason)

	// 探测成功后 End，熔断器闭合
	h := tracker.Begin(chID, testModel)
	h.End(false, false)

	// 闭合后完全正常
	ok, _ = tracker.IsAvailable(chID, testModel)
	assert.True(t, ok)
}

// --- 半开探测配额租约 ---
//
// IsAvailable 以副作用预占半开探测配额，只有真正发出请求并 End 才归还。
// 现实中大量路径「检查通过但请求从未发出」，没有租约时一次泄漏就让渠道
// 永久停在半开耗尽状态。以下用例锁定自愈行为，同时锁定租约不得反过来
// 削弱半开限流。
// ageHalfOpen 把半开轮次的租约时钟往前拨，模拟「探测配额泄漏后过去了很久」。
func ageHalfOpen(tracker *Tracker, id int, d time.Duration) {
	s := statsFor(tracker, id)
	s.openedAt.Store(time.Now().Add(-d).Unix())
	s.halfOpenSince.Store(time.Now().Add(-d).UnixNano())
}

func TestProbeLeakAfterAbandonedRequest(t *testing.T) {
	SetPolicy(testPolicy())
	tracker := newTestTracker()
	const id = 4242

	tracker.TripBreaker(id, testModel)
	ageHalfOpen(tracker, id, 10*time.Minute)

	// 第一次检查通过（预占 1 个探测配额）
	ok, reason := tracker.IsAvailable(id, testModel)
	require.True(t, ok, "first probe should be admitted, got %q", reason)

	// 模拟请求在 Begin/End 之前被放弃：客户端断开、计费准备失败，
	// 或 Responses WebSocket 中继根本不调用 End。租约过去后必须自愈。
	ageHalfOpen(tracker, id, 10*time.Minute)

	ok, reason = tracker.IsAvailable(id, testModel)
	assert.True(t, ok, "channel must self-heal once the abandoned probe's lease expires, got %q", reason)
}

func TestProbeLeakCannotPermanentlyDisableChannel(t *testing.T) {
	p := testPolicy()
	p.Default.Breaker.HalfOpenProbes = 2
	SetPolicy(p)
	tracker := newTestTracker()
	const id = 4343

	tracker.TripBreaker(id, testModel)
	// 进入半开后不再拨动时钟：模拟同一时间窗内连续到来的若干请求，
	// 它们全部「检查通过但请求被放弃」，配额只增不减。
	ageHalfOpen(tracker, id, 10*time.Minute)
	for range 3 {
		_, _ = tracker.IsAvailable(id, testModel)
	}
	require.EqualValues(t, 2, statsFor(tracker, id).halfOpenProbes.Load(),
		"precondition: probes accumulated to the budget without any End")

	// 配额用尽后必须被限流——否则半开保护形同虚设
	ok, reason := tracker.IsAvailable(id, testModel)
	assert.False(t, ok)
	assert.Equal(t, ReasonHalfOpenProbesExceeded, reason)

	// 修复前：熔断器既没回 closed 也没再 open，没有任何东西会重置这个
	// 计数，渠道在此永久不可用。租约到期后必须重新放行探测。
	ageHalfOpen(tracker, id, 10*time.Minute)
	ok, reason = tracker.IsAvailable(id, testModel)
	assert.True(t, ok, "channel must not be permanently disabled, got %q", reason)
}

// 租约未到期时不能过早放行，否则熔断器形同虚设。
func TestProbeLeaseDoesNotFireEarly(t *testing.T) {
	p := testPolicy()
	p.Default.Breaker.HalfOpenProbes = 1
	SetPolicy(p)
	tracker := newTestTracker()
	const id = 4444

	tracker.TripBreaker(id, testModel)
	ageHalfOpen(tracker, id, 10*time.Minute)
	require.True(t, func() bool { ok, _ := tracker.IsAvailable(id, testModel); return ok }())

	// 租约内：仍应被拒，否则熔断保护形同虚设
	ok, reason := tracker.IsAvailable(id, testModel)
	assert.False(t, ok)
	assert.Equal(t, ReasonHalfOpenProbesExceeded, reason)

	// 超过租约：重新放行
	ageHalfOpen(tracker, id, halfOpenProbeLease+time.Second)
	ok, reason = tracker.IsAvailable(id, testModel)
	assert.True(t, ok, "probe must be re-admitted after the lease, got %q", reason)
}

// 正常的探测成功必须立即关闭熔断器，不受租约影响。
func TestProbeLeaseDoesNotDelaySuccessfulRecovery(t *testing.T) {
	SetPolicy(testPolicy())
	tracker := newTestTracker()
	const id = 4545

	tracker.TripBreaker(id, testModel)
	ageHalfOpen(tracker, id, 10*time.Minute)
	ok, _ := tracker.IsAvailable(id, testModel)
	require.True(t, ok)

	// 探测真的发出了请求并成功
	tracker.Begin(id, testModel).End(false, false)

	assert.Equal(t, breakerClosed, breakerState(statsFor(tracker, id).state.Load()))
	// 关闭后 IsAvailable 不再受探测配额限制
	ok, reason := tracker.IsAvailable(id, testModel)
	assert.True(t, ok, "recovered channel should be available, got %q", reason)
}

// 并发上限检查失败时要归还本次预占的配额，且计数不能被扣成负数。
// 重读熔断状态来判断是否归还是不安全的：状态可能在两步之间被并发 End 改掉。
func TestOverloadRefundKeepsProbeCountNonNegative(t *testing.T) {
	p := testPolicy()
	p.Default.MaxInflight = 1
	SetPolicy(p)
	tracker := newTestTracker()
	const id = 4646

	tracker.TripBreaker(id, testModel)
	ageHalfOpen(tracker, id, 10*time.Minute)

	// 占满 inflight，使并发上限检查必然失败
	tracker.Begin(id, testModel)
	require.Equal(t, 1, tracker.Inflight(id))

	ok, reason := tracker.IsAvailable(id, testModel)
	assert.False(t, ok)
	assert.Equal(t, ReasonOverloaded, reason)
	assert.EqualValues(t, 0, statsFor(tracker, id).halfOpenProbes.Load(),
		"an overload rejection must refund the probe it just reserved")

	tracker.getInflight(id).Store(0)
	ok, reason = tracker.IsAvailable(id, testModel)
	assert.True(t, ok, "refunded probe must be re-admittable, got %q", reason)
}

// TestIsUpstreamModelUnavailableError 覆盖生产实测到的两类永不自愈的上游失效：
// 模型映射失效（一天 101 次，集中在 4 个渠道）与 OAuth 凭据刷新失效（一天 42 次）。
// 默认自动禁用状态码只有 401，覆盖不到 404 的「模型不存在」，这些渠道因此永远
// 留在池子里，每次命中都白烧一轮换渠道重试。
func TestIsUpstreamModelUnavailableError(t *testing.T) {
	deterministic := []*types.NewAPIError{
		{StatusCode: 404, Err: errors.New("模型不存在")},
		{StatusCode: 404, Err: errors.New("status_code=404, 模型不存在")},
		{StatusCode: 404, Err: errors.New("The model `grok-4.6` does not exist")},
		{StatusCode: 400, Err: errors.New("model_not_found: unknown model")},
		{StatusCode: 404, Err: errors.New("no such model: deepseek-v4-flash")},
		{StatusCode: 400, Err: errors.New("model is disabled on this gateway: deepseek/deepseek-v4.1-flash")},
		{StatusCode: 400, Err: errors.New("failed to get access token: oauth2: cannot fetch token: 400 Bad Request")},
	}
	for i, err := range deterministic {
		assert.True(t, IsUpstreamModelUnavailableError(err), "case %d 应当判为永不恢复的上游失效: %v", i, err)
	}
	assert.False(t, IsUpstreamModelUnavailableError(nil))

	// 会自愈的一律不得误判：限流、额度耗尽、5xx、参数问题都只是瞬时故障，
	// 误禁用会把健康渠道踢出池子。
	selfHealing := []*types.NewAPIError{
		{StatusCode: 429, Err: errors.New("rate limit exceeded, please retry after 3 seconds")},
		{StatusCode: 429, Err: errors.New("API key 额度已用完")},
		{StatusCode: 500, Err: errors.New("internal server error")},
		{StatusCode: 502, Err: errors.New("bad response status code 502")},
		{StatusCode: 400, Err: errors.New("Invalid request: unexpected EOF")},
		{StatusCode: 400, Err: errors.New("field ReasoningEffort invalid, should be one of: low, medium, high")},
	}
	for i, err := range selfHealing {
		assert.False(t, IsUpstreamModelUnavailableError(err), "case %d 是瞬时故障，不得判为永不恢复: %v", i, err)
	}
}

// TestIsUpstreamModelUnavailableError_NoFalsePositives 确认组合判据不误伤。
func TestIsUpstreamModelUnavailableError_NoFalsePositives(t *testing.T) {
	unrelated := []*types.NewAPIError{
		{StatusCode: 404, Err: errors.New("session does not exist")},
		{StatusCode: 404, Err: errors.New("the requested resource does not exist")},
		{StatusCode: 404, Err: errors.New("Not Found")},
	}
	for i, err := range unrelated {
		assert.False(t, IsUpstreamModelUnavailableError(err), "case %d 与模型无关，不得判为模型失效: %v", i, err)
	}
}

// TestTrackerInflightReturnedOnPanic pins the property the relay retry loop
// depends on for its panic guard.
//
// The loop in controller/relay.go registers `defer lbAttempt.End(false, false)`
// inside a for body, which is normally a no-op: a defer registered in a loop
// runs when the *function* exits, not when the iteration does. It is not a
// no-op on the panic path, because unwinding out of the panicking function is
// itself a function exit — every iteration's defer runs, in LIFO order, and the
// panicking attempt's handle is among them.
//
// Without that, a panic skips every explicit End on the way out and the
// channel's inflight slot is never returned: the concurrency budget for that
// channel is permanently one lower for the life of the process.
func TestTrackerInflightReturnedOnPanic(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := newTestTracker()
	const channelID = 7

	// Two attempts succeed normally, the third panics partway through — the
	// same shape as relay: Begin, do work, End, and the End is skipped when the
	// work panics.
	attempts := func() {
		for i := 0; i < 3; i++ {
			h := tr.Begin(channelID, testModel)
			defer h.End(false, false)
			if i == 2 {
				panic("upstream handler blew up")
			}
			h.End(false, false)
		}
	}

	func() {
		defer func() { assert.Equal(t, "upstream handler blew up", recover()) }()
		attempts()
	}()

	assert.Equal(t, 0, tr.Inflight(channelID), "a panicking attempt must still return its inflight slot")

	// The same guard must not double-count the attempts that did reach their
	// own End, or a single panic would inflate the failure count instead.
	assert.Zero(t, statsFor(tr, channelID).consecutiveFailures.Load(),
		"the panic guard must not add breaker failures of its own")
}

// TestRequestTimeoutPolicy pins the tri-state semantics of the non-stream
// request budget (request_timeout_ms).
//
// The budget exists because a non-stream upstream response "arrives all at
// once": the TTFT timer only bounds stream first-byte waits, so without a
// request-level budget a non-stream attempt could hang until the upstream (or
// its fronting gateway) decided to answer, and cross-channel retries stacked
// those waits without limit.
func TestRequestTimeoutPolicy(t *testing.T) {
	// 缺省（yaml 未写该键）：使用内置默认值。
	var omitted Policy
	assert.Equal(t, time.Duration(DefaultRequestTimeoutMs)*time.Millisecond, omitted.RequestTimeout())
	assert.Equal(t, 180*time.Second, DefaultPolicy().RequestTimeout())

	// 显式 0：关闭。
	off := 0
	omitted.RequestTimeoutMs = &off
	assert.Equal(t, time.Duration(0), omitted.RequestTimeout())

	// 显式正值：按配置生效。
	custom := 45000
	omitted.RequestTimeoutMs = &custom
	assert.Equal(t, 45*time.Second, omitted.RequestTimeout())

	// nil 接收者不 panic（GetPolicy 的兜底路径）。
	var nilPolicy *Policy
	assert.NotZero(t, nilPolicy.RequestTimeout())
}

func TestEmptyStreamPolicyAndTracker(t *testing.T) {
	// 1. 策略三态验证
	var omitted Policy
	assert.Equal(t, time.Duration(DefaultEmptyStreamRetryBudgetMs)*time.Millisecond, omitted.EmptyStreamRetryBudget())
	assert.Equal(t, DefaultEmptyStreamTripThreshold, omitted.EmptyStreamTripLimit())

	off := 0
	omitted.EmptyStreamRetryBudgetMs = &off
	omitted.EmptyStreamTripThreshold = &off
	assert.Equal(t, time.Duration(0), omitted.EmptyStreamRetryBudget())
	assert.Equal(t, 0, omitted.EmptyStreamTripLimit())

	customMs := 15000
	customThreshold := 5
	omitted.EmptyStreamRetryBudgetMs = &customMs
	omitted.EmptyStreamTripThreshold = &customThreshold
	assert.Equal(t, 15*time.Second, omitted.EmptyStreamRetryBudget())
	assert.Equal(t, 5, omitted.EmptyStreamTripLimit())

	// nil 接收者兜底
	var nilPolicy *Policy
	assert.NotZero(t, nilPolicy.EmptyStreamRetryBudget())
	assert.Equal(t, DefaultEmptyStreamTripThreshold, nilPolicy.EmptyStreamTripLimit())

	// 2. 连续空流熔断跟踪
	oldPolicy := currentPolicy.Load()
	p := testPolicy()
	three := 3
	p.EmptyStreamTripThreshold = &three
	currentPolicy.Store(p)
	defer currentPolicy.Store(oldPolicy)

	tr := newTestTracker()
	chID := 55

	assert.Equal(t, 0, tr.EmptyStreamStreak(chID, testModel))
	tr.RecordEmptyStream(chID, testModel)
	assert.Equal(t, 1, tr.EmptyStreamStreak(chID, testModel))
	ok, _ := tr.IsAvailable(chID, testModel)
	assert.True(t, ok)

	tr.RecordEmptyStream(chID, testModel)
	assert.Equal(t, 2, tr.EmptyStreamStreak(chID, testModel))
	ok, _ = tr.IsAvailable(chID, testModel)
	assert.True(t, ok)

	// 达到阈值 3：触发硬熔断，streak 重置为 0
	tr.RecordEmptyStream(chID, testModel)
	assert.Equal(t, 0, tr.EmptyStreamStreak(chID, testModel))
	ok, reason := tr.IsAvailable(chID, testModel)
	assert.False(t, ok, "达到阈值后应立即熔断")
	assert.Equal(t, "circuit_open", reason)

	// 清零方法验证
	chID2 := 56
	tr.RecordEmptyStream(chID2, testModel)
	assert.Equal(t, 1, tr.EmptyStreamStreak(chID2, testModel))
	tr.ClearEmptyStream(chID2, testModel)
	assert.Equal(t, 0, tr.EmptyStreamStreak(chID2, testModel))
}

// testModel 是测试里统一用的模型名。
//
// 它的作用不是「测试按模型熔断」，而是让这些用例继续落在**渠道级**：
// per_model 默认关闭，scopeKey 会把任何非空 model 折叠回渠道级，于是
// 这些老用例的语义与 v29.10 逐位一致。按模型的行为由 v2911_new_test.go
// 单独覆盖。
const testModel = "gpt-4o"

// newTestTracker 造一个空 Tracker。
// 熔断状态表与并发计数表是分开的两张（见 Tracker 注释），所以必须一起初始化。
func newTestTracker() *Tracker {
	return &Tracker{
		breakers: make(map[breakerKey]*ChannelStats),
		inflight: make(map[int]*atomic.Int32),
	}
}

// statsFor 取该渠道的**渠道级**条目。
// per_model 关闭时所有状态都落在这把键上（scopeKey 会把任何 model 折叠
// 成空串），所以它是这些老用例观察熔断状态的正确入口。
func statsFor(t *Tracker, id int) *ChannelStats {
	return t.getBreaker(scopeKey(id, ""))
}
