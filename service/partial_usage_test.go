package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deliveredCase(t *testing.T, endReason relaycommon.StreamEndReason, text string) *dto.Usage {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.1"},
	}
	info.SetEstimatePromptTokens(36861)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(endReason, context.Canceled)

	return DeliveredTextUsage(c, info, text)
}

// 客户端在首字到达前就放弃：上游从未生成，网关也没收到任何东西。
//
// 这是生产上真实发生过的形态——24 小时内 19 条，每条都按 3.6 万~12 万 token 的
// 本地估算收 prompt、输出为 0，且每条都带 frt=-1000（首字从未到达）。上游既然
// 没生成，也就没向网关收费，这笔账是网关自己凭空造的。
func TestDeliveredTextUsageZeroesAbandonedStream(t *testing.T) {
	usage := deliveredCase(t, relaycommon.StreamEndReasonClientGone, "")

	require.NotNil(t, usage)
	assert.EqualValues(t, 0, usage.PromptTokens,
		"客户端在首字前放弃 = 上游没生成也没计费，不能按本地估算收用户钱")
	assert.EqualValues(t, 0, usage.TotalTokens)
}

// 客户端读到一半才断开：这些内容是真的交付了，上游也真的生成了，必须照常计费。
//
// 这是上面那条的反向守卫。判据只认「交付了没有」，不能顺手把断连一律免单 ——
// 那会变成一条新的漏收通道。
func TestDeliveredTextUsageStillBillsPartialContentOnAbort(t *testing.T) {
	usage := deliveredCase(t, relaycommon.StreamEndReasonClientGone, "客户端确实读到的半段输出")

	require.NotNil(t, usage)
	assert.Greater(t, usage.CompletionTokens, 0, "已经交付的正文必须计费")
	assert.EqualValues(t, 36861, usage.PromptTokens)
}

// 正常收尾但正文为空：上游回了 200，prompt 是它真金白银消耗的，成本是真的，
// 维持原有行为不动。这条防止修复过头，把合法的空响应也免单。
func TestDeliveredTextUsageKeepsBillingNormalEnd(t *testing.T) {
	usage := deliveredCase(t, relaycommon.StreamEndReasonDone, "")

	require.NotNil(t, usage)
	assert.EqualValues(t, 36861, usage.PromptTokens,
		"正常收尾的空响应，上游确实消耗了 prompt，成本真实，不应免单")
}

// info 或 StreamStatus 为 nil 时不能炸，也不能凭空免单 —— 判不出来就维持原行为。
func TestDeliveredTextUsageFallsBackWhenStatusUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.1"},
	}
	info.SetEstimatePromptTokens(1000)

	usage := DeliveredTextUsage(c, info, "")
	require.NotNil(t, usage)
	assert.EqualValues(t, 1000, usage.PromptTokens, "没有 StreamStatus 就无法判定，维持原行为")

	assert.NotPanics(t, func() { DeliveredTextUsage(c, nil, "") })
}
