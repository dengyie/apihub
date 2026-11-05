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

// jsonErrorMessageDepth 限制错误报文里嵌套查找的深度。
//
// 上游错误体没有统一规范：同名字段这一家是字符串、下一家是数组（FastAPI 的
// detail 就是数组）、再下一家又嵌一层 error。逐层往下找可以覆盖这些形态，
// 但必须设上限——报文来自上游，嵌套多深由对方决定。
const jsonErrorMessageDepth = 4

// ExtractJsonErrorMessage extracts error descriptions from typical upstream JSON error responses.
//
// 用 RawMessage 而不是固定类型的结构体：只要有一个被声明的字段类型与实际上游
// 不符（例如 detail 是数组而非字符串），整条报文的反序列化就会失败，连同一份
// 报文里完好的 error.message 也会一起丢掉，最终只剩截断的原始片段。逐字段按
// 实际类型解释，一个字段形态不对不影响其他字段。
func ExtractJsonErrorMessage(body []byte) string {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	// 顺序即优先级：更具体的顶层描述优先于嵌套的 error 对象。
	for _, key := range []string{"message", "msg", "detail", "error", "reason", "description"} {
		if msg := jsonRawMessageText(payload[key], jsonErrorMessageDepth); msg != "" {
			return msg
		}
	}
	return ""
}

// jsonRawMessageText 从任意 JSON 值里取出人类可读的错误文本，取不到返回空串。
func jsonRawMessageText(raw json.RawMessage, depth int) string {
	if len(raw) == 0 || depth <= 0 {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return strings.TrimSpace(s)
		}
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return ""
		}
		for _, key := range []string{"message", "msg", "detail", "error", "reason", "description"} {
			if msg := jsonRawMessageText(obj[key], depth-1); msg != "" {
				return msg
			}
		}
	case '[':
		// 数组形态（如 FastAPI 的校验错误）逐项尝试，取第一条能读出来的。
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return ""
		}
		for _, item := range items {
			if msg := jsonRawMessageText(item, depth-1); msg != "" {
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
