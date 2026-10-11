package openai

import (
	"encoding/json"
	"strings"

	relaycommon "github.com/dengyie/apihub/relay/common"
)

// think_tag_to_reasoning：把 content 流里的字面 `<think>...</think>` 段拆进
// reasoning_content。这是「一个词一个思考块」故障家族的第三变体——部分上游
// （glm-5.3-flash 的 freeapi #262 / cpa-translate #62 类渠道）不用
// reasoning_content 字段，而是把思考用 think 标签包在 content 里逐词下发；
// 标签感知的客户端会把每个 content 增量渲染成独立的思考块。
// v29.30 修的是 reasoning 帧上的空 content 字段，v29.31 修的是答案整体进了
// reasoning 字段，本文件处理剩下的这一种。
//
// 与 reasoning_as_content 同一条设计约束：流式输出发出去就收不回来，自动检测
// 会误伤「正文里合法讨论 <think> 标签」的内容，所以只能按渠道显式开启
// （ChannelSettings.ThinkTagToReasoning，默认关）。
//
// 实现是跨帧状态机（relaycommon.ThinkTagSplitState）：`<think>` / `</think>`
// 可能被上游切在两帧之间，carry 保存可能是标签前缀的尾巴，finish_reason 帧
// 冲刷。帧内其它字段按原始字节回写（复用 strip_reasoning_content.go 的
// orderedMembers），上游有而我们没建模的字段全部存活。

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// longestTagPrefixLen 返回 s 末尾与 tag 前缀重合的最长长度（不含完整 tag）。
func longestTagPrefixLen(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if s[len(s)-k:] == tag[:k] {
			return k
		}
	}
	return 0
}

// feedThinkSplit 把一段 content 文本喂过标签状态机，返回（拆出的 reasoning，
// 剩余 content）。可能被标签切断的尾巴存进 state.Carry，不猜测、不扣字。
func feedThinkSplit(state *relaycommon.ThinkTagSplitState, text string) (reasoning, remaining string) {
	buf := state.Carry + text
	state.Carry = ""
	var r, c strings.Builder
	for buf != "" {
		switch state.Phase {
		case 0: // 等 <think>：标签前的正文仍是 content
			i := strings.Index(buf, thinkOpenTag)
			if i < 0 {
				keep := longestTagPrefixLen(buf, thinkOpenTag)
				c.WriteString(buf[:len(buf)-keep])
				state.Carry = buf[len(buf)-keep:]
				return r.String(), c.String()
			}
			c.WriteString(buf[:i])
			buf = buf[i+len(thinkOpenTag):]
			state.Phase = 1
		case 1: // 思考段内：内容归 reasoning，等 </think>
			i := strings.Index(buf, thinkCloseTag)
			if i < 0 {
				keep := longestTagPrefixLen(buf, thinkCloseTag)
				r.WriteString(buf[:len(buf)-keep])
				state.Carry = buf[len(buf)-keep:]
				return r.String(), c.String()
			}
			r.WriteString(buf[:i])
			buf = buf[i+len(thinkCloseTag):]
			state.Phase = 2
		default: // 思考结束：全部是正文
			c.WriteString(buf)
			return r.String(), c.String()
		}
	}
	return r.String(), c.String()
}

// applyThinkTagSplit 处理一个透传帧，返回改写后的帧文本。
// info 携带开关与跨帧状态；未开启开关时原样返回。
func applyThinkTagSplit(info *relaycommon.RelayInfo, data string) string {
	if info == nil || !info.ChannelSetting.ThinkTagToReasoning {
		return data
	}
	// 思考已结束且无尾巴：后续流不再可能触发改写，快速旁路
	if info.ThinkTagSplit.Phase == 2 && info.ThinkTagSplit.Carry == "" {
		return data
	}
	if !strings.Contains(data, `"content"`) && info.ThinkTagSplit.Carry == "" {
		return data
	}
	out, changed := rewriteStreamWithThinkSplit(info, data)
	if !changed {
		return data
	}
	return out
}

func rewriteStreamWithThinkSplit(info *relaycommon.RelayInfo, data string) (string, bool) {
	var top orderedMembers
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		return data, false
	}
	changed := false
	for i := range top {
		if top[i].key != "choices" {
			continue
		}
		var choices orderedChoices
		if err := json.Unmarshal(top[i].raw, &choices); err != nil {
			continue
		}
		choiceChanged := false
		for j := range choices {
			var choice orderedMembers
			if err := json.Unmarshal(choices[j], &choice); err != nil {
				continue
			}
			finish := false
			for _, m := range choice {
				if m.key == "finish_reason" && string(m.raw) != "null" {
					finish = true
				}
			}
			for k := range choice {
				if choice[k].key != "delta" {
					continue
				}
				var delta orderedMembers
				if err := json.Unmarshal(choice[k].raw, &delta); err != nil {
					continue
				}
				newDelta, deltaChanged := splitThinkTagInDelta(info, delta, finish)
				if !deltaChanged {
					continue
				}
				choice[k].raw = newDelta.encode()
				choiceChanged = true
			}
			if choiceChanged {
				choices[j] = choice.encode()
			}
		}
		if choiceChanged {
			b, err := choices.encode()
			if err != nil {
				continue
			}
			top[i].raw = b
			changed = true
		}
	}
	if !changed {
		return data, false
	}
	return string(top.encode()), true
}

// splitThinkTagInDelta 处理单个 delta：content 过一遍标签状态机，产出
// reasoning_content 与 content。finish 帧冲刷 carry。原 delta 里上游自带的
// reasoning_content 与拆出的思考段拼接（上游在前）。
func splitThinkTagInDelta(info *relaycommon.RelayInfo, delta orderedMembers, finish bool) (orderedMembers, bool) {
	var contentVal string
	hasContent := false
	var upstreamReasoning string
	hasUpstreamReasoning := false
	for _, m := range delta {
		switch m.key {
		case "content":
			if s, ok := jsonStringValue(m.raw); ok {
				contentVal, hasContent = s, true
			}
		case "reasoning_content":
			if s, ok := jsonStringValue(m.raw); ok {
				upstreamReasoning, hasUpstreamReasoning = s, true
			}
		}
	}
	if !hasContent && !finish {
		return delta, false
	}

	reasoning, remaining := feedThinkSplit(&info.ThinkTagSplit, contentVal)
	flushed := false
	if finish && info.ThinkTagSplit.Carry != "" {
		// 流终止：把可能悬着的标签前缀尾巴按当前相位冲刷，不扣字
		if info.ThinkTagSplit.Phase == 1 {
			reasoning += info.ThinkTagSplit.Carry
		} else {
			remaining += info.ThinkTagSplit.Carry
		}
		info.ThinkTagSplit.Carry = ""
		flushed = true
	}
	if reasoning == "" && remaining == contentVal && !flushed {
		return delta, false
	}

	out := make(orderedMembers, 0, len(delta)+1)
	for _, m := range delta {
		switch m.key {
		case "content":
			if !hasContent {
				out = append(out, m)
				continue
			}
			if remaining == "" {
				// 内容全部进了 reasoning：删掉 content 键而不是留空串——
				// 留着就等于自己制造 v29.30 的逐词分块触发帧
				continue
			}
			out = append(out, kvPair{key: "content", raw: rawJSONString(remaining)})
		case "reasoning_content":
			merged := joinNonEmpty(upstreamReasoning, reasoning)
			if merged == "" {
				out = append(out, m)
				continue
			}
			out = append(out, kvPair{key: "reasoning_content", raw: rawJSONString(merged)})
		default:
			out = append(out, m)
		}
	}
	// delta 原本没有 reasoning_content 键但拆出了思考：补上
	if !hasUpstreamReasoning && reasoning != "" {
		out = append(out, kvPair{key: "reasoning_content", raw: rawJSONString(reasoning)})
	}
	// delta 原本没有 content 键但 finish 冲刷出了正文尾巴：补上
	if !hasContent && remaining != "" {
		out = append(out, kvPair{key: "content", raw: rawJSONString(remaining)})
	}
	return out, true
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + b
	}
}

func rawJSONString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(b)
}
