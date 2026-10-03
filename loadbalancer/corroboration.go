package loadbalancer

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// corroborationEvictThreshold 超过它就在下一次建键时清扫一次。给得宽松，
// 正常规模（渠道数 × 模型数 × 4 个判据类别）远达不到，只有异常增长才触发。
const corroborationEvictThreshold = 4096

// 「确定性失效」的佐证计数器。
//
// 要解决的问题：自动禁用是**不可逆**动作 —— 渠道一旦被摘出轮转，要靠测活
// 或人工才回来；而判定它的证据却很弱：ShouldDisableChannel 的四条判据全部
// 是启发式的，两条靠对上游自由文本做子串匹配。
//
// 这个不对称是反的。同一套系统里，可逆的熔断要求连续 FailureThreshold 次
// 失败才触发（loadbalancer/tracker.go），不可逆的自动禁用却只要命中一次。
// 于是一次措辞巧合就足以永久摘掉一条健康渠道，而且**线上不会有任何报错**：
// 白名单命中不产生告警，唯一的外发通道是 NotifyRootUser，而它依赖 SMTP。
//
// 生产实测已见过代价：#144 40 分钟成功 39 次、失败 7 次，失败全是「选不出
// 渠道」这一类池状态；另有渠道只因单个模型返回一句
// "The model service rejected this request"（401）就整条下线，另外 9 个
// 从未被测过的模型陪葬。
//
// 这里的做法不是继续补词表（词表永远滞后于上游措辞变化），而是**让误判的
// 代价降下来**：未达阈值的信号一律只熔断、不摘除，交给自愈的熔断器处理；
// 连续/窗口内重复到阈值，才承认这是确定性失效。
//
// 键按 (渠道, 模型, 判据类别) 分桶，**不按报文原文** —— 否则
// "insufficient balance" 与 "Insufficient account balance" 这类同一失效的
// 不同措辞永远凑不齐佐证，等于要求上游每次都用完全相同的措辞才可能被摘掉。
var corroborationRegistry struct {
	mu sync.Mutex
	m  map[string]*corroborationState
}

type corroborationState struct {
	count       int
	windowStart time.Time
}

// 佐证判据类别。同一类别下的信号可以互相佐证，不同类别之间不互相累加 ——
// 「凭据失效」和「这个模型不存在」是两回事，同时各发生一次不构成互相印证。
const (
	CorroborationClassModelUnavailable = "model_unavailable"
	CorroborationClassChannelError      = "channel_error"
	CorroborationClassStatusCode        = "status_code"
	CorroborationClassKeyword           = "keyword"
)

// RecordAutoDisableSignal 记录一次「命中了自动禁用判据」的信号，
// 返回是否已达阈值（调用方才可以执行不可逆的禁用）与当前累计次数。
//
// 累计次数是要给调用方打日志用的：未达阈值而被挡下的那次禁用如果不留痕，
// 整个机制就是静默的 —— 运维看到的是「渠道还在，日志里有错误」，
// 猜不到中间还隔着一道佐证闸门。返回次数让它能只记首次（count==1）。
//
// 阈值为 1 时退化成旧行为（一次即禁），保留这个档位是为了在出问题时能立刻
// 退回原语义，而不是只能靠改代码。
func RecordAutoDisableSignal(channelID int, modelName, class string) (ready bool, count int) {
	policy := GetPolicy()
	if !policy.Enabled {
		return true, 1
	}
	threshold := policy.Default.Breaker.AutoDisableCorroborationThresholdOrDefault()
	if threshold <= 1 {
		return true, 1
	}
	window := time.Duration(policy.Default.Breaker.AutoDisableCorroborationWindowSecondsOrDefault()) * time.Second
	if window <= 0 {
		window = 10 * time.Minute
	}

	key := corroborationKey(channelID, modelName, class)
	now := time.Now()

	corroborationRegistry.mu.Lock()
	defer corroborationRegistry.mu.Unlock()
	if corroborationRegistry.m == nil {
		corroborationRegistry.m = make(map[string]*corroborationState)
	}

	// 与 exhaustionRegistry 同一理由：模型名来自客户端，是无界输入，
	// 不淘汰就是别人用随机模型名把这张表撑爆。
	//
	// 两道闸门互补：先删已过窗口的（正常路径，代价 O(n) 且删得准），仍超上限
	// 再按最旧硬丢（见 evict.go）—— 只做前者的话，一个窗口之内涌入的互不相同
	// 的键一条也删不掉，map 会一直长到攻击停止为止。
	if len(corroborationRegistry.m) > corroborationEvictThreshold {
		for k, st := range corroborationRegistry.m {
			if now.Sub(st.windowStart) > window {
				delete(corroborationRegistry.m, k)
			}
		}
		if len(corroborationRegistry.m) > corroborationCap {
			keys := make([]string, 0, len(corroborationRegistry.m))
			for k := range corroborationRegistry.m {
				keys = append(keys, k)
			}
			dropped := dropOldestWhenOverCap(keys,
				func(st *corroborationState) time.Time { return st.windowStart },
				corroborationRegistry.m, corroborationCap)
			if dropped > 0 {
				log.Printf("loadbalancer: corroboration registry hit hard cap %d, dropped %d oldest entries (key contains a client-supplied model name)",
					corroborationCap, dropped)
			}
		}
	}

	st := corroborationRegistry.m[key]
	if st == nil || now.Sub(st.windowStart) > window {
		st = &corroborationState{windowStart: now}
		corroborationRegistry.m[key] = st
	}
	st.count++
	return st.count >= threshold, st.count
}

// PeekAutoDisableCorroboration 只读地回答「这次命中若计入佐证，是否已经够阈值」，
// **不做任何计数**。返回 true 表示禁用会（立即）执行。
//
// 存在的理由：service.RecordPolicyFailure 跑在 ProcessChannelError **之前**
// （四条路径都是这样，见各自调用点），它要往请求策略事件流里写
// "channel_disable_requested"。计数挪到咽喉点之后，那条标签就成了假话 ——
// 明明最终没禁用，日志里却写着「已请求禁用」。这里让它能在不打扰计数的前提下
// 提前知道结论。
func PeekAutoDisableCorroboration(channelID int, modelName, class string) bool {
	policy := GetPolicy()
	if !policy.Enabled {
		return true
	}
	threshold := policy.Default.Breaker.AutoDisableCorroborationThresholdOrDefault()
	if threshold <= 1 {
		return true
	}
	return CorroborationCount(channelID, modelName, class)+1 >= threshold
}

// corroborationClasses 是判据类别的全集 —— 封闭集合，不是可扩展的。
//
// 复位按**精确键**删除而不是前缀扫描，根因是 modelName 来自客户端：模型名里
// 只要出现一个 "|"，前缀扫就会连兄弟桶一起删（复位 "a" 会把 "a|b" 的计数也
// 清掉）。类别既然是这四个固定值，逐个构造精确键既无歧义，也只要 4 次删除 ——
// 这正是它能被放进成功路径的原因：原先的全表扫描代价随注册表增长，
// 每个成功请求都调一次是不可接受的。
var corroborationClasses = [...]string{
	CorroborationClassModelUnavailable,
	CorroborationClassChannelError,
	CorroborationClassStatusCode,
	CorroborationClassKeyword,
}

// corroborationKey 是佐证注册表键的唯一构造处。class 恒为上述四个常量之一，
// 因此键尾部的 "|class" 不可能被客户端提供的模型名伪造出来，跨 (模型, 类别)
// 的碰撞构造不出来。
func corroborationKey(channelID int, modelName, class string) string {
	return fmt.Sprintf("%d|%s|%s", channelID, modelName, class)
}

// ResetCorroboration 清掉某 (渠道, 模型) 的佐证计数。
// 渠道测活成功、或人工恢复后调用，避免旧信号把下一次故障直接顶到阈值。
//
// **正常流量请求成功时也必须调用**（见 service.MarkRequestPolicySuccess）：
// 佐证要回答的是「这次失效是偶发还是持续」，而持续性该由**实际表现**回答，
// 不能只由时间窗回答。原先只有测活与人工恢复会复位，于是「攒到 2/3 → 渠道
// 自行恢复并成功跑了几十次 → 窗口内再来一次失败仍到 3/3 被摘」是可能的：
// 计数器只在时间流逝时衰减，不被恢复本身抵消。
func ResetCorroboration(channelID int, modelName string) {
	corroborationRegistry.mu.Lock()
	defer corroborationRegistry.mu.Unlock()
	for _, class := range corroborationClasses {
		delete(corroborationRegistry.m, corroborationKey(channelID, modelName, class))
	}
}

// ResetCorroborationForChannel 清掉某渠道**所有模型**的佐证计数。
//
// 渠道恢复时用：恢复意味着上一次那串失效信号已经不成立了，把它们留着会让
// 下一次真实故障在第一次就顶到阈值、立刻被摘掉 —— 佐证的作用是区分「偶发」
// 与「持续」，跨了一次人工/测活恢复的信号不该继续算数。
func ResetCorroborationForChannel(channelID int) {
	prefix := fmt.Sprintf("%d|", channelID)
	corroborationRegistry.mu.Lock()
	defer corroborationRegistry.mu.Unlock()
	for k := range corroborationRegistry.m {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(corroborationRegistry.m, k)
		}
	}
}

// CorroborationCount 返回当前累计次数，仅供日志与测试观察。
func CorroborationCount(channelID int, modelName, class string) int {
	key := corroborationKey(channelID, modelName, class)
	corroborationRegistry.mu.Lock()
	defer corroborationRegistry.mu.Unlock()
	if st := corroborationRegistry.m[key]; st != nil {
		return st.count
	}
	return 0
}