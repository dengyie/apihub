package openai

import (
	"encoding/json"
	"strings"
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
)

// think_tag_to_reasoning 渠道开关的回归测试。生产帧形取自 2026-10-11 对
// 渠道 #262 (freeapi) 的定点实测：glm-5.3-flash 的思考以 `<think>...</think>`
// 字面标签包在 content 里逐词下发，没有 reasoning_content 字段。

func splitPolicy() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{ThinkTagToReasoning: true}}}
}

func TestThinkTagSplitSingleFrameCompleteTag(t *testing.T) {
	info := splitPolicy()
	frame := `{"choices":[{"delta":{"content":"<think>\n分析用户要求。","role":"assistant"},"finish_reason":null,"index":0}]}`
	out := applyThinkTagSplit(info, frame)
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", out)
	}
	if strings.Contains(out, "<think>") {
		t.Fatalf("标签必须被剥掉: %s", out)
	}
	if strings.Contains(out, `"content":""`) {
		t.Fatalf("拆空后不得留下空 content 字段（v29.30 逐词分块触发帧）: %s", out)
	}
	if !strings.Contains(out, `"reasoning_content":"\n分析用户要求。"`) {
		t.Fatalf("思考段必须进入 reasoning_content: %s", out)
	}
}

// 标签与内容被上游切在多帧之间：跨帧状态机必须拼对。
func TestThinkTagSplitAcrossFrames(t *testing.T) {
	info := splitPolicy()
	frames := []string{
		`{"choices":[{"delta":{"content":"<thi"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"nk>需要"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"先思考"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"</think>"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"这是正文"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	var reasoning, content strings.Builder
	for _, f := range frames {
		out := applyThinkTagSplit(info, f)
		var obj struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatalf("帧输出不是合法 JSON: %v (%s)", err, out)
		}
		for _, ch := range obj.Choices {
			if ch.Delta.ReasoningContent != nil {
				reasoning.WriteString(*ch.Delta.ReasoningContent)
				if *ch.Delta.ReasoningContent == "" {
					t.Fatalf("reasoning 帧不得携带空字段: %s", out)
				}
			}
			if ch.Delta.Content != nil {
				content.WriteString(*ch.Delta.Content)
			}
		}
	}
	if got := reasoning.String(); got != "需要先思考" {
		t.Fatalf("跨帧思考段拼接错误: %q", got)
	}
	if got := content.String(); got != "这是正文" {
		t.Fatalf("标签后正文错误: %q", got)
	}
}

// 不含 think 标签的普通流不得被改写（逐字节原样）。
func TestThinkTagSplitPassthroughWithoutTag(t *testing.T) {
	info := splitPolicy()
	frame := `{"choices":[{"delta":{"content":"你好","role":"assistant"},"finish_reason":null,"index":0}]}`
	if out := applyThinkTagSplit(info, frame); out != frame {
		t.Fatalf("无标签帧必须逐字节原样返回: %s", out)
	}
}

// reasoning_content 与 content 同帧（上游收尾形态）：两个成员都活着。
func TestThinkTagSplitPreservesUpstreamReasoning(t *testing.T) {
	info := splitPolicy()
	// 生产实测形态（渠道 #262 尾帧）：思考段结束后，上游在 content 之后
	// 的帧上同时携带正文与自己的 reasoning_content
	applyThinkTagSplit(info, `{"choices":[{"delta":{"content":"<think>思考中"},"finish_reason":null}]}`)
	applyThinkTagSplit(info, `{"choices":[{"delta":{"content":"</think>"},"finish_reason":null}]}`)
	frame := `{"choices":[{"delta":{"content":"信息服务。","reasoning_content":"上游自己的推理"},"finish_reason":null}]}`
	out := applyThinkTagSplit(info, frame)
	var obj struct {
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", out)
	}
	d := obj.Choices[0].Delta
	if d.Content == nil || *d.Content != "信息服务。" {
		t.Fatalf("正文必须保留: %s", out)
	}
	if d.ReasoningContent == nil || !strings.Contains(*d.ReasoningContent, "上游自己的推理") {
		t.Fatalf("上游自带 reasoning 不得丢失: %s", out)
	}
}

// 未开启开关的渠道逐字节原样（默认关闭安全）。
func TestThinkTagSplitDisabledPassthrough(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	frame := `{"choices":[{"delta":{"content":"<think>思考"},"finish_reason":null}]}`
	if out := applyThinkTagSplit(info, frame); out != frame {
		t.Fatalf("开关关闭时必须原样透传: %s", out)
	}
}

// 正文里合法讨论 <think> 标签的内容：只有渠道开关开启才会被改写——
// 这正是该能力必须按渠道显式开启而非自动检测的原因（本测试文件自己的
// 源码里就有这些字面量）。
func TestThinkTagSplitOnlyWithinTagSegment(t *testing.T) {
	info := splitPolicy()
	// <think> 之前的正文保持在 content
	frame := `{"choices":[{"delta":{"content":"先说结论：<think>内心戏"},"finish_reason":null}]}`
	out := applyThinkTagSplit(info, frame)
	var obj struct {
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", out)
	}
	if got := *obj.Choices[0].Delta.Content; got != "先说结论：" {
		t.Fatalf("标签前正文必须留在 content: %q (%s)", got, out)
	}
	if got := *obj.Choices[0].Delta.ReasoningContent; got != "内心戏" {
		t.Fatalf("标签内内容必须进 reasoning: %q", got)
	}
}

// 生产帧实测形：上游把 `<` 转义成 `\u003cthink\u003e`（渠道 #262 原样抓帧）。
// json 解码后状态机必须照样识别标签。
func TestThinkTagSplitEscapedUnicodeProductionFrame(t *testing.T) {
	info := splitPolicy()
	// 原始字节：content 是 "\u003cthink\u003e\n分析" 的转义形
	frame := `{"choices":[{"delta":{"content":"\u003cthink\u003e\n分析用户要求。","role":"assistant"},"finish_reason":null,"index":0}]}`
	out := applyThinkTagSplit(info, frame)
	var obj struct {
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, out)
	}
	if strings.Contains(out, "think") {
		t.Fatalf("标签必须被剥掉: %s", out)
	}
	if obj.Choices[0].Delta.ReasoningContent == nil ||
		!strings.Contains(*obj.Choices[0].Delta.ReasoningContent, "分析用户要求") {
		t.Fatalf("思考段必须进 reasoning_content: %s", out)
	}
	if obj.Choices[0].Delta.Content != nil && *obj.Choices[0].Delta.Content != "" {
		t.Fatalf("content 应被删掉（拆空后留空串=自己制造 v29.30 触发帧）: %s", out)
	}
}

// n>1 多 choice 帧：状态机是单序列的，整帧跳过原样透传（不污染相位）。
func TestThinkTagSplitSkipsMultiChoiceFrames(t *testing.T) {
	info := splitPolicy()
	frame := `{"choices":[{"index":0,"delta":{"content":"<think>分支甲"}},{"index":1,"delta":{"content":"分支乙"}}],"finish_reason":null}`
	out := applyThinkTagSplit(info, frame)
	if out != frame {
		t.Fatalf("多 choice 帧必须整帧原样透传: %s", out)
	}
	// 相位不得被多 choice 帧推进：后续无标签普通帧仍应原样
	plain := `{"choices":[{"delta":{"content":"普通正文"},"finish_reason":null}]}`
	if out := applyThinkTagSplit(info, plain); out != plain {
		t.Fatalf("多 choice 帧不得污染后续帧的相位: %s", out)
	}
}

// 无 </think> 收尾的流：finish_reason 帧把 carry 按相位冲刷，
// 相位 0 的尾巴回到 content（不得丢字）。
func TestThinkTagSplitFlushesCarryOnFinish(t *testing.T) {
	info := splitPolicy()
	// 相位 0：正文以 "<thi" 结尾（疑似标签前缀被扣住），流随即正常结束
	applyThinkTagSplit(info, `{"choices":[{"delta":{"content":"正文开头<thi"},"finish_reason":null}]}`)
	out := applyThinkTagSplit(info, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	var obj struct {
		Choices []struct {
			Delta struct {
				Content *string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, out)
	}
	if obj.Choices[0].Delta.Content == nil || !strings.Contains(*obj.Choices[0].Delta.Content, "<thi") {
		t.Fatalf("finish 冲刷必须把相位 0 的尾巴还给 content（不丢字）: %s", out)
	}
}
