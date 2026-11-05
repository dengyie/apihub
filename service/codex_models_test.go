package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A 401 must stay a status, not an error.
//
// fetchCodexChannelModels refreshes the channel's OAuth credential when it sees
// 401, and it checks that before it looks at err -- but only after returning
// early on a non-nil err. Codex answers an expired token with
// {"error":{"message":...}}, so once FetchCodexModels started describing error
// bodies, every expired credential produced an error here and the refresh branch
// became unreachable: the channel could no longer heal itself.
func TestFetchCodexModelsKeeps401AsStatusSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired","code":"token_expired"}}`))
	}))
	defer server.Close()

	statusCode, models, err := FetchCodexModels(context.Background(), server.Client(), server.URL, &CodexOAuthKey{
		AccessToken: "expired-token",
		AccountID:   "account-id",
	}, "0.44.0")

	require.NoError(t, err, "a 401 must not be reported as an error, or the credential refresh path is skipped")
	assert.Equal(t, http.StatusUnauthorized, statusCode)
	assert.Nil(t, models)
}

// Other failures should still explain themselves: this is the whole point of
// routing the body through FormatUpstreamResponseError.
func TestFetchCodexModelsExplainsNonAuthFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	defer server.Close()

	statusCode, _, err := FetchCodexModels(context.Background(), server.Client(), server.URL, &CodexOAuthKey{
		AccessToken: "token",
		AccountID:   "account-id",
	}, "0.44.0")

	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, statusCode)
	assert.Contains(t, err.Error(), "upstream exploded")
}

func TestFetchCodexModelsExplainsHtmlResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Just a moment...</title></head><body></body></html>`))
	}))
	defer server.Close()

	_, _, err := FetchCodexModels(context.Background(), server.Client(), server.URL, &CodexOAuthKey{
		AccessToken: "token",
		AccountID:   "account-id",
	}, "0.44.0")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Just a moment...")
}

// The error text now carries a slice of the upstream body, and some upstreams
// echo the request headers back in their error pages. The key here is a full
// OAuth access token, so it must not travel to the admin panel inside a message.
func TestFetchCodexModelsRedactsTokenFromUpstreamBody(t *testing.T) {
	const accessToken = "sk-codex-super-secret-access-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html><head><title>Bad gateway for Bearer ` + accessToken + `</title></head><body>blocked</body></html>`))
	}))
	defer server.Close()

	_, _, err := FetchCodexModels(context.Background(), server.Client(), server.URL, &CodexOAuthKey{
		AccessToken: accessToken,
		AccountID:   "account-id",
	}, "0.44.0")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), accessToken)
	assert.Contains(t, err.Error(), "[REDACTED]")
}

// 脱敏只处理真正的凭证长度：短值做全局替换会把正常文本改烂（account id 若是
// "1"，"status code: 401" 会变成 "[REDACTED]0[REDACTED]"）。
func TestFetchCodexModelsDoesNotRedactShortValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded, retry after 401 seconds"}}`))
	}))
	defer server.Close()

	_, _, err := FetchCodexModels(context.Background(), server.Client(), server.URL, &CodexOAuthKey{
		AccessToken: "tok",
		AccountID:   "1",
	}, "0.44.0")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "[REDACTED]")
	assert.Contains(t, err.Error(), "401 seconds")
}
