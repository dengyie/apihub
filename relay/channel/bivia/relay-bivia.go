package bivia

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/common"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relay/helper"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"

	"github.com/gin-gonic/gin"
)

var decisionEndpoints = map[string]bool{
	"decide":  true,
	"boolean": true,
	"choice":  true,
	"score":   true,
	"set":     true,
	"span":    true,
	"extract": true,
}

func endpointForModel(model string) (string, error) {
	if model == "bivia" {
		return "decide", nil
	}
	if suffix, ok := strings.CutPrefix(model, "bivia-"); ok && decisionEndpoints[suffix] {
		return suffix, nil
	}
	return "", fmt.Errorf("unknown bivia model %q: expected bivia or bivia-{decide,boolean,choice,score,set,span,extract}", model)
}

func parseInstruction(text string) *biviaInstruction {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var instruction biviaInstruction
	if err := common.Unmarshal([]byte(trimmed), &instruction); err != nil || instruction.Question == "" {
		return nil
	}
	return &instruction
}

// convertBiviaRequest maps an OpenAI chat request onto Bivia's typed decision
// contract. If any message content is a JSON object with a "question" key it
// is the structured instruction; otherwise the last user message is the
// question and every other message becomes the decision state. Multiple
// structured instructions: the last one wins, earlier ones do not enter the
// state.
func convertBiviaRequest(info *relaycommon.RelayInfo, request dto.GeneralOpenAIRequest) (*biviaRequest, error) {
	endpoint, err := endpointForModel(info.UpstreamModelName)
	if err != nil {
		return nil, err
	}

	var instruction *biviaInstruction
	var contextParts []string
	var lastUser string
	for _, message := range request.Messages {
		text := message.StringContent()
		if text == "" {
			continue
		}
		if candidate := parseInstruction(text); candidate != nil {
			instruction = candidate
			continue
		}
		if message.Role == "user" {
			lastUser = text
			contextParts = append(contextParts, text)
		} else {
			contextParts = append(contextParts, message.Role+": "+text)
		}
	}

	biviaReq := &biviaRequest{}
	if instruction != nil {
		// biviaInstruction is a defined type over biviaRequest: one assignment
		// keeps the client instruction and the upstream wire shape identical.
		*biviaReq = biviaRequest(*instruction)
	} else {
		biviaReq.Question = lastUser
		biviaReq.State = strings.Join(contextParts, "\n\n")
	}
	if biviaReq.State == "" {
		biviaReq.State = biviaReq.Question
	}

	// Forgive generic candidate keys: keep whichever slot the client filled
	// in the endpoint's own slot and drop the alias slots, so no stray field
	// leaks to the upstream endpoint.
	switch endpoint {
	case "decide":
		biviaReq.Outcomes = resolveCandidates(biviaReq.Outcomes, biviaReq.Choices, biviaReq.Options)
		biviaReq.Choices, biviaReq.Options = nil, nil
	case "choice":
		biviaReq.Choices = resolveCandidates(biviaReq.Choices, biviaReq.Outcomes, biviaReq.Options)
		biviaReq.Outcomes, biviaReq.Options = nil, nil
	case "set":
		biviaReq.Options = resolveCandidates(biviaReq.Options, biviaReq.Choices, biviaReq.Outcomes)
		biviaReq.Choices, biviaReq.Outcomes = nil, nil
	}
	return biviaReq, nil
}

func resolveCandidates(primary, first, second []string) []string {
	if len(primary) > 0 {
		return primary
	}
	if len(first) > 0 {
		return first
	}
	return second
}

type biviaDecisionResult struct {
	content string
	usage   *dto.Usage
}

func parseDecisionResponse(resp *http.Response) (*biviaDecisionResult, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		service.CloseResponseBodyGracefully(resp)
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	service.CloseResponseBodyGracefully(resp)

	var decision biviaResponse
	if err := common.Unmarshal(responseBody, &decision); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	// /extract omits the status field entirely: its per-field verdicts live in
	// values[].found and the call never abstains as one unit, so synthesize
	// answered for it.
	status := fromPtr(decision.Status)
	if status == "" && decision.Type == "extract" && len(decision.Values) > 0 {
		status = "answered"
	}
	if status != "answered" && status != "abstained" {
		return nil, types.NewError(
			fmt.Errorf("bivia returned unexpected status %q", status),
			types.ErrorCodeBadResponseBody,
		)
	}

	// The client envelope keeps one uniform contract: value always carries the
	// answer, so the extract values map is forwarded under value.
	answer := decision.Value
	if len(answer) == 0 {
		answer = decision.Values
	}

	payload, err := common.Marshal(biviaDecision{
		Type:          decision.Type,
		Value:         answer,
		Probability:   decision.Probability,
		Distribution:  decision.Distribution,
		Status:        status,
		AbstainReason: fromPtr(decision.AbstainReason),
		Mode:          decision.Mode,
		Model:         decision.Model,
		Policy:        decision.Policy,
		LatencyMs:     decision.LatencyMs,
		Evidence:      decision.Evidence,
	})
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	// Articul8 bills billable_tokens for the whole call (1000 minimum in fast
	// mode); clamp the upstream numbers so a hostile response can never
	// produce a negative charge.
	prompt := max(decision.InputTokens, 0)
	total := max(decision.BillableTokens, 0)
	usage := &dto.Usage{
		PromptTokens:     prompt,
		CompletionTokens: max(total-prompt, 0),
		TotalTokens:      total,
	}
	return &biviaDecisionResult{content: string(payload), usage: usage}, nil
}

func fromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func biviaHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	result, apiErr := parseDecisionResponse(resp)
	if apiErr != nil {
		return nil, apiErr
	}

	response := dto.TextResponse{
		Id:      helper.GetResponseID(c),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index:        0,
				Message:      dto.Message{Role: "assistant", Content: result.content},
				FinishReason: "stop",
			},
		},
	}
	response.Usage = *result.usage

	jsonResponse, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(jsonResponse)

	return result.usage, nil
}

// biviaStreamHandler renders the single decision envelope as one SSE chunk
// followed by the stop chunk: Bivia has no incremental stream of its own.
func biviaStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	result, apiErr := parseDecisionResponse(resp)
	if apiErr != nil {
		return nil, apiErr
	}

	id := helper.GetResponseID(c)
	helper.SetEventStreamHeaders(c)

	chunk := dto.ChatCompletionsStreamResponse{
		Id:      id,
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
	}
	choice := dto.ChatCompletionsStreamResponseChoice{Index: 0}
	choice.Delta.SetContentString(result.content)
	chunk.Choices = []dto.ChatCompletionsStreamResponseChoice{choice}
	helper.ObjectData(c, chunk)

	stopResponse := helper.GenerateStopResponse(id, common.GetTimestamp(), info.UpstreamModelName, "stop")
	helper.ObjectData(c, stopResponse)
	helper.Done(c)

	return result.usage, nil
}
