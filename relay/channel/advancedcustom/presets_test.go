package advancedcustom_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/relay"
	"github.com/dengyie/apihub/relay/channel/advancedcustom"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSGLangChannelProtocols(t *testing.T) {
	apiType, ok := common.ChannelType2APIType(constant.ChannelTypeSGLang)
	require.True(t, ok)
	adaptor := relay.GetAdaptor(apiType)
	require.NotNil(t, adaptor)
	assert.Equal(t, "advanced_custom", adaptor.GetChannelName())
	assert.Contains(t, common.GetEndpointTypesByChannelType(constant.ChannelTypeSGLang, "served-model"), constant.EndpointTypeAnthropic)
	assert.Contains(t, common.GetEndpointTypesByChannelType(constant.ChannelTypeSGLang, "served-model"), constant.EndpointTypeOpenAIResponse)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang, ChannelBaseUrl: "https://inference.example/prefix", ApiKey: "test-key"}}
	info.ChannelOtherSettings.AdvancedCustom = common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)
	adaptor.Init(info)
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses", "/v1/messages"} {
		info.RequestURLPath = path
		adaptor = &advancedcustom.Adaptor{}
		adaptor.Init(info)
		url, err := adaptor.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://inference.example/prefix"+path, url)
	}
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		info.RelayFormat = format
		info.RequestURLPath = "/v1/messages"
		adaptor = &advancedcustom.Adaptor{}
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		headers := http.Header{}
		require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))
		assert.Equal(t, "Bearer test-key", headers.Get("Authorization"))
	}
	info.RequestURLPath = "/v1/chat/completions"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	// 「无需跨协议转换」时本适配器把请求原样交给 openai 适配器的直通分支；但那个分支
	// 自 2026-09-29 (v29.7) 起会**浅拷贝**请求再改写（reasoning_effort 归一、渠道级
	// 参数裁剪），理由是外层渠道重试要复用未被污染的原始结构
	// （relay/channel/openai/adaptor.go:311）。因此这里断言的是「内容等价」，而不是
	// 指针同一 —— 本用例写于 2026-09-21，早于那次改动，当时确实返回同一个指针。
	request := &dto.GeneralOpenAIRequest{Model: "served-model"}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	passThrough, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "直通分支不得改变请求类型：%T", converted)
	assert.Equal(t, request, passThrough, "直通分支不得改写请求内容")

	// 直通 ≠ 就地改写：reasoning_effort="max" 会被归一为 "high"，而调用方持有的原始
	// 请求必须保持 "max"，否则第二次重试看到的就是上一次尝试的残留。
	retryRequest := &dto.GeneralOpenAIRequest{Model: "served-model", ReasoningEffort: "max"}
	retryConverted, err := adaptor.ConvertOpenAIRequest(c, info, retryRequest)
	require.NoError(t, err)
	retryPassThrough, ok := retryConverted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "直通分支不得改变请求类型：%T", retryConverted)
	assert.Equal(t, "high", retryPassThrough.ReasoningEffort, "归一化必须在拷贝上生效")
	assert.Equal(t, "max", retryRequest.ReasoningEffort, "归一化不得写回调用方的原始请求")
	info.RequestURLPath = "/v1/messages"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	claudeRequest := &dto.ClaudeRequest{Model: "served-model", Thinking: &dto.Thinking{Type: "adaptive"}}
	converted, err = adaptor.ConvertClaudeRequest(c, info, claudeRequest)
	require.NoError(t, err)
	assert.Same(t, claudeRequest, converted)
	info.RequestURLPath = "/v1beta/models/test:generateContent"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	_, err = adaptor.ConvertGeminiRequest(nil, info, &dto.GeminiChatRequest{})
	require.Error(t, err)
}

func TestSGLangRerankRequestAndResponse(t *testing.T) {
	service.InitHttpClient()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/prefix/v1/rerank", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var request dto.RerankRequest
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, "served-reranker", request.Model)
		assert.Equal(t, []any{"first", "second"}, request.Documents)
		_, _ = io.WriteString(w, `[{"index":1,"score":0.9,"document":"second","meta_info":{"prompt_tokens":5}}]`)
	}))
	defer upstream.Close()
	info := &relaycommon.RelayInfo{
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang, ChannelBaseUrl: upstream.URL + "/prefix", ApiKey: "test-key"},
		RequestURLPath: "/rerank", RelayMode: relayconstant.RelayModeRerank,
		RelayFormat:  types.RelayFormatOpenAI,
		RerankerInfo: &relaycommon.RerankerInfo{Documents: []any{"first", "second"}, ReturnDocuments: true},
	}
	info.SetEstimatePromptTokens(12)
	adaptor := &advancedcustom.Adaptor{}
	info.ChannelOtherSettings.AdvancedCustom = common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)
	adaptor.Init(info)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/rerank", nil).WithContext(context.Background())
	converted, err := adaptor.ConvertRerankRequest(c, info.RelayMode, dto.RerankRequest{Model: "served-reranker", Query: "query", Documents: info.Documents})
	require.NoError(t, err)
	body, err := common.Marshal(converted)
	require.NoError(t, err)
	response, err := adaptor.DoRequest(c, info, bytes.NewReader(body))
	require.NoError(t, err)
	usage, apiErr := adaptor.DoResponse(c, response.(*http.Response), info)
	require.Nil(t, apiErr)
	assert.Equal(t, 12, usage.(*dto.Usage).TotalTokens)
	var decoded dto.RerankResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &decoded))
	assert.Equal(t, []dto.RerankResponseResult{{Index: 1, RelevanceScore: 0.9, Document: map[string]any{"text": "second"}}}, decoded.Results)
	assert.Equal(t, 12, decoded.Usage.PromptTokens)
	assert.Zero(t, decoded.Usage.CompletionTokens)
}

func TestSGLangRerankRejectsMalformedResults(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{"index":99,"score":1}]`, `[{"index":-1,"score":0}]`, `[{"index":0}]`, `[{"score":1}]`} {
		t.Run(body, func(t *testing.T) {
			adaptor := &advancedcustom.Adaptor{}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)}}, RequestURLPath: "/v1/rerank", RelayMode: relayconstant.RelayModeRerank, RerankerInfo: &relaycommon.RerankerInfo{Documents: []any{"only document"}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			usage, err := adaptor.DoResponse(c, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, info)
			require.NotNil(t, err)
			assert.Nil(t, usage)
			assert.Empty(t, recorder.Body.String())
		})
	}
}
