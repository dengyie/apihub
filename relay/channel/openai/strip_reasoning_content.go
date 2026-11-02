package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dengyie/apihub/relaykit/dto"
)

// NormalizeStreamFrame normalizes chat-completion streaming SSE data frames:
//  1. Unpacks non-standard content/reasoning block arrays (e.g. [{"type":"thinking",...}])
//     into standard delta reasoning_content and/or content strings.
//  2. Drops explicitly present but empty "content": "" fields from reasoning frames.
//  3. Normalizes non-standard "choices":null to "choices":[] to satisfy strict client-side
//     schema validators (e.g. Vercel AI SDK / ZCode union validation).
//  4. Sanitizes misleading code/type values inside top-level "error" objects of mid-stream
//     error data frames (insufficient_quota / invalid_api_key / account_deactivated ...),
//     so client SDKs parsing SSE error chunks never blame the user's own credentials or
//     account balance for a gateway/upstream fault.
//
// All rewrites are structure-aware: only real top-level members are touched, and every
// other byte — including string values that merely look like JSON — is preserved verbatim.
func NormalizeStreamFrame(data string) string {
	if !mightNeedNormalization(data) {
		return data
	}
	out, changed := rewriteStream(data)
	if !changed {
		return data
	}
	return out
}

// mightNeedNormalization checks whether data frame could possibly need rewriting.
// For 99% of streaming chunks carrying standard text content ("content":"word"),
// this gate returns false in <10ns without any JSON decoding or heap allocations.
// Gate hits are cheap false positives at worst: rewriteStream only rewrites members
// it actually recognizes.
func mightNeedNormalization(data string) bool {
	if data == "" {
		return false
	}
	// Mid-stream error frames need code/type sanitization. The substring only matches
	// structural positions: a JSON string value containing "error" always carries it
	// escaped as \"error\", which does not contain the raw substring.
	if strings.Contains(data, `"error"`) {
		return true
	}
	if !strings.Contains(data, `"choices"`) {
		return false
	}
	// Top-level "choices": null (any spacing) must be rewritten to [].
	if strings.Contains(data, `"choices":null`) || strings.Contains(data, `"choices": null`) {
		return true
	}
	// If reasoning field is present, it might need empty content stripped or array reasoning unpacked.
	if strings.Contains(data, `"reasoning`) {
		return true
	}
	// Without reasoning, normalization is ONLY needed if "content" is formatted as an array.
	idx := strings.Index(data, `"content"`)
	for idx != -1 {
		tail := data[idx+len(`"content"`):]
		colonIdx := strings.IndexByte(tail, ':')
		if colonIdx != -1 {
			afterColon := strings.TrimLeft(tail[colonIdx+1:], " \t\r\n")
			if len(afterColon) > 0 && afterColon[0] == '[' {
				return true
			}
		}
		next := strings.Index(tail, `"content"`)
		if next == -1 {
			break
		}
		idx += len(`"content"`) + next
	}
	return false
}

type kvPair struct {
	key string
	raw json.RawMessage
}

// orderedMembers decodes a JSON object while keeping member order, which a
// plain map would scramble. Raw values are kept as bytes so untouched fields
// are re-emitted exactly as the upstream sent them.
type orderedMembers []kvPair

func (o *orderedMembers) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errUnexpectedShape
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return errUnexpectedShape
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		*o = append(*o, kvPair{key: key, raw: raw})
	}
	_, err = dec.Token() // closing '}'
	return err
}

func (o orderedMembers) encode() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(m.key))
		buf.WriteByte(':')
		buf.Write(m.raw)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func rewriteStream(data string) (string, bool) {
	var top orderedMembers
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		return data, false
	}
	changed := false
	for i := range top {
		switch top[i].key {
		case "choices":
			// 顶层 "choices": null（任意空白间距）改写为 []，满足客户端严格 schema 校验。
			if bytes.Equal(bytes.TrimSpace(top[i].raw), []byte("null")) {
				top[i].raw = json.RawMessage("[]")
				changed = true
				continue
			}
			var choices orderedChoices
			if err := json.Unmarshal(top[i].raw, &choices); err != nil {
				continue
			}
			if !choices.rewriteDelta() {
				continue
			}
			raw, err := choices.encode()
			if err != nil {
				continue
			}
			top[i].raw = raw
			changed = true
		case "error":
			if sanitizeErrorMember(&top[i]) {
				changed = true
			}
		}
	}
	if !changed {
		return data, false
	}
	return string(top.encode()), true
}

// sanitizeErrorMember 清洗顶层 "error" 对象里误导客户端的 code/type 字符串值。
//
// 很多聚合中继站把 429 限流包装成 code:"insufficient_quota"，鉴权故障包装成
// code:"invalid_api_key"；这类值一旦随流中 data 帧直达客户端 SDK（Vercel AI SDK、
// ZCode、Cursor 都会解析 SSE 中的 error 块），就会被判定为用户自身欠费/凭证失效，
// 终止重试甚至禁用账户。清洗规则与 controller 终结路径（clientFacingOpenAIError）
// 对齐：message 原样保留（既是给用户的原始信息，也是 loadbalancer 限流分类的依据）；
// 仅当报文自身带限流措辞时映射为 rate_limit_*，否则统一清洗为 upstream_error。
// 数值 code（如 429）与诚实字段（server_error 等）不动。
func sanitizeErrorMember(m *kvPair) bool {
	trimmed := bytes.TrimSpace(m.raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var errObj orderedMembers
	if err := json.Unmarshal(trimmed, &errObj); err != nil {
		return false
	}
	message := ""
	for _, em := range errObj {
		if em.key == "message" {
			if s, isStr := jsonStringValue(em.raw); isStr {
				message = s
			}
			break
		}
	}
	rateLimited := hasRateLimitWording(message)
	changed := false
	for j := range errObj {
		s, isStr := jsonStringValue(errObj[j].raw)
		if !isStr || s == "" {
			continue
		}
		lower := strings.ToLower(s)
		switch errObj[j].key {
		case "code":
			if memberBlamesClientCredentials(lower) {
				if rateLimited {
					errObj[j].raw = json.RawMessage(`"rate_limit_exceeded"`)
				} else {
					errObj[j].raw = json.RawMessage(`"upstream_error"`)
				}
				changed = true
			}
		case "type":
			if memberBlamesClientCredentials(lower) || strings.Contains(lower, "auth") || strings.Contains(lower, "permission") {
				if rateLimited {
					errObj[j].raw = json.RawMessage(`"rate_limit_error"`)
				} else {
					errObj[j].raw = json.RawMessage(`"upstream_error"`)
				}
				changed = true
			}
		}
	}
	if !changed {
		return false
	}
	m.raw = errObj.encode()
	return true
}

// memberBlamesClientCredentials 命中会把渠道侧故障误报成用户自身问题的值。
// 与 controller.clientFacingOpenAIError 的清洗词表保持同口径。
func memberBlamesClientCredentials(lower string) bool {
	return strings.Contains(lower, "quota") ||
		strings.Contains(lower, "insufficient") ||
		strings.Contains(lower, "invalid_api_key") ||
		strings.Contains(lower, "account_deactivated")
}

// hasRateLimitWording 判断错误报文是否自带瞬时限流措辞。
// 与 loadbalancer.hasExplicitRateLimitSignal 的消息词表保持同口径（此处仅做帧内判断，
// 权威分类仍由 loadbalancer 基于完整错误对象执行）。
func hasRateLimitWording(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "tpm/rpm") ||
		strings.Contains(lower, "并发请求数限制") ||
		strings.Contains(lower, "请求数限制") ||
		strings.Contains(lower, "频次")
}

// orderedChoices is the choices array, kept as raw elements so choices we do
// not touch are written back byte for byte.
type orderedChoices []json.RawMessage

func (c *orderedChoices) UnmarshalJSON(b []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	*c = items
	return nil
}

func (c orderedChoices) encode() ([]byte, error) {
	return json.Marshal([]json.RawMessage(c))
}

func (c *orderedChoices) rewriteDelta() bool {
	changed := false
	for i := range *c {
		var choice orderedMembers
		if err := json.Unmarshal((*c)[i], &choice); err != nil {
			continue
		}
		choiceChanged := false
		for j := range choice {
			if choice[j].key != "delta" {
				continue
			}
			var delta orderedMembers
			if err := json.Unmarshal(choice[j].raw, &delta); err != nil {
				continue
			}
			normalized, ok := delta.normalize()
			if !ok {
				continue
			}
			choice[j].raw = normalized.encode()
			choiceChanged = true
		}
		if !choiceChanged {
			continue
		}
		(*c)[i] = choice.encode()
		changed = true
	}
	return changed
}

func (d orderedMembers) normalize() (orderedMembers, bool) {
	deltaChanged := false
	out := make(orderedMembers, 0, len(d)+1)

	var extractedReasoning string
	var extractedText string
	hasArrayContent := false

	// First pass: inspect content and reasoning fields for arrays
	for _, m := range d {
		trimmed := bytes.TrimSpace(m.raw)
		if m.key == "content" && len(trimmed) > 0 && trimmed[0] == '[' {
			r, t := dto.ExtractArrayContent(trimmed)
			extractedReasoning += r
			extractedText += t
			hasArrayContent = true
			deltaChanged = true
			continue // Do not include raw array content
		}
		if (m.key == "reasoning_content" || m.key == "reasoning") && len(trimmed) > 0 && trimmed[0] == '[' {
			r, t := dto.ExtractArrayContent(trimmed)
			// reasoning 字段里的 text 块同样是思考内容：r 与 t 同时存在时两者都必须保留，
			// 不得因 r 非空就丢弃 t（静默丢内容）。
			extractedReasoning += r
			extractedReasoning += t
			deltaChanged = true
			continue // Do not include raw array reasoning
		}
		out = append(out, m)
	}

	if hasArrayContent && extractedText != "" {
		found := false
		for i := range out {
			if out[i].key == "content" {
				if s, isStr := jsonStringValue(out[i].raw); isStr {
					combined, _ := json.Marshal(s + extractedText)
					out[i].raw = combined
				} else {
					combined, _ := json.Marshal(extractedText)
					out[i].raw = combined
				}
				found = true
				break
			}
		}
		if !found {
			rawText, _ := json.Marshal(extractedText)
			out = append(out, kvPair{key: "content", raw: rawText})
		}
	}

	if extractedReasoning != "" {
		found := false
		for i := range out {
			if out[i].key == "reasoning_content" {
				if s, isStr := jsonStringValue(out[i].raw); isStr {
					combined, _ := json.Marshal(s + extractedReasoning)
					out[i].raw = combined
				} else {
					combined, _ := json.Marshal(extractedReasoning)
					out[i].raw = combined
				}
				found = true
				break
			}
		}
		if !found {
			rawReasoning, _ := json.Marshal(extractedReasoning)
			out = append(out, kvPair{key: "reasoning_content", raw: rawReasoning})
		}
	}

	stripped, wasStripped := out.stripEmptyContentOnReasoning()
	if wasStripped {
		out = stripped
		deltaChanged = true
	}

	if !deltaChanged {
		return nil, false
	}
	return out, true
}

// stripEmptyContentOnReasoning reports whether this delta has an empty string
// "content" alongside non-empty reasoning, and returns the delta without it.
func (d orderedMembers) stripEmptyContentOnReasoning() (orderedMembers, bool) {
	hasReasoning := false
	for _, m := range d {
		if m.key != "reasoning_content" && m.key != "reasoning" {
			continue
		}
		if s, isString := jsonStringValue(m.raw); isString && s != "" {
			hasReasoning = true
			break
		}
	}
	if !hasReasoning {
		return nil, false
	}
	out := make(orderedMembers, 0, len(d))
	dropped := false
	for _, m := range d {
		if isEmptyStringMember(m.key, m.raw) {
			dropped = true
			continue
		}
		out = append(out, m)
	}
	if !dropped {
		return nil, false
	}
	return out, true
}

func isEmptyStringMember(key string, raw json.RawMessage) bool {
	if key != "content" {
		return false
	}
	s, isString := jsonStringValue(raw)
	return isString && s == ""
}

// jsonStringValue reports the value only when raw really is a JSON string.
// Unmarshalling null into a string succeeds without touching the target, so
// the leading quote has to be checked explicitly or null would read as "".
func jsonStringValue(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", false
	}
	return s, true
}

var errUnexpectedShape = &shapeError{}

type shapeError struct{}

func (*shapeError) Error() string { return "unexpected json shape" }
