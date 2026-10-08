package gemini

import (
	"testing"

	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiTruncatedStreamKeepsInterruptedUsage 断流时已送达客户端的输出不能白送。
//
// 断流的形状：上游吐了半句话就断了（没有 finishReason，也没有 [DONE]）。
// 客户端已经收到了这段内容，上游也已经按它扣了费。geminiStreamHandler 却
// `return nil, streamErr`，把累积到一半的 usage 丢掉；compatible_handler 看到
// err != nil 就直接 return，根本走不到 PostTextConsumeQuota —— 于是这一次请求
// 计费为 0。断流越多，亏得越多，而且账单上完全看不出异常。
//
// 修法沿用 Responses 那条路的既有约定：把中断时已累积的用量挂在
// info.InterruptedStreamUsage 上，由 controller 在整轮重试都失败之后统一结算
// （重试成功时那轮的正常结算已经覆盖整次请求，不能重复收）。
func TestGeminiTruncatedStreamKeepsInterruptedUsage(t *testing.T) {
	c, info, resp := geminiEOFCase(t, []dto.GeminiChatResponse{
		{
			Candidates: []dto.GeminiChatCandidate{{
				Content: dto.GeminiChatContent{
					Role:  "model",
					Parts: []dto.GeminiPart{{Text: "这是一段被上游中途掐断的输出，客户端已经收到了它，"}},
				},
			}},
		},
		{
			Candidates: []dto.GeminiChatCandidate{{
				Content: dto.GeminiChatContent{
					Role:  "model",
					Parts: []dto.GeminiPart{{Text: "客户端也应当为它付费。"}},
				},
			}},
		},
	})

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.NotNil(t, newAPIError, "本用例的前提就是流确实断了")
	require.NotNil(t, usage,
		"断流时必须把已累积的用量连同错误一起返回，由调用方转交 controller 结算，否则客户端白拿一次输出")
	assert.Greater(t, usage.CompletionTokens, 0,
		"已经送达客户端的文本量必须体现在结算里")
	assert.Greater(t, usage.TotalTokens, 0)
}

// TestGeminiEmptyFailedStreamIsNotBilled 一无所获的失败不该产生账单。
//
// 这是上面那条的反向守卫：返回的 usage 只在**客户端确实收到过东西**时才非 nil。
// 纯失败（一个字节都没吐）按 0 计费才是对的 —— 否则一次上游 5xx 反复
// 重试失败，反而会凭空收用户钱。
func TestGeminiEmptyFailedStreamIsNotBilled(t *testing.T) {
	c, info, resp := geminiEOFCase(t, nil) // 一个数据块都没有

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.NotNil(t, newAPIError)
	assert.Nil(t, usage, "客户端什么都没收到，不该产生任何待结算用量")
}
