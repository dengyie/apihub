package loadbalancer

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPolicy() *Policy {
	return &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			MaxInflight:   2,
			TTFTTimeoutMs: 5000,
			Breaker: BreakerPolicy{
				FailureThreshold: 3,
				CooldownSeconds:  60,
				HalfOpenProbes:   1,
			},
		},
		Channels: map[int]ChannelPolicy{
			123: {MaxInflight: 10},
		},
	}
}

func TestPolicyResolve(t *testing.T) {
	p := testPolicy()

	// 未覆盖的渠道用默认
	r := p.Resolve(999)
	assert.Equal(t, 2, r.MaxInflight)
	assert.Equal(t, int64(5000), r.TTFTTimeoutMs)

	// 覆盖的渠道：设置的字段生效，未设置的回退默认
	r = p.Resolve(123)
	assert.Equal(t, 10, r.MaxInflight)
	assert.Equal(t, int64(5000), r.TTFTTimeoutMs)
	assert.Equal(t, 3, r.Breaker.FailureThreshold)

	// 关闭时返回空策略
	p.Enabled = false
	r = p.Resolve(123)
	assert.Equal(t, 0, r.MaxInflight)
}

func TestTrackerInflight(t *testing.T) {
	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	h1 := tr.Begin(1)
	h2 := tr.Begin(1)
	assert.Equal(t, 2, tr.Inflight(1))

	h1.End(false, false)
	assert.Equal(t, 1, tr.Inflight(1))

	h2.End(false, false)
	assert.Equal(t, 0, tr.Inflight(1))

	// 重复 End 只计一次
	h2.End(false, false)
	assert.Equal(t, 0, tr.Inflight(1))
}

func TestBreakerTripAndRecover(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 连续 3 次慢请求（非失败）只触发软降级，不硬熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(7)
		h.End(true, false)
	}
	ok, _ := tr.IsAvailable(7)
	assert.True(t, ok, "channel 7 should stay available after 3 slow requests (soft degrade only)")
	assert.True(t, tr.IsDegraded(7), "channel 7 should be degraded after 3 slow requests")

	// 连续 3 次硬失败触发熔断
	for i := 0; i < 3; i++ {
		h := tr.Begin(8)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(8)
	assert.False(t, ok, "channel 8 should be unavailable after 3 hard failures")
	assert.Equal(t, "circuit_open", reason)

	// 手动把 openedAt 拨到冷却期之前，模拟冷却结束
	s := tr.getOrCreate(8)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 半开：允许 1 个探测
	ok, _ = tr.IsAvailable(8)
	assert.True(t, ok, "channel 8 should allow 1 probe in half-open")
	// 第 2 个被拒绝
	ok, _ = tr.IsAvailable(8)
	assert.False(t, ok, "channel 8 should reject 2nd probe in half-open")

	// 探测成功后熔断器关闭
	h := tr.Begin(8)
	h.End(false, false)
	ok, _ = tr.IsAvailable(8)
	assert.True(t, ok, "channel 8 should be available after successful probe")
}

func TestBreakerHalfOpenProbeFailure(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// 1. 触发硬熔断（3次失败）
	for i := 0; i < 3; i++ {
		h := tr.Begin(100)
		h.End(false, true)
	}
	ok, reason := tr.IsAvailable(100)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 2. 冷却结束，进入半开
	s := tr.getOrCreate(100)
	s.openedAt.Store(time.Now().Unix() - 61)

	// 3. 放行 1 个探测
	ok, _ = tr.IsAvailable(100)
	assert.True(t, ok)

	// 4. 探测失败！此时应立即切回 circuit_open，并开启新的冷却时间
	h := tr.Begin(100)
	h.End(false, true)

	assert.Equal(t, int32(breakerOpen), s.state.Load(), "state should revert to breakerOpen after failed probe")
	assert.Equal(t, int32(0), s.halfOpenProbes.Load(), "halfOpenProbes should be reset to 0")

	// 此时冷却未结束（刚熔断），不应可用
	ok, reason = tr.IsAvailable(100)
	assert.False(t, ok)
	assert.Equal(t, "circuit_open", reason)

	// 模拟过了新的一轮冷却时间后，应再次允许探测（不会永久死锁）
	s.openedAt.Store(time.Now().Unix() - 61)
	ok, _ = tr.IsAvailable(100)
	assert.True(t, ok, "channel should allow probe again after next cooldown")
}

func TestTrackerIgnoreZeroChannel(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// channel <= 0 不应崩溃且不应污染统计
	h := tr.Begin(0)
	assert.NotNil(t, h)
	h.MarkFirstByte()
	h.End(false, true)

	assert.Equal(t, 0, tr.Inflight(0))
	assert.Equal(t, int64(-1), tr.AvgTTFT(0))
	assert.False(t, tr.IsDegraded(0))
	ok, _ := tr.IsAvailable(0)
	assert.True(t, ok)

	tr.RecordFailure(0)
	tr.RecordSuccess(0)
}

func TestOverloadSkip(t *testing.T) {
	old := currentPolicy.Load()
	currentPolicy.Store(testPolicy())
	defer currentPolicy.Store(old)

	tr := &Tracker{channels: make(map[int]*ChannelStats)}

	// MaxInflight=2，打满
	_ = tr.Begin(9)
	_ = tr.Begin(9)

	ok, reason := tr.IsAvailable(9)
	assert.False(t, ok, "channel 9 should be unavailable when overloaded")
	assert.Equal(t, "overloaded", reason)
}

func TestPolicyHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lb.yaml")

	write := func(maxInflight int) {
		data := []byte("enabled: true\ndefault:\n  max_inflight: " + strconv.Itoa(maxInflight) + "\n")
		require.NoError(t, os.WriteFile(path, data, 0644))
	}

	// 备份全局状态
	oldPath, oldPolicy, oldMtime := policyPath, currentPolicy.Load(), policyMtime
	defer func() {
		policyPath, policyMtime = oldPath, oldMtime
		currentPolicy.Store(oldPolicy)
	}()

	policyPath = path
	write(50)
	require.NoError(t, reload())
	assert.Equal(t, 50, GetPolicy().Default.MaxInflight)

	// 修改文件后重新加载生效
	write(100)
	require.NoError(t, reload())
	assert.Equal(t, 100, GetPolicy().Default.MaxInflight)
}

func TestTTFTTimeoutError(t *testing.T) {
	err := &TTFTTimeoutError{ChannelID: 1, TimeoutMs: 5000}
	assert.True(t, IsTTFTTimeout(err))
	assert.False(t, IsTTFTTimeout(nil))
	assert.False(t, IsTTFTTimeout(errors.New("other")))
}
