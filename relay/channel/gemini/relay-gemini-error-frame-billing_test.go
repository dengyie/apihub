package gemini

import (
	"bytes"
	"io"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiNoContentFrameIsNotBilled 上游只吐了一个空帧就断了，**不能计费**。
//
// 错误/空响应同样以数据帧送达，ReceivedResponseCount == 1，但客户端没有任何产出。
// 空产出会被 ResponseText2Usage 按 GetEstimatePromptTokens() 估成整段 prompt，
// 于是这次失败的请求反过来收用户一遍全款。
func TestGeminiNoContentFrameIsNotBilled(t *testing.T) {
	// 不传任何块：helper 生成的 body 是空的，这里只求拿到 c/info/resp 三件套，
	// 随后把 body 换成「一个零内容帧」。
	c, info, resp := geminiEOFCase(t, nil)
	info.SetEstimatePromptTokens(8000)

	frame, err := common.Marshal(dto.GeminiChatResponse{})
	require.NoError(t, err)
	var body bytes.Buffer
	body.WriteString("data: " + string(frame) + "\n")
	resp.Body = io.NopCloser(bytes.NewReader(body.Bytes()))

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})

	require.NotNil(t, newAPIError, "没有终止标记的零内容流应当判为断流")
	assert.Nil(t, usage,
		"只收到一个零内容帧、没有任何产出，不得按整段 prompt 计费")
}
