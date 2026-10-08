package service

import (
	"slices"
	"sync"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/model"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

const requestPolicyContextKey = "request_policy_state"

type PolicyDecision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Source string `json:"source"`
}

type PolicyEvent struct {
	Attempt     int            `json:"attempt"`
	ChannelID   int            `json:"channel_id,omitempty"`
	Group       string         `json:"group,omitempty"`
	Rule        string         `json:"rule,omitempty"`
	Status      int            `json:"status,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorSource string         `json:"error_source,omitempty"`
	ElapsedMS   int64          `json:"elapsed_ms"`
	Decision    PolicyDecision `json:"decision"`
	Health      string         `json:"health,omitempty"`
}

// RequestPolicyState records how one request was routed so administrators can
// read the decision flow in the log details. Channel selection and the retry
// decision stay in the relay flows; this state only records them.
type RequestPolicyState struct {
	FinalLogged       bool
	StartedAt         time.Time
	Attempts          int
	SelectedGroup     string
	SessionMode       string
	SessionModeSource string
	RuleName          string
	Successful        bool
	OutcomeRecorded   bool
	mu                sync.Mutex
	events            []PolicyEvent
}

func RequestPolicy(c *gin.Context) *RequestPolicyState {
	if c != nil {
		if value, ok := c.Get(requestPolicyContextKey); ok {
			return value.(*RequestPolicyState)
		}
	}
	state := &RequestPolicyState{StartedAt: time.Now()}
	if c != nil {
		c.Set(requestPolicyContextKey, state)
	}
	return state
}

func (s *RequestPolicyState) AddEvent(event PolicyEvent) {
	event.Attempt, event.ElapsedMS = s.Attempts, time.Since(s.StartedAt).Milliseconds()
	if event.Group == "" {
		event.Group = s.SelectedGroup
	}
	if event.Rule == "" {
		event.Rule = s.RuleName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) < 512 {
		s.events = append(s.events, event)
	}
}

func (s *RequestPolicyState) Events() []PolicyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *RequestPolicyState) BeginAttempt(channel *model.Channel, group string) {
	s.Attempts++
	s.Successful = false
	s.OutcomeRecorded = false
	s.SelectedGroup = group
	s.AddEvent(PolicyEvent{ChannelID: channel.Id, Decision: PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}})
}

// RecordPolicyFailure appends the failed attempt and the retry decision made
// for it. The health entry mirrors the check in ProcessChannelError, which
// performs the actual disable.
func RecordPolicyFailure(c *gin.Context, channelID int, err *types.NewAPIError, decision PolicyDecision) {
	if c == nil || err == nil {
		return
	}
	source := "upstream"
	switch {
	case types.IsChannelError(err):
		source = "channel"
	case err.GetErrorCode() == types.ErrorCodeDoRequestFailed:
		source = "transport"
	case err.GetErrorType() == types.ErrorTypeNewAPIError:
		source = "local"
	}
	state := RequestPolicy(c)
	event := PolicyEvent{ChannelID: channelID, Status: err.StatusCode, ErrorCode: string(err.GetErrorCode()), ErrorSource: source, Decision: PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: source}}
	if source == "local" {
		event.Decision.Reason = "local_rejection"
	}
	state.AddEvent(event)
	event.Decision, event.Health = decision, "unchanged"
	// 本函数跑在 ProcessChannelError **之前**，而禁用是否真的执行由后者的佐证
	// 闸门决定（四条路径都是先 RecordPolicyFailure 再 processChannelError）。
	// 这里用只读的 Peek 提前知道结论，否则标签会说「已请求禁用」而渠道根本没被
	// 禁用 —— 那正是这个特性当初要消灭的那类「日志与事实不符」。
	if source != "local" && c.GetBool("auto_ban") {
		if verdict := classifyAutoDisable(channelID, err); verdict.Disable {
			modelName := c.GetString(string(constant.ContextKeyOriginalModel))
			if loadbalancer.PeekAutoDisableCorroboration(channelID, modelName, verdict.Class) {
				event.Health = "channel_disable_requested"
				if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
					event.Health = "key_disable_requested"
				}
			} else {
				event.Health = "channel_disable_pending_corroboration"
			}
		}
	}
	state.AddEvent(event)
}

func MarkRequestPolicySuccess(c *gin.Context, stream *relaycommon.StreamStatus) {
	state := RequestPolicy(c)
	if state.OutcomeRecorded {
		return
	}
	state.OutcomeRecorded = true
	state.Successful = stream == nil || stream.IsNormalEnd() && !stream.HasErrors() && (stream.ResponseOutcome() == "" || stream.ResponseOutcome() == "completed")
	decision := PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}
	if !state.Successful {
		decision = PolicyDecision{Action: "stop", Reason: "stream_not_successful", Source: "system"}
	}
	channelID := 0
	if c != nil {
		channelID = c.GetInt("channel_id")
	}
	// 成功即证伪：这个 (渠道, 模型) 刚刚真的通了，之前攒的「确定性失效」信号
	// 不该继续替它说话。不复位的话，一次故障高峰攒到 2/3 之后渠道自行恢复、
	// 成功跑了几十次，窗口内的下一次失败仍会顶到阈值被摘 —— 佐证就退化成了
	// 「三次即禁」，而它本来的作用恰恰是区分偶发与持续。
	//
	// modelName 为空时不复位：空模型名是所有「拿不到模型名」的失败共用的桶，
	// 一次成功无权代表整个桶。
	if state.Successful && channelID != 0 {
		if modelName := c.GetString(string(constant.ContextKeyOriginalModel)); modelName != "" {
			loadbalancer.ResetCorroboration(channelID, modelName)
		}
	}
	state.AddEvent(PolicyEvent{ChannelID: channelID, Decision: decision})
}

// Rules explicitly inherit the global default or override it. Rules without a
// mode retain their legacy retry behavior until an administrator changes them.
func EffectiveSessionMode(setting *operation_setting.ChannelAffinitySetting, rule operation_setting.ChannelAffinityRule) (mode, source string) {
	if rule.SessionMode == "inherit" {
		if setting.SessionMode != "" {
			return setting.SessionMode, "global"
		}
		return "prefer", "global"
	}
	if rule.SessionMode != "" {
		return rule.SessionMode, "session_rule"
	}
	if rule.SkipRetryOnFailure {
		return "strict", "session_rule"
	}
	return "prefer", "session_rule"
}

// RecordRequestPolicyTermination appends the final decision after routing has
// stopped. It never writes a log row itself: the per-channel error log written
// by ProcessChannelError already carries the decision record, so a second row
// here would duplicate it.
func RecordRequestPolicyTermination(c *gin.Context, apiErr *types.NewAPIError) {
	if c == nil || apiErr == nil {
		return
	}
	state := RequestPolicy(c)
	if state.FinalLogged {
		return
	}
	state.FinalLogged = true
	events := state.Events()
	if len(events) == 0 || events[len(events)-1].Decision.Action != "stop" {
		state.AddEvent(PolicyEvent{ChannelID: c.GetInt("channel_id"), Status: apiErr.StatusCode, ErrorCode: string(apiErr.GetErrorCode()), Decision: PolicyDecision{Action: "stop", Reason: "request_failed", Source: "system"}, Health: "unchanged"})
	}
}
