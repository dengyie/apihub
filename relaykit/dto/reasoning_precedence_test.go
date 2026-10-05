package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An upstream that populates both reasoning fields but leaves reasoning_content
// empty must not have its reasoning silently dropped: GetReasoningContent
// returning "" makes the converter treat the chunk as plain text, so the block
// type flips and every word starts its own thinking block.
func TestGetReasoningContentPrefersPopulatedField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"only reasoning_content", `{"reasoning_content":"a"}`, "a"},
		{"only reasoning", `{"reasoning":"a"}`, "a"},
		{"both populated", `{"reasoning_content":"a","reasoning":"a"}`, "a"},
		{"empty reasoning_content with reasoning", `{"reasoning_content":"","reasoning":"a"}`, "a"},
		{"neither", `{}`, ""},
		{"both empty", `{"reasoning_content":"","reasoning":""}`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delta ChatCompletionsStreamResponseChoiceDelta
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &delta))
			assert.Equal(t, tc.want, delta.GetReasoningContent())
		})
	}
}
