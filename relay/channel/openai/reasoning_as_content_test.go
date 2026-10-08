package openai

import (
	"strings"
	"testing"

	"github.com/dengyie/apihub/relaykit/dto"
)

func p(s string) *string { return &s }

// The reported production shape: the whole answer arrives in reasoning and
// content never appears, which renders one thinking block per token.
func TestReasoningAsContentClaude(t *testing.T) {
	responses := []*dto.ClaudeResponse{
		{Type: "content_block_start", ContentBlock: &dto.ClaudeMediaMessage{
			Type: "thinking", Thinking: p("")}},
		{Type: "content_block_delta", Delta: &dto.ClaudeMediaMessage{
			Type: "thinking_delta", Thinking: p("让我")}},
		{Type: "content_block_delta", Delta: &dto.ClaudeMediaMessage{
			Type: "thinking_delta", Thinking: p("先设计")}},
		{Type: "content_block_stop"},
	}
	got := reasoningAsContentClaude(responses)

	if len(got) != 4 {
		t.Fatalf("length changed: %d", len(got))
	}
	if cb := got[0].ContentBlock; cb == nil || cb.Type != "text" {
		t.Fatalf("block start not converted: %+v", got[0].ContentBlock)
	} else if cb.Thinking != nil {
		t.Error("thinking payload left on the block start")
	}
	for i := 1; i <= 2; i++ {
		d := got[i].Delta
		if d.Type != "text_delta" {
			t.Fatalf("delta %d type = %q, want text_delta", i, d.Type)
		}
		if d.Thinking != nil {
			t.Errorf("delta %d still carries thinking", i)
		}
	}
	if got[1].Delta.Text == nil || *got[1].Delta.Text != "让我" {
		t.Errorf("thinking text lost: %+v", got[1].Delta)
	}
	if got[3].Type != "content_block_stop" {
		t.Errorf("stop event changed: %q", got[3].Type)
	}
}

// Thinking followed by real content must keep block indices intact; it becomes
// two text blocks rather than one merged or one dropped.
func TestReasoningAsContentClaudeKeepsIndices(t *testing.T) {
	i0, i1 := 0, 1
	responses := []*dto.ClaudeResponse{
		{Index: &i0, Type: "content_block_start", ContentBlock: &dto.ClaudeMediaMessage{
			Type: "thinking", Thinking: p("")}},
		{Index: &i0, Type: "content_block_delta", Delta: &dto.ClaudeMediaMessage{
			Type: "thinking_delta", Thinking: p("思考")}},
		{Index: &i0, Type: "content_block_stop"},
		{Index: &i1, Type: "content_block_start", ContentBlock: &dto.ClaudeMediaMessage{
			Type: "text", Text: p("")}},
		{Index: &i1, Type: "content_block_delta", Delta: &dto.ClaudeMediaMessage{
			Type: "text_delta", Text: p("答案")}},
	}
	got := reasoningAsContentClaude(responses)
	for _, r := range got {
		if r.Index == nil {
			t.Fatalf("index dropped on %q", r.Type)
		}
	}
	if got[1].Index != &i0 {
		t.Error("thinking block index changed")
	}
	if got[4].Delta.Text == nil || *got[4].Delta.Text != "答案" {
		t.Errorf("real content damaged: %+v", got[4].Delta)
	}
}

func TestReasoningAsContentClaudeEmpty(t *testing.T) {
	if got := reasoningAsContentClaude(nil); got != nil {
		t.Error("nil should stay nil")
	}
}

func TestReasoningAsContentFrame(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantHas []string
		wantNot []string
	}{
		{
			name:    "reasoning becomes content",
			in:      `{"choices":[{"index":0,"delta":{"reasoning_content":"让我"}}]}`,
			wantHas: []string{`"content":"让我"`},
			wantNot: []string{"reasoning_content"},
		},
		{
			name:    "alternate reasoning field also moves",
			in:      `{"choices":[{"index":0,"delta":{"reasoning":"a"}}]}`,
			wantHas: []string{`"content":"a"`},
			wantNot: []string{`"reasoning"`},
		},
		{
			name:    "empty content key is replaced not duplicated",
			in:      `{"choices":[{"index":0,"delta":{"content":"","reasoning_content":"a"}}]}`,
			wantHas: []string{`"content":"a"`},
			wantNot: []string{`"content":""`, "reasoning_content"},
		},
		{
			name:    "real content wins and reasoning is dropped",
			in:      `{"choices":[{"index":0,"delta":{"content":"答案","reasoning_content":"思考"}}]}`,
			wantHas: []string{`"content":"答案"`},
			wantNot: []string{"reasoning_content", "思考"},
		},
		{
			name:    "content only frame untouched",
			in:      `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantHas: []string{`"content":"hi"`},
		},
		// 下面三条钉住「改写后 delta 里一个 reasoning 键都不剩」这条不变式。
		// 上游可能同时下发 reasoning_content 与 reasoning（v29.30 的
		// GetReasoningContent 正是为这个形态修的），而实现只处理其中**最后一个**
		// 命中键，另一个原样留在帧里：非空时答案被投递两遍，空串时客户端仍会
		// 按「字段存在」开出一个空思考块 —— 开关在这些上游上等于没开。
		{
			name:    "both reasoning keys merge into one content",
			in:      `{"choices":[{"index":0,"delta":{"reasoning_content":"a","reasoning":"b"}}]}`,
			wantHas: []string{`"content":"ab"`},
			wantNot: []string{"reasoning_content", `"reasoning"`},
		},
		{
			name:    "both reasoning keys dropped when content wins",
			in:      `{"choices":[{"index":0,"delta":{"reasoning_content":"a","reasoning":"b","content":"c"}}]}`,
			wantHas: []string{`"content":"c"`},
			wantNot: []string{"reasoning_content", `"reasoning"`},
		},
		{
			name:    "empty placeholder reasoning key is removed too",
			in:      `{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"","reasoning":"a"}}]}`,
			wantHas: []string{`"role":"assistant"`, `"content":"a"`},
			wantNot: []string{"reasoning_content", `"reasoning"`},
		},
		{
			name:    "role opening frame untouched",
			in:      `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":""}}]}`,
			wantHas: []string{`"role":"assistant"`},
			wantNot: []string{`"content":"让我"`},
		},
		{
			name:    "malformed json returned unchanged",
			in:      `{"choices":[{"delta":`,
			wantHas: []string{`{"choices":[{"delta":`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reasoningAsContentFrame(tc.in)
			for _, w := range tc.wantHas {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %s in %s", w, got)
				}
			}
		})
	}
}

// The exact production frame, including fields we do not model.
func TestReasoningAsContentFrameProduction(t *testing.T) {
	in := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"","reasoning_content":" 需要","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"finish_reason":null,"logprobs":null}]}`
	got := reasoningAsContentFrame(in)
	if !strings.Contains(got, `"content":" 需要"`) {
		t.Fatalf("reasoning not moved to content: %s", got)
	}
	if strings.Contains(got, "reasoning_content") {
		t.Fatalf("reasoning field survived: %s", got)
	}
	for _, keep := range []string{`"function_call":null`, `"refusal":""`, `"tool_calls":[]`, `"extra_fields":null`, `"logprobs":null`, `"finish_reason":null`} {
		if !strings.Contains(got, keep) {
			t.Errorf("upstream field %s dropped: %s", keep, got)
		}
	}
}

func TestApplyReasoningAsContentToDTO(t *testing.T) {
	resp := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: p("思考")}},
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: p("答案"), ReasoningContent: p("思考")}},
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: p("纯正文")}},
		},
	}
	applyReasoningAsContentToDTO(resp)

	if got := resp.Choices[0].Delta.GetContentString(); got != "思考" {
		t.Errorf("choice 0 content = %q, want 思考", got)
	}
	if resp.Choices[0].Delta.ReasoningContent != nil {
		t.Error("choice 0 reasoning not cleared")
	}
	if got := resp.Choices[1].Delta.GetContentString(); got != "答案" {
		t.Errorf("choice 1 content = %q, want 答案 (real content must win)", got)
	}
	if got := resp.Choices[2].Delta.GetContentString(); got != "纯正文" {
		t.Errorf("choice 2 damaged: %q", got)
	}
}

// Default off: with the flag unset nothing changes.
func TestReasoningAsContentDefaultsOff(t *testing.T) {
	var s dto.ChannelSettings
	if s.ReasoningAsContent {
		t.Fatal("ReasoningAsContent must default to false")
	}
}
