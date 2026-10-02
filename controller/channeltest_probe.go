package controller

import (
	"encoding/json"
	"math/rand"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/samber/lo"
)

// 渠道测活拟真探针。
//
// 上游已实测部署反测活检测（#138 黑与白 返回「反测活已拦截本次请求：短消息命中测活探针」），
// 触发特征是真实流量里不存在的请求形状：超短 prompt（"hi" / "hello world"，1~2 token）配
// max_tokens=16。线上真实分布（2026-10-02 采样 9652 条对话请求）：prompt p10=163 / p50=551
// token，completion 均值 237 token —— "hi" 连 p10 都够不到，一打一个准。
//
// 因此测活请求必须与真实客户端流量同分布：像样的 system + user 消息、够长的正文、
// 正常的 max_tokens，并在多次探测间轮换内容避免指纹固定。用户 2026-10-02 明确要求
// 按正式站点真实客户端请求构造，不必节省 token。
//
// 注意：探针只打 status=3 的已下线渠道与手动测试按钮，不碰在架健康流量。

// probeUserPrompts 模拟真实开发者求助流量。每条都是自包含的真实问题，
// 长度落在生产 prompt 分布的 p10~p50 区间（163~551 token）。
var probeUserPrompts = []string{
	`我在用 Go 写一个并发抓取器遇到一个诡异问题：50 个 worker 通过带缓冲 channel 分发任务，偶尔会丢几个结果。骨架大概是这样：

func worker(id int, jobs <-chan Job, results chan<- Result) {
	for j := range jobs {
		r, err := process(j)
		if err != nil {
			log.Printf("job %d failed: %v", j.ID, err)
			continue
		}
		results <- r
	}
}

主 goroutine 起 50 个 worker、close(jobs)、wg.Wait() 之后再 close(results)。但只要 process 里发生 panic 且被上层 recover 吞掉，那个 job 就既没有 result 也没有 error 记录。帮我列出所有可能丢结果的路径（包括 recover 位置、channel 关闭时序、worker 提前 return），然后给一个能保证「每个提交的任务要么有 Result 要么有 Err」的改造写法，最好带完整可编译的示例。`,
	`帮我看一个 PostgreSQL 慢查询。表结构大约 4000 万行，查询长这样：

SELECT u.id, u.name, count(o.id) AS order_count
FROM users u
LEFT JOIN orders o ON o.user_id = u.id AND o.created_at >= now() - interval '30 days'
WHERE u.status = 'active'
GROUP BY u.id, u.name
HAVING count(o.id) > 3
ORDER BY order_count DESC
LIMIT 50;

explain analyze 显示对 orders 做了全表顺序扫描，即使 user_id+created_at 上有复合索引也没用。我已经跑过 analyze 了。问题：1) 为什么复合索引没被选上；2) 这个查询有没有更好的改写方式（比如先聚合再 join）；3) 如果 orders 按月分区，对这条查询有多大收益？请给出具体的索引 DDL 和改写后的 SQL。`,
	`我们的服务在 Kubernetes 里跑，Pod 内存会一直涨到 limit 然后被 OOMKill，重启后又慢慢涨上去，大约 6 小时一个周期。这是一个 Go 1.25 的 HTTP 服务，用了 Gin、gorm（Postgres 驱动）和一个 websocket 长连接网关。pprof heap 的 Top 里 gorm 的 prepared statement 缓存占了 400MB+，websocket 连接对象也有 100MB 左右。

我已经确认：1) 连接数稳定在 200 左右不涨；2) goroutine 数量稳定；3) Go 版本 1.25。请帮我分析：gorm 的 stmt cache 是不是应该设置 PrepareStmt=false 或者用容量限制？websocket 断连后对象没释放，除了 SetReadDeadline 还需要在哪些地方显式清理？另外 GOMEMLIMIT 和 GOGC 在这种场景下建议怎么配？给出具体的参数值和理由。`,
	`写一个 Python 脚本处理这种需求：目录下有几千个 CSV，每个文件结构是 timestamp,user_id,action,duration_ms，需要统计每个 user_id 在过去 7 天内每天的平均 duration_ms，输出成 JSON。难点是文件太多不能全读进内存，而且 timestamp 有的是毫秒有的是秒（不同来源格式不一样，需要自动判断）。

请用 pandas 分块读 + 聚合的方式写，注意：1) 毫秒和秒的判断逻辑要稳（不要靠阈值猜，看数量级）；2) 内存要控制在几百 MB 以内；3) 缺失的 action 记录要跳过并计数。最后给一个 pytest 测试用例覆盖两种时间戳格式混在一起的场景。`,
	`帮我排查一个前端内存泄漏。React 18 + Vite 项目，有个实时数据面板，用 setInterval 每 2 秒拉一次数据 setState。切走页面后组件卸载了，但 Chrome DevTools 的 memory 时间线显示堆还在涨，Detached HTMLSpanElement 越积越多。

我怀疑是这几个地方：1) useEffect 返回的清理函数里只 clear 了 interval，没处理进行中的 fetch；2) 有个 ECharts 实例在 resize listener 里被引用着没 dispose；3) WebSocket 的 onmessage 闭包捕获了大对象。请按可能性排序，给出每一处的标准修复写法（带 cleanup 代码），并告诉我用 Chrome 的 Allocation instrumentation on timeline 怎么验证修好了。`,
	`写一个 shell 脚本做日志轮转后的磁盘回收：/var/log/app/ 下有按日期命名的日志 app-2026-10-01.log 这种，要求保留最近 14 天，更早的先 gzip 压缩，压缩后超过 30 天的删除。有几个坑要处理：1) 文件名里的日期要解析出来判断，不要用 mtime（有些文件会被 touch 过）；2) 压缩必须原子（先写临时文件再 mv），避免轮转脚本同时写入时压到一半；3) 要处理磁盘满时 gzip 失败的情况并告警但不退出；4) 用 flock 防止 cron 重叠执行。给完整脚本，shellcheck 干净。`,
}

// probeSystemPrompts 与真实客户端 system prompt 同形：定位角色 + 行为约束，不超长。
var probeSystemPrompts = []string{
	"You are a senior software engineer. Give concrete, working code first, then explain the key trade-offs in a few sentences. Point out edge cases the user may have missed.",
	"You are an experienced on-call engineer helping debug production issues. Be precise, ask no follow-up questions, and prefer concrete commands and config snippets over generic advice.",
	"You are a helpful programming assistant. Answer in the same language as the user. Use fenced code blocks with language tags, and keep prose short.",
}

// probeMaxTokens 拟真 completion 上限。真实流量 completion 均值 237 token，
// 真实客户端实际配的上限远大于此，取一个正常客户端会配的值。
const probeMaxTokens = uint(1024)

// probeReasoningMaxTokens 思考型模型的 completion 上限：思考本身要吃掉配额，
// 给太小会出现「思考耗尽配额、正文为空」的假失败。
const probeReasoningMaxTokens = uint(4096)

func probePick[T any](pool []T) T {
	return pool[rand.Intn(len(pool))]
}

// probeSystemMessage 返回 OpenAI Messages 形态的 system 消息。
func probeSystemMessage() dto.Message {
	return dto.Message{Role: "system", Content: probePick(probeSystemPrompts)}
}

// probeResponsesInstructions 返回 Responses 端点的 instructions 字段（JSON 字符串）。
// 真实 Codex CLI 把系统指令放在顶层 instructions、input 里只放对话轮次 ——
// v29.15 一度把 system 塞进 input 数组，那不是任何真实客户端的形状，对
// codex 型上游是否被接受也没验证过；对齐真实形状后该风险不复存在。
func probeResponsesInstructions() json.RawMessage {
	raw, _ := json.Marshal(probePick(probeSystemPrompts))
	return raw
}

// probeResponsesInput 构造 Responses 端点的拟真 Input：仅 user 轮次，
// 系统指令走顶层 instructions（见上）。
func probeResponsesInput() json.RawMessage {
	payload := []map[string]any{
		{"role": "user", "content": probePick(probeUserPrompts)},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// buildRealisticChatProbe 按端点类型构造拟真测活请求。
// 所有聊天族端点共用同一组 prompt 池，保证多次探测之间内容轮换。
func buildRealisticChatProbe(model string, endpointType constant.EndpointType, isStream bool) dto.Request {
	userContent := probePick(probeUserPrompts)

	switch endpointType {
	case constant.EndpointTypeAnthropic:
		return &dto.ClaudeRequest{
			Model:     model,
			System:    probePick(probeSystemPrompts),
			Stream:    lo.ToPtr(isStream),
			MaxTokens: lo.ToPtr(probeMaxTokensForModel(model)),
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: userContent},
			},
		}
	case constant.EndpointTypeGemini:
		return &dto.GeminiChatRequest{
			SystemInstructions: &dto.GeminiChatContent{
				Role:  "user",
				Parts: []dto.GeminiPart{{Text: probePick(probeSystemPrompts)}},
			},
			Contents: []dto.GeminiChatContent{
				{Role: "user", Parts: []dto.GeminiPart{{Text: userContent}}},
			},
			GenerationConfig: dto.GeminiChatGenerationConfig{
				MaxOutputTokens: lo.ToPtr(uint(3000)),
			},
		}
	case constant.EndpointTypeOpenAIResponse:
		// Compact 端点由 buildTestRequest 自行构造 CompactionRequest，不经过这里。
		return &dto.OpenAIResponsesRequest{
			Model:        model,
			Instructions: probeResponsesInstructions(),
			Input:        probeResponsesInput(),
			Stream:       lo.ToPtr(isStream),
		}
	default:
		// OpenAI chat completions（含自动检测兜底路径）
		req := &dto.GeneralOpenAIRequest{
			Model:     model,
			Stream:    lo.ToPtr(isStream),
			Messages:  []dto.Message{probeSystemMessage(), {Role: "user", Content: userContent}},
			MaxTokens: lo.ToPtr(probeMaxTokensForModel(model)),
		}
		if isStream {
			req.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
		}
		return req
	}
}

// 拟真非聊天端点载荷。这些端点没有「对话形状」可伪装，
// 但 input/prompt 至少要是一段真实内容而非占位词。
const (
	probeEmbeddingInput = `new-api is a self-hosted LLM API gateway that routes requests across multiple upstream providers, applies per-channel rate limits, and records token usage for billing. The health checker sends a small number of realistic probes to disabled channels so they can rejoin rotation automatically once their upstream recovers.`
	probeImagePrompt    = `A photorealistic wide shot of a wooden workbench in a warm workshop at dusk, a laptop showing a terminal with green text, scattered hand tools, shallow depth of field, natural window light, 50mm lens`
	probeRerankQuery    = `How do I prevent goroutine leaks when a consumer stops reading from a channel in Go?`
)

// probeRerankDocs 切片不能进 const 块，单独 var。
var probeRerankDocs = []any{
	"A goroutine leak happens when a goroutine blocks forever on a channel send or receive because the other side has gone away. Use context cancellation or a done channel to unblock it.",
	"Buffered channels let senders proceed without a ready receiver up to the buffer capacity, but they do not prevent leaks once the buffer is full.",
	"Channel closing semantics: only the sender should close a channel, and a receive on a closed channel returns the zero value immediately.",
}

// probeMaxTokensForModel 按模型形状决定 completion 上限：思考型模型必须留足
// 思考配额，否则会出现「配额被思考耗尽、正文为空」的假失败，把健康渠道误判为故障。
// o 系列判定复用 dto.IsOpenAIReasoningOModel（前缀语义），不另造一套包含匹配。
func probeMaxTokensForModel(model string) uint {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "thinking") || strings.Contains(lower, "-high") ||
		dto.IsOpenAIReasoningOModel(model) {
		return probeReasoningMaxTokens
	}
	return probeMaxTokens
}
