package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractArrayContent(t *testing.T) {
	cases := []struct {
		name          string
		input         string
		wantReasoning string
		wantContent   string
	}{
		{
			name:          "nested thinking text array from incident",
			input:         `[{"type":"thinking","thinking":[{"type":"text","text":"All"}],"closed":true}]`,
			wantReasoning: "All",
			wantContent:   "",
		},
		{
			name:          "simple string thinking",
			input:         `[{"type":"thinking","thinking":"let me think"}]`,
			wantReasoning: "let me think",
			wantContent:   "",
		},
		{
			name:          "thinking with text field",
			input:         `[{"type":"thinking","text":"deep thoughts"}]`,
			wantReasoning: "deep thoughts",
			wantContent:   "",
		},
		{
			name:          "standard text block",
			input:         `[{"type":"text","text":"hello world"}]`,
			wantReasoning: "",
			wantContent:   "hello world",
		},
		{
			name:          "mixed thinking and text blocks",
			input:         `[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"result"}]`,
			wantReasoning: "pondering",
			wantContent:   "result",
		},
		{
			name:          "plain string array",
			input:         `["part 1 ", "part 2"]`,
			wantReasoning: "",
			wantContent:   "part 1 part 2",
		},
		{
			name:          "empty array",
			input:         `[]`,
			wantReasoning: "",
			wantContent:   "",
		},
		{
			name:          "not an array",
			input:         `"plain string"`,
			wantReasoning: "",
			wantContent:   "plain string",
		},
		{
			name:          "nested thinking with content field",
			input:         `[{"type":"thinking","content":[{"type":"text","text":"nested content"}]}]`,
			wantReasoning: "nested content",
			wantContent:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotR, gotC := ExtractArrayContent([]byte(tc.input))
			assert.Equal(t, tc.wantReasoning, gotR)
			assert.Equal(t, tc.wantContent, gotC)
		})
	}
}

func TestChatCompletionsStreamResponseChoiceDelta_UnmarshalJSON(t *testing.T) {
	t.Run("standard string content", func(t *testing.T) {
		raw := `{"content":"hello","role":"assistant"}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Equal(t, "hello", delta.GetContentString())
		assert.Equal(t, "assistant", delta.Role)
		assert.Equal(t, "", delta.GetReasoningContent())
	})

	t.Run("incident array thinking content", func(t *testing.T) {
		raw := `{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"All"}],"closed":true}]}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Equal(t, "All", delta.GetReasoningContent())
		assert.Equal(t, "", delta.GetContentString())
	})

	t.Run("null content", func(t *testing.T) {
		raw := `{"content":null,"reasoning_content":"reason"}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Nil(t, delta.Content)
		assert.Equal(t, "reason", delta.GetReasoningContent())
	})

	// content 数组提取的 thinking 与显式 reasoning_content 字符串同时出现时，
	// 两种 JSON 字段顺序必须得到完全相同的结果（追加合并，不允许按先到覆盖丢弃）。
	t.Run("array content and explicit reasoning merge order-independently", func(t *testing.T) {
		explicitFirst := `{"reasoning_content":"explicit ","content":[{"type":"thinking","thinking":"array "}]}`
		arrayFirst := `{"content":[{"type":"thinking","thinking":"array "}],"reasoning_content":"explicit "}`
		var a, b ChatCompletionsStreamResponseChoiceDelta
		require.NoError(t, json.Unmarshal([]byte(explicitFirst), &a))
		require.NoError(t, json.Unmarshal([]byte(arrayFirst), &b))
		assert.Equal(t, a.GetReasoningContent(), b.GetReasoningContent(), "字段顺序不得影响合并结果")
		assert.Equal(t, "array explicit ", a.GetReasoningContent())
		assert.Equal(t, "", a.GetContentString(), "纯 thinking 数组不得产生 content")
	})

	t.Run("array text content merges with explicit content string", func(t *testing.T) {
		raw := `{"content":[{"type":"text","text":"from array"}],"reasoning_content":"think"}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Equal(t, "from array", delta.GetContentString())
		assert.Equal(t, "think", delta.GetReasoningContent())
	})

	// reasoning 字段数组里 thinking 与 text 块混排时，两者都必须保留（不因 thinking 非空丢 text）。
	t.Run("mixed blocks in reasoning array are both preserved", func(t *testing.T) {
		raw := `{"reasoning":[{"type":"thinking","thinking":"a"},{"type":"text","text":"b"}]}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Equal(t, "ab", delta.GetReasoningContent(), "GetReasoningContent 应回退合并 reasoning 字段")
	})

	t.Run("mixed blocks in reasoning_content array are both preserved", func(t *testing.T) {
		raw := `{"reasoning_content":[{"type":"thinking","thinking":"a"},{"type":"text","text":"b"}]}`
		var delta ChatCompletionsStreamResponseChoiceDelta
		err := json.Unmarshal([]byte(raw), &delta)
		require.NoError(t, err)
		assert.Equal(t, "ab", delta.GetReasoningContent())
	})

	t.Run("stream response with error object", func(t *testing.T) {
		raw := `{"id":"1","error":{"message":"rate limit","type":"requests","code":"rate_limit_exceeded"}}`
		var resp ChatCompletionsStreamResponse
		err := json.Unmarshal([]byte(raw), &resp)
		require.NoError(t, err)
		oaiErr := resp.GetOpenAIError()
		require.NotNil(t, oaiErr)
		assert.Equal(t, "rate limit", oaiErr.Message)
		assert.Equal(t, "requests", oaiErr.Type)
	})

	t.Run("stream response with error object message only", func(t *testing.T) {
		raw := `{"id":"1","error":{"message":"upstream balance insufficient"}}`
		var resp ChatCompletionsStreamResponse
		err := json.Unmarshal([]byte(raw), &resp)
		require.NoError(t, err)
		oaiErr := resp.GetOpenAIError()
		require.NotNil(t, oaiErr)
		assert.Equal(t, "upstream balance insufficient", oaiErr.Message)
		assert.Equal(t, "", oaiErr.Type)
	})
}

func TestChatCompletionsStreamResponse_MarshalJSON(t *testing.T) {
	t.Run("nil choices serializes to empty array", func(t *testing.T) {
		var resp ChatCompletionsStreamResponse
		resp.Id = "chatcmpl-test"
		resp.Model = "test-model"

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		jsonStr := string(data)
		assert.Contains(t, jsonStr, `"choices":[]`)
		assert.NotContains(t, jsonStr, `"choices":null`)
	})

	t.Run("zero-valued struct serializes choices as empty array", func(t *testing.T) {
		var resp ChatCompletionsStreamResponse
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		jsonStr := string(data)
		assert.Contains(t, jsonStr, `"choices":[]`)
		assert.NotContains(t, jsonStr, `"choices":null`)
	})

	t.Run("non-empty choices serializes normally", func(t *testing.T) {
		resp := ChatCompletionsStreamResponse{
			Id: "test",
			Choices: []ChatCompletionsStreamResponseChoice{
				{
					Index: 0,
					Delta: ChatCompletionsStreamResponseChoiceDelta{
						Role: "assistant",
					},
				},
			},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		jsonStr := string(data)
		assert.Contains(t, jsonStr, `"role":"assistant"`)
	})
}
