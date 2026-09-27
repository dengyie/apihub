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
		}
		go watchLoop()
	})
}

// GetPolicy 返回当前生效的策略（热加载安全）
func GetPolicy() *Policy {
	p := currentPolicy.Load()
	if p == nil {
		return DefaultPolicy()
	}
	return p
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
