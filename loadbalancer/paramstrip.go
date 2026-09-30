package loadbalancer

import (
	"fmt"
	"net/http"
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
	msgLower := strings.ToLower(msg)

	// 特殊识别 reasoning_effort 等级/参数不支持错误：
	// 例如：level "max" not supported, valid levels: low, medium, high
	if strings.Contains(msgLower, "valid levels:") ||
		(strings.Contains(msgLower, "level") && strings.Contains(msgLower, "not supported") && (strings.Contains(msgLower, "low") || strings.Contains(msgLower, "medium") || strings.Contains(msgLower, "high") || strings.Contains(msgLower, "max") || strings.Contains(msgLower, "xhigh"))) ||
		regexp.MustCompile(`(?i)level\s+["']?[a-zA-Z0-9_]+["']?\s+(?:is\s+)?not supported`).MatchString(msg) {
		return "reasoning_effort", true
	}

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

// IsUpstreamQuotaError 判断是否为上游中继站或渠道自身的额度耗尽/欠费错误（通常返回 400/402/403/429）。
// 很多聚合中继站（如 One-API、New-API 等）在上游额度不足时会返回 400 且附带 "credit insufficient balance"
// 或 "insufficient_user_quota"，这属于渠道侧可用性故障而非客户端 Bad Request，应触发换渠道重试与熔断。
func IsUpstreamQuotaError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "credit insufficient balance") ||
		strings.Contains(msg, "insufficient_user_quota") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "insufficient balance") ||
		strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "your credit balance is too low") ||
		strings.Contains(msg, "user quota not enough") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "balance is not enough") ||
		strings.Contains(msg, "quota_exceeded") ||
		strings.Contains(msg, "user_quota_exhausted") ||
		strings.Contains(msg, "account_deactivated") ||
		strings.Contains(msg, "用量上限") ||
		strings.Contains(msg, "今日已用完") ||
		strings.Contains(msg, "额度已用完") ||
		strings.Contains(msg, "额度不足") ||
		strings.Contains(msg, "余额不足") ||
		strings.Contains(msg, "entitlement exhausted") ||
		strings.Contains(msg, "reached your weekly") ||
		strings.Contains(msg, "reached your daily") {
		return true
	}
	if oe, ok := err.RelayError.(types.OpenAIError); ok {
		codeStr := strings.ToLower(fmt.Sprintf("%v", oe.Code))
		typeStr := strings.ToLower(oe.Type)
		if strings.Contains(codeStr, "insufficient") || strings.Contains(codeStr, "quota") ||
			strings.Contains(typeStr, "insufficient") || strings.Contains(typeStr, "quota") {
			return true
		}
	}
	return false
}

// IsUpstreamRoutingError 判断是否为上游网关会话路由头缺失错误（通常返回 400）。
// 例如部分中间层中继（如 ioll.pp.ua）未正确透传 x-opencode-session / x-opencode-* 等请求头，
// 导致目标上游（如 OpenCode）拒绝服务并返回 "Request is missing x-opencode-session and cannot be routed efficiently."
// 这属于渠道代理配置缺陷而非客户端错误，应触发换渠道重试与熔断。
func IsUpstreamRoutingError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "x-opencode-session") ||
		strings.Contains(msg, "missingsessionid") ||
		strings.Contains(msg, "cannot be routed efficiently")
}

// IsThinkingModeHistoryError 判断是否为上游思考模式历史消息校验错误（通常返回 400）。
// 例如 SiliconFlow 等上游在 thinking mode 下严格要求多轮对话中历史 assistant 消息必须回传 reasoning_content，
// 而标准客户端 SDK（如 ZCode、OpenCode 等）历史消息未持久化该字段，导致特定渠道拒绝请求。
// 这属于渠道特性不兼容/上游校验错误，应触发换渠道重试与熔断隔离。
func IsThinkingModeHistoryError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return (strings.Contains(msg, "reasoning_content") && strings.Contains(msg, "thinking mode")) ||
		strings.Contains(msg, "thinking mode must be passed back") ||
		strings.Contains(msg, "content[].thinking in the thinking mode must be passed back")
}

// IsUpstreamPermissionError 判断是否为上游渠道权限/分组无权访问/TokenPlan不支持等错误（通常返回 403 或 404）。
// 例如聚合中继站（One-API/New-API 等）返回 "无权访问 按量分组 分组"、"user_group_no_permission"、
// "当前分组本时段不可调用"、"当前分组无可用渠道"，或订阅计划返回 "deepseek-v4-flash is not supported by TokenPlan"。
// 这类错误属于上游渠道配置、分组或套餐权限缺陷，而非下游客户端认证失败。
// 应触发换渠道重试与即时熔断隔离，并在透传保护中映射为 502 Bad Gateway 避免客户端终止会话。
func IsUpstreamPermissionError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode == http.StatusForbidden {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "无权访问") ||
		strings.Contains(msg, "按量分组") ||
		strings.Contains(msg, "user_group_no_permission") ||
		strings.Contains(msg, "group_no_permission") ||
		strings.Contains(msg, "not authorized for this group") ||
		strings.Contains(msg, "not supported by tokenplan") ||
		strings.Contains(msg, "not supported by token plan") ||
		strings.Contains(msg, "is not supported by tokenplan") ||
		strings.Contains(msg, "is not supported by token plan") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "permission_denied") ||
		strings.Contains(msg, "operation not allowed") ||
		strings.Contains(msg, "your account is not authorized") ||
		(strings.Contains(msg, "当前分组") && (strings.Contains(msg, "不可调用") || strings.Contains(msg, "无可用渠道") || strings.Contains(msg, "无权"))) {
		return true
	}
	if oe, ok := err.RelayError.(types.OpenAIError); ok {
		codeStr := strings.ToLower(fmt.Sprintf("%v", oe.Code))
		typeStr := strings.ToLower(oe.Type)
		msgStr := strings.ToLower(oe.Message)
		if strings.Contains(codeStr, "group_no_permission") ||
			strings.Contains(codeStr, "user_group_no_permission") ||
			strings.Contains(codeStr, "permission_denied") ||
			strings.Contains(typeStr, "group_no_permission") ||
			strings.Contains(typeStr, "permission_denied") ||
			strings.Contains(msgStr, "无权访问") ||
			strings.Contains(msgStr, "not supported by tokenplan") ||
			strings.Contains(msgStr, "is not supported by tokenplan") {
			return true
		}
	} else if poe, ok := err.RelayError.(*types.OpenAIError); ok && poe != nil {
		codeStr := strings.ToLower(fmt.Sprintf("%v", poe.Code))
		typeStr := strings.ToLower(poe.Type)
		msgStr := strings.ToLower(poe.Message)
		if strings.Contains(codeStr, "group_no_permission") ||
			strings.Contains(codeStr, "user_group_no_permission") ||
			strings.Contains(codeStr, "permission_denied") ||
			strings.Contains(typeStr, "group_no_permission") ||
			strings.Contains(typeStr, "permission_denied") ||
			strings.Contains(msgStr, "无权访问") ||
			strings.Contains(msgStr, "not supported by tokenplan") ||
			strings.Contains(msgStr, "is not supported by tokenplan") {
			return true
		}
	}
	return false
}

// IsUpstreamRelayError 判断是否为上游聚合中继站（如 One-API、New-API 等）自身代理转发失败的报错（通常返回 400/404/500/502 等）。
// 很多聚合中继站向上游发送请求失败时，会将上游状态码包装并返回 HTTP 400，带有 "来自上游渠道的报错: bad response status code 400"
// 或 "bad response status code"、"unknown provider for model"、"[上游问题]"、"[渠道出错]" 等特征。
// 这属于渠道代理侧的可用性故障，而非客户端请求本身格式错误（Bad Request），应触发换渠道重试与即时熔断。
func IsUpstreamRelayError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	// 429 或限流/并发超限属于上游频次/并发瞬时限制，绝非中继代理网关故障，不应归为中继代理失效
	if err.StatusCode == http.StatusTooManyRequests || IsUpstreamRateLimitError(err) {
		return false
	}
	// 若为参数不支持类的 400 错误（如 thinking、reasoning_effort 等），优先由参数裁剪机制处理，不计入上游中继失效熔断
	if _, ok := IsParamNotSupportedError(err); ok {
		return false
	}
	if IsThinkingModeHistoryError(err) {
		return true
	}
	if IsUpstreamPermissionError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "来自上游渠道") ||
		strings.Contains(msg, "bad response status code") ||
		strings.Contains(msg, "bad_response_status_code") ||
		strings.Contains(msg, "上游问题") ||
		strings.Contains(msg, "渠道出错") ||
		strings.Contains(msg, "upstream request failed") ||
		strings.Contains(msg, "error from provider") ||
		strings.Contains(msg, "unknown provider for model") ||
		strings.Contains(msg, "no available channel for model") ||
		strings.Contains(msg, "reasoning_content") ||
		strings.Contains(msg, "thinking mode must be passed back") {
		return true
	}
	if oe, ok := err.RelayError.(types.OpenAIError); ok {
		typeStr := strings.ToLower(oe.Type)
		if strings.Contains(typeStr, "upstream_error") ||
			strings.Contains(typeStr, "one_api_error") ||
			strings.Contains(typeStr, "new_api_error") {
			return true
		}
	} else if poe, ok := err.RelayError.(*types.OpenAIError); ok && poe != nil {
		typeStr := strings.ToLower(poe.Type)
		if strings.Contains(typeStr, "upstream_error") ||
			strings.Contains(typeStr, "one_api_error") ||
			strings.Contains(typeStr, "new_api_error") {
			return true
		}
	}
	return false
}

// IsUpstreamRateLimitError 判断是否为上游瞬时频次限流或并发超限错误（如 429 Too Many Requests、最多同时处理1个请求、rpm/tpm 超限等）。
// 区别于配额永久耗尽（IsUpstreamQuotaError），限流通常在短时间（几秒到几十秒）后自动解除，
// 不应触发长达数百秒的常规硬熔断，而应采用短冷却（默认 30 秒）并触发换渠道重试，避免健康渠道被过度隔离导致全池瘫痪。
func IsUpstreamRateLimitError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	// 真正的额度/欠费耗尽优先归入 QuotaError 处理
	if IsUpstreamQuotaError(err) {
		return false
	}
	if err.StatusCode == http.StatusTooManyRequests {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "并发请求数限制") ||
		strings.Contains(msg, "最多同时处理") ||
		strings.Contains(msg, "请求数限制") ||
		strings.Contains(msg, "总请求数限制") ||
		strings.Contains(msg, "rpm") ||
		strings.Contains(msg, "tpm") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "超额临时冻结") ||
		strings.Contains(msg, "负载已饱和")
}

// IsUpstreamModelUnavailableError 判断是否为「这个渠道永远不会好」的上游失效。
//
// 与 IsUpstreamRelayError 那一类瞬时故障的区别：那些换个时间/换个渠道就好，
// 熔断一小时后自动恢复即可；这里的两类不会自愈，重试再多次也是同样结果，
// 于是每一次命中都要白白消耗一整轮换渠道重试预算：
//
//  1. 模型映射失效：渠道声明的某个模型在上游已不存在（"模型不存在"、
//     "model not found"、"unknown model"、"no such model"）。生产实测一天 101 次，
//     集中在 4 个渠道上，且默认自动禁用状态码只有 401，这些渠道永远不会下线。
//  2. OAuth 凭据刷新失效："oauth2: cannot fetch token"，refresh token 已被上游
//     撤销或过期，渠道再也无法取得访问令牌。
//
// 判据刻意保守：只认上游明确表达的确定性失效，不含 5xx、限流、额度耗尽——
// 那三类都会随时间自愈，误禁用会把健康渠道踢出池子。
func IsUpstreamModelUnavailableError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "模型不存在") ||
		strings.Contains(msg, "模型不可用") ||
		strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "model_not_found") ||
		strings.Contains(msg, "unknown model") ||
		strings.Contains(msg, "no such model") ||
		// "The model `x` does not exist" 这类措辞只有和 model 同时出现才算数：
		// 单独的 "does not exist" 会把 "session does not exist" 之类的无关 404 一起误伤。
		(strings.Contains(msg, "does not exist") && strings.Contains(msg, "model")) ||
		strings.Contains(msg, "cannot fetch token") {
		return true
	}
	return false
}
