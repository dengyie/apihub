package xai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func streamResponseXAI2OpenAI(xAIResp *dto.ChatCompletionsStreamResponse, usage *dto.Usage) *dto.ChatCompletionsStreamResponse {
	if xAIResp == nil {
		return nil
	}
	if xAIResp.Usage != nil {
		xAIResp.Usage.CompletionTokens = usage.CompletionTokens
	}
	openAIResp := &dto.ChatCompletionsStreamResponse{
		Id:      xAIResp.Id,
		Object:  xAIResp.Object,
		Created: xAIResp.Created,
		Model:   xAIResp.Model,
		Choices: xAIResp.Choices,
		Usage:   xAIResp.Usage,
	}

	return openAIResp
}

// xaiInterruptedUsage 给断流的那一次尝试估出已交付部分的用量，返回值交给
// 调用方转交 controller 结算。
//
// 判据是真正累积到的产出而非「收到了帧」：上游错误同样以数据帧送达，空产出会
// 被估成整段 prompt，等于给一次失败的请求收一遍全款 —— 所以正文为空时返回
// nil，不计费。responseTextBuilder 由 ProcessStreamResponse 填充，同时收了正文、
// reasoning 与工具名/参数。
func xaiInterruptedUsage(c *gin.Context, info *relaycommon.RelayInfo, text string, toolCount int) *dto.Usage {
	interrupted := service.InterruptedTextUsage(c, info, text)
	if interrupted != nil {
		interrupted.CompletionTokens += toolCount * 7
	}
	return interrupted
}

func xAIStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	usage := &dto.Usage{}
	var responseTextBuilder strings.Builder
	var toolCount int
	var containStreamUsage bool

	helper.SetEventStreamHeaders(c)

	if info != nil {
		if info.StreamStatus == nil {
			info.StreamStatus = relaycommon.NewStreamStatus()
		}
		info.StreamStatus.RequireTerminal()
	}

	if streamErr := helper.ToNewAPIError(helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var xAIResp *dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &xAIResp); err != nil {
			common.SysLog("error unmarshalling stream response: " + err.Error())
			sr.Error(err)
			return
		}

		for _, choice := range xAIResp.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				info.StreamStatus.MarkCompleted()
			}
		}

		// 把 xAI 的usage转换为 OpenAI 的usage
		if xAIResp.Usage != nil {
			containStreamUsage = true
			usage.PromptTokens = xAIResp.Usage.PromptTokens
			usage.TotalTokens = xAIResp.Usage.TotalTokens
			usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
		}

		openaiResponse := streamResponseXAI2OpenAI(xAIResp, usage)
		_ = openai.ProcessStreamResponse(*openaiResponse, &responseTextBuilder, &toolCount)
		if err := helper.ObjectData(c, openaiResponse); err != nil {
			common.SysLog(err.Error())
			sr.Error(err)
		}
	})); streamErr != nil {
		return xaiInterruptedUsage(c, info, responseTextBuilder.String(), toolCount), streamErr
	}

	if info != nil && info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
		logger.LogWarn(c, fmt.Sprintf("stream ended abnormally (%s), skipping final [DONE] frame", info.StreamStatus.Summary()))
		return xaiInterruptedUsage(c, info, responseTextBuilder.String(), toolCount), types.NewErrorWithStatusCode(
			&loadbalancer.StreamBrokenError{ChannelID: info.GetChannelID(), Reason: info.StreamStatus.Summary()},
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
		)
	}

	if !containStreamUsage {
		usage = service.DeliveredTextUsage(c, info, responseTextBuilder.String())
		usage.CompletionTokens += toolCount * 7
	}

	helper.Done(c)
	service.CloseResponseBodyGracefully(resp)
	return usage, nil
}

func xAIHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	var xaiResponse ChatCompletionResponse
	err = common.Unmarshal(responseBody, &xaiResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if xaiResponse.Usage != nil {
		xaiResponse.Usage.CompletionTokens = xaiResponse.Usage.TotalTokens - xaiResponse.Usage.PromptTokens
		xaiResponse.Usage.CompletionTokenDetails.TextTokens = xaiResponse.Usage.CompletionTokens - xaiResponse.Usage.CompletionTokenDetails.ReasoningTokens
	}

	// new body
	encodeJson, err := common.Marshal(xaiResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	service.IOCopyBytesGracefully(c, resp, encodeJson)

	return xaiResponse.Usage, nil
}
