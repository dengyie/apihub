package loadbalancer

import (
	"fmt"
	"testing"
	"time"
)

func TestModelExhaustionCountsUpToThreshold(t *testing.T) {
	installPolicy(t, exhaustionPolicy(3))
	resetExhaustion(t)
	for i := 0; i < 2; i++ {
		RecordModelExhausted("m1", "group=default")
	}
	if got := exhaustionCount("m1"); got != 2 {
		t.Fatalf("阈值前应累计，got %d", got)
	}
	RecordModelExhausted("m1", "group=default") // 到阈值，触发后清零
	if got := exhaustionCount("m1"); got != 0 {
		t.Fatalf("触发告警后应清零以便下一轮重新计数，got %d", got)
	}
}

func TestModelExhaustionResetsOnSuccess(t *testing.T) {
	installPolicy(t, exhaustionPolicy(3))
	resetExhaustion(t)
	RecordModelExhausted("m1", "group=default")
	RecordModelExhausted("m1", "group=default")
	ResetModelExhausted("m1") // 模型仍在正常服务
	RecordModelExhausted("m1", "group=default")
	if got := exhaustionCount("m1"); got != 1 {
		t.Fatalf("成功后应从零重新累计，got %d", got)
	}
}

// 阈值配 0（未配置）走默认 5，而不是变成"永远不告警"或"立刻告警"。
func TestModelExhaustionNonPositiveThresholdFallsBackToDefault(t *testing.T) {
	installPolicy(t, exhaustionPolicy(0)) // 0 → OrDefault 给 5
	resetExhaustion(t)
	for i := 0; i < 4; i++ {
		RecordModelExhausted("m1", "g")
	}
	if got := exhaustionCount("m1"); got != 4 {
		t.Fatalf("未到默认阈值 5 应继续累计，got %d", got)
	}
	RecordModelExhausted("m1", "g") // 第 5 次到阈值 → 告警并清零
	if got := exhaustionCount("m1"); got != 0 {
		t.Fatalf("到默认阈值应触发并清零，got %d", got)
	}
	// 策略整体关闭时不记录
	installPolicy(t, &Policy{Enabled: false, Default: ChannelPolicy{Breaker: BreakerPolicy{ModelExhaustionThreshold: 1}}})
	RecordModelExhausted("m2", "g")
	if got := exhaustionCount("m2"); got != 0 {
		t.Fatalf("策略关闭时不应记录，got %d", got)
	}
}

func TestModelExhaustionIgnoresEmptyModelName(t *testing.T) {
	installPolicy(t, exhaustionPolicy(1))
	resetExhaustion(t)
	RecordModelExhausted("", "g")
	ResetModelExhausted("")
	exhaustionRegistry.mu.Lock()
	n := len(exhaustionRegistry.m)
	exhaustionRegistry.mu.Unlock()
	if n != 0 {
		t.Fatalf("空模型名不应建条目，got %d", n)
	}
}

// 模型名来自客户端请求，是无界输入：表必须能自我回收，否则是内存放大器。
func TestModelExhaustionRegistryStaysBounded(t *testing.T) {
	installPolicy(t, exhaustionPolicy(1))
	resetExhaustion(t)
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 2000; i++ {
		RecordModelExhausted("m", "g")
	}
	exhaustionRegistry.mu.Lock()
	for _, st := range exhaustionRegistry.m {
		st.windowStart = old
		st.lastAlert = old
	}
	exhaustionRegistry.mu.Unlock()
	RecordModelExhausted("trigger-sweep", "g") // 超过 1024 触发清扫

	exhaustionRegistry.mu.Lock()
	n := len(exhaustionRegistry.m)
	exhaustionRegistry.mu.Unlock()
	if n > 10 {
		t.Fatalf("清扫应把陈旧条目压到个位数，剩 %d", n)
	}
}

// 键就是客户端给的模型名，任何一次 503 no available channel 都会记一条 ——
// 随机模型名必然选不出渠道，所以一个持有效 token 的客户端可以稳定地往里灌。
//
// 已有用例只覆盖了「删过期条目」那条路径：一个窗口之内涌入的互不相同的键
// 一条也过期不了，那条清扫等于没做，map 会一直长到攻击停止为止。这里钉住
// 第二道闸门：总量硬上限。
func TestModelExhaustionRegistryHardCap(t *testing.T) {
	installPolicy(t, exhaustionPolicy(1000000))
	resetExhaustion(t)

	flood := exhaustionCap + 2000
	for i := 0; i < flood; i++ {
		// 全部落在同一个窗口内：过期清扫一条也删不掉
		RecordModelExhausted(fmt.Sprintf("flood-model-%d", i), "group=default")
	}

	exhaustionRegistry.mu.Lock()
	n := len(exhaustionRegistry.m)
	exhaustionRegistry.mu.Unlock()
	// 上限是 exhaustionCap+1 而非 exhaustionCap：清扫发生在本条记录写入之前，
	// 刚清扫完的 cap 条加上本条正好是 cap+1。差这一条不影响「有界」这个性质，
	// 也不值得为了整数好看把清扫挪到写入之后。
	if n > exhaustionCap+1 {
		t.Fatalf("未过期的键也必须有硬上限：灌入 %d 条后剩 %d，上限 %d",
			flood, n, exhaustionCap+1)
	}
	if n < exhaustionCap/2 {
		t.Fatalf("硬上限不应误伤正常规模的条目，剩 %d", n)
	}
}
