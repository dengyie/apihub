package gemini

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini 的流没有「显式终止帧」这种东西：它每一块都是一个完整的 JSON 对象，
// 流是否正常结束只能由**响应内容**判断 —— 而唯一能充当终止标记的字段只有
// `candidates[].finishReason`（以及 `promptFeedback.blockReason` 这条没有
// candidates 的旁路）。
//
// geminiStreamHandler 声明了 RequireTerminal()，所以 EOF 时若没见到任何终止
// 标记，StreamScannerHandler 会判 StreamBrokenError → 502 → 调用方立即
// tripModelScope 熔断该渠道。判据清单少一项，就等于「上游正常收尾」被当成
// 「上游中途断流」，代价是摘掉一条健康渠道 —— 而这类误判没有任何告警。
//
// 这批用例盯的是「哪些响应形状算正常收尾」。它们刻意**不发送 data: [DONE]**：
// 带 [DONE] 的流走的是 EndReasonDone 分支，根本到不了 IsNormalEnd 的 EOF 判定，
// 也就测不出这里的 bug。

func geminiEOFCase(t *testing.T, chunks []dto.GeminiChatResponse) (*gin.Context, *relaycommon.RelayInfo, *http.Response) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	var body bytes.Buffer
	for _, chunk := range chunks {
		data, err := common.Marshal(chunk)
		require.NoError(t, err)
		body.WriteString("data: " + string(data) + "\n")
	}
	return c, info, &http.Response{Body: io.NopCloser(bytes.NewReader(body.Bytes()))}
}

// TestGeminiStreamBlockReasonIsNormalEnd 安全拦截的响应是**完整**响应，不是断流。
//
// 形状：{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{...}}
// Candidates 为空。这条响应没有任何内容可返回，但它是上游正常给出的完整答复 —
// 用户就是被安全策略拦下了。没有它，下面那个 for 循环不执行，于是流以 EOF
// 收尾时看不到任何终止标记，被判成 StreamBrokenError 并熔断渠道 #4。
func TestGeminiStreamBlockReasonIsNormalEnd(t *testing.T) {
	blockReason := "SAFETY"
	c, info, resp := geminiEOFCase(t, []dto.GeminiChatResponse{{
		PromptFeedback: &dto.GeminiChatPromptFeedback{BlockReason: &blockReason},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 12,
			TotalTokenCount:  12,
		},
	}})

	_, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.Nil(t, newAPIError,
		"安全拦截是上游给出的完整答复，必须按正常收尾处理；判成断流会 502 并熔断健康渠道")
	assert.True(t, info.StreamStatus.IsNormalEnd())
	assert.True(t, info.PerformanceBusinessRejection, "业务拒绝标记仍要保留，供面板归因")
}

// TestGeminiStreamFinishReasonIsNormalEnd 常规 STOP 收尾也必须是正常结束。
//
// 这是对照组：证明上面那条不是因为「加了 [DONE] 才过」，而是终止标记本身
// 被正确识别。若哪天有人把 MarkCompleted 整个删掉，本条会与上一条一起红。
func TestGeminiStreamFinishReasonIsNormalEnd(t *testing.T) {
	finish := "STOP"
	c, info, resp := geminiEOFCase(t, []dto.GeminiChatResponse{{
		Candidates: []dto.GeminiChatCandidate{{
			FinishReason: &finish,
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
	}})

	_, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.Nil(t, newAPIError)
	assert.True(t, info.StreamStatus.IsNormalEnd())
}

// TestGeminiStreamTruncationStillDetected 反向守卫：真正的断流必须仍然被抓到。
//
// 上面两条把「正常结束」放宽了，如果这个放宽过头，中途截断的流就会被当成
// 正常结束 —— 客户端拿到一段没有收尾的内容却收到 200，断流问题原地复发。
// 这条确保修复是「补齐清单」而不是「取消判定」。
func TestGeminiStreamTruncationStillDetected(t *testing.T) {
	// 只有中间内容块，没有任何终止标记，也没有 [DONE]
	c, info, resp := geminiEOFCase(t, []dto.GeminiChatResponse{{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "半句话"}},
			},
		}},
	}})

	_, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.NotNil(t, newAPIError,
		"没有终止标记的截断必须仍然报错 —— 放宽终止标记清单不能把断流一起放过")
	assert.False(t, info.StreamStatus.IsNormalEnd())
}

// TestGeminiStreamFinishReasonUnspecifiedIsNormalEnd 末块显式带
// FINISH_REASON_UNSPECIFIED 也是完整响应。
//
// 该值是 proto 的零值：上游只在**收尾那一块**填 finishReason，中间块根本不
// 带这个字段（解出来是 nil）。所以「显式写了 UNSPECIFIED」本身就说明这是
// 收尾块，把它排除在终止标记之外等于让这类正常响应也变成断流。
func TestGeminiStreamFinishReasonUnspecifiedIsNormalEnd(t *testing.T) {
	finish := "FINISH_REASON_UNSPECIFIED"
	c, info, resp := geminiEOFCase(t, []dto.GeminiChatResponse{{
		Candidates: []dto.GeminiChatCandidate{{
			FinishReason: &finish,
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hi"}},
			},
		}},
	}})

	_, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.Nil(t, newAPIError,
		"显式给出 finishReason 即表示这是收尾块；UNSPECIFIED 是零值而非「还没结束」")
	assert.True(t, info.StreamStatus.IsNormalEnd())
}
