package openai

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformPassthroughStreamFrame(t *testing.T) {
	t.Run("empty string returns empty", func(t *testing.T) {
		assert.Equal(t, "", TransformPassthroughStreamFrame(nil, ""))
	})

	t.Run("strips empty content on reasoning frame by default", func(t *testing.T) {
		input := `{"choices":[{"delta":{"content":"","reasoning_content":"hello"}}]}`
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		assert.NotContains(t, out, `"content":""`)
		assert.Contains(t, out, `"reasoning_content":"hello"`)
	})

	t.Run("transforms reasoning as content when enabled", func(t *testing.T) {
		input := `{"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		assert.Contains(t, out, `"content":"thinking..."`)
	})

	t.Run("splits think tags into reasoning when ThinkTagToReasoning enabled", func(t *testing.T) {
		input := `{"choices":[{"delta":{"content":"<think>deep thought</think>answer"}}]}`
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ThinkTagToReasoning: true,
				},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		assert.Contains(t, out, `"reasoning_content":"deep thought"`)
		assert.Contains(t, out, `"content":"answer"`)
	})
}

func TestSendStreamData_FastPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{
				ReasoningAsContent: true,
			},
		},
	}
	input := `{"choices":[{"delta":{"reasoning_content":"hello world"}}]}`
	err := sendStreamData(c, info, input, false, false)
	require.NoError(t, err)

	body := w.Body.String()
	assert.Contains(t, body, "data: ")
	assert.Contains(t, body, `"content":"hello world"`)
}

