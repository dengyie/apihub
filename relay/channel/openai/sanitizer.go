package openai

import (
	"slices"
	"strings"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/samber/lo"
)

// RequestSanitizer 负责对出站到 OpenAI 协议渠道的请求进行清洗与规范化：
// 1. 规范化 reasoning_effort 级别（防非标 max/xhigh 报 400）
// 2. 依据渠道配置与运行时学习结果裁剪不受支持的参数（防 400）
// 3. 规范化上下文历史中的 assistant reasoning_content（DeepSeek 补齐 vs 非 DeepSeek 清理）
// 4. 适配 o-series 模型的 developer role 映射

// NormalizeReasoningEffort 规范化 reasoning_effort：
// 标准 OpenAI 仅支持 low / medium / high（以及部分模型的 minimal）。
// 客户端（如 ZCode 的 Thought Level Max）常传入 max 或 xhigh，统一规范化为 high，
// 避免上游渠道报 400 (level "max" not supported, valid levels: low, medium, high)。
func NormalizeReasoningEffort(request *dto.GeneralOpenAIRequest) {
	if request == nil || request.ReasoningEffort == "" {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(request.ReasoningEffort))
	switch effort {
	case "max", "xhigh":
		request.ReasoningEffort = "high"
	default:
		request.ReasoningEffort = effort
	}
}

// StripOpenAIParam 按参数名裁剪 GeneralOpenAIRequest 的对应字段。
// 用于上游不支持某些可选参数的渠道，避免 400 透传给客户端。
func StripOpenAIParam(request *dto.GeneralOpenAIRequest, param string) {
	if request == nil {
		return
	}
	switch param {
	case "thinking":
		request.THINKING = nil
	case "reasoning_effort":
		request.ReasoningEffort = ""
	case "reasoning":
		request.Reasoning = nil
	case "stream_options":
		request.StreamOptions = nil
	case "response_format":
		request.ResponseFormat = nil
	case "tools":
		request.Tools = nil
	case "tool_choice":
		request.ToolChoice = nil
	case "parallel_tool_calls":
		request.ParallelTooCalls = nil
	case "logprobs":
		request.LogProbs = nil
	case "top_logprobs":
		request.TopLogProbs = nil
	case "seed":
		request.Seed = nil
	case "user":
		request.User = nil
	case "metadata":
		request.Metadata = nil
	case "store":
		request.Store = nil
	case "service_tier":
		request.ServiceTier = nil
	case "safety_identifier":
		request.SafetyIdentifier = nil
	case "enable_thinking":
		request.EnableThinking = nil
	case "thinking_budget":
		request.ThinkingBudget = nil
	case "web_search_options":
		request.WebSearchOptions = nil
	case "modalities":
		request.Modalities = nil
	case "audio":
		request.Audio = nil
	case "prediction":
		request.Prediction = nil
	case "extra_body":
		request.ExtraBody = nil
	case "verbosity":
		request.Verbosity = nil
	case "max_completion_tokens":
		if request.MaxCompletionTokens != nil && request.MaxTokens == nil {
			request.MaxTokens = request.MaxCompletionTokens
		}
		request.MaxCompletionTokens = nil
	case "max_tokens":
		if request.MaxTokens != nil && request.MaxCompletionTokens == nil {
			request.MaxCompletionTokens = request.MaxTokens
		}
		request.MaxTokens = nil
	case "reasoning_content":
		copied := false
		for i, msg := range request.Messages {
			if msg.Role == "assistant" && (msg.ReasoningContent != nil || msg.Reasoning != nil) {
				if !copied {
					messagesCopy := make([]dto.Message, len(request.Messages))
					copy(messagesCopy, request.Messages)
					request.Messages = messagesCopy
					copied = true
				}
				request.Messages[i].ReasoningContent = nil
				request.Messages[i].Reasoning = nil
			}
		}
	}
}

// StripUnsupportedParams 执行渠道与模型维度的参数裁剪。
func StripUnsupportedParams(request *dto.GeneralOpenAIRequest, channelID int, originModelName string, info *relaycommon.RelayInfo) {
	if request == nil {
		return
	}
	stripList := loadbalancer.GetStripParams(channelID, originModelName)
	for _, param := range stripList {
		StripOpenAIParam(request, param)
	}
	if info != nil && slices.Contains(stripList, "reasoning_effort") {
		info.SetReasoningEffort("")
	}
}

// parseBoolParam 将 interface{} 参数兼容解析为布尔值（支持 bool, "true"/"1", 1 等形式）。
func parseBoolParam(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		s := strings.ToLower(strings.TrimSpace(val))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	case int:
		return val == 1
	case int64:
		return val == 1
	case float64:
		return val == 1.0
	}
	return false
}

// IsAgentRouterChannel 检查当前渠道是否为 AgentRouter（依据 BaseURL / 渠道名包含 agentrouter，或 param_override 显式声明）。
func IsAgentRouterChannel(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	if info.ChannelMeta != nil {
		if strings.Contains(strings.ToLower(info.ChannelMeta.ChannelBaseUrl), "agentrouter") {
			return true
		}
		if strings.Contains(strings.ToLower(info.ChannelMeta.ChannelName), "agentrouter") {
			return true
		}
		if info.ChannelMeta.ParamOverride != nil {
			if v, ok := info.ChannelMeta.ParamOverride["agentrouter_thinking_placeholder"]; ok {
				if parseBoolParam(v) {
					return true
				}
			}
		}
	}
	if strings.Contains(strings.ToLower(info.ChannelBaseUrl), "agentrouter") {
		return true
	}
	return false
}

// SanitizeAssistantReasoningHistory 清洗与规范化多轮历史中的 assistant 消息：
//  1. 针对 DeepSeek 系列模型：
//     - 针对 AgentRouter 渠道：由于其内部将 DeepSeek 请求转为 Anthropic 协议并对 thinking 块严格校验非空，
//     若缺少有效非空 reasoning_content，填充占位字符串 "..."，杜绝 400 校验报错。
//     - 针对其他所有普通渠道：100% 保持原生既有逻辑，仅当 reasoning_content 为 nil 时注入空字符串 ""，
//     绝不修改已有空字符串或注入占位符，确保完全不影响其他渠道的原生表现与思考链传输。
//  2. 针对非 DeepSeek 模型：移除历史 assistant 消息中的 reasoning_content 与 reasoning，
//     防止 GLM / Claude / GPT 等上游因未定义该字段报 400 Bad Request。
//
// 判据以最终发往上游的目标模型 upstreamModelName 为准。
func SanitizeAssistantReasoningHistory(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) {
	if request == nil || len(request.Messages) == 0 {
		return
	}

	targetModel := request.Model
	if info != nil && info.UpstreamModelName != "" {
		targetModel = info.UpstreamModelName
	}
	targetModelLower := strings.ToLower(targetModel)
	isDeepSeekModel := strings.Contains(targetModelLower, "deepseek") || strings.Contains(targetModelLower, "r1")

	if isDeepSeekModel {
		if IsAgentRouterChannel(info) {
			// 仅在命中 AgentRouter 渠道时注入非空占位符
			var messagesCopy []dto.Message
			for i, msg := range request.Messages {
				if msg.Role == "assistant" {
					hasValidReasoningContent := msg.ReasoningContent != nil && strings.TrimSpace(*msg.ReasoningContent) != ""
					if !hasValidReasoningContent {
						if messagesCopy == nil {
							messagesCopy = make([]dto.Message, len(request.Messages))
							copy(messagesCopy, request.Messages)
						}
						if msg.Reasoning != nil && strings.TrimSpace(*msg.Reasoning) != "" {
							messagesCopy[i].ReasoningContent = msg.Reasoning
						} else {
							placeholder := "..."
							messagesCopy[i].ReasoningContent = &placeholder
						}
					}
				}
			}
			if messagesCopy != nil {
				request.Messages = messagesCopy
			}
		} else {
			// 其他所有通用渠道：完全保留原生行为，仅在 nil 时填空字符串 ""
			var messagesCopy []dto.Message
			for i, msg := range request.Messages {
				if msg.Role == "assistant" && msg.ReasoningContent == nil {
					if messagesCopy == nil {
						messagesCopy = make([]dto.Message, len(request.Messages))
						copy(messagesCopy, request.Messages)
					}
					if msg.Reasoning != nil {
						messagesCopy[i].ReasoningContent = msg.Reasoning
					} else {
						emptyStr := ""
						messagesCopy[i].ReasoningContent = &emptyStr
					}
				}
			}
			if messagesCopy != nil {
				request.Messages = messagesCopy
			}
		}
	} else {
		var messagesCopy []dto.Message
		for i, msg := range request.Messages {
			if msg.Role == "assistant" && (msg.ReasoningContent != nil || msg.Reasoning != nil) {
				if messagesCopy == nil {
					messagesCopy = make([]dto.Message, len(request.Messages))
					copy(messagesCopy, request.Messages)
				}
				messagesCopy[i].ReasoningContent = nil
				messagesCopy[i].Reasoning = nil
			}
		}
		if messagesCopy != nil {
			request.Messages = messagesCopy
		}
	}
}

// SanitizeDeveloperRole 将顶层 system 消息转换为 o-series 支持的 developer 角色。
func SanitizeDeveloperRole(request *dto.GeneralOpenAIRequest, useDeveloperRole bool) {
	if request == nil || !useDeveloperRole || len(request.Messages) == 0 {
		return
	}
	if request.Messages[0].Role == "system" {
		messagesCopy := make([]dto.Message, len(request.Messages))
		copy(messagesCopy, request.Messages)
		messagesCopy[0].Role = "developer"
		request.Messages = messagesCopy
	}
}

// SanitizeModelCapabilitiesAndLimits 依据模型已知能力及自愈学到的上限对请求进行调整与钳制：
// 1. 转换/迁移 max_tokens 与 max_completion_tokens
// 2. 依据学习到的已知上限限制 token 数 (防止 400)
// 3. 剥离模型不支持的 temperature、top_p、logprobs 等高级采样参数
func SanitizeModelCapabilitiesAndLimits(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo, capabilities dto.OpenAIChatCapabilities) {
	if request == nil || info == nil {
		return
	}
	if capabilities.UseMaxCompletionTokens {
		if lo.FromPtrOr(request.MaxCompletionTokens, uint(0)) == 0 && lo.FromPtrOr(request.MaxTokens, uint(0)) != 0 {
			request.MaxCompletionTokens = request.MaxTokens
			request.MaxTokens = nil
		}
	}
	// 把 max_completion_tokens / max_tokens 钳到该渠道该上游模型的已知上限。
	// 上游对超限一律回 400，自述上限已在 service.ProcessChannelError 里学到手。
	if v := lo.FromPtrOr(request.MaxCompletionTokens, uint(0)); v > 0 {
		request.MaxCompletionTokens = lo.ToPtr(loadbalancer.ClampMaxCompletionTokens(info.ChannelId, info.UpstreamModelName, v))
	}
	if v := lo.FromPtrOr(request.MaxTokens, uint(0)); v > 0 {
		request.MaxTokens = lo.ToPtr(loadbalancer.ClampMaxCompletionTokens(info.ChannelId, info.UpstreamModelName, v))
	}
	if !capabilities.SupportsTemperature {
		request.Temperature = nil
	}
	if !capabilities.SupportsTopP {
		request.TopP = nil
	}
	if !capabilities.SupportsLogProbs {
		request.LogProbs = nil
		request.TopLogProbs = nil
	}
}

// SanitizeProtocolStreamOptions 针对非原生 OpenAI / Azure 渠道裁剪不支持的 StreamOptions。
func SanitizeProtocolStreamOptions(request *dto.GeneralOpenAIRequest, channelType int) {
	if request == nil {
		return
	}
	if channelType != constant.ChannelTypeOpenAI && channelType != constant.ChannelTypeAzure {
		request.StreamOptions = nil
	}
}

// PostIntentSanitizeRequest 在推理意图与模型后缀推导完成后，统一执行请求参数清洗与自适应调整：
// 1. 模型能力与限制钳制 (MaxTokens / Temperature / TopP / LogProbs)
// 2. 多轮历史 assistant 思考上下文净化
// 3. 开发者角色转换 (system -> developer for o-series)
// 4. 协议兼容性过滤 (非原生 OpenAI / Azure 渠道裁剪 StreamOptions)
// 5. 渠道自定义裁剪参数保底剥离 (StripUnsupportedParams)
func PostIntentSanitizeRequest(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) {
	if request == nil || info == nil {
		return
	}
	capabilities := dto.GetOpenAIChatCapabilities(info.UpstreamModelName, info.ReasoningEffort)
	SanitizeModelCapabilitiesAndLimits(request, info, capabilities)
	SanitizeAssistantReasoningHistory(request, info)
	SanitizeDeveloperRole(request, capabilities.UseDeveloperRole)
	SanitizeProtocolStreamOptions(request, info.ChannelType)
	StripUnsupportedParams(request, info.ChannelId, info.OriginModelName, info)
}
