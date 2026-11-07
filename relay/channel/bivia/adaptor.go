package bivia

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/dengyie/apihub/relay/channel"
	"github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct{}

func (a *Adaptor) Init(info *common.RelayInfo) {}

// GetRequestURL maps the model name to Bivia's native decision endpoint:
// "bivia" auto-detects the decision type via /decide, "bivia-<kind>" targets
// that endpoint directly.
func (a *Adaptor) GetRequestURL(info *common.RelayInfo) (string, error) {
	endpoint, err := endpointForModel(info.UpstreamModelName)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/bivia/%s", info.ChannelBaseUrl, endpoint), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *common.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Content-Type", "application/json")
	req.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *common.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	return convertBiviaRequest(info, *request)
}

// DoRequest always performs the single upstream call: Bivia answers with one
// JSON envelope even for stream requests, so the stream flag only changes how
// DoResponse renders the decision back to the client.
func (a *Adaptor) DoRequest(c *gin.Context, info *common.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *common.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.IsStream {
		return biviaStreamHandler(c, info, resp)
	}
	return biviaHandler(c, info, resp)
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
