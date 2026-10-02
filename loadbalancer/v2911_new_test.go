package loadbalancer

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

// v29.11：熔断粒度从「渠道」改为「(渠道, 模型)」。
//
// 这批用例钉的是三件事：
//  1. 熔一个模型不影响同渠道的其它模型（这是本次改动的全部意义）
//  2. 账号级熔断仍然挡住全部模型，且不会被某个模型的成功顺手解掉
//  3. inflight 仍按渠道计——它跟着拆到模型级会打掉 max_inflight 的保护

// installPolicy 临时换掉全局策略，返回还原函数。
func installPolicy(t *testing.T, p *Policy) {
	t.Helper()
	old := currentPolicy.Load()
	currentPolicy.Store(p)
	t.Cleanup(func() { currentPolicy.Store(old) })
}

// perModelPolicy 造一份打开 per_model 的策略。
func perModelPolicy(maxInflight int) *Policy {
	yes := true
	return &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			MaxInflight: maxInflight,
			Breaker: BreakerPolicy{
				FailureThreshold: 3,
				CooldownSeconds:  60,
				HalfOpenProbes:   1,
				PerModel:         &yes,
			},
		},
	}
}

// 熔一个模型，同渠道的其它模型必须照常可用。
func TestModelScopeBreakerIsolatesOtherModels(t *testing.T) {
	installPolicy(t, perModelPolicy(0))
	tr := newTestTracker()
	const ch = 7001

	ok, reason := tr.IsAvailable(ch, "gpt-5")
	assert.True(t, ok, "初始应可用: "+reason)
	ok, reason = tr.IsAvailable(ch, "claude-4")
	assert.True(t, ok, "初始应可用: "+reason)

	tr.TripBreaker(ch, "gpt-5")

	ok, reason = tr.IsAvailable(ch, "gpt-5")
	assert.False(t, ok, "被熔的模型必须退出轮转")
	assert.Equal(t, ReasonCircuitOpen, reason)

	ok, reason = tr.IsAvailable(ch, "claude-4")
	assert.True(t, ok, "同渠道的其它模型不受影响，这就是 v29.11 要修的: "+reason)
}

// 账号级熔断（传空 model）仍然挡住该渠道的全部模型。
func TestChannelScopeBreakerBlocksEveryModel(t *testing.T) {
	installPolicy(t, perModelPolicy(0))
	tr := newTestTracker()
	const ch = 7002

	tr.TripBreaker(ch, "")

	_, reason := tr.IsAvailable(ch, "gpt-5")
	assert.Equal(t, ReasonCircuitOpen, reason)
	_, reason = tr.IsAvailable(ch, "claude-4")
	assert.Equal(t, ReasonCircuitOpen, reason)
}

// 关闭 per_model 时行为必须与 v29.10 逐位一致：熔任何模型都等于熔整个渠道。
// 这是灰度与回滚手段，必须有测试钉住。
func TestPerModelDisabledFoldsToChannelScope(t *testing.T) {
	installPolicy(t, perModelPolicy(0)) // 先开
	tr := newTestTracker()
	const ch = 7003

	// 关掉：PerModel 指针为 nil
	off := perModelPolicy(0)
	off.Default.Breaker.PerModel = nil
	installPolicy(t, off)

	tr.TripBreaker(ch, "gpt-5") // 写了模型名，但开关关着

	_, reason := tr.IsAvailable(ch, "gpt-5")
	assert.Equal(t, ReasonCircuitOpen, reason)
	_, reason = tr.IsAvailable(ch, "claude-4")
	assert.Equal(t, ReasonCircuitOpen, reason,
		"per_model 关闭时熔断粒度必须退回渠道级")
}

// 失败计数也按模型隔离：gpt-5 攒够阈值不牵连 claude-4。
func TestFailureAccumulationIsPerModel(t *testing.T) {
	installPolicy(t, perModelPolicy(0)) // FailureThreshold = 3
	tr := newTestTracker()
	const ch = 7004

	for i := 0; i < 3; i++ {
		tr.Begin(ch, "gpt-5").End(false, true)
	}

	ok, reason := tr.IsAvailable(ch, "gpt-5")
	assert.False(t, ok, "连续 3 次失败应熔断该模型")
	assert.Equal(t, ReasonCircuitOpen, reason)

	ok, reason = tr.IsAvailable(ch, "claude-4")
	assert.True(t, ok, "别的模型没失败过，不该被牵连: "+reason)

	// claude-4 只失败一次，够不到阈值
	tr.Begin(ch, "claude-4").End(false, true)
	ok, reason = tr.IsAvailable(ch, "claude-4")
	assert.True(t, ok, "1 次失败不该熔断: "+reason)
}

// inflight 必须留在渠道级。跟着熔断一起拆到模型级的话，
// N 个并发请求分散到 N 个模型时每个键各自看到 0，max_inflight 就废了。
func TestInflightStaysChannelScopedAcrossModels(t *testing.T) {
	installPolicy(t, perModelPolicy(2))
	tr := newTestTracker()
	const ch = 7005

	h1 := tr.Begin(ch, "model-a")
	h2 := tr.Begin(ch, "model-b")
	assert.Equal(t, 2, tr.Inflight(ch),
		"两个模型上的在途请求必须汇总到同一个渠道计数器")

	// 第三个模型也必须看到「渠道已经满了」，而不是「自己这把键是空的」
	_, reason := tr.IsAvailable(ch, "model-c")
	assert.Equal(t, ReasonOverloaded, reason,
		"并发上限是账号级预算，与模型无关")

	h1.End(false, false)
	assert.Equal(t, 1, tr.Inflight(ch))
	h2.End(false, false)
	assert.Equal(t, 0, tr.Inflight(ch))

	ok, reason := tr.IsAvailable(ch, "model-c")
	assert.True(t, ok, "槽位归还后应恢复可用: "+reason)
}

// 熔断粒度：v29.14 起**一律** ScopeModel。
//
// 这张表原来还把「额度耗尽 / 宵禁 / 会话路由失败 / 中继代理异常 / 上游限流」
// 判成 ScopeChannel。生产数据推翻了那个分级：controller/relay.go 里对应分支
// 硬编码了 `TripBreaker(id, "")`，绕过了本函数，判据从未生效；同时中继代理
// 异常的关键词表含 `no available channel for model`，一句话把「上游缺这个模型」
// 变成整渠道下线。账号级失效改由自动下线兜底，熔断只负责快速绕开。
func TestBreakerScopeAlwaysModel(t *testing.T) {
	cases := []struct {
		name string
		err  *types.NewAPIError
	}{
		{"额度耗尽", &types.NewAPIError{StatusCode: http.StatusForbidden, Err: errors.New("insufficient_user_quota")}},
		{"宵禁", &types.NewAPIError{StatusCode: http.StatusForbidden, Err: errors.New("provider_code=system_curfew")}},
		{"会话路由失败", &types.NewAPIError{StatusCode: http.StatusBadRequest, Err: errors.New("missing x-opencode-session")}},
		{"中继代理异常", &types.NewAPIError{StatusCode: http.StatusBadGateway, Err: errors.New("bad response status code")}},
		{"上游限流", &types.NewAPIError{StatusCode: http.StatusTooManyRequests, Err: errors.New("rate limit exceeded")}},
		{"令牌停用", &types.NewAPIError{StatusCode: http.StatusUnauthorized, Err: errors.New("API key not recognised")}},
		{"模型不存在", &types.NewAPIError{StatusCode: http.StatusNotFound, Err: errors.New("The model `x` does not exist")}},
		{"上游无该模型渠道", &types.NewAPIError{StatusCode: http.StatusBadRequest, Err: errors.New("No available channel for model deepseek-v4-pro under group default (distributor)")}},
		{"上游未知 provider", &types.NewAPIError{StatusCode: http.StatusBadRequest, Err: errors.New("unknown provider for model foo")}},
		{"模型 EOL", &types.NewAPIError{StatusCode: http.StatusGone, Err: errors.New("gone")}},
		{"通用 500", &types.NewAPIError{StatusCode: http.StatusInternalServerError, Err: errors.New("upstream boom")}},
		{"nil", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, ScopeModel, BreakerScopeOf(c.err),
				"v29.14 起熔断一律按模型粒度")
		})
	}
}

// 一个模型成功，不得顺手把账号级封锁解掉。
func TestModelSuccessDoesNotClearAccountBlock(t *testing.T) {
	installPolicy(t, perModelPolicy(0))
	tr := newTestTracker()
	const ch = 7007

	tr.TripBreakerUntil(ch, "", time.Now().Add(2*time.Hour))

	tr.Begin(ch, "gpt-5").End(false, false) // 该模型成功

	ok, reason := tr.IsAvailable(ch, "gpt-5")
	assert.False(t, ok,
		"gpt-5 成功不代表额度恢复了，不能提前解掉账号级封锁")
	assert.Equal(t, ReasonCircuitBlockedUntil, reason)
}

// 过载放弃时，两把键预占的探测配额都必须归还。
// 漏还任何一把都会让该键永久卡在半开耗尽（直到租约到期）。
func TestOverloadRefundReleasesBothProbes(t *testing.T) {
	installPolicy(t, perModelPolicy(1))
	tr := newTestTracker()
	const ch = 7008

	// 先占满渠道唯一的并发槽
	held := tr.Begin(ch, "other")
	defer held.End(false, false)

	// 让渠道级与模型级同时处于半开（冷却已过）
	old := time.Now().Unix() - 3600
	tr.TripBreaker(ch, "")
	tr.TripBreaker(ch, "gpt-5")
	statsFor(tr, ch).openedAt.Store(old)
	tr.getBreaker(breakerKey{channelID: ch, model: "gpt-5"}).openedAt.Store(old)

	_, reason := tr.IsAvailable(ch, "gpt-5")
	assert.Equal(t, ReasonOverloaded, reason)

	chanKey := statsFor(tr, ch)
	modelKey := tr.getBreaker(breakerKey{channelID: ch, model: "gpt-5"})
	assert.EqualValues(t, 0, chanKey.halfOpenProbes.Load(),
		"渠道级预占的探测配额必须归还")
	assert.EqualValues(t, 0, modelKey.halfOpenProbes.Load(),
		"模型级预占的探测配额必须归还")
}

// 淘汰清扫：键空间变成「渠道 × 模型」后，条目会多一个量级，
// 渠道删除/改名后的残留必须能被清掉，否则 map 无限增长。
func TestEvictionSweepDropsIdleClosedEntries(t *testing.T) {
	tr := newTestTracker()

	stale := tr.getBreaker(breakerKey{channelID: 1, model: "old"})
	stale.lastSeen.Store(time.Now().Add(-2 * time.Hour).Unix())
	fresh := tr.getBreaker(breakerKey{channelID: 2, model: "new"})
	fresh.lastSeen.Store(time.Now().Unix())

	// 灌到超过阈值，最后一次新建会触发清扫
	for i := 0; i <= breakerEvictThreshold+1; i++ {
		k := breakerKey{channelID: 1000 + i, model: "m"}
		tr.getBreaker(k).lastSeen.Store(time.Now().Add(-2 * time.Hour).Unix())
	}

	_, staleAlive := tr.breakers[breakerKey{channelID: 1, model: "old"}]
	assert.False(t, staleAlive, "陈旧且已关闭的条目应被淘汰")

	_, freshAlive := tr.breakers[breakerKey{channelID: 2, model: "new"}]
	assert.True(t, freshAlive, "正在使用的条目不能被淘汰")
}

// per_model 在渠道级未指定时必须跟随全局，不能被「只配了 failure_threshold」
// 静默覆盖成 false——那是整批渠道悄悄退回渠道级熔断。
func TestPerModelFollowsGlobalWhenChannelOmitsIt(t *testing.T) {
	yes := true
	p := &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			Breaker: BreakerPolicy{PerModel: &yes},
		},
		Channels: map[int]ChannelPolicy{
			// 这个渠道只配了阈值，没提 per_model
			42: {Breaker: BreakerPolicy{FailureThreshold: 9}},
		},
	}
	assert.True(t, p.Resolve(42).Breaker.PerModelOrDefault(),
		"渠道未指定 per_model 时应跟随全局 true")

	// 显式配 false 才覆盖
	no := false
	p.Channels[43] = ChannelPolicy{Breaker: BreakerPolicy{PerModel: &no}}
	assert.False(t, p.Resolve(43).Breaker.PerModelOrDefault(),
		"显式配 false 应能覆盖全局")
}
