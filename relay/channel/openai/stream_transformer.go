package openai

import (
	"strings"

	"github.com/dengyie/apihub/common"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// TransformPassthroughStreamFrame 对透传的原始 SSE 数据帧执行管道式链式处理：
// 1. 若开启 ReasoningAsContent：将 reasoning 字段内容改写合并至 content
// 2. 若开启 ThinkTagToReasoning：将内嵌的 <think>...</think> 标签拆分并转为 reasoning_content
// 3. 始终清洗思考帧上的空 content: "" 字段（防止客户端渲染成逐词独立块）
func TransformPassthroughStreamFrame(info *relaycommon.RelayInfo, data string) string {
	if data == "" {
		return ""
	}
	if info != nil && info.ChannelSetting.ReasoningAsContent {
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
		if info != nil && info.ChannelSetting.ReasoningAsContent {
			applyReasoningAsContentToDTO(&lastStreamResponse)
		}
		return helper.ObjectData(c, lastStreamResponse)
	}

	return processThinkingToContentState(c, info, &lastStreamResponse)
}
