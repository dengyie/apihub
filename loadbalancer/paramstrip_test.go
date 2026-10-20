package loadbalancer

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dengyie/apihub/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrappedParamError 造一条生产原样的错误：中转层把上游的 pydantic 校验报文
// 包在自己的 5xx 里返回（渠道 #238 / ilovecat520-cc，2026-10-06 实测）。
func wrappedParamError(status int, msg string) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(msg), types.ErrorCodeBadResponseBody, status)
}

// withParamStripState 装一份开启的策略，并清空自动学习集合。
//
// 两件事都必须做，缺一件测试就会**空转通过**：MarkParamUnsupported 与
// GetStripParams 开头都是 !Enabled() 直接返回，策略没打开时
// 「裁剪列表里有这个参数」在什么都没发生时也成立；而 paramStripLearned 是
// 包级变量，同包其它用例学到的东西会渗进来。
func withParamStripState(t *testing.T) {
	t.Helper()
	installPolicy(t, testPolicy())

	paramStripMu.Lock()
	oldLearned := paramStripLearned
	oldConfig := paramStripConfig
	paramStripLearned = make(map[paramStripKey]map[string]struct{})
	paramStripConfig = make(map[int]map[string]struct{})
	paramStripMu.Unlock()

	t.Cleanup(func() {
		paramStripMu.Lock()
		paramStripLearned = oldLearned
		paramStripConfig = oldConfig
		paramStripMu.Unlock()
	})
}

// TestParamStripLearnsFromWrapped5xxParamError 走完整条链路：
// 报文识别 → MarkParamUnsupported → GetStripParams。
//
// 只测第一步是不够的：v29.32 之前 5xx 在 IsParamNotSupportedError 第一行就被
// 挡掉，学习链路整条静默失效——不标记、不裁剪，每个请求稳定白烧一轮报错。
func TestParamStripLearnsFromWrapped5xxParamError(t *testing.T) {
	withParamStripState(t)

	err := wrappedParamError(http.StatusInternalServerError,
		"Validation: Unsupported parameter(s): `enable_thinking`")

	param, ok := IsParamNotSupportedError(err)
	require.True(t, ok, "5xx 包着的校验报文必须被认成参数不支持")
	assert.Equal(t, "enable_thinking", param)

	MarkParamUnsupported(238, "glm-5.3-flash", param)
	assert.Contains(t, GetStripParams(238, "glm-5.3-flash"), "enable_thinking",
		"学到的参数必须真的出现在该渠道该模型的裁剪列表里，否则学习是无声无息的白做")
}

// TestParamStripLearnedIsScopedToOneModel 锁住自动学习的作用域。
//
// 「上游不支持某参数」是关于**那个模型**的观察，不是关于这条渠道的：
// 渠道 #238 同时挂着 gemini-3.8-flash / kimi-k3 / claude-opus-4-8 /
// glm-5.3-flash / deepseek-v4.1-flash / deepseek-v4-flash 六个模型，
// 按渠道存会让任一模型的一次误判外溢到另外五个，把它们本该支持的参数一起裁掉。
func TestParamStripLearnedIsScopedToOneModel(t *testing.T) {
	withParamStripState(t)

	err := wrappedParamError(http.StatusInternalServerError,
		"Validation: Unsupported parameter(s): `enable_thinking`")
	param, ok := IsParamNotSupportedError(err)
	require.True(t, ok)
	MarkParamUnsupported(238, "glm-5.3-flash", param)

	for _, sibling := range []string{"kimi-k3", "claude-opus-4-8", "deepseek-v4.1-flash", "gemini-3.8-flash"} {
		assert.NotContains(t, GetStripParams(238, sibling), "enable_thinking",
			"一个模型的误判不得外溢到同渠道的其它模型 %s", sibling)
	}
	assert.NotContains(t, GetStripParams(237, "glm-5.3-flash"), "enable_thinking",
		"一条渠道的观测不得外溢到另一条渠道")
	assert.Contains(t, GetStripParams(238, "glm-5.3-flash"), "enable_thinking",
		"被观测到的那个模型本身必须仍然能裁")
}

// TestParamStripConfigPresetStaysChannelScoped 锁住那条**有意保留**的例外：
// 运维在 yaml 里显式写的 paramStripConfig 仍按渠道生效。
//
// 它是人的决定，不是观测——不能因为自动学习收窄到了模型级，就把人写的规则也
// 一起收窄，那会让 #238 上写好的预设对 kimi-k3 之类的模型静默失效。
func TestParamStripConfigPresetStaysChannelScoped(t *testing.T) {
	withParamStripState(t)

	SetParamStripConfig(map[int][]string{238: {"enable_thinking", "thinking"}})
	assert.Contains(t, GetStripParams(238, "kimi-k3"), "enable_thinking",
		"运维预置按渠道生效，不该被自动学习的作用域收窄")
	assert.Contains(t, GetStripParams(238, "glm-5.3-flash"), "enable_thinking")
	assert.NotContains(t, GetStripParams(237, "kimi-k3"), "enable_thinking",
		"预置只作用于写明的那条渠道")
}

// TestParamStripIgnoreLooseProseOutside400 锁住 5xx 上刻意保留的保守：
// 散文式措辞（does not support X）只在 400 上认。
//
// 放宽到 5xx 的前提是「不误判无关故障」，而 `SMTP server does not support
// STARTTLS` 这类通用文案会命中并提取出 "STARTTLS" 这种不是请求参数的名字。
func TestParamStripIgnoreLooseProseOutside400(t *testing.T) {
	withParamStripState(t)

	_, ok := IsParamNotSupportedError(wrappedParamError(http.StatusInternalServerError,
		"the relay does not support ClickHouse"))
	assert.False(t, ok, "散文式措辞在 5xx 上不得被当成参数错误")

	param, ok := IsParamNotSupportedError(wrappedParamError(http.StatusBadRequest,
		"the relay does not support ClickHouse"))
	require.True(t, ok, "同样的措辞在 400 上仍按既往行为识别")
	assert.Equal(t, "click_house", param, "抽取结果照常过一遍 normalizeParamName 的驼峰拆分")
}

// TestParamStripExcludesAuthAndRoutingStatusCodes 锁住放宽的另一半：只进 400 /
// 422 / 5xx，其余一律不进。
//
// 401/403/404/429 下即便出现「不支持某参数」的措辞，真实原因几乎必然是凭据、
// 权限或路由，与裁剪参数无关，放进来只会误伤——把一条只是 key 失效的渠道
// 学上一笔裁剪规则，然后对着 401 反复重发同一个被裁掉的请求。
func TestParamStripExcludesAuthAndRoutingStatusCodes(t *testing.T) {
	withParamStripState(t)

	const msg = "Validation: Unsupported parameter(s): `enable_thinking`"

	for _, code := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
		http.StatusMovedPermanently,
	} {
		_, ok := IsParamNotSupportedError(wrappedParamError(code, msg))
		assert.False(t, ok, "状态码 %d 不承载参数校验失败，不得识别为参数不支持", code)
	}

	// 对照：被放行的三类必须仍然识别，否则上面这条排除就没有边界了。
	for _, code := range []int{
		http.StatusBadRequest,
		http.StatusUnprocessableEntity,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		_, ok := IsParamNotSupportedError(wrappedParamError(code, msg))
		assert.True(t, ok, "状态码 %d 必须识别为参数不支持", code)
	}
}

func TestIsReasoningHydrationError(t *testing.T) {
	err1 := types.NewErrorWithStatusCode(
		errors.New("The encrypted content for item rs_01fd9964e6696813016ac9d2f15b3c819395da0763f4a21526 could not be verified. Reason: reasoning hydration failed: Encrypted content could not be decrypted or parsed."),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.True(t, IsReasoningHydrationError(err1))
	assert.False(t, IsUpstreamRelayError(err1), "推理水合解密失败不得被判定为中继代理失效")

	err2 := types.NewErrorWithStatusCode(
		errors.New("Encrypted content could not be decrypted"),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.True(t, IsReasoningHydrationError(err2))
	assert.False(t, IsUpstreamRelayError(err2), "推理水合解密失败不得被判定为中继代理失效")

	// 验证 RelayError 为 types.OpenAIError 或 *types.OpenAIError 时的解包识别
	errWrappedVal := &types.NewAPIError{
		StatusCode: http.StatusBadRequest,
		RelayError: types.OpenAIError{
			Message: "The encrypted content for item rs_abc could not be verified. Reason: reasoning hydration failed",
		},
	}
	assert.True(t, IsReasoningHydrationError(errWrappedVal), "OpenAIError 值结构体中的推理水合错误必须被识别")

	errWrappedPtr := &types.NewAPIError{
		StatusCode: http.StatusBadRequest,
		RelayError: &types.OpenAIError{
			Message: "Encrypted content could not be decrypted or parsed",
		},
	}
	assert.True(t, IsReasoningHydrationError(errWrappedPtr), "OpenAIError 指针结构体中的推理水合错误必须被识别")

	errOther := types.NewErrorWithStatusCode(
		errors.New("invalid parameter: temperature"),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.False(t, IsReasoningHydrationError(errOther))
	assert.False(t, IsReasoningHydrationError(nil))
}

func TestIsCodexValidationBadRequest(t *testing.T) {
	err1 := types.NewErrorWithStatusCode(
		errors.New(`{"error":{"message":"invalid codex request","type":"new_api_error","code":"invalid_responses_request"}}`),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.True(t, IsCodexValidationBadRequest(err1))
	assert.False(t, IsUpstreamRelayError(err1), "invalid codex request 不得被误判为中继代理失效熔断")

	err2 := types.NewErrorWithStatusCode(
		errors.New(`{"error":{"message":"invalid request","code":"invalid_responses_request"}}`),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.True(t, IsCodexValidationBadRequest(err2))
	assert.False(t, IsUpstreamRelayError(err2))

	errNormal400 := types.NewErrorWithStatusCode(
		errors.New("invalid parameter: temperature"),
		types.ErrorCodeBadResponseBody,
		http.StatusBadRequest,
	)
	assert.False(t, IsCodexValidationBadRequest(errNormal400))
	assert.False(t, IsCodexValidationBadRequest(nil))
}

func TestAnyRouterLoadSaturationIsRateLimit(t *testing.T) {
	errSaturated := types.NewErrorWithStatusCode(
		errors.New(`{"error":{"message":"当前模型 gpt-6-astra 负载已经达到上限，请稍后重试 (request id: 20261011061804119294113i3RTqyyg)","type":"new_api_error","param":"","code":"get_channel_failed"}}`),
		types.ErrorCodeBadResponseBody,
		http.StatusInternalServerError,
	)
	assert.True(t, IsUpstreamRateLimitError(errSaturated), "AnyRouter 负载达到上限必须识别为瞬时限流而非硬熔断")
	assert.False(t, IsUpstreamRelayError(errSaturated), "限流/负载饱和错误不得被误判为中继故障而触发级联硬熔断")
}

func TestParamStripReasoningContentAndRelayErrorHardening(t *testing.T) {
	// 1. Pydantic style without loc: messages.1.reasoning_content: extra fields not permitted
	err1 := &types.NewAPIError{
		StatusCode: http.StatusBadRequest,
		Err:        errors.New("messages.1.reasoning_content: extra fields not permitted"),
	}
	p1, ok1 := IsParamNotSupportedError(err1)
	assert.True(t, ok1)
	assert.Equal(t, "reasoning_content", p1)

	// 2. Wrapped in 5xx: loc: ['body', 'messages', 1, 'reasoning_content']
	err2 := wrappedParamError(http.StatusInternalServerError,
		"Validation error: extra fields not permitted, loc: ['body', 'messages', 1, 'reasoning_content']")
	p2, ok2 := IsParamNotSupportedError(err2)
	assert.True(t, ok2)
	assert.Equal(t, "reasoning_content", p2)

	// 3. Generic reasoning unsupported
	err3 := &types.NewAPIError{
		StatusCode: http.StatusBadRequest,
		Err:        errors.New("parameter reasoning is not supported"),
	}
	p3, ok3 := IsParamNotSupportedError(err3)
	assert.True(t, ok3)
	assert.Equal(t, "reasoning", p3)

	// 4. Plain Bad Request from upstream relay
	err4 := &types.NewAPIError{
		StatusCode: http.StatusBadRequest,
		Err:        errors.New("status_code=400, Bad Request"),
	}
	assert.True(t, IsUpstreamRelayError(err4))

	// 5. Upstream relay vendor error types
	for _, errType := range []string{"pm_api_error", "new_api_error", "provider_error", "channel_error", "relay_error", "router_error"} {
		errRelay := &types.NewAPIError{
			StatusCode: http.StatusBadRequest,
			Err:        errors.New("upstream failed"),
			RelayError: types.OpenAIError{
				Message: "upstream failed",
				Type:    errType,
				Code:    400,
			},
		}
		assert.True(t, IsUpstreamRelayError(errRelay), "error type %s must be recognized as upstream relay error", errType)
	}
}

// TestIsParamNotSupportedErrorInvalidRequestParameters 钉住「Invalid request
// parameters: X」这一族措辞。
//
// 生产实测渠道 #52（gpt-6-luna，2026-10-21 02:29）的原话是：
//
//	status_code=400, status_code=400, Invalid request parameters: enable_thinking is not supported.
//
// 它同时躲过了两条既有判据：reInvalidParamColon 要求 "invalid" 与
// "parameter" 之间只有空白，而这里隔着 "request"；reParamNotSupportedBare
// 要求 "parameter" 后面直接跟参数名，而这里跟的是 "s:"（复数 parameters）。
// 于是这条 400 从未被认成参数问题，#52 每个请求都白烧一轮 enable_thinking，
// 而且永不自愈 —— 自动学习链路一次也没被触发过。
func TestIsParamNotSupportedErrorInvalidRequestParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "生产原话",
			msg:  "status_code=400, status_code=400, Invalid request parameters: enable_thinking is not supported. (request id: 202610201829178775553148268d9d66N0jSnzz)",
			want: "enable_thinking",
		},
		{
			name: "单数 parameter",
			msg:  "Invalid request parameter: top_k",
			want: "top_k",
		},
		{
			name: "小写",
			msg:  "invalid request parameters: top_k",
			want: "top_k",
		},
		{
			name: "引号包裹",
			msg:  "Invalid request parameters: 'top_k'",
			want: "top_k",
		},
		{
			name: "双引号包裹",
			msg:  `Invalid request parameters: "top_k"`,
			want: "top_k",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 400 走全量判据表。
			e := &types.NewAPIError{StatusCode: http.StatusBadRequest, Err: errors.New(tc.msg)}
			p, ok := IsParamNotSupportedError(e)
			assert.True(t, ok, "400 路径必须识别: %s", tc.msg)
			assert.Equal(t, tc.want, p, "参数名提取错误: %s", tc.msg)

			// 同一族措辞被中转层包成 5xx 时也必须认 —— 判据是措辞不是状态码。
			e5 := &types.NewAPIError{StatusCode: 502, Err: errors.New(tc.msg)}
			p5, ok5 := IsParamNotSupportedError(e5)
			assert.True(t, ok5, "5xx 路径必须识别: %s", tc.msg)
			assert.Equal(t, tc.want, p5, "5xx 参数名提取错误: %s", tc.msg)
		})
	}
}

// TestIsParamNotSupportedErrorInvalidRequestParamsNoFalsePositive 钉住这条新
// 判据不会把无关文案误判成参数问题。
//
// 误判的代价是双向的：既会裁掉一个上游其实支持的参数，又会让
// IsUpstreamRelayError 提前 return false 而不熔断真正坏掉的渠道。
func TestIsParamNotSupportedErrorInvalidRequestParamsNoFalsePositive(t *testing.T) {
	for _, msg := range []string{
		"invalid request body: json: unknown field \"enable_thinking\"",
		"invalid request",
		"invalid request parameters are not acceptable here",
		"internal server error",
	} {
		e := &types.NewAPIError{StatusCode: http.StatusBadRequest, Err: errors.New(msg)}
		p, ok := IsParamNotSupportedError(e)
		assert.False(t, ok, "无关文案不得被误判成参数问题: %s", msg)
		assert.Empty(t, p, "误判时不应带出参数名: %s", msg)
	}
}
