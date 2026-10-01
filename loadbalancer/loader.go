package loadbalancer

import (
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	// policyLoadFailed 记录「首次加载策略文件失败」。热加载失败不影响它：
	// 热加载出错时 reload 保留上一份可用策略，判据仍然可信。
	policyLoadFailed atomic.Bool
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
// 只有「首次加载就失败」才为不可信：此时 currentPolicy 是内置 DefaultPolicy，
// 其中 enabled=false、channels 为空，于是所有基于配置的判据都退化成「一律
// 不豁免」——尤其是 breaker_exempt。IsBreakerExempt 会因 !Enabled() 直接返回
// false，把本机 CPA 兜底渠道从豁免名单里悄悄摘掉；一旦它们再被自动禁用，
// 整条链路就没有退路了，而且不会有任何一行日志指向真正的原因。
//
// 热加载失败不影响本标志：那时 currentPolicy 仍是上一份可用配置。
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

func reload() error {
	policyMu.Lock()
	defer policyMu.Unlock()

	data, err := os.ReadFile(policyPath)
	if err != nil {
		return err
	}
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Channels == nil {
		p.Channels = make(map[int]ChannelPolicy)
	}
	fi, err := os.Stat(policyPath)
	if err == nil {
		policyMtime = fi.ModTime()
	}
	currentPolicy.Store(&p)
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

func watchLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		fi, err := os.Stat(policyPath)
		if err != nil {
			continue
		}
		policyMu.Lock()
		changed := fi.ModTime().After(policyMtime)
		policyMu.Unlock()
		if changed {
			if err := reload(); err != nil {
				log.Printf("loadbalancer: policy hot-reload failed: %v", err)
			}
		}
	}
}
