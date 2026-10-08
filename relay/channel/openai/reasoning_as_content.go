package openai

import (
	"encoding/json"

	"github.com/dengyie/apihub/relaykit/dto"
)

// reasoningAsContentClaude rewrites thinking blocks into text blocks.
//
// Some upstreams put the entire answer in reasoning_content and never emit
// content, so a client renders one collapsible thinking block per token and
// never receives an answer body. Because the frames are already on the wire by
// the time that becomes visible, the repair has to happen while the block is
// still being built, which is what this does.
//
// The rewrite is one-to-one and keeps every block index, so an interleaved
// stream that carries real thinking followed by real content stays valid: it
// simply becomes two consecutive text blocks.
func reasoningAsContentClaude(responses []*dto.ClaudeResponse) []*dto.ClaudeResponse {
	if len(responses) == 0 {
		return responses
	}
	converted := make([]*dto.ClaudeResponse, 0, len(responses))
	for _, resp := range responses {
		if resp == nil {
			continue
		}
		switch resp.Type {
		case "content_block_start":
			if cb := resp.ContentBlock; cb != nil && cb.Type == "thinking" {
				text := ""
				if cb.Thinking != nil {
					text = *cb.Thinking
				}
				cb.Type = "text"
				cb.Text = &text
				cb.Thinking = nil
			}
		case "content_block_delta":
			if d := resp.Delta; d != nil && d.Type == "thinking_delta" {
				text := ""
				if d.Thinking != nil {
					text = *d.Thinking
				}
				d.Type = "text_delta"
				d.Text = &text
				d.Thinking = nil
			}
		}
		converted = append(converted, resp)
	}
	return converted
}

// reasoningAsContentFrame moves reasoning_content into content on a raw
// chat-completions frame, for the same reason as reasoningAsContentClaude.
// The frame is rewritten in place order-preservingly so passthrough fidelity
// for every other field is preserved.
func reasoningAsContentFrame(data string) string {
	var top orderedMembers
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		return data
	}
	changed := false
	for i := range top {
		if top[i].key != "choices" {
			continue
		}
		var choices []json.RawMessage
		if err := json.Unmarshal(top[i].raw, &choices); err != nil {
			continue
		}
		choicesChanged := false
		for j := range choices {
			var choice orderedMembers
			if err := json.Unmarshal(choices[j], &choice); err != nil {
				continue
			}
			choiceChanged := false
			for k := range choice {
				if choice[k].key != "delta" {
					continue
				}
				var delta orderedMembers
				if err := json.Unmarshal(choice[k].raw, &delta); err != nil {
					continue
				}
				if moveReasoningToContent(&delta) {
					choice[k].raw = delta.encode()
					choiceChanged = true
				}
			}
			if !choiceChanged {
				continue
			}
			choices[j] = choice.encode()
			choicesChanged = true
		}
		if !choicesChanged {
			continue
		}
		raw, err := json.Marshal(choices)
		if err != nil {
			continue
		}
		top[i].raw = raw
		changed = true
	}
	if !changed {
		return data
	}
	return string(top.encode())
}

// isReasoningMemberKey 判断一个 delta 成员是否承载思考内容。
func isReasoningMemberKey(key string) bool {
	return key == "reasoning_content" || key == "reasoning"
}

// moveReasoningToContent rewrites one delta so its reasoning text is delivered
// as content. Reasoning that already arrived as content is left alone, and a
// frame carrying both keeps the content and drops the duplicate reasoning.
//
// 不变式：**改写后 delta 里一个 reasoning 键都不剩。** 两个键都得处理——
// 只处理其中一个会留下两种看得见的故障：
//   - 两个都非空：残留那个让答案既作思考块又作正文，输出两遍；
//   - 另一个是空串占位：客户端按「字段存在」而非「取值非空」判断思考块开始，
//     于是每帧都开一个空思考块——正是 strip_reasoning_content.go 要消灭的
//     「一个词一个块」症状。
//
// 取值仍然累加全部非空 reasoning，与原先跨多键拼接的语义一致。
func moveReasoningToContent(delta *orderedMembers) bool {
	reasoning := ""
	for _, m := range *delta {
		if !isReasoningMemberKey(m.key) {
			continue
		}
		if s, isString := jsonStringValue(m.raw); isString && s != "" {
			reasoning += s
		}
	}
	if reasoning == "" {
		return false
	}
	hasContent := false
	for _, m := range *delta {
		if m.key != "content" {
			continue
		}
		if s, isString := jsonStringValue(m.raw); isString && s != "" {
			hasContent = true
		}
	}
	if hasContent {
		// Both present: content is authoritative, drop every reasoning copy so
		// the answer is not duplicated.
		out := (*delta)[:0]
		for _, m := range *delta {
			if isReasoningMemberKey(m.key) {
				continue
			}
			out = append(out, m)
		}
		*delta = out
		return true
	}
	quoted, err := json.Marshal(reasoning)
	if err != nil {
		return false
	}
	out := make(orderedMembers, 0, len(*delta))
	replaced := false
	for _, m := range *delta {
		switch {
		case isReasoningMemberKey(m.key):
			// 第一个 reasoning 键就地转成 content，其余 reasoning 键直接丢弃。
			if !replaced {
				out = append(out, kvPair{key: "content", raw: quoted})
				replaced = true
			}
		case m.key == "content":
			if !replaced {
				out = append(out, kvPair{key: "content", raw: quoted})
				replaced = true
			}
		default:
			out = append(out, m)
		}
	}
	if !replaced {
		out = append(out, kvPair{key: "content", raw: quoted})
	}
	*delta = out
	return true
}

// applyReasoningAsContentToDTO is the force_format counterpart of
// reasoningAsContentFrame: that path has already parsed the frame into the
// DTO, so the move happens on the struct rather than on raw bytes.
func applyReasoningAsContentToDTO(resp *dto.ChatCompletionsStreamResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		delta := &resp.Choices[i].Delta
		reasoning := delta.GetReasoningContent()
		if reasoning == "" {
			continue
		}
		delta.ReasoningContent = nil
		delta.Reasoning = nil
		if delta.GetContentString() == "" {
			delta.SetContentString(reasoning)
		}
	}
}
