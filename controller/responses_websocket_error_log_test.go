package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableResponsesWSErrorLog 打开 type=5 记账。用例显式置位并在 t.Cleanup 里还原，
// 避免依赖进程环境变量或包级初值（ERROR_LOG_ENABLED 的包级默认值会随 common/init.go 变动）。
func enableResponsesWSErrorLog(t *testing.T) {
	t.Helper()
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })
}

func responsesWSTurnRequest(input string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"`+input+`"}`))
}

// channelFailedTurn 复刻 relay/responses_websocket.go 里 runCall 的渠道失败形状：
// 渠道号写在**子请求上下文**上（轮次结束即丢弃），失败由 service.ProcessChannelError
// 落一行 type=5 并在该子请求上下文上打去重标记。
func channelFailedTurn(channelId int) func(*gin.Context) *types.NewAPIError {
	return func(c *gin.Context) *types.NewAPIError {
		common.SetContextKey(c, constant.ContextKeyChannelId, channelId)
		apiErr := types.NewErrorWithStatusCode(errors.New("upstream websocket handshake failed"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
		service.ProcessChannelError(c, *types.NewChannelError(channelId, constant.ChannelTypeOpenAI, "responses-ws-error-log", false, "", false), apiErr, nil)
		return apiErr
	}
}

// preChannelFailedTurn 复刻「还没选出渠道就失败」的形状：选渠道、模型/映射解析、
// 预扣费、令牌模型限制都在这一类里，它们从不经过 ProcessChannelError，所以这一轮
// 必须靠逐轮终结记账才有 type=5 行。
func preChannelFailedTurn() func(*gin.Context) *types.NewAPIError {
	return func(*gin.Context) *types.NewAPIError {
		return types.NewErrorWithStatusCode(errors.New("no channel available for this model"), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable)
	}
}

// preChannelFailedTurnOnChannel 是 preChannelFailedTurn 的变体：渠道号已经写在子请求
// 上下文上（选中了渠道才失败，例如握手/前置校验），但仍然没有走 ProcessChannelError。
// 这是唯一能证明「逐轮终结记账把渠道号取对了」的形状：渠道路径自己写的行根本不需要
// 这段记账，两轮都走渠道路径的话这段代码删掉测试照样绿。
func preChannelFailedTurnOnChannel(channelId int) func(*gin.Context) *types.NewAPIError {
	turn := preChannelFailedTurn()
	return func(c *gin.Context) *types.NewAPIError {
		common.SetContextKey(c, constant.ContextKeyChannelId, channelId)
		return turn(c)
	}
}

// Defect 1 的核心回归：同一条连接上连续两轮失败，必须留下**两行** type=5。
// 旧的实现把去重标记从子请求搬到了外层连接上下文，于是第二轮（以及之后每一轮）
// 都被上一轮的打标挡住——第一轮的渠道尝试行还在，但第二轮的失败彻底不可见，
// 通用日志页上又回到了「只看得到成功请求」的状态。
func TestResponsesWSRequestRunnerGivesEachFailedTurnItsOwnErrorLogRow(t *testing.T) {
	user, token := setupResponsesWSRequestTest(t)
	enableResponsesWSErrorLog(t)
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	runner, _ := newResponsesWSTestRunner(t, token)

	require.NotNil(t, runner(responsesWSTurnRequest("first"), "ws-turn-1", channelFailedTurn(303)))
	require.NotNil(t, runner(responsesWSTurnRequest("second"), "ws-turn-2", preChannelFailedTurn()))

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ? AND user_id = ?", model.LogTypeError, user.Id).Order("id").Find(&logs).Error)
	require.Len(t, logs, 2, "两轮失败必须各留一行：不能是 0（被上一轮标记吞掉），也不能是 3（轮内 N+1）")
	assert.Equal(t, "ws-turn-1", logs[0].RequestId)
	assert.Equal(t, "ws-turn-2", logs[1].RequestId)
	assert.Equal(t, 303, logs[0].ChannelId)
	// 选渠道之前就失败的一轮没有任何渠道可归属，channel_id 只能是 0。
	assert.Zero(t, logs[1].ChannelId, "选渠道之前就失败的一轮没有渠道可归属")
	for index, stored := range logs {
		assert.Zero(t, stored.Quota, "第 %d 行错误行恒为零计费，不得动到额度语义", index)
		assert.Zero(t, stored.PromptTokens)
		assert.Zero(t, stored.CompletionTokens)
	}
}

// Defect 2 的回归：带渠道号的失败行必须带上**真实**渠道号，而且**这条断言必须由
// 逐轮终结记账（responses_websocket.go 里的 RecordRequestErrorLog 调用）来满足**。
// 第 1、3 轮走 preChannelFailedTurnOnChannel——它们在子请求上下文上设了
// ContextKeyChannelId 却没有调用 ProcessChannelError，那两行只能由逐轮记账写。
// 把那行代码删掉，本用例只剩第 2 轮那一行，require.Len(logs, 3) 立刻红。
//
// 第 2 轮反过来钉住去重：它走 ProcessChannelError，自己写了一行并在**本轮**子请求
// 上下文上打了去重标记，本轮随后的终结记账必须被挡住，只贡献一行。去重标记是每轮
// 独立的（子请求上下文每轮新建），所以它绝不能跨轮生效——第 3 轮即使紧接着失败，
// 也照样要自己写一行；标记若误跨轮，第 3 轮会消失，总数掉到 2 而不是 3。
//
// 「选渠道之前就失败的一轮 channel_id 为 0」由上面的
// TestResponsesWSRequestRunnerGivesEachFailedTurnItsOwnErrorLogRow 断言，不在此重复。
func TestResponsesWSEachTurnErrorLogRowCarriesTheRealChannelId(t *testing.T) {
	user, token := setupResponsesWSRequestTest(t)
	enableResponsesWSErrorLog(t)
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	runner, _ := newResponsesWSTestRunner(t, token)

	require.NotNil(t, runner(responsesWSTurnRequest("first"), "ws-turn-1", preChannelFailedTurnOnChannel(909)))
	require.NotNil(t, runner(responsesWSTurnRequest("second"), "ws-turn-2", channelFailedTurn(707)))
	require.NotNil(t, runner(responsesWSTurnRequest("third"), "ws-turn-3", preChannelFailedTurnOnChannel(424)))

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ? AND user_id = ?", model.LogTypeError, user.Id).Order("id").Find(&logs).Error)
	require.Len(t, logs, 3, "三轮各一行：第 1、3 行只能由逐轮终结记账写出；第 2 行证明去重标记挡住了同轮 ProcessChannelError 行之后的重复；第 3 行证明标记没有跨轮生效")
	assert.Equal(t, 909, logs[0].ChannelId, "选中渠道后失败的一轮必须把渠道号从子请求上下文写进行里")
	assert.Equal(t, 707, logs[1].ChannelId)
	assert.Equal(t, 424, logs[2].ChannelId)
	for index, stored := range logs {
		assert.NotZero(t, stored.ChannelId, "第 %d 行必须可归因到渠道", index)
	}
}

// 走完整条 WebSocket 通道的端到端回归：一条连接上连续发两次 response.create，
// 上游两次都在**请求被接受之前**用 error 帧拒绝。这种失败不经过 ProcessChannelError，
// 因此每一轮都必须由逐轮终结记账补出且只补出一行 type=5。
func TestResponsesWebSocketWritesOneErrorLogRowPerFailedTurn(t *testing.T) {
	const rejection = `{"type":"error","status":500,"error":{"type":"server_error","code":"server_error","message":"upstream rejected the request"}}`
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		// 握手成功后连接被复用，两轮 response.create 落在同一条上游连接上。
		for turn := 0; turn < 2; turn++ {
			if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
				return
			}
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(rejection))) {
				return
			}
		}
		_, _, _ = ws.ReadMessage()
	})
	enableResponsesWSErrorLog(t)

	for turn := 0; turn < 2; turn++ {
		require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","event_id":"turn-%d","model":"ws-billing","input":"hi"}`, turn))))
		rejected := readResponsesWSTestEvent(t, fixture.client)
		require.Equal(t, "error", rejected["type"], "第 %d 轮应当被上游拒绝", turn)
		require.Equal(t, float64(http.StatusInternalServerError), rejected["status"])
	}
	fixture.closeAndWait(t)

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ? AND user_id = ?", model.LogTypeError, fixture.user.Id).Order("id").Find(&logs).Error)
	require.Len(t, logs, 2, "一条连接上连续两轮失败必须留下两行 type=5，而不是一行")
	for index, stored := range logs {
		assert.Equal(t, fixture.channel.Id, stored.ChannelId, "第 %d 行必须归属到真实渠道，不能是 0", index)
		assert.Equal(t, fmt.Sprintf("responses-ws-billing-ws-%d", index), stored.RequestId)
		assert.Zero(t, stored.Quota, "错误行恒为零计费")
	}
}
