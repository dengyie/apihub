package loadbalancer

import (
	"log"
	"sync"
	"time"
)

// 「某模型持续拿不到渠道」检测器。
//
// 为什么需要它：熔断让路由更快绕开，但它**只负责绕**。当一个模型的所有渠道
// 同时熔断/打满/被自动禁用时，客户端拿到的是 503 no available channel —
// 这是「这个模型对外已经不可用」的对外信号，而网关内部此刻是安静的：没有
// 任何一条既有日志会说「这个模型已经连续 N 次选不出渠道了」。
// 半夜一次模型全池挂掉，没人收到通知，只能等客户端来问。
//
// 定位：**只产生结构化证据，不假装自己是告警通道**。本项目没有外发通道
// （现有 webhook 全是支付回调），所以这里输出的是一条稳定可 grep 的
// WARN 行 + 节流（同一模型在冷却期内只报一次），任何外部采集器
// （logrotate 后的 grep、system_task、将来的 webhook）都能接。
// 把它做成"告警系统"会引入新密钥、新失败模式，而这里一行就能接上任何采集。
//
// 节流是必须的：客户端重试风暴会在几秒内打出上百次同类 503，不节流的日志
// 只会把真正的信号淹掉。

// exhaustionEvictThreshold 超过它就在下一次记录时清扫一次。给得宽松：正常
// 规模是「在售模型数」量级，远达不到，只有异常增长才触发。
const exhaustionEvictThreshold = 1024

// 每个模型一条：窗口内的累计次数、窗口起点、上次告警时刻、最近原因。
var exhaustionRegistry struct {
	mu sync.Mutex
	m  map[string]*exhaustionState
}

type exhaustionState struct {
	count       int
	windowStart time.Time
	lastReason  string
	lastAlert   time.Time
}

// RecordModelExhausted 记录一次「该模型选不出任何渠道」。
// 窗口内累计到阈值即输出一次告警行，冷却期内不重复输出。
func RecordModelExhausted(model, reason string) {
	if model == "" {
		return
	}
	policy := GetPolicy()
	if !policy.Enabled {
		return
	}
	window := time.Duration(policy.Default.Breaker.ModelExhaustionWindowSecondsOrDefault()) * time.Second
	threshold := policy.Default.Breaker.ModelExhaustionThresholdOrDefault()
	cooldown := time.Duration(policy.Default.Breaker.ModelExhaustionAlertCooldownSecondsOrDefault()) * time.Second
	if threshold <= 0 || window <= 0 {
		return
	}

	now := time.Now()
	exhaustionRegistry.mu.Lock()
	defer exhaustionRegistry.mu.Unlock()
	if exhaustionRegistry.m == nil {
		exhaustionRegistry.m = make(map[string]*exhaustionState)
	}

	// 顺带做一次清扫：模型名来自客户端请求，是**无界输入**，不淘汰就是
	// 别人用一个随机模型名就能把这张表撑爆。与 breakers 用同一套理由。
	//
	// 这张表的键**就是**模型名本身，比佐证表更容易被灌：任何一次 503
	// no available channel 都会记一条，随机模型名必然选不出渠道，所以一个
	// 持有效 token 的客户端可以稳定地往里灌。因此除了「删过期的」，还必须
	// 有硬上限（见 evict.go）—— 只做前者的话，一个窗口之内涌入的互不相同
	// 的模型名一条也删不掉。
	if len(exhaustionRegistry.m) > exhaustionEvictThreshold {
		for k, st := range exhaustionRegistry.m {
			if now.Sub(st.windowStart) > window && now.Sub(st.lastAlert) > cooldown {
				delete(exhaustionRegistry.m, k)
			}
		}
		if len(exhaustionRegistry.m) > exhaustionCap {
			keys := make([]string, 0, len(exhaustionRegistry.m))
			for k := range exhaustionRegistry.m {
				keys = append(keys, k)
			}
			dropped := dropOldestWhenOverCap(keys,
				func(st *exhaustionState) time.Time { return st.windowStart },
				exhaustionRegistry.m, exhaustionCap)
			if dropped > 0 {
				log.Printf("loadbalancer: exhaustion registry hit hard cap %d, dropped %d oldest entries (keys are client-supplied model names)",
					exhaustionCap, dropped)
			}
		}
	}

	st := exhaustionRegistry.m[model]
	if st == nil || now.Sub(st.windowStart) > window {
		st = &exhaustionState{windowStart: now}
		exhaustionRegistry.m[model] = st
	}
	st.count++
	st.lastReason = reason
	if st.count < threshold {
		return
	}
	// 到阈值：告警，然后重置计数，让冷却期结束后的新一轮窗口重新计数。
	if !st.lastAlert.IsZero() && now.Sub(st.lastAlert) < cooldown {
		return
	}
	st.lastAlert = now
	st.count = 0
	st.windowStart = now

	log.Printf("loadbalancer: MODEL_EXHAUSTION model=%s threshold=%d window=%ds reason=%s —— 该模型持续选不出任何渠道，对外表现为 503 no available channel",
		model, threshold, int(window.Seconds()), reason)
}

// ResetModelExhaustion 在该模型成功选到渠道时清空计数，让「曾经短暂耗尽」
// 不会和「现在真的挂了」混在同一条证据里。
func ResetModelExhausted(model string) {
	if model == "" {
		return
	}
	exhaustionRegistry.mu.Lock()
	defer exhaustionRegistry.mu.Unlock()
	delete(exhaustionRegistry.m, model)
}
