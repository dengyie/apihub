package loadbalancer

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// 通用参数裁剪：不同上游对可选参数的支持程度不同（如 thinking、
// reasoning_effort、stream_options 等），不支持时报 400。
// 本模块按渠道维护需要裁剪的参数列表：
//  1. 配置文件中手动指定（strip_params）
//  2. 运行时从 400 错误中自动学习参数名
//
// 对标 CPA 的 payload.filter，但粒度是按渠道而非按模型。

var (
	paramStripMu      sync.RWMutex
	paramStripConfig  = make(map[int]map[string]struct{})
	paramStripLearned = make(map[int]map[string]struct{})
)

// SetParamStripConfig 从配置加载手动指定的裁剪规则
// cfg: channelID -> 参数名列表
func SetParamStripConfig(cfg map[int][]string) {
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	paramStripConfig = make(map[int]map[string]struct{}, len(cfg))
	for id, params := range cfg {
		set := make(map[string]struct{}, len(params))
		for _, p := range params {
			if n := normalizeParamName(p); n != "" {
				set[n] = struct{}{}
			}
		}
		paramStripConfig[id] = set
	}
}

// SetThinkingStripChannels 兼容旧接口：设置需要裁剪 thinking 的渠道
func SetThinkingStripChannels(ids []int) {
	cfg := make(map[int][]string, len(ids))
	for _, id := range ids {
		cfg[id] = []string{"thinking"}
	}
	// 合并而非覆盖：保留已有的其他参数规则
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	for id, params := range cfg {
		set := paramStripConfig[id]
		if set == nil {
			set = make(map[string]struct{})
			paramStripConfig[id] = set
		}
		for _, p := range params {
			if n := normalizeParamName(p); n != "" {
				set[n] = struct{}{}
			}
		}
	}
}

// GetStripParams 返回某渠道需要裁剪的参数名列表（已去重）
func GetStripParams(channelID int) []string {
	if !Enabled() || channelID <= 0 {
		return nil
	}
	paramStripMu.RLock()
	defer paramStripMu.RUnlock()
	var out []string
	for _, set := range []map[string]struct{}{paramStripConfig[channelID], paramStripLearned[channelID]} {
		for p := range set {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ShouldStripThinking 兼容旧接口：该渠道是否需要裁剪 thinking
func ShouldStripThinking(channelID int) bool {
	return slices.Contains(GetStripParams(channelID), "thinking")
}

// MarkParamUnsupported 标记某渠道不支持某参数（自动学习）
func MarkParamUnsupported(channelID int, param string) {
	param = normalizeParamName(param)
	if param == "" || !Enabled() || channelID <= 0 {
		return
	}
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	set := paramStripLearned[channelID]
	if set == nil {
		set = make(map[string]struct{})
		paramStripLearned[channelID] = set
	}
	set[param] = struct{}{}
}

// MarkThinkingUnsupported 兼容旧接口
func MarkThinkingUnsupported(channelID int) {
	MarkParamUnsupported(channelID, "thinking")
}

func normalizeParamName(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// 如果全是纯大写（如 THINKING），直接转为小写
	hasLower := false
	for _, r := range p {
		if unicode.IsLower(r) {
			hasLower = true
			break
		}
	}
	if !hasLower {
		return strings.ToLower(p)
	}

	// 转换 PascalCase/camelCase 到 snake_case（例如 ReasoningEffort -> reasoning_effort）
	var b strings.Builder
	runes := []rune(p)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsUpper(r) {
			if i > 0 && runes[i-1] != '_' && unicode.IsLower(runes[i-1]) {
				b.WriteRune('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// 常见 400 参数不支持错误的模式，用于提取参数名
var unsupportedParamPatterns = []*regexp.Regexp{
	// "thinking" is not supported on /v1/chat/completions
	regexp.MustCompile(`["']([a-zA-Z_][a-zA-Z0-9_]*)["']\s+is not supported`),
	// Unsupported parameter: 'reasoning_effort'
	regexp.MustCompile(`[Uu]nsupported parameter:\s*["']?([a-zA-Z_][a-zA-Z0-9_]*)["']?`),
	// Unrecognized request argument supplied: stream_options
	regexp.MustCompile(`[Uu]nrecognized request argument supplied:\s*["']?([a-zA-Z_][a-zA-Z0-9_]*)["']?`),
	// Additional properties are not allowed ('foo' was unexpected)
	regexp.MustCompile(`\(\s*["']([a-zA-Z_][a-zA-Z0-9_]*)["']\s+was unexpected\s*\)`),
	// does not support parameter "bar"
	regexp.MustCompile(`does not support (?:the |parameter\s+)?["']?([a-zA-Z_][a-zA-Z0-9_]*)["']?`),
	// field ReasoningEffort invalid, should be one of: ... (SenseNova / 商汤系)
	regexp.MustCompile(`(?i)field\s+['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?\s+invalid`),
	// parameter 'xxx' is invalid / 'xxx' is invalid
	regexp.MustCompile(`(?i)(?:parameter\s+)?['"]([a-zA-Z_][a-zA-Z0-9_]*)['"]\s+is invalid`),
	// unknown parameter / unknown field
	regexp.MustCompile(`(?i)unknown\s+(?:parameter|field):\s*['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?`),
	// invalid parameter: xxx
	regexp.MustCompile(`(?i)invalid\s+parameter:\s*['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?`),
	// parameter xxx is not supported
	regexp.MustCompile(`(?i)parameter\s+['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?\s+is not supported`),
	// extra fields not permitted ... loc: ['body', 'xxx']
	regexp.MustCompile(`(?i)extra fields not permitted.*loc.*?['"]([a-zA-Z_][a-zA-Z0-9_]*)['"]`),
}

// IsParamNotSupportedError 判断是否为"参数不支持"的 400 错误，
// 是则返回参数名。支持 thinking/reasoning_effort/stream_options 等。
func IsParamNotSupportedError(err *types.NewAPIError) (string, bool) {
	if err == nil || err.StatusCode != 400 {
		return "", false
	}
	msg := err.Error()
	for _, re := range unsupportedParamPatterns {
		if m := re.FindStringSubmatch(msg); m != nil {
			return normalizeParamName(m[1]), true
		}
	}
	return "", false
}

// IsThinkingNotSupportedError 兼容旧接口
func IsThinkingNotSupportedError(err *types.NewAPIError) bool {
	p, ok := IsParamNotSupportedError(err)
	return ok && p == "thinking"
}

// IsCurfewError 判断是否为"宵禁"类 403 错误（00:00-8:00 服务不可用）。
// 这类渠道需要熔断到早 8 点，而不是常规的几分钟冷却。
func IsCurfewError(err *types.NewAPIError) bool {
	if err == nil || err.StatusCode != 403 {
		return false
	}
	msg := err.Error()
	// provider_code=system_curfew 或中文"宵禁"提示
	return strings.Contains(msg, "system_curfew") || strings.Contains(msg, "宵禁")
}

// IsEOLError 判断是否为模型已下线 (End of Life) 或永久不可用错误（410 Gone 等）。
// 这类错误表示该渠道上的该模型已永久退役，应立即熔断该渠道并避免向下游客户端直接透传 410。
func IsEOLError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode == 410 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "end of life") ||
		strings.Contains(msg, "no longer available") ||
		strings.Contains(msg, "has reached its end of life")
}

// CurfewEndTime 计算宵禁结束时间（上海时间早 8 点）。
// 如果现在已过早 8 点，返回次日早 8 点。
func CurfewEndTime(now time.Time) time.Time {
	// 上海时区
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	nowShanghai := now.In(loc)
	// 当天早 8 点
	end := time.Date(nowShanghai.Year(), nowShanghai.Month(), nowShanghai.Day(), 8, 0, 0, 0, loc)
	if !nowShanghai.Before(end) {
		// 已过早 8 点，取次日
		end = end.Add(24 * time.Hour)
	}
	return end
}
