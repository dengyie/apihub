package loadbalancer

import (
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dengyie/apihub/common"
	"gopkg.in/yaml.v3"
)

// loader 负责策略配置的热加载：轮询文件 mtime，变化时重新加载。
// 使用 atomic.Pointer 保证读取策略时无锁且总能拿到完整版本。

var (
	currentPolicy atomic.Pointer[Policy]
	policyPath    string
	policyMtime   time.Time
	policyMu      sync.Mutex
	watcherOnce   sync.Once
	// policyLoadFailed 记录「当前生效的策略不是从文件读出来的」。
	//
	// 它必须在 reload 成功时**清回 false**：这个标志记录的是「首次加载失败」
	// 这个事件，却一直被当成「当前没有可用策略」这个状态来用，于是热加载早已
	// 恢复、判据早已可信，自动禁用却仍在整个进程生命周期内静默失效。触发
	// 条件在真实运维里很常见（supervisord 先拉起二进制、策略 yaml 尚未落盘）。
	policyLoadFailed atomic.Bool
	// policyReloadFailures 记录连续失败次数，只服务两件事：把热加载失败的日志
	// 降频，以及让日志能回答「坏了多久 / 试过几次」。成功一次即清零。
	policyReloadFailures atomic.Int64
)

// Init 初始化负载均衡器：加载策略文件并启动热加载监听。
// path 为空时使用默认策略（关闭状态）。
// 重复调用只生效一次。
func Init(path string) {
	watcherOnce.Do(func() {
		policyPath = path
		if path == "" {
			currentPolicy.Store(DefaultPolicy())
			return
		}
		if err := reload(); err != nil {
			log.Printf("loadbalancer: initial policy load failed, using default: %v", err)
			currentPolicy.Store(DefaultPolicy())
			policyLoadFailed.Store(true)
		}
		go watchLoop()
	})
}

// PolicyReliable 报告策略判据是否可信。
//
// 为不可信只有一种情形：**当前生效的策略不是从文件读出来的**（首次加载失败，
// 回退到了内置 DefaultPolicy）。此时 currentPolicy 里 enabled=false、channels
// 为空，于是所有基于配置的判据都退化成「一律不豁免」——尤其是 breaker_exempt。
// IsBreakerExempt 会因 !Enabled() 直接返回 false，把本机 CPA 兜底渠道从豁免
// 名单里悄悄摘掉；一旦它们再被自动禁用，整条链路就没有退路了，而且不会有任何
// 一行日志指向真正的原因。
//
// 读成功过一次就恢复：reload 在 currentPolicy.Store 之后把这个标志清回 false。
// 早期版本只在 Init 里置 true 而从不恢复，结果一次启动期的配置读取故障就能让
// 自动禁用在整个进程生命周期内永久静默失效 —— 熔断照常工作、策略照样热加载，
// 外部一切正常，唯独再没有任何渠道会被摘掉。
//
// 未配置策略文件（Init 传空路径）不算不可信：那是显式的「不启用智能负载」。
func PolicyReliable() bool {
	return !policyLoadFailed.Load()
}

// MarkPolicyLoadFailedForTest 设置「首次加载失败」标志并返回还原函数。
// 该状态在生产里只由 Init 触发，测试需要单独构造。
func MarkPolicyLoadFailedForTest(failed bool) func() {
	prev := policyLoadFailed.Load()
	policyLoadFailed.Store(failed)
	return func() { policyLoadFailed.Store(prev) }
}

// GetPolicy 返回当前生效的策略（热加载安全）
func GetPolicy() *Policy {
	p := currentPolicy.Load()
	if p == nil {
		return DefaultPolicy()
	}
	return p
}

// SetPolicy 设置当前生效策略（用于测试或编程式配置）
func SetPolicy(p *Policy) {
	if p == nil {
		p = DefaultPolicy()
	}
	currentPolicy.Store(p)
}

// Enabled 策略总开关是否打开
func Enabled() bool {
	return GetPolicy().Enabled
}

// reload 读入并应用策略文件。
//
// 成功时把 policyMtime 推到文件当前 mtime，并把 policyLoadFailed 清回 false。
// 后者是「自动禁用永久静默失效」的根因修复：标志的语义是「当前生效的策略不是
// 从文件读出来的」，所以一旦读成功过一次，它就必须失效。
//
// 读失败与解析失败被刻意区别对待，因为它们的可重试性完全不同：
//
//   - **读**失败是瞬时的（文件正被 cp 覆盖、nfs 抖动、权限刚改）。此时**不**
//     推进 mtime，下一轮继续重试 —— 同一份文件几秒后就可能读得出来。
//   - **解析**失败是确定性的（坏 yaml 不会自己变好）。此时**推进** mtime，
//     把重试压到「文件再次被编辑」为止。
//
// 此前两条路径都不推进 mtime，坏 yaml 于是每 5 秒刷一行日志、一天一万七千行，
// 把真正的信号淹掉；而配置本身是安全的（失败时保留上一份可用策略）。
func reload() error {
	policyMu.Lock()
	defer policyMu.Unlock()

	data, err := os.ReadFile(policyPath)
	if err != nil {
		policyReloadFailures.Add(1)
		return err
	}
	fi, statErr := os.Stat(policyPath)
	if statErr != nil {
		// 读到了内容却 stat 不到（刚被替换 / 刚被删）：同样是瞬时情况，
		// 推进不了 mtime 就退化为每轮重试。
		policyReloadFailures.Add(1)
		return statErr
	}
	policyMtime = fi.ModTime()

	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		policyReloadFailures.Add(1)
		return err
	}
	if p.Channels == nil {
		p.Channels = make(map[int]ChannelPolicy)
	}
	currentPolicy.Store(&p)
	if policyLoadFailed.Swap(false) {
		// 这一行是运维能拿到的唯一信号：自动禁用从「永久静默失效」回到了
		// 「正常工作」。没有它，从启动故障里恢复这件事在日志上是不可见的。
		log.Printf("loadbalancer: policy load recovered, auto-disable judgments re-enabled")
	}
	policyReloadFailures.Store(0)
	// 同步重试次数到全局：失败后换渠道重试，而不是直接报错
	if p.Enabled && p.MaxRetries > 0 {
		common.RetryTimes = p.MaxRetries
	} else if !p.Enabled {
		common.RetryTimes = 0
	}
	// 同步 thinking 裁剪渠道列表（兼容旧配置）
	SetThinkingStripChannels(p.StripThinkingChannels)
	// 同步通用参数裁剪规则
	SetParamStripConfig(p.StripParams)
	log.Printf("loadbalancer: policy reloaded")
	return nil
}

// watchLoop 每 5 秒比对策略文件的 mtime。
//
// 解析失败由 reload 推进 mtime 挡住了（坏 yaml 只在再次被编辑时才会重试），
// 所以这里的重试循环实际只服务于**读**失败 —— 那类瞬时故障值得每轮重试，但
// 不值得每轮打一行日志：连续失败时前两次每次都打（快速定位），之后压到每
// 5 分钟一行，并带上连续次数与首次失败时刻，让人看得出这是一个持续性问题。
func watchLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastFailureLog time.Time
	for range ticker.C {
		fi, err := os.Stat(policyPath)
		if err != nil {
			continue
		}
		policyMu.Lock()
		changed := fi.ModTime().After(policyMtime)
		policyMu.Unlock()
		if !changed {
			continue
		}
		if err := reload(); err != nil {
			n := policyReloadFailures.Load()
			now := time.Now()
			if n <= 2 || now.Sub(lastFailureLog) >= 5*time.Minute {
				log.Printf("loadbalancer: policy hot-reload failed (consecutive=%d): %v", n, err)
				lastFailureLog = now
			}
		}
	}
}
