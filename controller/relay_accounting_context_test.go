package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dengyie/apihub/loadbalancer"
	perfmetrics "github.com/dengyie/apihub/pkg/perf_metrics"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
)

// armRequestBudget 的 cancel 不能污染记账：非流式请求的 perf 指标必须照常落库。
//
// 缺陷的形状值得写清楚，因为它**不报错、不 panic、只表现为数据消失**：
//
//	Relay 里的 defer 是 LIFO。记账的 defer 注册在前，而 armRequestBudget 的
//	`defer cancelRequestBudget()` 注册在后 —— cancel 先跑、记账后跑。记账读
//	c.Request.Context()，而 armRequestBudget 早就把 c.Request 换成了预算
//	context，于是每次非流式请求到记账时都是 context.Canceled。
//
//	ClassifyRelayOutcome 里 `ctx.Err() == context.Canceled → OutcomeIgnored`
//	本意是排除「客户端自己掐掉」的请求（那确实不该记进渠道健康度），在这里
//	却把每一个非流式请求都误判成客户端取消 → Record 永不调用 → perf 指标
//	对非流式流量整片变黑。
//
//	为什么一直没人发现：armRequestBudget 对**流式**直接返回 nil，流式指标一直
//	正常。生产里黑掉的只有非流式那半边，而没人去查它。
func TestNonStreamingRelayRecordsPerfMetricsDespiteRequestBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)

	policy := loadbalancer.DefaultPolicy()
	budgetMs := 30000 // 非 0 即走 armRequestBudget 分支
	policy.RequestTimeoutMs = &budgetMs
	previousPolicy := loadbalancer.GetPolicy()
	loadbalancer.SetPolicy(policy)
	t.Cleanup(func() { loadbalancer.SetPolicy(previousPolicy) })

	parentCtx := context.Background()
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(parentCtx)

	budgetCtx, cancel := armRequestBudget(ginContext, false, types.RelayFormatOpenAIResponses)
	require.NotNil(t, cancel, "非流式 + 非 realtime + 预算>0 时必须套上预算 context")
	require.False(t, parentCtx == budgetCtx,
		"context.Context 是接口，用 == 判同一性；预算 context 应是父 context 的派生，不是同一个")

	cancel()
	require.ErrorIs(t, budgetCtx.Err(), context.Canceled,
		"预算 context 自身必须已被取消 —— 这正是会误导记账的那次取消")
	require.NoError(t, parentCtx.Err(),
		"释放预算不得污染父 context：记账读的就是它")
}

// TestClassifyRelayOutcomeDistinguishesBudgetReleaseFromClientCancel
// 钉住分类语义本身：父 context 干净 → 记为真实故障；父 context 被取消 → 忽略。
//
// 与上一个用例互补：上一个钉「记账该读哪个 context」，这个钉「读对了之后
// 分类器给出什么结论」。两层都钉住，这类缺陷才算关死 —— 只修 Relay 那一行
// 而没有这条，回归时没人知道判断标准被悄悄改过。
func TestClassifyRelayOutcomeDistinguishesBudgetReleaseFromClientCancel(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "budget-model", UsingGroup: "default"}
	upstreamErr := types.NewErrorWithStatusCode(
		context.DeadlineExceeded, types.ErrorCodeBadResponse, http.StatusBadGateway)

	// 预算释放：父 context 仍然有效 → 这是真实的渠道故障，应当计入。
	assert.Equal(t, perfmetrics.OutcomeFailure,
		perfmetrics.ClassifyRelayOutcome(context.Background(), info, upstreamErr),
		"释放预算不应被当成客户端取消；上游 5xx 是真实渠道故障")

	// 客户端真的走了：父 context 被取消 → 不该记进渠道健康度。
	clientGone, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, perfmetrics.OutcomeIgnored,
		perfmetrics.ClassifyRelayOutcome(clientGone, info, upstreamErr),
		"客户端主动断开必须继续被忽略 —— 这条保护不能因为修了预算缺陷而被一起删掉")
}