package jev

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testInfo(model string, relayMode int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
		RelayMode:   relayMode,
	}
}

func TestGetRequestURLPathRule(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{base: "https://pool.futureppo.top", want: "https://pool.futureppo.top/systemone"},
		{base: "https://api.typesafe.ai/v1", want: "https://api.typesafe.ai/v1/systemone"},
		{base: "https://api.typesafe.ai/v1/systemone", want: "https://api.typesafe.ai/v1/systemone"},
		{base: "https://api.mindshub.ai/v1/decisions", want: "https://api.mindshub.ai/v1/decisions"},
		{base: "https://example.com/v1/decide", want: "https://example.com/v1/decide"},
		{base: "https://example.com/v1/systemone/", want: "https://example.com/v1/systemone"},
	}
	for _, tc := range cases {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: tc.base}}
		got, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err, "base %q", tc.base)
		assert.Equal(t, tc.want, got, "base %q", tc.base)
	}
}

func TestConvertSystemOneRequestMapsModel(t *testing.T) {
	request := &dto.SystemOneRequest{
		Model:     "jev-latest",
		State:     json.RawMessage(`{"observation":"ante 4"}`),
		Questions: json.RawMessage(`{"play":{"type":"choice","criteria":{"a":"x"}}}`),
	}
	converted, err := (&Adaptor{}).ConvertSystemOneRequest(nil, testInfo("jev", 0), request)
	require.NoError(t, err)
	upstream, ok := converted.(*dto.SystemOneRequest)
	require.True(t, ok)
	assert.Equal(t, "jev", upstream.Model, "model mapping must override the wire name")
	assert.Equal(t, request.State, upstream.State)
	assert.Equal(t, request.Questions, upstream.Questions)
}

func TestConvertOpenAIRequestProbe(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{}
	request.Messages = []dto.Message{
		{Role: "system", Content: "you route"},
		{Role: "user", Content: "ante 4 with 3 jokers"},
	}
	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, testInfo("jev-latest", 0), request)
	require.NoError(t, err)
	m, ok := converted.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "jev-latest", m["model"])
	questions, ok := m["questions"].(map[string]any)
	require.True(t, ok)
	probe, ok := questions["probe"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "noul", probe["type"])

	_, err = (&Adaptor{}).ConvertOpenAIRequest(nil, testInfo("jev-latest", 0), &dto.GeneralOpenAIRequest{})
	require.Error(t, err, "no user message must fail the probe")
}

func systemOneEnvelope() string {
	return `{"model":"jev-1.13.0","answers":{"play":{"choice":"all-in","probabilities":{"all-in":0.94}}}}`
}

func TestJevSystemOneHandlerPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", nil)

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(systemOneEnvelope()))}
	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, testInfo("jev-1.13.0", relayconstant.RelayModeSystemOne))
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	assert.Equal(t, systemOneEnvelope(), recorder.Body.String(), "native passthrough must be byte-identical")
	assert.True(t, usage.(*dto.Usage).TotalTokens > 0, "synthesized usage must be positive")
}

func TestJevChatProbeHandlerWrapsOpenAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(systemOneEnvelope()))}
	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, testInfo("jev-latest", 0))
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	var out dto.TextResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	assert.Equal(t, "chat.completion", out.Object)
	assert.Contains(t, out.Choices[0].Message.Content, `"choice":"all-in"`)
}

func TestJevHandlerRejectsEmptyAnswers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", nil)

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"model":"jev-1.13.0"}`))}
	_, apiErr := (&Adaptor{}).DoResponse(c, resp, testInfo("jev-1.13.0", relayconstant.RelayModeSystemOne))
	require.NotNil(t, apiErr)
}
