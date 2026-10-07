package oaichat

import (
	"testing"

	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A chunk carrying reasoning AND content must not lose the content: the
// converter's reasoning branch wins and returns only a thinking_delta, so the
// answer text vanishes from the stream.
func TestChunkWithBothReasoningAndContentKeepsContent(t *testing.T) {
	t.Parallel()

	content := "answer"
	reasoning := "thinking"
	info := &convmeta.Values{}

	responses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id: "id",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
				Content:          &content,
				ReasoningContent: &reasoning,
			},
		}},
	}, info)

	var sawThinking, sawText bool
	for _, r := range responses {
		if r.Delta == nil {
			continue
		}
		if r.Delta.Type == "thinking_delta" {
			sawThinking = true
		}
		if r.Delta.Type == "text_delta" && r.Delta.Text != nil && *r.Delta.Text == content {
			sawText = true
		}
	}
	assert.True(t, sawThinking, "reasoning must still be delivered")
	assert.True(t, sawText, "content delivered in the same chunk must not be dropped")
}

// Upstreams that populate both reasoning fields but leave reasoning_content
// empty must not lose the reasoning word; otherwise each word that arrives in
// the alternate field is treated as non-reasoning and starts its own block.
func TestReasoningInAlternateFieldDoesNotSplitBlocks(t *testing.T) {
	t.Parallel()

	info := &convmeta.Values{}
	var starts int
	for _, word := range []string{"do", "a", "sustained"} {
		empty := ""
		full := word
		responses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
			Id: "id",
			Choices: []dto.ChatCompletionsStreamResponseChoice{{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ReasoningContent: &empty,
					Reasoning:        &full,
				},
			}},
		}, info)
		for _, r := range responses {
			if r.Type == "content_block_start" && r.ContentBlock != nil && r.ContentBlock.Type == "thinking" {
				starts++
			}
		}
	}
	require.Equal(t, 1, starts, "continuous reasoning must stay in a single thinking block")
}
