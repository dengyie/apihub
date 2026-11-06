package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EnsureInputItemIDs 修复无 id 的 function_call / reasoning input item：
// 严格 serde 上游（如 zmx.xyz.hr）对不带 id 的这类 item 反序列化直接失败
// （400 json_parse_error "data did not match any variant of untagged enum ResponseInput"）。
func TestEnsureInputItemIDs(t *testing.T) {
	t.Run("fills missing id on function_call items", func(t *testing.T) {
		req := &OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"message","role":"user","content":"hi"},
				{"type":"function_call","call_id":"call_2","name":"run","arguments":"{}"}
			]`),
		}
		changed := req.EnsureInputItemIDs()
		require.True(t, changed)

		var items []map[string]any
		require.NoError(t, json.Unmarshal(req.Input, &items))
		assert.Equal(t, "fc_call_2", items[1]["id"])
		// 原 parsing 错列位在最后 item 之后 —— 确认只有目标 item 被修改
		assert.NotContains(t, items[0], "id")
	})

	t.Run("does not touch existing id on function_call items", func(t *testing.T) {
		req := &OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"function_call","id":"fc_orig","call_id":"call_a","name":"run","arguments":"{}"}
			]`),
		}
		changed := req.EnsureInputItemIDs()
		assert.False(t, changed)
		var items []map[string]any
		require.NoError(t, json.Unmarshal(req.Input, &items))
		assert.Equal(t, "fc_orig", items[0]["id"])
	})

	t.Run("fills reasoning items deterministically by position", func(t *testing.T) {
		req := &OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"reasoning","summary":[]},
				{"type":"message","role":"user","content":"next"},
				{"type":"reasoning","summary":[]}
			]`),
		}
		changed := req.EnsureInputItemIDs()
		require.True(t, changed)
		var items []map[string]any
		require.NoError(t, json.Unmarshal(req.Input, &items))
		assert.Equal(t, "rs_input_0", items[0]["id"])
		assert.Equal(t, "rs_input_2", items[2]["id"])
	})

	t.Run("fill is idempotent", func(t *testing.T) {
		req := &OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"function_call","call_id":"call_2","name":"run","arguments":"{}"}
			]`),
		}
		require.True(t, req.EnsureInputItemIDs())
		first := string(req.Input)
		require.False(t, req.EnsureInputItemIDs())
		assert.Equal(t, first, string(req.Input))
	})

	t.Run("no-op when input is a string", func(t *testing.T) {
		req := &OpenAIResponsesRequest{Input: json.RawMessage(`"say hi"`)}
		assert.False(t, req.EnsureInputItemIDs())
		assert.Equal(t, `"say hi"`, string(req.Input))
	})

	t.Run("no-op when input is empty", func(t *testing.T) {
		req := &OpenAIResponsesRequest{}
		assert.False(t, req.EnsureInputItemIDs())
	})

	t.Run("function_call without call_id falls back to positional id", func(t *testing.T) {
		req := &OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"function_call","name":"run","arguments":"{}"}
			]`),
		}
		changed := req.EnsureInputItemIDs()
		require.True(t, changed)
		var items []map[string]any
		require.NoError(t, json.Unmarshal(req.Input, &items))
		assert.Equal(t, "fc_input_0", items[0]["id"])
	})
}
