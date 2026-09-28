package loadbalancer

import "errors"

// selector.go 提供与 new-api 渠道选择逻辑的集成点。
//
// 集成方式（在 service/channel_select.go 中调用）：
//  1. 选渠道前：调用 FilterAvailable 过滤掉过载/熔断的渠道
//  2. 请求开始：调用 GlobalTracker().Begin(channelID) 获取句柄
//  3. 首字到达：调用 handle.MarkFirstByte()
//  4. 请求结束：调用 handle.End(slow, failed)
//
// TTFT 超时检测由调用方在读取流时实现：
// 超过 policy.TTFTTimeoutMs 未收到首字时，应主动取消上游请求，
// 将本次标记为 slow=true 调用 End，并把错误视为可重试
// （在 service/relay_error.go 的 DecideRelayRetry 中处理）。

// ErrTTFTTimeout 首字超时错误标记。
// 调用方在首字超时时可用此错误触发重试决策。
type TTFTTimeoutError struct {
	ChannelID int
	TimeoutMs int64
}

func (e *TTFTTimeoutError) Error() string {
	return "loadbalancer: time to first token timeout"
}

// IsTTFTTimeout 判断是否为首字超时错误（支持从 NewAPIError 中解包）
func IsTTFTTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ttftErr *TTFTTimeoutError
	return errors.As(err, &ttftErr)
}

// EmptyStreamError 空流错误：上游正常结束了流（reason=done），
// 但一个有效内容块都没发（received=0）。视同渠道失败，触发换渠道重试。
// 由于客户端尚未收到任何数据，重试对客户端透明。
type EmptyStreamError struct {
	ChannelID int
}

func (e *EmptyStreamError) Error() string {
	return "loadbalancer: upstream returned empty stream"
}

// IsEmptyStream 判断是否为空流错误（支持从 NewAPIError 中解包）
func IsEmptyStream(err error) bool {
	if err == nil {
		return false
	}
	var emptyErr *EmptyStreamError
	return errors.As(err, &emptyErr)
}

// StreamBrokenError 流中断错误：上游在流传输中途断开连接
// （如 HTTP/2 INTERNAL_ERROR、connection reset），
// 且客户端尚未收到任何有效内容。视同渠道失败，触发换渠道重试。
// 由于客户端尚未收到任何数据，重试对客户端透明。
type StreamBrokenError struct {
	ChannelID int
	Reason    string
}

func (e *StreamBrokenError) Error() string {
	return "loadbalancer: upstream stream broken: " + e.Reason
}

// IsStreamBroken 判断是否为流中断错误（支持从 NewAPIError 中解包）
func IsStreamBroken(err error) bool {
	if err == nil {
		return false
	}
	var brokenErr *StreamBrokenError
	return errors.As(err, &brokenErr)
}

