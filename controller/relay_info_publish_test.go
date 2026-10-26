package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRelayPublishesRelayInfoBeforeEarlyReturns 钉住发布契约：Relay 在 GenRelayInfo
// 成功后必须把 *RelayInfo 写进 gin context 的 ContextKeyRelayInfo，且位于一切早退
// 分支之前。
//
// 消费方是 distributor 在 c.Next() 之后调用的 RecordChannelAffinity——零字节断流
// 清绑定的守卫从这里读 StreamStatus。2026-10-27 的教训：守卫初版上线时这个 key
// 只有 WS 路径写、HTTP 路径没人写，守卫成了死代码，而针对守卫本身的单测因为
// 手动 set 了 key 而全绿。这类缺陷只有对真 Relay() 的端到端断言才能拦住：发布
// 行被删，本用例必红。
//
// 刻意不要求请求成功：发布在计费/选路之前，任何后续失败（无定价、无渠道）都
// 不影响断言。请求体合法即可走到 GenRelayInfo 之后的发布点。
func TestRelayPublishesRelayInfoBeforeEarlyReturns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"relay-info-publish-probe","messages":[{"role":"user","content":"hi"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	Relay(ctx, types.RelayFormatOpenAI)

	info, ok := common.GetContextKeyType[*relaycommon.RelayInfo](ctx, constant.ContextKeyRelayInfo)
	require.True(t, ok, "Relay 必须在 ctx 上发布 relay_info；缺失则断流清绑定守卫读不到流状态")
	require.NotNil(t, info)
	require.NotNil(t, ctx.Request, "发布不得篡改请求对象")
}
