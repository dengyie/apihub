package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newErrorLogTestEnv 起一套内存日志库，让错误行真的落库可查。
// 与 relay_error_log_test.go 同一套脚手架：错误行唯一的价值就是能被查出来，
// 只断言「函数被调用过」等于没测。
func newErrorLogTestEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: 9, Username: "failure-log-owner", Group: "default"}).Error)
	return database
}

func newErrorLogTestContext(t *testing.T) *gin.Context {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 9)
	ctx.Set("username", "failure-log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 13)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))
	return ctx
}

// countErrorLogs 只数 type=5 行——通用日志页可见的就是这一类。
func countErrorLogs(t *testing.T, database *gorm.DB, userId int) int64 {
	t.Helper()
	var total int64
	require.NoError(t, database.Model(&model.Log{}).Where("type = ? AND user_id = ?", model.LogTypeError, userId).Count(&total).Error)
	return total
}

// 前置失败（模型/映射解析、选渠道、预扣费）从不经过 ProcessChannelError，
// 此前在通用日志页上完全不可见。终结路径必须补这一行。
func TestRecordRelayRequestFailureLogRecordsPreChannelFailure(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	apiErr := types.NewError(errors.New("model mapping resolved to an empty name"), types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithStatusCode(http.StatusBadRequest))

	recordRequestErrorLogSafely(ctx, nil, apiErr)

	assert.Equal(t, int64(1), countErrorLogs(t, database, 9))
	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, model.LogTypeError, stored.Type)
	// 错误行恒为零计费，不得因为新增这条路径而动到额度语义。
	assert.Equal(t, 0, stored.Quota)
	assert.Equal(t, 0, stored.PromptTokens)
	assert.Equal(t, 0, stored.CompletionTokens)
	other, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadRequest), other["status_code"])
}

// 最重要的回归断言：渠道重试里每次尝试失败都已由 ProcessChannelError 落行，
// 终结路径再补一行就是 N+1 行。这里必须恰好一行。
func TestRecordRelayRequestFailureLogDoesNotDuplicateChannelError(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	ctx.Set("channel_id", 303)
	channelError := types.ChannelError{ChannelId: 303, ChannelType: 1, ChannelName: "dup-check-channel", AutoBan: false}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	// 两次渠道尝试失败（重试循环的真实形状），随后请求以同一个错误终结。
	processChannelError(ctx, channelError, apiErr, nil)
	processChannelError(ctx, channelError, apiErr, nil)
	require.Equal(t, int64(2), countErrorLogs(t, database, 9), "ProcessChannelError 自身的行为不在本次改动范围内")

	recordRequestErrorLogSafely(ctx, nil, apiErr)
	assert.Equal(t, int64(2), countErrorLogs(t, database, 9), "终结路径必须被去重标记挡住")
}

// 下面几条是**开关/opt-out/守卫闸门**用例：其中断言 0 行的那些，把整条 type=5 记账
// 删掉之后同样会绿。它们的作用是在有人不小心放宽 IsClientAbortedError /
// ErrOptionWithNoRecordErrorLog / ErrorLogEnabled 三个闸门时立刻变红——每条都配了
// 阳性对照或前置条件断言，保证 0 行归因得到。真正会因为「特性被删」而变红的是上面
// 两条（断言 1 行和 2 行）以及 responses_websocket_error_log_test.go 里的逐轮用例。

// 客户端取消不是上游故障，不记渠道失败、不记请求失败。
//
// 注意这一条真正挡住它的是**哪道闸门**：types.NewClientAbortedError 构造时就带上了
// ErrOptionWithNoRecordErrorLog，所以它是被 !IsRecordErrorLog 那道闸门挡下的，
// 和下面 HonorsNoRecordOptOut 走的是同一条路。它并没有覆盖 IsClientAbortedError。
// 真正覆盖那道闸门的是 TestRecordRelayRequestFailureLogSkipsUpstreamCodedClientAbort。
func TestRecordRelayRequestFailureLogSkipsClientAbort(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	apiErr := types.NewClientAbortedError(context.Canceled)

	recordRequestErrorLogSafely(ctx, nil, apiErr)

	assert.Equal(t, int64(0), countErrorLogs(t, database, 9))
}

// IsClientAbortedError 这道闸门**不是**死代码，删掉它会造成真实的日志回归。
//
// 判据是错误码，而不是「谁构造的」：relaykit/types/error.go 里 errorCode 字段有三条
// 与 NewClientAbortedError 无关的赋值路径，其中 WithOpenAIError (:359) 和
// WithClaudeError (:383) 直接把**上游响应体里的 code/type 字符串**写成 errorCode，且
// 这两条都不施加 ErrOptionWithNoRecordErrorLog。
//
// 真实形状见 relay/responses_websocket.go:439 的「上游在请求被接受之前用 error 帧拒绝」：
// rejected := types.WithOpenAIError(*rejection.Error, rejection.Status, ErrOptionWithSkipRetry())
// ——上游给什么 code 就是什么 code，没有 opt-out，而且这条路径不经过 ProcessChannelError，
// 所以 ContextKeyErrorLogRecorded 去重标记也没人打。三道闸门里只有 IsClientAbortedError
// 拦得住它，删掉就等于让上游可以凭空决定「这条失败要不要进通用日志页」。
//
// 下面两个断言分别钉住「只有这道闸门拦得住」和「拦不住就真的会写行」。
func TestRecordRelayRequestFailureLogSkipsUpstreamCodedClientAbort(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	upstreamCoded := types.WithOpenAIError(types.OpenAIError{
		Message: "upstream rejected the request",
		Type:    "invalid_request_error",
		Code:    "client_aborted",
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())

	// 前置条件：另外两道闸门都拦不住它，所以 0 行只能归功于 IsClientAbortedError。
	require.True(t, types.IsClientAbortedError(upstreamCoded))
	require.True(t, types.IsRecordErrorLog(upstreamCoded), "上游构造的 code 不带 opt-out，no-record 闸门拦不住")
	require.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyErrorLogRecorded), "没有 ProcessChannelError，去重闸门拦不住")

	recordRequestErrorLogSafely(ctx, nil, upstreamCoded)
	assert.Equal(t, int64(0), countErrorLogs(t, database, 9), "上游把 code 写成 client_aborted 时不得落行")
}

// 阳性对照：与上一条形状完全相同、只把 code 换成别的值，就必须落行。
// 没有它，上一条的 0 行无法区分「闸门正确挡住」与「整条路径根本没在工作」。
func TestRecordRelayRequestFailureLogWritesRowForNonAbortUpstreamCode(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	ordinary := types.WithOpenAIError(types.OpenAIError{
		Message: "ordinary upstream rejection",
		Type:    "invalid_request_error",
		Code:    "invalid_request_error",
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())

	require.False(t, types.IsClientAbortedError(ordinary))
	require.True(t, types.IsRecordErrorLog(ordinary))

	recordRequestErrorLogSafely(ctx, nil, ordinary)
	assert.Equal(t, int64(1), countErrorLogs(t, database, 9), "非 client_aborted 的上游拒绝必须照常落行")
}

// 显式关闭记账的错误（billing_session、responses_websocket 的 no-record 分支等）
// 不能被新的终结路径翻案。
func TestRecordRelayRequestFailureLogHonorsNoRecordOptOut(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)
	apiErr := types.NewErrorWithStatusCode(errors.New("insufficient user quota"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithNoRecordErrorLog())

	recordRequestErrorLogSafely(ctx, nil, apiErr)

	assert.Equal(t, int64(0), countErrorLogs(t, database, 9))
}

// 开关关闭时不写任何行：ERROR_LOG_ENABLED=false 必须真的关掉记账。
func TestRecordRelayRequestFailureLogRespectsDisabledFlag(t *testing.T) {
	database := newErrorLogTestEnv(t)
	constant.ErrorLogEnabled = false
	ctx := newErrorLogTestContext(t)
	apiErr := types.NewError(errors.New("upstream failed"), types.ErrorCodeGenRelayInfoFailed)

	recordRequestErrorLogSafely(ctx, nil, apiErr)

	assert.Equal(t, int64(0), countErrorLogs(t, database, 9))
}

// 成功路径守卫：成功的请求**一行 type=5 都不能产生**。
//
// 单靠「0 行」这条断言是**不承重**的：`RecordRequestErrorLog` 有两道闸门
// （`err == nil` 和 `!types.IsRecordErrorLog(err)`），而 `types.IsRecordErrorLog`
// 对 nil 接收者是 nil-safe 的（relaykit/types/error.go:464-467）。只删掉 `err == nil`，
// nil 仍被第二道闸门挡住，断言照样绿。两道都删掉，nil 会一路流到
// `writeErrorLogRow` 里的 `err.StatusCode` **字段**解引用（注意不是 nil-safe 的
// `Get*` 方法）上 panic，而 `recordRequestErrorLogSafely` 的 `recover()` 会把 panic
// 吞掉——结果仍然是 0 行。原来那条注释声称「删掉闸门没有测试会报警」，是错的。
//
// 因此这里断言三件事：
//  1. 阴性：nil 经带 recover 的包装层 → 0 行（保留原语义）。
//  2. 阳性对照：同一条路径拿到**真实错误**必须恰好 1 行。没有它，上面那条 0 行
//     无法区分「闸门正确挡住」与「路径整个坏掉/全在 panic」。
//  3. panic 探针：绕开 recover 直接调用 `service.RecordRequestErrorLog(directCtx, nil, nil)`，
//     断言它**干净返回**。这是本用例真正的承重点——闸门一旦被同时放宽，nil 会流到
//     `err.StatusCode` 字段解引用上 panic，用例立刻变红，而不是靠 0 行蒙混过关。
func TestRecordRelayRequestFailureLogWritesNothingOnSuccess(t *testing.T) {
	database := newErrorLogTestEnv(t)
	ctx := newErrorLogTestContext(t)

	// (1) 阴性：成功请求经终结路径不得写 type=5 行。
	recordRequestErrorLogSafely(ctx, nil, nil)
	assert.Equal(t, int64(0), countErrorLogs(t, database, 9), "成功请求不得写 type=5 行")

	// (2) 阳性对照：同一路径遇到真实错误必须恰好写 1 行。用独立 context，避免
	// ContextKeyErrorLogRecorded 去重标记把这一步挡掉。
	positiveCtx := newErrorLogTestContext(t)
	positiveErr := types.NewError(errors.New("upstream failed"), types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithStatusCode(http.StatusBadGateway))
	recordRequestErrorLogSafely(positiveCtx, nil, positiveErr)
	require.Equal(t, int64(1), countErrorLogs(t, database, 9), "阳性对照：真实错误必须写 1 行，否则上面的 0 行只是因为整条路径失效")

	// (3) panic 探针：直接调用 service 层，绕开 recordRequestErrorLogSafely 的 recover。
	// 闸门被同时放宽时这里会 panic 而不是返回。
	directCtx := newErrorLogTestContext(t)
	require.NotPanics(t, func() {
		service.RecordRequestErrorLog(directCtx, nil, nil)
	}, "RecordRequestErrorLog(ctx, nil, nil) 必须干净返回：err==nil 与 !IsRecordErrorLog 两道闸门若被同时删掉，nil 会流到 writeErrorLogRow 的 err.StatusCode 字段解引用上 panic")
	require.Equal(t, int64(1), countErrorLogs(t, database, 9), "直接以 nil 调用 service 层不得补写任何 type=5 行")
}
