package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// MaxUpstreamBodyBytes 限制一次性缓冲的上游响应体大小。
//
// 模型发现类的请求把响应整体读进内存再解析，而错误响应现在也要读出来才能给出
// 可读原因——于是一个异常大的拦截页会被完整缓冲。上限取得比任何真实模型列表都
// 宽，只用来挡住异常体量。
const MaxUpstreamBodyBytes = 8 << 20

// ReadUpstreamBody 读到上限即停，超出上限按错误返回，避免把超大响应读进内存。
func ReadUpstreamBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxUpstreamBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxUpstreamBodyBytes {
		return nil, fmt.Errorf("上游响应超过 %d 字节上限", MaxUpstreamBodyBytes)
	}
	return data, nil
}

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
