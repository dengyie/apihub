package openai

import (
	"strings"

	"github.com/dengyie/apihub/common"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// ShouldApplyReasoningAsContent 判定当前请求是否应当执行 reasoning 转 content 改写。
// 核心防护不变式：
//  1. 渠道必须开启了 ChannelSetting.ReasoningAsContent。
//  2. 原生显式思维链推理模型（如 deepseek-v4-pro、deepseek-r1、deepseek-reasoner、kimi-k3、o1、o3、o4、qwq 等）恒定豁免，
//     绝不强行将合法的 reasoning_content 拍扁进 content，彻底杜绝多模型聚合渠道中原生推理体验被破坏。
func ShouldApplyReasoningAsContent(info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || !info.ChannelSetting.ReasoningAsContent {
		return false
	}
	originModel := strings.ToLower(strings.TrimSpace(info.GetOriginModelName()))
	upstreamModel := strings.ToLower(strings.TrimSpace(info.GetUpstreamModelName()))
	if isNativeThinkingReasoningModel(originModel) || isNativeThinkingReasoningModel(upstreamModel) {
		return false
	}
	return true
}

func isNativeThinkingReasoningModel(model string) bool {
	if model == "" {
		return false
	}
	// 剥离可能存在的供应商/组织前缀（例如 "deepseek-ai/DeepSeek-V4-Pro" -> "deepseek-v4-pro"）
	baseModel := model
	if idx := strings.LastIndex(baseModel, "/"); idx != -1 {
		baseModel = baseModel[idx+1:]
	}

	// 显式推理模型后缀（例如 "claude-3-7-sonnet:thinking", "gemini-2.5-flash-thinking-preview"）
	if strings.HasSuffix(baseModel, "-thinking") ||
		strings.HasSuffix(baseModel, ":thinking") ||
		strings.Contains(baseModel, "-thinking-") ||
		strings.Contains(baseModel, ":thinking-") {
		return true
	}

	// DeepSeek 原生思维链系列
	if strings.HasPrefix(baseModel, "deepseek-v4-pro") ||
		strings.HasPrefix(baseModel, "deepseek-r1") ||
		strings.HasPrefix(baseModel, "deepseek-reasoner") {
		return true
	}

	// Kimi / Moonshot 思维链系列
	if strings.HasPrefix(baseModel, "kimi-k3") ||
		strings.HasPrefix(baseModel, "kimi-k2-thinking") ||
		strings.HasPrefix(baseModel, "kimi-k2.5-thinking") ||
		strings.HasPrefix(baseModel, "moonshot-v1-thinking") {
		return true
	}

	// OpenAI 原生 o 系列推理模型 (o1, o3, o4 等)
	if dto.IsOpenAIReasoningOModel(baseModel) {
		return true
	}

	// Qwen QwQ 推理系列 (qwq-32b, qwen-qwq-32b, qwen2.5-qwq 等)
	if strings.HasPrefix(baseModel, "qwq-") || baseModel == "qwq" ||
		strings.Contains(baseModel, "-qwq-") || strings.HasSuffix(baseModel, "-qwq") {
		return true
	}

	return false
}

// TransformPassthroughStreamFrame 对透传的原始 SSE 数据帧执行管道式链式处理：
// 1. 若开启 ReasoningAsContent 且当前模型不属于原生思维链模型：将 reasoning 字段内容改写合并至 content
// 2. 若开启 ThinkTagToReasoning：将内嵌的 <think>...</think> 标签拆分并转为 reasoning_content
// 3. 始终清洗思考帧上的空 content: "" 字段（防止客户端渲染成逐词独立块）
func TransformPassthroughStreamFrame(info *relaycommon.RelayInfo, data string) string {
	if data == "" {
		return ""
	}
	if ShouldApplyReasoningAsContent(info) {
		data = reasoningAsContentFrame(data)
	}
	if info != nil && info.ChannelSetting.ThinkTagToReasoning {
		data = applyThinkTagSplit(info, data)
	}
	return stripEmptyContentOnReasoningFrame(data)
}

// processThinkingToContentState 驱动流式响应中思考内容转正文的状态机转换：
// 处理首次进入思考时的 <think>\n 标签，以及退出思考转正文时的 \n</think>\n 标签闭合。
func processThinkingToContentState(c *gin.Context, info *relaycommon.RelayInfo, resp *dto.ChatCompletionsStreamResponse) error {
	hasThinkingContent := false
	hasContent := false
	var thinkingContent strings.Builder
	for _, choice := range resp.Choices {
		if len(choice.Delta.GetReasoningContent()) > 0 {
			hasThinkingContent = true
			thinkingContent.WriteString(choice.Delta.GetReasoningContent())
		}
		if len(choice.Delta.GetContentString()) > 0 {
			hasContent = true
		}
	}

	// 首次遇到思考内容：下发 <think>\n 开头
	if info.ThinkingContentInfo.IsFirstThinkingContent {
		if hasThinkingContent {
			response := resp.Copy()
			for i := range response.Choices {
				response.Choices[i].Delta.SetContentString("<think>\n" + thinkingContent.String())
				response.Choices[i].Delta.ReasoningContent = nil
				response.Choices[i].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.IsFirstThinkingContent = false
			info.ThinkingContentInfo.HasSentThinkingContent = true
			return helper.ObjectData(c, response)
		}
	}

	if len(resp.Choices) == 0 {
		return helper.ObjectData(c, resp)
	}

	// 遍历所有 choice 转换思考内容
	for i, choice := range resp.Choices {
		// 从思考向正文过渡：下发 \n</think>\n 闭合标签
		if hasContent && !info.ThinkingContentInfo.SendLastThinkingContent && info.ThinkingContentInfo.HasSentThinkingContent {
			response := resp.Copy()
			for j := range response.Choices {
				response.Choices[j].Delta.SetContentString("\n</think>\n")
				response.Choices[j].Delta.ReasoningContent = nil
				response.Choices[j].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.SendLastThinkingContent = true
			if err := helper.ObjectData(c, response); err != nil {
				return err
			}
		}

		if len(choice.Delta.GetReasoningContent()) > 0 {
			resp.Choices[i].Delta.SetContentString(choice.Delta.GetReasoningContent())
			resp.Choices[i].Delta.ReasoningContent = nil
			resp.Choices[i].Delta.Reasoning = nil
		} else if !hasThinkingContent && !hasContent {
			resp.Choices[i].Delta.ReasoningContent = nil
			resp.Choices[i].Delta.Reasoning = nil
		}
	}

	return helper.ObjectData(c, resp)
}

// sendStreamData 统一分发并发送流式数据帧给客户端。
func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}

	// 快速透传路径：无需 JSON 反序列化，直接在字节/字符串层级流水线变换后发出
	if !forceFormat && !thinkToContent {
		transformed := TransformPassthroughStreamFrame(info, data)
		return helper.StringData(c, transformed)
	}

	// 结构化格式化 / 思考转正文路径：需要解码并执行 DTO 级别改写
	var lastStreamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &lastStreamResponse); err != nil {
		return err
	}

	if !thinkToContent {
		if ShouldApplyReasoningAsContent(info) {
			applyReasoningAsContentToDTO(&lastStreamResponse)
		}
		return helper.ObjectData(c, lastStreamResponse)
	}

	return processThinkingToContentState(c, info, &lastStreamResponse)
}
