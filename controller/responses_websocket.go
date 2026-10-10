package controller

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/middleware"
	"github.com/dengyie/apihub/relay"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
)

type responsesWSRequestContextKey struct{}

type responsesWSRequestState struct {
	requestID string
	handle    func(*gin.Context) *types.NewAPIError
	apiError  *types.NewAPIError
}

// Each response.create runs the ordinary request middleware to completion,
// without routing another HTTP request or retaining a pooled Gin context.
var responsesWSRequestEngine = sync.OnceValue(func() *gin.Engine {
	engine := gin.New()
	engine.ForwardedByClientIP = false
	_ = engine.SetTrustedProxies(nil)
	engine.POST("/v1/responses", func(c *gin.Context) {
		state := c.Request.Context().Value(responsesWSRequestContextKey{}).(*responsesWSRequestState)
		c.Set(common.RequestIdKey, state.requestID)
		common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
		c.Next()
	}, middleware.BodyStorageCleanup(), middleware.TokenAuth(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		state := c.Request.Context().Value(responsesWSRequestContextKey{}).(*responsesWSRequestState)
		state.apiError = state.handle(c)
		// 终结记账放在**本轮**的子请求上下文上，而不是轮次循环外：
		//  - gin 每次 ServeHTTP 都会把 Keys 置空（engine.pool 只是复用对象，
		//    reset() 会清空 c.Keys），子请求上下文天然每轮一份，去重窗口因此
		//    严格等于一轮 response.create——上一轮渠道失败打上的标记不可能把
		//    这一轮（以及之后每一轮）的终结行吞掉。
		//  - 渠道号也只能在这里取：ContextKeyChannelId 由
		//    middleware/distributor.go 与 relay/relay_task.go 写在子请求上下文上，
		//    轮次结束即随上下文丢弃。RecordRequestErrorLog 自己就从这份上下文读
		//    渠道号，所以此处不需要（也不应该）再往 state 上抄一份。
		//
		// 本轮已经因为渠道尝试失败写过行时，去重标记会挡住这一行（N 次尝试仍是
		// N 行）；本轮没走到渠道（选渠道、模型/映射解析、预扣费、令牌模型限制等）
		// 时则补且只补一行——通用日志页上失败请求必须可见。
		//
		// 必须走 recordRequestErrorLogSafely，不能裸调：本 engine 是裸 gin.New()，
		// 全仓唯一的 gin.CustomRecovery 挂在 main.go:258 的主 server 上，覆盖不到这里。
		// 裸调一旦 panic 会一路穿过 ServeHTTP → runCall → ResponsesWebSocketHelper，
		// 把整条 WebSocket 连接掀掉；HTTP 路径同样包了 recover，两条路径必须对称。
		recordRequestErrorLogSafely(c, wsTurnRelayInfo(c), state.apiError)
		if state.apiError != nil {
			status := state.apiError.StatusCode
			if status < http.StatusBadRequest {
				status = http.StatusInternalServerError
			}
			c.Status(status)
		}
	})
	return engine
})

// wsTurnRelayInfo 取回本轮 relay 层构建的 RelayInfo，让 WebSocket 的 type=5 行带上
// response_model / billing_model / conversion_diagnostics，与 HTTP 行对齐。没有它时
// 返回 nil：writeErrorLogRow 及其下游 Append* 全部对 nil 安全，行会退化成修复前的形状。
func wsTurnRelayInfo(c *gin.Context) *relaycommon.RelayInfo {
	info, _ := common.GetContextKeyType[*relaycommon.RelayInfo](c, constant.ContextKeyRelayInfo)
	return info
}

type responsesWSResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *responsesWSResponseWriter) Header() http.Header {
	return w.header
}

func (w *responsesWSResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responsesWSResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(data)
}

func newResponsesWSRequestRunner(c *gin.Context) relay.ResponsesWSRequestRunner {
	// Capture credentials before channel selection or header overrides. Resolve
	// the peer once with the public router's trusted-proxy configuration.
	headers := c.Request.Header.Clone()
	for _, name := range []string{"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol", "Content-Length", "Content-Encoding"} {
		headers.Del(name)
	}
	remoteAddr := net.JoinHostPort(c.ClientIP(), "0")
	return func(request *http.Request, requestID string, handle func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
		state := &responsesWSRequestState{requestID: requestID, handle: handle}
		ctx := context.WithValue(request.Context(), responsesWSRequestContextKey{}, state)
		ctx = context.WithValue(ctx, common.RequestIdKey, requestID)
		request = request.Clone(ctx)
		request.Method = http.MethodPost
		request.URL.Path = "/v1/responses"
		request.URL.RawPath = ""
		request.Header = headers.Clone()
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = remoteAddr
		response := &responsesWSResponseWriter{header: make(http.Header)}
		responsesWSRequestEngine().ServeHTTP(response, request)
		if state.apiError != nil {
			return state.apiError
		}
		if response.status < http.StatusBadRequest {
			return nil
		}
		var body struct {
			Error *types.OpenAIError `json:"error"`
		}
		if common.Unmarshal(response.body.Bytes(), &body) == nil && body.Error != nil {
			return types.WithOpenAIError(*body.Error, response.status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		// The existing in-memory rate limiter returns a bare 429 response.
		return types.NewErrorWithStatusCode(errors.New(http.StatusText(response.status)), types.ErrorCodeInvalidRequest, response.status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
}

func ResponsesWebSocket(c *gin.Context) {
	runner := newResponsesWSRequestRunner(c)
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	// ResponsesWebSocketHelper 没有返回值，这是刻意的：它内部把每一轮 response.create
	// 的失败都在子请求上下文上处理并逐轮记账完了，返回一个错误只会被这里静默丢弃。
	// 契约由签名强制（编译器保证它无法返回需要上报的连接级失败），而不是靠注释维持。
	// 本轮记账的唯一入口是上面的逐轮终结记账。
	relay.ResponsesWebSocketHelper(c, ws, runner)
}
