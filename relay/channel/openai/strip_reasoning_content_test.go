package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripEmptyContentOnReasoningFrame(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "real upstream frame loses the empty content field",
			in:   `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":" need","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"finish_reason":null}],"usage":null}`,
			want: `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":" need","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"finish_reason":null}],"usage":null}`,
		},
		{
			name: "alternate reasoning field is recognised too",
			in:   `{"choices":[{"index":0,"delta":{"content":"","reasoning":"a"},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"reasoning":"a"},"finish_reason":null}]}`,
		},
		{
			name: "role opening frame keeps its empty content",
			in:   `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":""},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":""},"finish_reason":null}]}`,
		},
		{
			name: "non-empty content is never touched",
			in:   `{"choices":[{"index":0,"delta":{"content":"answer","reasoning_content":"a"},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"answer","reasoning_content":"a"},"finish_reason":null}]}`,
		},
		{
			name: "reasoning-only frame without a content field is untouched",
			in:   `{"choices":[{"index":0,"delta":{"reasoning_content":"a"},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"reasoning_content":"a"},"finish_reason":null}]}`,
		},
		{
			name: "empty content with no reasoning is untouched",
			in:   `{"choices":[{"index":0,"delta":{"content":"","tool_calls":[]},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"","tool_calls":[]},"finish_reason":null}]}`,
		},
		{
			name: "only the reasoning choice is rewritten",
			in:   `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null},{"index":1,"delta":{"content":"","reasoning_content":"a"},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null},{"index":1,"delta":{"reasoning_content":"a"},"finish_reason":null}]}`,
		},
		{
			name: "null content is not an empty string",
			in:   `{"choices":[{"index":0,"delta":{"content":null,"reasoning_content":"a"},"finish_reason":null}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":null,"reasoning_content":"a"},"finish_reason":null}]}`,
		},
		{
			name: "empty choices array is untouched",
			in:   `{"choices":[]}`,
			want: `{"choices":[]}`,
		},
		{
			name: "delta may be absent",
			in:   `{"choices":[{"index":0,"finish_reason":"stop"}]}`,
			want: `{"choices":[{"index":0,"finish_reason":"stop"}]}`,
		},
		{
			name: "non stream object is untouched",
			in:   `{"id":"c1","object":"chat.completion"}`,
			want: `{"id":"c1","object":"chat.completion"}`,
		},
		{
			name: "malformed json is returned unchanged",
			in:   `{"choices":[{"delta":{"content":"",`,
			want: `{"choices":[{"delta":{"content":"",`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripEmptyContentOnReasoningFrame(tc.in)
			if got != tc.want {
				t.Fatalf("mismatch\n in: %s\nwant: %s\n got: %s", tc.in, tc.want, got)
			}
		})
	}
}

// A rewritten frame must still parse to the same object minus the dropped key.
func TestStripEmptyContentPreservesEverythingElse(t *testing.T) {
	in := `{"id":"c1","model":"m","created":1,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"We","extra_fields":{"a":1},"tool_calls":[{"id":"t","type":"function"}]},"finish_reason":null}],"usage":{"total_tokens":9}}`
	got := stripEmptyContentOnReasoningFrame(in)

	var wantMap, gotMap map[string]any
	if err := json.Unmarshal([]byte(in), &wantMap); err != nil {
		t.Fatalf("input does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}

	wantDelta := wantMap["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	gotDelta := gotMap["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)

	if _, ok := gotDelta["content"]; ok {
		t.Error("content should have been dropped from the reasoning delta")
	}
	if gotDelta["reasoning_content"] != "We" {
		t.Errorf("reasoning_content = %v, want We", gotDelta["reasoning_content"])
	}
	if len(wantDelta) != len(gotDelta)+1 {
		t.Errorf("delta lost more than the content key: want %d keys, got %d", len(wantDelta), len(gotDelta))
	}
	for k, v := range wantDelta {
		if k == "content" {
			continue
		}
		if !jsonEqual(gotDelta[k], v) {
			t.Errorf("field %q changed: want %v, got %v", k, v, gotDelta[k])
		}
	}
}

// Key order must survive, since this runs on a passthrough path.
func TestStripEmptyContentPreservesKeyOrder(t *testing.T) {
	in := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"a"},"finish_reason":null}],"usage":null}`
	got := stripEmptyContentOnReasoningFrame(in)
	for _, want := range []string{`"id"`, `"object"`, `"choices"`, `"usage"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Index(got, `"id"`) > strings.Index(got, `"object"`) {
		t.Errorf("top level key order changed: %s", got)
	}
	if strings.Index(got, `"delta"`) > strings.Index(got, `"finish_reason"`) {
		t.Errorf("choice key order changed: %s", got)
	}
}

// The frame we observed in production must come out with no content field left.
func TestStripEmptyContentOnProductionFrame(t *testing.T) {
	in := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"","reasoning_content":" answer","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"finish_reason":null,"logprobs":null}]}`
	got := stripEmptyContentOnReasoningFrame(in)
	if strings.Contains(got, `"content"`) {
		t.Fatalf("empty content survived: %s", got)
	}
	if !strings.Contains(got, `"reasoning_content":" answer"`) {
		t.Fatalf("reasoning lost: %s", got)
	}
	for _, keep := range []string{`"function_call":null`, `"refusal":""`, `"tool_calls":[]`, `"extra_fields":null`, `"logprobs":null`} {
		if !strings.Contains(got, keep) {
			t.Errorf("upstream field %s was dropped: %s", keep, got)
		}
	}
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
