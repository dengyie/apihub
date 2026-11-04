package common

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsHtmlOrXmlResponse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        bool
	}{
		{
			name:        "HTML content type",
			contentType: "text/html; charset=utf-8",
			body:        []byte("hello"),
			want:        true,
		},
		{
			name:        "XML content type",
			contentType: "application/xml",
			body:        []byte("<xml></xml>"),
			want:        true,
		},
		{
			name:        "Body starting with DOCTYPE",
			contentType: "application/octet-stream",
			body:        []byte("<!DOCTYPE html><html></html>"),
			want:        true,
		},
		{
			name:        "Body starting with html tag and leading spaces",
			contentType: "",
			body:        []byte("   <html><head></head><body></body></html>"),
			want:        true,
		},
		{
			name:        "Body starting with xml declaration",
			contentType: "text/plain",
			body:        []byte("<?xml version=\"1.0\"?>"),
			want:        true,
		},
		{
			name:        "Valid JSON object",
			contentType: "application/json",
			body:        []byte(`{"data":[{"id":"gpt-4"}]}`),
			want:        false,
		},
		{
			name:        "Valid JSON array",
			contentType: "application/json",
			body:        []byte(`[{"id":"gpt-4"}]`),
			want:        false,
		},
		{
			name:        "Empty body",
			contentType: "text/plain",
			body:        nil,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsHtmlOrXmlResponse(tt.contentType, tt.body)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractHtmlMessage(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "Extracts title tag from zzzcoding pool exhausted page",
			html: `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <title>号池已耗尽，请关注帖子</title>
</head>
<body><h1>号池已耗尽</h1></body>
</html>`,
			want: "号池已耗尽，请关注帖子",
		},
		{
			name: "Extracts h1 when title is missing",
			html: `<html><body><h1>502 Bad Gateway</h1><p>The server encountered a temporary error.</p></body></html>`,
			want: "502 Bad Gateway",
		},
		{
			name: "Extracts text snippet when tags are simple",
			html: `<div class="error">Service Temporarily Unavailable</div>`,
			want: "Service Temporarily Unavailable",
		},
		{
			name: "Unescapes HTML entities",
			html: `<title>Error &amp; Notice &mdash; Cloudflare</title>`,
			want: "Error & Notice — Cloudflare",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractHtmlMessage([]byte(tt.html))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractJsonErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "Nested error.message object",
			json: `{"error":{"message":"This account only allows Codex official clients","type":"forbidden_error"}}`,
			want: "This account only allows Codex official clients",
		},
		{
			name: "Direct error string",
			json: `{"error":"Invalid API Key"}`,
			want: "Invalid API Key",
		},
		{
			name: "Direct message string",
			json: `{"message":"Unauthorized access"}`,
			want: "Unauthorized access",
		},
		{
			name: "Direct msg string",
			json: `{"msg":"Token expired"}`,
			want: "Token expired",
		},
		{
			name: "Direct detail string",
			json: `{"detail":"Not found"}`,
			want: "Not found",
		},
		{
			name: "Normal model list data without error",
			json: `{"data":[{"id":"gpt-4"}]}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJsonErrorMessage([]byte(tt.json))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatUpstreamResponseError(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		contentType string
		body        []byte
		wantSnippet string
	}{
		{
			name:        "HTML page with HTTP 200 (zzzcoding case)",
			statusCode:  http.StatusOK,
			contentType: "text/html",
			body:        []byte(`<html><head><title>号池已耗尽，请关注帖子</title></head></html>`),
			wantSnippet: "上游返回了 HTML 页面 (status code: 200): 号池已耗尽，请关注帖子",
		},
		{
			name:        "HTML page with HTTP 502",
			statusCode:  http.StatusBadGateway,
			contentType: "text/html",
			body:        []byte(`<html><head><title>502 Bad Gateway</title></head></html>`),
			wantSnippet: "上游返回了 HTML 页面 (status code: 502): 502 Bad Gateway",
		},
		{
			name:        "JSON error payload with HTTP 403",
			statusCode:  http.StatusForbidden,
			contentType: "application/json",
			body:        []byte(`{"error":{"message":"This account only allows Codex official clients"}}`),
			wantSnippet: "上游返回错误 (status code: 403): This account only allows Codex official clients",
		},
		{
			name:        "Empty body with HTTP 504",
			statusCode:  http.StatusGatewayTimeout,
			contentType: "text/plain",
			body:        nil,
			wantSnippet: "上游返回空响应 (status code: 504)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FormatUpstreamResponseError(tt.statusCode, tt.contentType, tt.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSnippet)
		})
	}
}
