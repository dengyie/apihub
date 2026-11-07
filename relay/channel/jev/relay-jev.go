package jev

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/dengyie/apihub/common"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"

	"github.com/gin-gonic/gin"
)

// systemOneResponse is the System One decision envelope: one calibrated
// verdict per named question. Everything besides answers passes through to
// the client untouched.
type systemOneResponse struct {
	Model   string          `json:"model"`
	Answers json.RawMessage `json:"answers"`
}

func readDecisionBody(resp *http.Response) ([]byte, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		service.CloseResponseBodyGracefully(resp)
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	service.CloseResponseBodyGracefully(resp)

	var decision systemOneResponse
	if err := common.Unmarshal(responseBody, &decision); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if len(decision.Answers) == 0 {
		return nil, types.NewError(errors.New("jev returned an empty answers map"), types.ErrorCodeBadResponseBody)
	}
	return responseBody, nil
}

// synthesizeUsage bills on estimated tokens over the wire payloads: upstream
// System One responses carry no usage of their own.
func synthesizeUsage(c *gin.Context, info *relaycommon.RelayInfo, responseBody []byte) *dto.Usage {
	promptTokens := 0
	if requestJSON, err := common.Marshal(info.Request); err == nil {
		promptTokens = service.EstimateTokenByModel(info.UpstreamModelName, string(requestJSON))
	}
	return service.ResponseText2Usage(c, string(responseBody), info.UpstreamModelName, promptTokens)
}

// jevSystemOneHandler serves the native protocol: the client already speaks
// System One, so the upstream decision envelope passes through verbatim.
func jevSystemOneHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, apiErr := readDecisionBody(resp)
	if apiErr != nil {
		return nil, apiErr
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(responseBody)

	return synthesizeUsage(c, info, responseBody), nil
}

// jevChatProbeHandler serves the channel-test probe: it wraps the answers map
// into an OpenAI text response so the admin test button can judge the channel
// by its own wire format.
func jevChatProbeHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, apiErr := readDecisionBody(resp)
	if apiErr != nil {
		return nil, apiErr
	}

	var decision systemOneResponse
	if err := common.Unmarshal(responseBody, &decision); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	response := dto.TextResponse{
		Id:      helper.GetResponseID(c),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index:        0,
				Message:      dto.Message{Role: "assistant", Content: string(decision.Answers)},
				FinishReason: "stop",
			},
		},
	}
	usage := synthesizeUsage(c, info, responseBody)
	response.Usage = *usage

	jsonResponse, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(jsonResponse)

	return usage, nil
}
