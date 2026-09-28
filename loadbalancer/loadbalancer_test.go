package loadbalancer

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
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
	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	h1 := tr.Begin(1)
	h2 := tr.Begin(1)
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

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 连续 3 次慢请求（非失败）只触发软降级，不硬熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(7)
		h.End(true, false)
	}
	ok, _ := tr.IsAvailable(7)
	assert.True(t, ok, "channel 7 should stay available after 3 slow requests (soft degrade only)")
	assert.True(t, tr.IsDegraded(7), "channel 7 should be degraded after 3 slow requests")

	// 连续 3 次硬失败触发熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(8)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(8)
	assert.False(t, ok, "channel 8 should be unavailable after 3 hard failures")
	assert.Equal(t, "circuit_open", reason)

	// 手动把 openedAt 拨到冷却期之前，模拟冷却结束
	s := tr.getOrCreate(8)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 半开：允许 1 个探测
	ok, _ = tr.IsAvailable(8)
	assert.True(t, ok, "channel 8 should allow 1 probe in half-open")
	// 第 2 个被拒绝
	ok, _ = tr.IsAvailable(8)
	assert.False(t, ok, "channel 8 should reject 2nd probe in half-open")

	// 探测成功后熔断器关闭
	h := tr.Begin(8)
	h.End(false, false)
	ok, _ = tr.IsAvailable(8)
	assert.True(t, ok, "channel 8 should be available after successful probe")
}

func TestBreakerHalfOpenProbeFailure(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 1. 触发硬熔断（3次失败）
	for i := 0; i < 3; i++ {
		h := tr.Begin(100)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(100)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 2. 冷却结束，进入半开
	s := tr.getOrCreate(100)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 3. 放行 1 个探测
	ok, _ = tr.IsAvailable(100)
	assert.True(t, ok)

	// 4. 探测失败！此时应立即切回 circuit_open，并开启新的冷却时间
	h := tr.Begin(100)
	h.End(false, true)

	assert.Equal(t, int32(breakerOpen), s.state.Load(), "state should revert to breakerOpen after failed probe")
	assert.Equal(t, int32(0), s.halfOpenProbes.Load(), "halfOpenProbes should be reset to 0")

	// 此时冷却未结束（刚熔断），不应可用
	ok, reason = tr.IsAvailable(100)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 模拟过了新的一轮冷却时间后，应再次允许探测（不会永久死锁）
	s.openedAt.Store(time.Now().Unix() - 61)
	ok, _ = tr.IsAvailable(100)
	assert.True(t, ok, "channel should allow probe again after next cooldown")
}

func TestTrackerBreakerHalfOpenProbeSuccessRecordSuccess(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 1. 触发硬熔断
	for i := 0; i < 3; i++ {
		tr.RecordFailure(200)
	}
	s := tr.getOrCreate(200)
	assert.Equal(t, int32(breakerOpen), s.state.Load())

	// 2. 冷却结束，进入半开
	s.openedAt.Store(time.Now().Unix() - 61)

	// 3. 放行 1 个探测请求
	ok, _ := tr.IsAvailable(200)
	assert.True(t, ok)
	assert.Equal(t, int32(breakerHalfOpen), s.state.Load())
	assert.Equal(t, int32(1), s.halfOpenProbes.Load())

	// 4. 请求成功调用 RecordSuccess
	tr.RecordSuccess(200)

	// 5. 验证熔断器成功闭合，且 halfOpenProbes 重置为 0
	assert.Equal(t, int32(breakerClosed), s.state.Load(), "state should close to breakerClosed after RecordSuccess")
	assert.Equal(t, int32(0), s.halfOpenProbes.Load(), "halfOpenProbes should be reset to 0")
	assert.Equal(t, int32(0), s.consecutiveFailures.Load(), "consecutiveFailures should be reset to 0")

	// 6. 后续请求应该完全可用，不会出现 circuit_half_open_probes_exhausted
	ok, reason := tr.IsAvailable(200)
	assert.True(t, ok, "channel should be available after breaker closed")
	assert.Empty(t, reason)
}

func TestTrackerIgnoreZeroChannel(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// channel <= 0 不应崩溃且不应污染统计
	h := tr.Begin(0)
	assert.NotNil(t, h)
	h.MarkFirstByte()
	h.End(false, true)

	assert.Equal(t, 0, tr.Inflight(0))
	assert.Equal(t, int64(-1), tr.AvgTTFT(0))
	assert.False(t, tr.IsDegraded(0))
	ok, _ := tr.IsAvailable(0)
	assert.True(t, ok)

	tr.RecordFailure(0)
	tr.RecordSuccess(0)
}

func TestOverloadSkip(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// MaxInflight=2，打满
	_ = tr.Begin(9)
	_ = tr.Begin(9)

	ok, reason := tr.IsAvailable(9)
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

	// 500 不应触发参数裁剪
	err4 := &types.NewAPIError{
		StatusCode: 500,
		Err:        errors.New("field ReasoningEffort invalid"),
	}
	_, ok4 := IsParamNotSupportedError(err4)
	assert.False(t, ok4)
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

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 初始可用
	ok, _ := tr.IsAvailable(55)
	assert.True(t, ok)

	// 立即熔断
	tr.TripBreaker(55)
	ok, reason := tr.IsAvailable(55)
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
