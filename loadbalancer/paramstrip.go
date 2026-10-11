package loadbalancer

// paramstrip.go 只承担**参数裁剪**职责：按渠道（运维配置）与 (渠道, 模型)
// （运行时学习）维护需要裁剪的出站参数，以及「400 参数不支持」这一类错误的
// 识别与参数名提取。
//
// 上游错误分类器（额度/权限/限流/中继故障/模型不可用……）与熔断作用域判定
// 在 errorclass.go——它们曾与本文件混在一起（2026-10 review P3 拆分），
// 分类器是熔断最敏感的部分，必须有独立的家。

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/dengyie/apihub/relaykit/types"
)

// 通用参数裁剪：不同上游对可选参数的支持程度不同（如 thinking、
// reasoning_effort、stream_options 等），不支持时报 400。
// 本模块按渠道维护需要裁剪的参数列表：
//  1. 配置文件中手动指定（strip_params）
//  2. 运行时从 400 错误中自动学习参数名
//
// 对标 CPA 的 payload.filter，但粒度是按渠道而非按模型。

var (
	paramStripMu     sync.RWMutex
	paramStripConfig = make(map[int]map[string]struct{})
	// paramStripLearned 按 **(渠道, 模型)** 存，不按渠道存。
	//
	// 「上游不支持某参数」是关于**那个模型**的观察，不是关于这条渠道的：
	// 渠道 #238 同时挂 gemini-3.8-flash / kimi-k3 / claude-opus-4-8 /
	// glm-5.3-flash / deepseek-v4.1-flash / deepseek-v4-flash 六个模型，
	// 按渠道存会让任一模型的一次误判外溢到另外五个，把它们本该支持的参数
	// 一起裁掉。此前它是这一串错误处理里**唯一**停在渠道级的环节——熔断自
	// v29.14 起是 ScopeModel、isModelScopedAutoDisable 存在的理由也正是
	// 「这个模型坏了 ≠ 这条渠道坏了」。
	//
	// 运维在 yaml 里显式写的 paramStripConfig 仍按渠道：那是人的决定，不是观测。
	paramStripLearned = make(map[paramStripKey]map[string]struct{})
)

// paramStripKey 是学习集合的键。两端必须传**同一个**模型名，否则学到的裁剪
// 永远不会生效（且不会有任何报错）——学习侧传 relayInfo.OriginModelName，
// 出站裁剪侧传 info.OriginModelName，与熔断键同源。
type paramStripKey struct {
	channelID int
	model     string
}

// maxTokensLimitLearned 记录「渠道 × 上游模型」真实的 max_completion_tokens 上限。
//
// 上游对超限请求一律回 400，但报文里通常直接写明它自己的上限
// （"max_completion_tokens is too large: 384000. This model supports at most
// 262144 completion tokens."）。与其每次都先失败一次再重试，不如第一次收到
// 就把这个上限学到手，之后出站前直接钳制——客户端要的长回复一次就成。
//
// key 为 channelID → model → limit。模型级而非渠道级：同一中转站的
// 不同模型上限可以差一个数量级。
var maxTokensLimitLearned sync.Map // map[int]map[string]uint

// maxTokensLimitRe 解析上游回传的 max_completion_tokens 上限。
// 兼容中英文与不同措辞：只认「超限 + 上限数字」这一组语义。
var maxTokensLimitRe = regexp.MustCompile(
	`(?is)max_completion_tokens\s+is\s+too\s+large.*?supports\s+at\s+most\s+(\d+)`)

// ParseMaxCompletionTokensLimit 从上游 400 报文里解析该模型允许的
// max_completion_tokens 上限。未命中返回 (0, false)。
func ParseMaxCompletionTokensLimit(err *types.NewAPIError) (uint, bool) {
	if err == nil {
		return 0, false
	}
	m := maxTokensLimitRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	parsed, perr := strconv.ParseUint(m[1], 10, 64)
	if perr != nil || parsed == 0 {
		return 0, false
	}
	return uint(parsed), true
}

// RecordMaxCompletionTokensLimit 记住该渠道该模型的上限。
// 只在「本次学到的上限比已知更小」时覆盖：上限只会收紧，放宽会让先前
// 按更小上限做的钳制失效。
func RecordMaxCompletionTokensLimit(channelID int, modelName string, limit uint) {
	if channelID <= 0 || modelName == "" || limit == 0 {
		return
	}
	inner, _ := maxTokensLimitLearned.LoadOrStore(channelID, &sync.Map{})
	perModel, ok := inner.(*sync.Map)
	if !ok {
		return
	}
	if prev, loaded := perModel.Load(modelName); loaded {
		if old, ok := prev.(uint); ok && old <= limit {
			return
		}
	}
	perModel.Store(modelName, limit)
}

// GetMaxCompletionTokensLimit 返回该渠道该模型的已知上限；未学到返回 0。
func GetMaxCompletionTokensLimit(channelID int, modelName string) uint {
	if channelID <= 0 || modelName == "" {
		return 0
	}
	inner, ok := maxTokensLimitLearned.Load(channelID)
	if !ok {
		return 0
	}
	perModel, ok := inner.(*sync.Map)
	if !ok {
		return 0
	}
	if v, ok := perModel.Load(modelName); ok {
		if limit, ok := v.(uint); ok {
			return limit
		}
	}
	return 0
}

// ClampMaxCompletionTokens 把请求的上限钳到该渠道该模型的已知上限内。
// limit 为 0（未学到）或请求本身已在上限内时原样返回。
func ClampMaxCompletionTokens(channelID int, modelName string, requested uint) uint {
	limit := GetMaxCompletionTokensLimit(channelID, modelName)
	if limit == 0 || requested <= limit {
		return requested
	}
	return limit
}

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

// GetStripParams 返回某渠道某模型需要裁剪的参数名列表（已去重）。
//
// 运维预置按渠道生效，人写的规则不该被自动学习的作用域收窄；
// 自动学习到的按 (渠道, 模型) 生效，见 paramStripLearned 的注释。
func GetStripParams(channelID int, modelName string) []string {
	if !Enabled() || channelID <= 0 {
		return nil
	}
	paramStripMu.RLock()
	defer paramStripMu.RUnlock()
	key := paramStripKey{channelID: channelID, model: modelName}
	var out []string
	for _, set := range []map[string]struct{}{paramStripConfig[channelID], paramStripLearned[key]} {
		for p := range set {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// MarkParamUnsupported 标记该渠道**该模型**不支持某参数（自动学习）。
func MarkParamUnsupported(channelID int, modelName string, param string) {
	param = normalizeParamName(param)
	if param == "" || !Enabled() || channelID <= 0 {
		return
	}
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	key := paramStripKey{channelID: channelID, model: modelName}
	set := paramStripLearned[key]
	if set == nil {
		set = make(map[string]struct{})
		paramStripLearned[key] = set
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

// 常见 400 参数不支持错误的模式，用于提取参数名。
//
// 每条正则只编译一次（包级变量），两张判据表（400 全量 / 400 之外的严格子集）
// 从同一组变量组合而成——此前两条列表各自内联了一份几乎相同的正则，
// 改措辞要改两处，漏一处就是「400 认得、5xx 不认得」的分叉。
var (
	reParamNotSupportedQuoted = regexp.MustCompile(`["']([a-zA-Z_][a-zA-Z0-9_]*)["']\s+is not supported`)
	// ↑ "thinking" is not supported on /v1/chat/completions

	reUnsupportedParam = regexp.MustCompile(`[Uu]nsupported parameters?(?:\(s\))?:\s*[^a-zA-Z0-9_]{0,2}([a-zA-Z_][a-zA-Z0-9_]*)`)
	// ↑ Unsupported parameter: 'reasoning_effort'
	//   Unsupported parameter(s): `enable_thinking`
	//   ↑ 复数 (s) 与反引号包裹（pydantic / FastAPI 风格，huan666 等中转站用它）。
	//   原先字面量写死 "parameter:"，遇到 "parameter(s):" 直接漏识别——生产实测
	//   渠道 #68 因这一条白烧了 68 次 400。分隔符用「非标识符字符」类匹配，
	//   不枚举引号种类（Go 原始字符串里也无法写反引号）。

	reUnrecognizedArgument = regexp.MustCompile(`[Uu]nrecognized request argument supplied:\s*["']?([a-zA-Z_][a-zA-Z0-9_]*)["']?`)
	// ↑ Unrecognized request argument supplied: stream_options

	reWasUnexpected = regexp.MustCompile(`\(\s*["']([a-zA-Z_][a-zA-Z0-9_]*)["']\s+was unexpected\s*\)`)
	// ↑ Additional properties are not allowed ('foo' was unexpected)

	reDoesNotSupport = regexp.MustCompile(`does not support (?:the |parameter\s+)?["']?([a-zA-Z_][a-zA-Z0-9_]*)["']?`)
	// ↑ does not support parameter "bar"。散文式措辞（SMTP server does not
	//   support STARTTLS 也会命中），只允许出现在 400 全量表里——5xx 报文
	//   内容不受我们控制，这条会提取出完全不是请求参数的名字。

	reFieldInvalid = regexp.MustCompile(`(?i)field\s+['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?\s+invalid`)
	// ↑ field ReasoningEffort invalid, should be one of: ... (SenseNova / 商汤系)。
	//   同样是散文式（"field name invalid" 会命中），仅限 400。

	reParamInvalidQuoted = regexp.MustCompile(`(?i)(?:parameter\s+)?['"]([a-zA-Z_][a-zA-Z0-9_]*)['"]\s+is invalid`)
	// ↑ parameter 'xxx' is invalid / 'xxx' is invalid

	reUnknownParamField = regexp.MustCompile(`(?i)unknown\s+(?:parameter|field):\s*['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?`)
	// ↑ unknown parameter / unknown field

	reInvalidParamColon = regexp.MustCompile(`(?i)invalid\s+parameter:\s*['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?`)
	// ↑ invalid parameter: xxx

	reParamNotSupportedBare = regexp.MustCompile(`(?i)parameter\s+['"]?([a-zA-Z_][a-zA-Z0-9_]*)['"]?\s+is not supported`)
	// ↑ parameter xxx is not supported

	reExtraFieldsLast    = regexp.MustCompile(`(?i)extra fields not permitted.*loc.*['"]([a-zA-Z_][a-zA-Z0-9_]*)['"]\s*,?\s*[\]\)]`)
	reExtraFieldsBracket = regexp.MustCompile(`(?i)extra fields not permitted.*loc.*?\[\s*['"]([a-zA-Z_][a-zA-Z0-9_]*)['"]`)
	// ↑ extra fields not permitted ... loc: ['body', 'xxx']
	//   pydantic v2 的 loc 是**路径数组**：首元素是定位前缀（body/query/...），
	//   末元素才是真正被拒的字段（如 messages.1.reasoning_content 最后的 reasoning_content）。
	//   reExtraFieldsLast 用贪婪匹配精准截取括号前最后一个标识符，彻底防止误抓 body/messages 前缀。
	//   reExtraFieldsBracket 兜底单元素或缺少右括号的边界情况。

	reReasoningEffortLevel = regexp.MustCompile(`(?i)level\s+["']?[a-zA-Z0-9_]+["']?\s+(?:is\s+)?not supported`)
	// ↑ level "max" not supported（reasoning_effort 等级措辞）。
	//   提为包级变量：原先在 IsParamNotSupportedError 里每次调用都重新编译。
)

// unsupportedParamPatterns 是 400 全量判据，顺序不可调换。
var unsupportedParamPatterns = []*regexp.Regexp{
	reParamNotSupportedQuoted,
	reUnsupportedParam,
	reUnrecognizedArgument,
	reWasUnexpected,
	reDoesNotSupport,
	reFieldInvalid,
	reParamInvalidQuoted,
	reUnknownParamField,
	reInvalidParamColon,
		reParamNotSupportedBare,
		reExtraFieldsLast,
		reExtraFieldsBracket,
	}

	// unsupportedParamPatternsStrict 是「报文里明确点名了某个参数」的措辞子集，
	// 即全量表去掉两条散文式措辞（reDoesNotSupport / reFieldInvalid），顺序与
	// 原实现逐位一致。
	//
	// 存在的唯一理由是要在 **400 之外**（422 / 5xx）也认参数错误，而放宽到这些
	// 状态码的前提是「不会把无关故障误判成参数问题」——5xx 报文的内容不受我们
	// 控制，什么都可能出现在里面。于是按「是否强制出现 parameter / field /
	// argument 这类词，或强制要求带引号的参数名 + 校验框架」来划线，每一条都
	// 只可能来自请求校验，不可能是通用故障文案。
	var unsupportedParamPatternsStrict = []*regexp.Regexp{
		reParamNotSupportedQuoted,
		reUnsupportedParam,
		reUnrecognizedArgument,
		reWasUnexpected,
		reUnknownParamField,
		reInvalidParamColon,
		reParamNotSupportedBare,
		reParamInvalidQuoted,
		reExtraFieldsLast,
		reExtraFieldsBracket,
	}

// matchParamPatterns 依次跑一组模式，返回第一个命中的规范化参数名。
func matchParamPatterns(msg string, patterns []*regexp.Regexp) (string, bool) {
	for _, re := range patterns {
		if m := re.FindStringSubmatch(msg); m != nil {
			return normalizeParamName(m[1]), true
		}
	}
	return "", false
}

// isParamValidationStatusCode 判断该状态码是否可能承载「参数校验失败」。
//
// 为什么不能只认 400：参数校验错误的状态码在各家上游极不统一 ——
// OpenAI 风格 400、FastAPI/pydantic 原生 422、以及**中转层把上游 400
// 包成自己的 5xx**。生产实测渠道 #238（ilovecat520-cc）就是最后一种：
// 同一句 `Validation: Unsupported parameter(s): enable_thinking`（原报文用
// 反引号把参数名包起来），直连上游回 500，网关侧于是完全没认出它是参数问题。
//
// 而 5xx 是网关侧故障的状态码，语义上「一定是上游挂了」。把它纳入进来
// 正是本函数存在的意义：这条错误其实是**请求形状问题**，换个渠道/裁掉
// 参数就能过，不该按渠道故障熔断，更不该每个请求白烧一轮。
//
// 仍刻意排除 401/403/404/429：这些码下若出现「不支持某参数」的措辞，
// 真实原因几乎必然是凭据/权限/路由，与裁剪参数无关，放进来只会误伤。
func isParamValidationStatusCode(code int) bool {
	return code == http.StatusBadRequest ||
		code == http.StatusUnprocessableEntity ||
		(code >= 500 && code <= 599)
}

// IsParamNotSupportedError 判断是否为「参数不支持」错误，是则返回参数名。
// 支持 thinking/reasoning_effort/enable_thinking/stream_options 等。
//
// 判据是**报文措辞**而非状态码：同一个校验错误，OpenAI 风格回 400、
// pydantic 原生回 422、中转层包成 5xx 都见过（见 isParamValidationStatusCode）。
// 早先这里第一行写死 `StatusCode != 400` 就返回，把「上游用什么码包装」
// 当成了「上游在说什么」，于是非 400 的上游整条自动学习链路静默失效：
// 不标记、不裁剪，每个请求稳定白烧一轮报错，且永不自愈。
func IsParamNotSupportedError(err *types.NewAPIError) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	msgLower := strings.ToLower(msg)

		// 400 走完整判据（含散文式措辞与 reasoning_effort 等级启发式），
		// 行为与既往逐位一致 —— 那 71 条生产 400 正是靠它自愈的，不能动。
		if err.StatusCode == http.StatusBadRequest {
			// 特殊识别 reasoning_effort 等级/参数不支持错误：
			// 例如：level "max" not supported, valid levels: low, medium, high
			if strings.Contains(msgLower, "valid levels:") ||
				(strings.Contains(msgLower, "level") && strings.Contains(msgLower, "not supported") && (strings.Contains(msgLower, "low") || strings.Contains(msgLower, "medium") || strings.Contains(msgLower, "high") || strings.Contains(msgLower, "max") || strings.Contains(msgLower, "xhigh"))) ||
				reReasoningEffortLevel.MatchString(msg) {
				return "reasoning_effort", true
			}
			if param, ok := matchParamPatterns(msg, unsupportedParamPatterns); ok {
				return param, true
			}
			// 特殊识别非标准报文格式中的 reasoning_content / reasoning 不支持错误（如 messages.1.reasoning_content: extra fields not permitted）：
			if strings.Contains(msgLower, "reasoning_content") &&
				(strings.Contains(msgLower, "not supported") ||
					strings.Contains(msgLower, "unsupported") ||
					strings.Contains(msgLower, "unexpected") ||
					strings.Contains(msgLower, "extra field") ||
					strings.Contains(msgLower, "unrecognized") ||
					strings.Contains(msgLower, "invalid") ||
					strings.Contains(msgLower, "not permitted") ||
					strings.Contains(msgLower, "unknown")) {
				return "reasoning_content", true
			}
			if strings.Contains(msgLower, "reasoning") &&
				!strings.Contains(msgLower, "effort") &&
				!strings.Contains(msgLower, "reasoning_content") &&
				(strings.Contains(msgLower, "not supported") ||
					strings.Contains(msgLower, "unsupported") ||
					strings.Contains(msgLower, "unexpected") ||
					strings.Contains(msgLower, "extra field") ||
					strings.Contains(msgLower, "unrecognized") ||
					strings.Contains(msgLower, "invalid") ||
					strings.Contains(msgLower, "not permitted") ||
					strings.Contains(msgLower, "unknown")) {
				return "reasoning", true
			}
			return "", false
		}

		// 400 之外只认「明确点名参数」的严格子集：5xx 报文不受我们控制，
		// 散文式措辞会把无关故障误判成参数问题。
		if !isParamValidationStatusCode(err.StatusCode) {
			return "", false
		}
		if param, ok := matchParamPatterns(msg, unsupportedParamPatternsStrict); ok {
			return param, true
		}
		if strings.Contains(msgLower, "reasoning_content") &&
			(strings.Contains(msgLower, "extra field") || strings.Contains(msgLower, "not permitted") || strings.Contains(msgLower, "not supported") || strings.Contains(msgLower, "unsupported")) {
			return "reasoning_content", true
		}
		return "", false
}
