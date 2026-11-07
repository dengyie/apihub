package bivia

import (
	"io"
	"net/http"
	"strings"
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testInfo(model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model}}
}

func TestEndpointForModel(t *testing.T) {
	cases := []struct {
		model    string
		endpoint string
		wantErr  bool
	}{
		{model: "bivia", endpoint: "decide"},
		{model: "bivia-boolean", endpoint: "boolean"},
		{model: "bivia-choice", endpoint: "choice"},
		{model: "bivia-extract", endpoint: "extract"},
		{model: "bivia-ground", wantErr: true},
		{model: "gpt-4o", wantErr: true},
		{model: "", wantErr: true},
	}
	for _, tc := range cases {
		endpoint, err := endpointForModel(tc.model)
		if tc.wantErr {
			require.Error(t, err, "model %q", tc.model)
		} else {
			require.NoError(t, err, "model %q", tc.model)
			assert.Equal(t, tc.endpoint, endpoint, "model %q", tc.model)
		}
	}
}

func TestConvertBiviaRequest(t *testing.T) {
	t.Run("structured instruction fills the typed slots", func(t *testing.T) {
		request := &dto.GeneralOpenAIRequest{}
		request.Messages = []dto.Message{
			{Role: "user", Content: `{"question":"Which team owns this ticket?","choices":["Infra","Billing"]}`},
		}
		biviaReq, err := convertBiviaRequest(testInfo("bivia-choice"), *request)
		require.NoError(t, err)
		assert.Equal(t, "Which team owns this ticket?", biviaReq.Question)
		assert.Equal(t, []string{"Infra", "Billing"}, biviaReq.Choices)
		assert.NotEmpty(t, biviaReq.State)
	})

	t.Run("generic options slot is moved to the endpoint's own slot", func(t *testing.T) {
		request := &dto.GeneralOpenAIRequest{}
		request.Messages = []dto.Message{
			{Role: "user", Content: `{"question":"pick","options":["a","b"]}`},
		}
		biviaReq, err := convertBiviaRequest(testInfo("bivia-choice"), *request)
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, biviaReq.Choices)
		assert.Empty(t, biviaReq.Options)
	})

	t.Run("plain messages fall back to last user question with state", func(t *testing.T) {
		request := &dto.GeneralOpenAIRequest{}
		request.Messages = []dto.Message{
			{Role: "system", Content: "You route tickets."},
			{Role: "user", Content: "I was double-charged."},
			{Role: "user", Content: "Is this urgent?"},
		}
		biviaReq, err := convertBiviaRequest(testInfo("bivia-boolean"), *request)
		require.NoError(t, err)
		assert.Equal(t, "Is this urgent?", biviaReq.Question)
		assert.Contains(t, biviaReq.State, "You route tickets.")
		assert.Contains(t, biviaReq.State, "I was double-charged.")
	})

	t.Run("unknown model is rejected", func(t *testing.T) {
		request := &dto.GeneralOpenAIRequest{}
		_, err := convertBiviaRequest(testInfo("bivia-ground"), *request)
		require.Error(t, err)
	})
}

func TestParseDecisionResponseUsage(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		prompt     int
		completion int
		total      int
	}{
		{
			name:       "fast mode minimum billing maps to prompt+completion",
			body:       `{"type":"boolean","value":"yes","probability":0.99,"status":"answered","input_tokens":64,"billable_tokens":1000}`,
			prompt:     64,
			completion: 936,
			total:      1000,
		},
		{
			name:       "negative billable_tokens is clamped to zero charge",
			body:       `{"type":"boolean","value":"yes","status":"answered","input_tokens":64,"billable_tokens":-5}`,
			prompt:     64,
			completion: 0,
			total:      0,
		},
		{
			name:       "billable below input never yields negative completion",
			body:       `{"type":"boolean","value":"yes","status":"answered","input_tokens":900,"billable_tokens":500}`,
			prompt:     900,
			completion: 0,
			total:      500,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}
			result, apiErr := parseDecisionResponse(resp)
			require.Nil(t, apiErr)
			require.NotNil(t, result.usage)
			assert.Equal(t, tc.prompt, result.usage.PromptTokens)
			assert.Equal(t, tc.completion, result.usage.CompletionTokens)
			assert.Equal(t, tc.total, result.usage.TotalTokens)
		})
	}

	t.Run("abstained status is forwarded not rejected", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"type":"boolean","value":null,"status":"abstained","abstain_reason":"low_confidence","input_tokens":64,"billable_tokens":1000}`,
		))}
		result, apiErr := parseDecisionResponse(resp)
		require.Nil(t, apiErr)
		assert.Contains(t, result.content, `"status":"abstained"`)
		assert.Contains(t, result.content, `"abstain_reason":"low_confidence"`)
	})

	t.Run("extract envelope without status maps values under uniform value", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"type":"extract","values":{"termination notice period":{"value":"60 days written notice","found":true}},"grounded":true,"input_tokens":22,"image_tokens":0,"billable_tokens":1000}`,
		))}
		result, apiErr := parseDecisionResponse(resp)
		require.Nil(t, apiErr)
		assert.Contains(t, result.content, `"status":"answered"`)
		assert.Contains(t, result.content, `"value":{"termination notice period"`)
		assert.NotContains(t, result.content, `"probability"`)
		assert.Equal(t, 22, result.usage.PromptTokens)
		assert.Equal(t, 978, result.usage.CompletionTokens)
		assert.Equal(t, 1000, result.usage.TotalTokens)
	})

	t.Run("unexpected status is an upstream error", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"type":"boolean","status":"exploded"}`,
		))}
		_, apiErr := parseDecisionResponse(resp)
		require.NotNil(t, apiErr)
	})
}
