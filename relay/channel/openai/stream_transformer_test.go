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

	t.Run("transforms reasoning as content for glm-5.3-flash", func(t *testing.T) {
		input := `{"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`
		info := &relaycommon.RelayInfo{
			OriginModelName: "glm-5.3-flash",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		assert.Contains(t, out, `"content":"thinking..."`)
		assert.NotContains(t, out, `"reasoning_content"`)
	})

	t.Run("exempts native reasoning model deepseek-v4-pro even if ReasoningAsContent enabled", func(t *testing.T) {
		input := `{"choices":[{"delta":{"content":"","reasoning_content":"step 1 thinking"}}]}`
		info := &relaycommon.RelayInfo{
			OriginModelName: "deepseek-v4-pro",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		// reasoning_content 必须保留，空 content 必须被清洗，严禁压扁进正文
		assert.Contains(t, out, `"reasoning_content":"step 1 thinking"`)
		assert.NotContains(t, out, `"content":""`)
		assert.NotContains(t, out, `"content":"step 1 thinking"`)
	})

	t.Run("exempts upstream mapped native reasoning model", func(t *testing.T) {
		input := `{"choices":[{"delta":{"reasoning_content":"deep thought"}}]}`
		info := &relaycommon.RelayInfo{
			OriginModelName: "custom-alias",
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-r1",
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		out := TransformPassthroughStreamFrame(info, input)
		assert.Contains(t, out, `"reasoning_content":"deep thought"`)
		assert.NotContains(t, out, `"content":"deep thought"`)
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

func TestShouldApplyReasoningAsContent(t *testing.T) {
	t.Run("nil info returns false", func(t *testing.T) {
		assert.False(t, ShouldApplyReasoningAsContent(nil))
	})

	t.Run("nil ChannelMeta returns false", func(t *testing.T) {
		info := &relaycommon.RelayInfo{}
		assert.False(t, ShouldApplyReasoningAsContent(info))
	})

	t.Run("disabled setting returns false", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			OriginModelName: "glm-5.3-flash",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: false,
				},
			},
		}
		assert.False(t, ShouldApplyReasoningAsContent(info))
	})

	t.Run("GLM family applies reasoning as content", func(t *testing.T) {
		models := []string{"glm-5.3-flash", "glm-5.2", "glm-5.4-flash", "glm-4-flash"}
		for _, m := range models {
			info := &relaycommon.RelayInfo{
				OriginModelName: m,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelSetting: dto.ChannelSettings{
						ReasoningAsContent: true,
					},
				},
			}
			assert.True(t, ShouldApplyReasoningAsContent(info), "expected true for model %s", m)
		}
	})

	t.Run("native reasoning models are strictly exempted", func(t *testing.T) {
		exemptModels := []string{
			"deepseek-v4-pro",
			"deepseek-ai/DeepSeek-V4-Pro",
			"deepseek-r1",
			"deepseek-reasoner",
			"kimi-k3",
			"kimi-k2.5-thinking",
				"moonshot-v1-thinking",
				"o1",
				"o1-mini",
				"o3",
				"o3-mini",
				"o4-mini",
				"qwq-32b",
				"qwen-qwq-32b",
				"qwen2.5-qwq-7b",
				"claude-3-7-sonnet-thinking",
				"claude-3-7-sonnet:thinking",
				"gemini-2.5-flash-thinking-preview",
			}
		for _, m := range exemptModels {
			info := &relaycommon.RelayInfo{
				OriginModelName: m,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelSetting: dto.ChannelSettings{
						ReasoningAsContent: true,
					},
				},
			}
			assert.False(t, ShouldApplyReasoningAsContent(info), "model %s must be exempted from reasoning as content rewrite", m)
		}
	})

	t.Run("upstream mapped native reasoning model is exempted", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			OriginModelName: "custom-gateway-model",
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "deepseek-v4-pro",
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		assert.False(t, ShouldApplyReasoningAsContent(info))
	})
}

func TestSendStreamData_ModelAwareGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("GLM model converts reasoning to content in sendStreamData", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		info := &relaycommon.RelayInfo{
			OriginModelName: "glm-5.3-flash",
			RelayFormat:     types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		input := `{"choices":[{"delta":{"reasoning_content":"glm thinking"}}]}`
		err := sendStreamData(c, info, input, false, false)
		require.NoError(t, err)
		assert.Contains(t, w.Body.String(), `"content":"glm thinking"`)
		assert.NotContains(t, w.Body.String(), `"reasoning_content"`)
	})

	t.Run("DeepSeek model preserves reasoning_content in sendStreamData", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		info := &relaycommon.RelayInfo{
			OriginModelName: "deepseek-v4-pro",
			RelayFormat:     types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		input := `{"choices":[{"delta":{"content":"","reasoning_content":"deepseek CoT"}}]}`
		err := sendStreamData(c, info, input, false, false)
		require.NoError(t, err)
		assert.Contains(t, w.Body.String(), `"reasoning_content":"deepseek CoT"`)
		assert.NotContains(t, w.Body.String(), `"content":"deepseek CoT"`)
	})

	t.Run("DeepSeek model in forceFormat mode preserves reasoning_content", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		info := &relaycommon.RelayInfo{
			OriginModelName: "deepseek-v4-pro",
			RelayFormat:     types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		input := `{"id":"chatcmpl-1","choices":[{"delta":{"reasoning_content":"deepseek CoT"}}]}`
		err := sendStreamData(c, info, input, true, false)
		require.NoError(t, err)
		assert.Contains(t, w.Body.String(), `"reasoning_content":"deepseek CoT"`)
		assert.NotContains(t, w.Body.String(), `"content":"deepseek CoT"`)
	})

	t.Run("Non-stream GLM response converts reasoning to content", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			OriginModelName: "glm-5.3-flash",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		reasoning := "glm non-stream thinking"
		simpleResponse := dto.OpenAITextResponse{
			Choices: []dto.OpenAITextResponseChoice{
				{
					Message: dto.Message{
						Role:             "assistant",
						Content:          "",
						ReasoningContent: &reasoning,
					},
				},
			},
		}

		assert.True(t, ShouldApplyReasoningAsContent(info))
		for i := range simpleResponse.Choices {
			msg := &simpleResponse.Choices[i].Message
			r := msg.GetReasoningContent()
			if r != "" {
				msg.ReasoningContent = nil
				msg.Reasoning = nil
				if msg.StringContent() == "" {
					msg.SetStringContent(r)
				}
			}
		}

		assert.Equal(t, "glm non-stream thinking", simpleResponse.Choices[0].Message.StringContent())
		assert.Nil(t, simpleResponse.Choices[0].Message.ReasoningContent)
		assert.Empty(t, simpleResponse.Choices[0].Message.GetReasoningContent())
	})

	t.Run("Non-stream DeepSeek response strictly preserves reasoning_content", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			OriginModelName: "deepseek-v4-pro",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{
					ReasoningAsContent: true,
				},
			},
		}
		reasoning := "deepseek non-stream CoT"
		simpleResponse := dto.OpenAITextResponse{
			Choices: []dto.OpenAITextResponseChoice{
				{
					Message: dto.Message{
						Role:             "assistant",
						Content:          "final answer",
						ReasoningContent: &reasoning,
					},
				},
			},
		}

		assert.False(t, ShouldApplyReasoningAsContent(info))
		assert.Equal(t, "final answer", simpleResponse.Choices[0].Message.StringContent())
		assert.Equal(t, "deepseek non-stream CoT", simpleResponse.Choices[0].Message.GetReasoningContent())
	})
}

