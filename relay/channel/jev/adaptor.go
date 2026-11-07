package jev

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/relay/channel"
	"github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct{}

func (a *Adaptor) Init(info *common.RelayInfo) {}

// GetRequestURL follows the upstream rule decidex established across the three
// gateways: a base URL that already names a decision endpoint (/systemone,
// /decide, /decisions) is used verbatim, anything else gets the official
// /systemone path appended.
func (a *Adaptor) GetRequestURL(info *common.RelayInfo) (string, error) {
	base := strings.TrimSuffix(info.ChannelBaseUrl, "/")
	if strings.HasSuffix(base, "/systemone") || strings.HasSuffix(base, "/decide") || strings.HasSuffix(base, "/decisions") {
		return base, nil
	}
	return base + "/systemone", nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *common.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Content-Type", "application/json")
	req.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

// ConvertSystemOneRequest is the native path: the client already speaks System
// One, so the only conversion is model-name mapping (public jev-latest to the
// upstream's own spelling, e.g. MindsHub's bare "jev" via model_mapping).
func (a *Adaptor) ConvertSystemOneRequest(c *gin.Context, info *common.RelayInfo, request *dto.SystemOneRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	upstream := *request
	upstream.Model = info.UpstreamModelName
	return &upstream, nil
}

// ConvertOpenAIRequest exists only so the channel-test probe (chat format) can
// exercise the upstream: it folds the last user message into a single System
// One noul question. It is not a general chat bridge.
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *common.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	var lastUser string
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		if text := message.StringContent(); text != "" {
			lastUser = text
		}
	}
	if lastUser == "" {
		return nil, errors.New("no user message to probe with")
	}
	return map[string]any{
		"model": info.UpstreamModelName,
		"state": map[string]any{"observation": lastUser},
		"questions": map[string]any{
			"probe": map[string]any{
				"type":         "noul",
				"instructions": "Is the statement in the state true?",
			},
		},
	}, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *common.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *common.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.RelayMode == relayconstant.RelayModeSystemOne {
		return jevSystemOneHandler(c, info, resp)
	}
	return jevChatProbeHandler(c, info, resp)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *common.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *common.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *common.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *common.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *common.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *common.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
