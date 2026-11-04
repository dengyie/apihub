package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
)

var (
	titleTagRegex = regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	h1TagRegex    = regexp.MustCompile(`(?i)<h[12][^>]*>([^<]+)</h[12]>`)
	htmlTagRegex  = regexp.MustCompile(`<[^>]+>`)
)

// IsHtmlOrXmlResponse detects whether the response Content-Type or body content indicates HTML or XML.
func IsHtmlOrXmlResponse(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "application/xhtml+xml") ||
		strings.Contains(ct, "text/xml") ||
		strings.Contains(ct, "application/xml") {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '<' {
		return true
	}
	return false
}

// ExtractHtmlMessage extracts a human-readable title or text snippet from an HTML document.
func ExtractHtmlMessage(body []byte) string {
	s := string(body)
	if m := titleTagRegex.FindStringSubmatch(s); len(m) > 1 {
		title := cleanHtmlText(m[1])
		if title != "" {
			return title
		}
	}
	if m := h1TagRegex.FindStringSubmatch(s); len(m) > 1 {
		h1 := cleanHtmlText(m[1])
		if h1 != "" {
			return h1
		}
	}
	clean := cleanHtmlText(htmlTagRegex.ReplaceAllString(s, " "))
	if len(clean) > 120 {
		runes := []rune(clean)
		if len(runes) > 100 {
			clean = string(runes[:100]) + "..."
		}
	}
	return clean
}

func cleanHtmlText(s string) string {
	s = html.UnescapeString(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// ExtractJsonErrorMessage extracts error descriptions from typical upstream JSON error responses.
func ExtractJsonErrorMessage(body []byte) string {
	var errPayload struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &errPayload); err != nil {
		return ""
	}
	if errPayload.Message != "" {
		return errPayload.Message
	}
	if errPayload.Msg != "" {
		return errPayload.Msg
	}
	if errPayload.Detail != "" {
		return errPayload.Detail
	}
	if errPayload.Error != nil {
		switch v := errPayload.Error.(type) {
		case string:
			return v
		case map[string]any:
			if msg, ok := v["message"].(string); ok && msg != "" {
				return msg
			}
		}
	}
	return ""
}

// FormatUpstreamResponseError produces a concise, actionable error message diagnosing
// upstream response errors (including HTML pages, non-200 statuses, and upstream JSON error objects).
func FormatUpstreamResponseError(statusCode int, contentType string, body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Errorf("上游返回空响应 (status code: %d)", statusCode)
	}

	if IsHtmlOrXmlResponse(contentType, trimmed) {
		msg := ExtractHtmlMessage(trimmed)
		if msg != "" {
			return fmt.Errorf("上游返回了 HTML 页面 (status code: %d): %s", statusCode, msg)
		}
		return fmt.Errorf("上游返回了 HTML 页面 (status code: %d)", statusCode)
	}

	if jsonErr := ExtractJsonErrorMessage(trimmed); jsonErr != "" {
		return fmt.Errorf("上游返回错误 (status code: %d): %s", statusCode, jsonErr)
	}

	snippet := string(trimmed)
	if len(snippet) > 120 {
		runes := []rune(snippet)
		if len(runes) > 100 {
			snippet = string(runes[:100]) + "..."
		}
	}

	if statusCode != http.StatusOK {
		return fmt.Errorf("上游返回错误 (status code: %d): %s", statusCode, snippet)
	}
	return fmt.Errorf("上游返回非预期格式响应 (status code: 200): %s", snippet)
}
