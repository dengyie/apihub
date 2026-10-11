package controller

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/model"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件钉住 applyUpstreamFailureEffects 的两条不变式：
//
//  1. **一次上游失败恰好一次 tripBreaker**。错误分类器之间有意的重叠
//     （relay 分类收编 permission/thinking、宵禁的 403 命中 permission、
//     new-api 家族包装的额度错误命中 relay 的 type 白名单）在并列 if 结构下
//     曾让一次失败触发 2-3 次 trip，递增退避在首次失败就跳到 2-3 倍冷却。
//     断言手段：每个分支的熔断日志各含「已立即熔断」一次，数日志行数。
//  2. 每类错误走**它自己的分支**（日志特征可辨），兜底的 relay 分支只在
//     更特异的分类都不认的时候才接手。

func breakerTripTestPolicy() *loadbalancer.Policy {
	p := loadbalancer.DefaultPolicy()
	p.Enabled = true
	perModel := true
	p.Default.Breaker.PerModel = &perModel
	p.Default.Breaker.FailureThreshold = 5
	p.Default.Breaker.CooldownSeconds = 60
	p.Default.Breaker.HalfOpenProbes = 1
	return p
}

// breakerTripFixture 返回隔离的 gin 上下文、单模型渠道与日志捕获。
// 日志计数是断言「恰好一次 trip」的探针：applyUpstreamFailureEffects 里
// 每个分支的 LogWarn 都含「已立即熔断」（限流分支是「已设置短期避让冷却」）。
type breakerTripFixture struct {
	c       *gin.Context
	channel *model.Channel
	info    *relaycommon.RelayInfo
	attempt *loadbalancer.RequestHandle
	logs    *bytes.Buffer
}

func newBreakerTripFixture(t *testing.T, channelID int) *breakerTripFixture {
	t.Helper()
	prevPolicy := loadbalancer.GetPolicy()
	loadbalancer.SetPolicy(breakerTripTestPolicy())
	t.Cleanup(func() {
		loadbalancer.SetPolicy(prevPolicy)
		loadbalancer.GlobalTracker().ResetChannelBreakers(channelID)
	})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	f := &breakerTripFixture{
		c:       c,
		channel: &model.Channel{Id: channelID},
		info:    &relaycommon.RelayInfo{OriginModelName: "trip-test-model"},
	}
	f.attempt = loadbalancer.GlobalTracker().Begin(channelID, f.info.OriginModelName)
	return f
}

// captureLogs 把熔断日志（WARN 走 DefaultErrorWriter，INFO 走 DefaultWriter）
// 导进缓冲。置换必须持 common.LogWriterMu 写锁：logHelper 在读锁内取 writer
// 指针，裸赋值对并发读是数据竞争——顺序测试下不触发，但任何 t.Parallel 化
// 都会把它变成真实竞争。
func (f *breakerTripFixture) captureLogs(t *testing.T) {
	t.Helper()
	f.logs = &bytes.Buffer{}
	common.LogWriterMu.Lock()
	prevErr, prevOut := gin.DefaultErrorWriter, gin.DefaultWriter
	gin.DefaultErrorWriter, gin.DefaultWriter = f.logs, f.logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter, gin.DefaultWriter = prevErr, prevOut
		common.LogWriterMu.Unlock()
	})
}

func (f *breakerTripFixture) tripLogCount() int {
	// 两个探针覆盖两种熔断日志：常规冷却分支打「已立即熔断」，
	// 宵禁分支（TripBreakerUntil）打「宵禁中，已熔断到」。
	return strings.Count(f.logs.String(), "已立即熔断") +
		strings.Count(f.logs.String(), "宵禁中，已熔断到")
}

func TestBreakerTripChainEachClassTripsExactlyOnce(t *testing.T) {
	cases := []struct {
		name        string
		err         *types.NewAPIError
		wantTrips   int
		wantLogMark string
	}{
		{
			name:        "权限错误只熔一次（曾与 relay 分类重复）",
			err:         types.NewErrorWithStatusCode(errors.New("无权访问 按量分组 分组"), types.ErrorCodeBadResponseBody, 403),
			wantTrips:   1,
			wantLogMark: "权限受限",
		},
		{
			name: "上游 new-api 包装的额度错误只熔一次（曾与 relay 的 type 白名单重复）",
			err: func() *types.NewAPIError {
				e := types.NewErrorWithStatusCode(errors.New("insufficient balance, 额度已用完"), types.ErrorCodeBadResponseBody, 400)
				e.RelayError = types.OpenAIError{Message: "insufficient balance", Type: "new_api_error"}
				return e
			}(),
			wantTrips:   1,
			wantLogMark: "额度已耗尽",
		},
		{
			name:        "thinking 历史不兼容只熔一次（曾与 relay 分类重复）",
			err:         types.NewErrorWithStatusCode(errors.New("The `reasoning_content` in the thinking mode must be passed back to the API."), types.ErrorCodeBadResponseBody, 400),
			wantTrips:   1,
			wantLogMark: "思考模式历史",
		},
		{
			name:        "宵禁错误只熔一次且走绝对到期（403 曾同时命中 permission 分支）",
			err:         types.NewErrorWithStatusCode(errors.New("system_curfew: 该账号 00:00-8:00 宵禁"), types.ErrorCodeBadResponseBody, 403),
			wantTrips:   1,
			wantLogMark: "宵禁中",
		},
		{
			name:        "中继代理异常兜底分类只熔一次",
			err:         types.NewErrorWithStatusCode(errors.New("来自上游渠道的报错: bad response status code 502"), types.ErrorCodeBadResponseBody, 400),
			wantTrips:   1,
			wantLogMark: "中继代理异常",
		},
		{
			name:        "限流走短避让，不产生常规熔断日志",
			err:         types.NewErrorWithStatusCode(errors.New("rate limit exceeded"), types.ErrorCodeBadResponseBody, 429),
			wantTrips:   0,
			wantLogMark: "已设置短期避让冷却",
		},
		{
			name:        "无特征 5xx 不进任何即时熔断分支（交给阈值累计）",
			err:         types.NewErrorWithStatusCode(errors.New("internal server error"), types.ErrorCodeBadResponseStatusCode, 500),
			wantTrips:   0,
			wantLogMark: "",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBreakerTripFixture(t, 7700+i)
			f.captureLogs(t)

			applyUpstreamFailureEffects(f.c, f.channel, f.info, f.attempt, tc.err)

			assert.Equal(t, tc.wantTrips, f.tripLogCount(),
				"一次失败必须恰好 %d 次常规熔断（tripCount 多涨会让递增退避在首次失败就翻倍）", tc.wantTrips)
			if tc.wantLogMark != "" {
				assert.Contains(t, f.logs.String(), tc.wantLogMark,
					"必须命中它自己的分支，而不是被兜底/相邻分类截胡")
			}
		})
	}
}

// TestBreakerTripChainRateLimitBlocksAndExpires 限流避让的可观测语义：
// blockedUntil 生效、常规冷却未开。
func TestBreakerTripChainRateLimitBlocksAndExpires(t *testing.T) {
	f := newBreakerTripFixture(t, 7790)
	err := types.NewErrorWithStatusCode(errors.New("rate limit exceeded"), types.ErrorCodeBadResponseBody, 429)

	applyUpstreamFailureEffects(f.c, f.channel, f.info, f.attempt, err)

	ok, reason := loadbalancer.GlobalTracker().IsAvailable(f.channel.Id, f.info.OriginModelName)
	assert.False(t, ok, "限流避让期内渠道必须不可选")
	assert.Equal(t, loadbalancer.ReasonCircuitBlockedUntil, reason)
}

// TestBreakerTripChainTripsSiblingModels 映射到同一上游模型的兄弟客户端模型
// 一并熔断（写侧收口，读侧零成本）。失败模型必须是 mapping 中的客户端名：
// 兄弟关系按「映射到同一上游名」判定，失败名不在 mapping 里时按原样透传，
// 没有兄弟。
func TestBreakerTripChainTripsSiblingModels(t *testing.T) {
	f := newBreakerTripFixture(t, 7791)
	mapping := `{"model-a":"upstream-x","model-b":"upstream-x","model-c":"upstream-y"}`
	f.channel.ModelMapping = &mapping
	f.info.OriginModelName = "model-a"

	err := types.NewErrorWithStatusCode(errors.New("model not found"), types.ErrorCodeBadResponseBody, 404)
	applyUpstreamFailureEffects(f.c, f.channel, f.info, f.attempt, err)

	tracker := loadbalancer.GlobalTracker()
	ok, _ := tracker.IsAvailable(f.channel.Id, "model-a")
	assert.False(t, ok, "失败模型本身必须被熔断")
	ok, _ = tracker.IsAvailable(f.channel.Id, "model-b")
	assert.False(t, ok, "映射到同一上游模型的兄弟一并熔断")
	ok, _ = tracker.IsAvailable(f.channel.Id, "model-c")
	assert.True(t, ok, "映射到不同上游模型的模型不受牵连")
}

// TestBreakerTripChainNilErrorNoop 防御：空错误不得触碰熔断状态。
func TestBreakerTripChainNilErrorNoop(t *testing.T) {
	f := newBreakerTripFixture(t, 7792)
	f.captureLogs(t)

	applyUpstreamFailureEffects(f.c, f.channel, f.info, f.attempt, nil)

	assert.Equal(t, 0, f.tripLogCount())
	ok, _ := loadbalancer.GlobalTracker().IsAvailable(f.channel.Id, f.info.OriginModelName)
	require.True(t, ok, "空错误不得改变渠道可用性")
}

// TestBreakerTripChainYieldsToThresholdTrip 钉住「一次失败至多一次熔断」的
// 跨层不变式：End 的连续失败阈值与显式分类熔断是同一次失败事件的两个决策
// 点。阈值跨越的那次失败，End 先熔断并在句柄上登记，链式显式分支必须让位
// （留日志、不重复递增 tripCount）。回归手法：threshold=1 时每次失败都由
// End 熔断，链分支的每次调用都应打出「不重复递增」的让位日志——若有人把
// 句柄标记或让位逻辑改坏，这条用例会先红。
func TestBreakerTripChainYieldsToThresholdTrip(t *testing.T) {
	f := newBreakerTripFixture(t, 7793)
	p := breakerTripTestPolicy()
	p.Default.Breaker.FailureThreshold = 1
	loadbalancer.SetPolicy(p)
	t.Cleanup(func() { loadbalancer.SetPolicy(breakerTripTestPolicy()) })
	f.captureLogs(t)

	err := types.NewErrorWithStatusCode(errors.New("无权访问 按量分组 分组"), types.ErrorCodeBadResponseBody, 403)
	// 生产序列：End 先于链分支，threshold=1 时 End 在此熔断并登记句柄标记
	f.attempt.End(false, true)
	applyUpstreamFailureEffects(f.c, f.channel, f.info, f.attempt, err)

	assert.Equal(t, 1, f.tripLogCount(), "阈值已熔断，显式分支让位：不产生第二次常规熔断")
	assert.Contains(t, f.logs.String(), "不重复递增退避", "让位必须留痕，否则 tripCount 的账目无法对账")
}
