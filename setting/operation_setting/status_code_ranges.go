package operation_setting

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type StatusCodeRange struct {
	Start int
	End   int
}

// statusCodeRules 是状态码规则的整体不可变快照。
//
// 这些规则由管理端选项在运行期改写（Automatic*FromString），却被中继热路径
// 读取（每个失败请求都要过 DecideRelayRetry）。此前它们是裸包级变量：写侧虽
// 在 OptionMapRWMutex 之内，热路径读侧完全不取锁，是一个真实的数据竞争
// （2026-09-30 review 发现）。改为「不可变快照 + atomic.Pointer」：写侧构造
// 新快照后 CAS 替换，读侧无锁加载，与 loadbalancer 的 currentPolicy 同构。
type statusCodeRules struct {
	disableRanges    []StatusCodeRange
	retryRanges      []StatusCodeRange
	alwaysSkipStatus map[int]struct{}
	alwaysSkipCodes  map[types.ErrorCode]struct{}
}

// 默认重试区间：1xx、3xx、4xx（除 400/408）、全部 5xx；2xx 不重试。
//
// 上游遗留实现对 504/524 永不重试（alwaysSkipStatus 原本硬编码这两个码），
// 理由是 origin 已等待很久、重试会翻倍客户端等待。这里刻意背离该约定，依据
// 是 2026-09-30 生产日志分析（v29.3 部署后 1 小时 15 次终态 502）：
//
//  1. 本部署的上游普遍在 Cloudflare 之后，慢上游的失败形态恰是 CF 网关超时
//     504/524——恰恰是「换个渠道立刻就好」的那类故障，不重试等于把一次可恢复
//     的失败直接打给客户端；
//  2. 该决定原本唯一的代价是双倍等待，而 v29.3 的「客户端已断开即停止重试」
//     守卫已经把这个代价封了顶：客户端离开后循环不再烧任何上游调用；
//  3. 每次命中 IsUpstreamRelayError 都会立即熔断该渠道，所以同一个慢渠道在
//     冷却期内只会坑到第一个请求，不会成为持续性尾巴。
func defaultStatusCodeRules() *statusCodeRules {
	return &statusCodeRules{
		disableRanges: []StatusCodeRange{{Start: 401, End: 401}},
		retryRanges: []StatusCodeRange{
			{Start: 100, End: 199},
			{Start: 300, End: 399},
			{Start: 401, End: 407},
			{Start: 409, End: 499},
			{Start: 500, End: 599},
		},
		// 保留机制但默认为空：不再有「任何启发式都拦不住的必跳过状态码」。
		// 504/524 已移入常规重试区间，见上方注释。
		alwaysSkipStatus: map[int]struct{}{},
		alwaysSkipCodes: map[types.ErrorCode]struct{}{
			types.ErrorCodeBadResponseBody: {},
		},
	}
}

var statusCodeRulesPtr atomic.Pointer[statusCodeRules]

func init() {
	statusCodeRulesPtr.Store(defaultStatusCodeRules())
}

func loadStatusCodeRules() *statusCodeRules {
	if rules := statusCodeRulesPtr.Load(); rules != nil {
		return rules
	}
	return defaultStatusCodeRules()
}

func AutomaticDisableStatusCodesToString() string {
	return statusCodeRangesToString(loadStatusCodeRules().disableRanges)
}

func AutomaticDisableStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	storeStatusCodeRules(func(rules *statusCodeRules) { rules.disableRanges = ranges })
	return nil
}

func ShouldDisableByStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(loadStatusCodeRules().disableRanges, code)
}

func AutomaticRetryStatusCodesToString() string {
	return statusCodeRangesToString(loadStatusCodeRules().retryRanges)
}

func AutomaticRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	storeStatusCodeRules(func(rules *statusCodeRules) { rules.retryRanges = ranges })
	return nil
}

func IsAlwaysSkipRetryStatusCode(code int) bool {
	_, exists := loadStatusCodeRules().alwaysSkipStatus[code]
	return exists
}

func IsAlwaysSkipRetryCode(errorCode types.ErrorCode) bool {
	_, exists := loadStatusCodeRules().alwaysSkipCodes[errorCode]
	return exists
}

func ShouldRetryByStatusCode(code int) bool {
	rules := loadStatusCodeRules()
	if _, exists := rules.alwaysSkipStatus[code]; exists {
		return false
	}
	return shouldMatchStatusCodeRanges(rules.retryRanges, code)
}

// storeStatusCodeRules 基于当前快照应用 mut 后原子替换。CAS 循环而非直写，
// 让并发测试里多写方也不丢更新。
func storeStatusCodeRules(mut func(rules *statusCodeRules)) {
	for {
		old := loadStatusCodeRules()
		updated := *old
		mut(&updated)
		if statusCodeRulesPtr.CompareAndSwap(old, &updated) {
			return
		}
	}
}

func statusCodeRangesToString(ranges []StatusCodeRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, strconv.Itoa(r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func shouldMatchStatusCodeRanges(ranges []StatusCodeRange, code int) bool {
	if code < 100 || code > 599 {
		return false
	}
	for _, r := range ranges {
		if code < r.Start {
			return false
		}
		if code <= r.End {
			return true
		}
	}
	return false
}

func ParseHTTPStatusCodeRanges(input string) ([]StatusCodeRange, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	input = strings.NewReplacer("，", ",").Replace(input)
	segments := strings.Split(input, ",")

	var ranges []StatusCodeRange
	var invalid []string

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		r, err := parseHTTPStatusCodeToken(seg)
		if err != nil {
			invalid = append(invalid, seg)
			continue
		}
		ranges = append(ranges, r)
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid http status code rules: %s", strings.Join(invalid, ", "))
	}
	if len(ranges) == 0 {
		return nil, nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})

	merged := []StatusCodeRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}

	return merged, nil
}

func parseHTTPStatusCodeToken(token string) (StatusCodeRange, error) {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, " ", "")
	if token == "" {
		return StatusCodeRange{}, fmt.Errorf("empty token")
	}

	if strings.Contains(token, "-") {
		parts := strings.Split(token, "-")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return StatusCodeRange{}, fmt.Errorf("invalid range token: %s", token)
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range start: %s", token)
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range end: %s", token)
		}
		if start > end {
			return StatusCodeRange{}, fmt.Errorf("range start > end: %s", token)
		}
		if start < 100 || end > 599 {
			return StatusCodeRange{}, fmt.Errorf("range out of bounds: %s", token)
		}
		return StatusCodeRange{Start: start, End: end}, nil
	}

	code, err := strconv.Atoi(token)
	if err != nil {
		return StatusCodeRange{}, fmt.Errorf("invalid status code: %s", token)
	}
	if code < 100 || code > 599 {
		return StatusCodeRange{}, fmt.Errorf("status code out of bounds: %s", token)
	}
	return StatusCodeRange{Start: code, End: code}, nil
}
