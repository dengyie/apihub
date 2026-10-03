package common

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

// ResponseOutcome is the protocol-level result of one response, independent of
// how the transport ended. Adaptors mark it from the events they already parse.
type ResponseOutcome string

const (
	ResponseOutcomeUnknown    ResponseOutcome = ""
	ResponseOutcomeCompleted  ResponseOutcome = "completed"
	ResponseOutcomeFailed     ResponseOutcome = "failed"
	ResponseOutcomeIncomplete ResponseOutcome = "incomplete"
	ResponseOutcomeCancelled  ResponseOutcome = "cancelled"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu         sync.Mutex
	Errors     []StreamErrorEntry
	ErrorCount int

	response         ResponseOutcome
	errorCode        string
	errorType        string
	errorStatus      int
	incompleteReason string
	expectsTerminal  bool
}

// StreamOutcome holds classification facts only; upstream messages never
// enter it because they may contain credentials or request content.
type StreamOutcome struct {
	EndReason        StreamEndReason
	HasErrors        bool
	ExpectsTerminal  bool
	Response         ResponseOutcome
	ErrorCode        string
	ErrorType        string
	ErrorStatus      int
	IncompleteReason string
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.EndReason = reason
		s.EndError = err
	})
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

// RequireTerminal declares that the protocol always ends with an explicit
// terminal event, so a stream that ends without one was cut short.
func (s *StreamStatus) RequireTerminal() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectsTerminal = true
}

func (s *StreamStatus) ExpectsTerminal() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expectsTerminal
}

// MarkCompleted, MarkIncomplete and MarkCancelled keep the first terminal seen;
// MarkFailed always wins because an error after completion is still a failure.
func (s *StreamStatus) MarkCompleted() {
	s.markTerminal(ResponseOutcomeCompleted, "")
}

func (s *StreamStatus) MarkIncomplete(reason string) {
	s.markTerminal(ResponseOutcomeIncomplete, reason)
}

func (s *StreamStatus) MarkCancelled() {
	s.markTerminal(ResponseOutcomeCancelled, "")
}

func (s *StreamStatus) markTerminal(outcome ResponseOutcome, incompleteReason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.response != ResponseOutcomeUnknown {
		return
	}
	s.response = outcome
	s.incompleteReason = incompleteReason
}

// MarkFailed records a protocol failure. Empty details never erase details
// recorded earlier, so a bare error envelope keeps the structured error.
func (s *StreamStatus) MarkFailed(code, errorType string, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.response = ResponseOutcomeFailed
	if code != "" {
		s.errorCode = code
	}
	if errorType != "" {
		s.errorType = errorType
	}
	if status != 0 {
		s.errorStatus = status
	}
}

func (s *StreamStatus) ResponseOutcome() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.response)
}

func (s *StreamStatus) ResponseFailed() bool {
	return s.ResponseOutcome() == string(ResponseOutcomeFailed)
}

func (s *StreamStatus) OutcomeSnapshot() StreamOutcome {
	if s == nil {
		return StreamOutcome{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return StreamOutcome{
		EndReason:        s.EndReason,
		HasErrors:        s.hasErrorsLocked(),
		ExpectsTerminal:  s.expectsTerminal,
		Response:         s.response,
		ErrorCode:        s.errorCode,
		ErrorType:        s.errorType,
		ErrorStatus:      s.errorStatus,
		IncompleteReason: s.incompleteReason,
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasErrorsLocked()
}

func (s *StreamStatus) hasErrorsLocked() bool {
	if s.response == ResponseOutcomeFailed {
		return true
	}
	if s.ErrorCount > 0 {
		return true
	}
	if s.EndError != nil {
		// client_gone with context.Canceled is client-side cancellation, not upstream error
		if s.EndReason == StreamEndReasonClientGone && errors.Is(s.EndError, context.Canceled) {
			return false
		}
		return true
	}
	switch s.EndReason {
	case StreamEndReasonScannerErr, StreamEndReasonTimeout, StreamEndReasonPanic:
		return true
	}
	return false
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

// IsClientAbort 判断本次流是否因客户端主动断开而结束。
//
// 客户端断开是下游行为而不是上游渠道故障：它既不该计入熔断计数，也不该触发
// 换渠道重试，更不该把一个健康渠道判成「上游流中断」而立即熔断。
//
// 这个判据此前在三处各写一遍且彼此不等价——hasErrorsLocked 只在 EndError 分支
// 豁免（被前面的 ErrorCount>0 短路掉）、流扫描器的两处又各自拼一遍 context
// canceled 字符串匹配。三处判据不一致导致客户端取消被误判成上游流故障：
// 收尾期我们自己关闭上游 body 产生的读错误记入 ErrorCount，hasErrorsLocked
// 随之返回 true，控制器把它升级成 StreamBrokenError 并立即熔断健康渠道。
// 收敛成唯一判据，消掉这一类不一致。
func (s *StreamStatus) IsClientAbort() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EndReason == StreamEndReasonClientGone {
		return true
	}
	return s.EndError != nil && errors.Is(s.EndError, context.Canceled)
}

// IsUpstreamStreamFault 判断本次流是否应被归因为上游渠道的传输故障——即该不该
// 计入熔断计数、该不该换一个渠道重试。
//
// 这是唯一判据。此前 controller 与流扫描器各写了一遍等价逻辑而彼此不等价：
// controller 那份的软错误分支漏掉了客户端断开豁免，于是收尾期我们自己关闭上游
// body 产生的读错误会把「客户端按 ESC 放弃」升级成「上游流中断」，实测一天误
// 熔断 112 次健康渠道并为 35 个已死请求各多烧一次真实上游调用。任何新增的流
// 终止路径都必须走这里，不要再自己拼条件。
func (s *StreamStatus) IsUpstreamStreamFault() bool {
	if s == nil {
		return false
	}
	if s.IsClientAbort() {
		return false
	}
	return s.HasErrors() || !s.IsNormalEnd()
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.EndReason == StreamEndReasonDone || s.EndReason == StreamEndReasonHandlerStop {
		return true
	}
	if s.EndReason == StreamEndReasonEOF {
		if s.expectsTerminal {
			// 若协议要求显式终止帧（如 OpenAI 的 [DONE]、Claude 的 message_stop），
			// 只有明确收到了完成标识（MarkCompleted）时，EOF 才算正常收尾。
			// 否则中途断开（无 finish_reason/stop）属于异常截断。
			return s.response == ResponseOutcomeCompleted
		}
		return true
	}
	return false
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	return b.String()
}

// IsResponsesTerminalStatus reports whether the raw JSON status field of an OpenAI Responses object
// represents a terminal status (e.g. "completed", "failed", "cancelled", "canceled", "incomplete").
func IsResponsesTerminalStatus(status []byte) bool {
	if len(status) == 0 {
		return false
	}
	s := strings.Trim(string(status), "\" \t\r\n")
	switch strings.ToLower(s) {
	case "completed", "failed", "cancelled", "canceled", "incomplete":
		return true
	default:
		return false
	}
}
