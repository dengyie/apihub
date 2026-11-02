package helper

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/logger"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func FlushWriter(c *gin.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("flush panic recovered: %v", r)
		}
	}()

	if c == nil || c.Writer == nil {
		return nil
	}

	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return errors.New("streaming error: flusher not found")
	}

	flusher.Flush()
	return nil
}

func requestContextDone(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

func SetEventStreamHeaders(c *gin.Context) {
	// 检查是否已经设置过头部
	if _, exists := c.Get("event_stream_headers_set"); exists {
		return
	}

	// 设置标志，表示头部已经设置过
	c.Set("event_stream_headers_set", true)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

func ClaudeData(c *gin.Context, resp dto.ClaudeResponse) error {
	if requestContextDone(c) {
		return nil
	}

	jsonData, err := common.Marshal(resp)
	if err != nil {
		common.SysError("error marshalling stream response: " + err.Error())
	} else {
		c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
		c.Render(-1, common.CustomEvent{Data: "data: " + string(jsonData)})
	}
	_ = FlushWriter(c)
	return nil
}

func ClaudeChunkData(c *gin.Context, resp dto.ClaudeResponse, data string) {
	if requestContextDone(c) {
		return
	}

	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("data: %s\n", data)})
	_ = FlushWriter(c)
}

func ResponseChunkData(c *gin.Context, resp dto.ResponsesStreamResponse, data string) error {
	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("data: %s", data)})
	return FlushWriter(c)
}

func StringData(c *gin.Context, str string) error {
	if c == nil || c.Writer == nil {
		return errors.New("context or writer is nil")
	}

	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	c.Render(-1, common.CustomEvent{Data: "data: " + str})
	return FlushWriter(c)
}

func PingData(c *gin.Context) error {
	if c == nil || c.Writer == nil {
		return errors.New("context or writer is nil")
	}

	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	if _, err := c.Writer.Write([]byte(": PING\n\n")); err != nil {
		return fmt.Errorf("write ping data failed: %w", err)
	}
	return FlushWriter(c)
}

func ObjectData(c *gin.Context, object any) error {
	if object == nil {
		return errors.New("object is nil")
	}
	jsonData, err := common.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	return StringData(c, string(jsonData))
}

func Done(c *gin.Context) {
	_ = StringData(c, "[DONE]")
}

func WssString(c *gin.Context, ws *websocket.Conn, str string) error {
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", str))
	return ws.WriteMessage(1, []byte(str))
}

func WssObject(c *gin.Context, ws *websocket.Conn, object any) error {
	jsonData, err := common.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", jsonData))
	return ws.WriteMessage(1, jsonData)
}

func WssError(c *gin.Context, ws *websocket.Conn, openaiError types.OpenAIError) {
	if ws == nil {
		return
	}
	errorObj := &dto.RealtimeEvent{
		Type:    "error",
		EventId: GetLocalRealtimeID(c),
		Error:   &openaiError,
	}
	_ = WssObject(c, ws, errorObj)
}

func GetResponseID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("chatcmpl-%s", logID)
}

func GetLocalRealtimeID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("evt_%s", logID)
}

func GenerateStartEmptyResponse(id string, createAt int64, model string, systemFingerprint *string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Role:    "assistant",
					Content: common.GetPointer(""),
				},
			},
		},
	}
}

func GenerateStopResponse(id string, createAt int64, model string, finishReason string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				FinishReason: &finishReason,
			},
		},
	}
}

func GenerateFinalUsageResponse(id string, createAt int64, model string, usage dto.Usage) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices:           make([]dto.ChatCompletionsStreamResponseChoice, 0),
		Usage:             &usage,
	}
}

// IsEmptyStreamFrame detects whether an SSE data chunk carries zero useful information
// (e.g. keep-alive frames, empty objects "{}", empty choices "[]", or zero-valued Go structs
// `{"id":"","object":"","created":0,"model":"","system_fingerprint":null,"choices":null,"usage":null}`).
//
// Suppressing these empty frames prevents committing the HTTP response with invalid schema
// chunks, and allows StreamScannerHandler to detect real empty streams and trigger load balancer failover.
func IsEmptyStreamFrame(data string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return true
	}
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
		if strings.TrimSpace(trimmed[1:len(trimmed)-1]) == "" {
			return true
		}
	}
	if trimmed == "[DONE]" {
		return false
	}
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return false
	}

	// 1. Error chunks must never be treated as empty
	if strings.Contains(trimmed, `"error"`) {
		return false
	}

	// 2. Usage chunks must never be treated as empty (needed for token tracking and stream_options)
	if strings.Contains(trimmed, `"prompt_tokens"`) ||
		strings.Contains(trimmed, `"total_tokens"`) ||
		strings.Contains(trimmed, `"completion_tokens"`) ||
		strings.Contains(trimmed, `"input_tokens"`) ||
		strings.Contains(trimmed, `"output_tokens"`) {
		return false
	}

	// 3. Typed event streams (OpenAI Responses API, Claude SSE, Realtime, Images)
	if strings.Contains(trimmed, `"type"`) {
		return false
	}

	// 4. OpenAI Chat Completions choices handling
	if strings.Contains(trimmed, `"choices"`) {
		// Case A: choices is explicitly null or empty array
		if strings.Contains(trimmed, `"choices":null`) || strings.Contains(trimmed, `"choices": null`) ||
			strings.Contains(trimmed, `"choices":[]`) || strings.Contains(trimmed, `"choices": []`) {
			return true
		}

		// Case B: choices is present, check if it carries any substantive delta or finish_reason.
		// 白名单必须是「真实承载内容的字段」而非「碰巧认识的字段」：OpenAI 官方流式 delta 还有
		// refusal（拒答文本）、audio（语音转写）、annotations（引用标注），漏掉任何一个都会把
		// 真实内容帧判成空帧，触发误换渠道甚至误熔断。新增官方 delta 字段时必须同步补录到这里。
		if strings.Contains(trimmed, `"role"`) ||
			strings.Contains(trimmed, `"tool_calls"`) ||
			strings.Contains(trimmed, `"function_call"`) ||
			hasNonEmptyField(trimmed, "content") ||
			hasNonEmptyField(trimmed, "reasoning_content") ||
			hasNonEmptyField(trimmed, "reasoning") ||
			hasNonEmptyField(trimmed, "thinking") ||
			hasNonEmptyField(trimmed, "text") ||
			hasNonEmptyField(trimmed, "finish_reason") ||
			hasNonEmptyField(trimmed, "refusal") ||
			hasNonEmptyField(trimmed, "audio") ||
			hasNonEmptyField(trimmed, "annotations") {
			return false
		}

		// choices is present but has empty delta and null/empty finish_reason -> empty frame!
		return true
	}

	// 5. If it does not have choices, but has other content/metadata indicators:
	if strings.Contains(trimmed, `"candidates"`) ||
		strings.Contains(trimmed, `"parts"`) ||
		hasNonEmptyField(trimmed, "text") ||
		hasNonEmptyField(trimmed, "delta") {
		return false
	}

	return false
}

func hasNonEmptyField(data string, field string) bool {
	target := `"` + field + `"`
	idx := strings.Index(data, target)
	for idx != -1 {
		tail := data[idx+len(target):]
		colon := strings.IndexByte(tail, ':')
		if colon != -1 {
			val := strings.TrimLeft(tail[colon+1:], " \t\r\n")
			if len(val) > 0 {
				if val[0] == '"' && len(val) > 1 && val[1] != '"' {
					return true
				}
				if val[0] == '[' {
					inner := strings.TrimSpace(val[1:])
					if len(inner) > 0 && inner[0] != ']' {
						return true
					}
				}
				if val[0] == '{' {
					inner := strings.TrimSpace(val[1:])
					if len(inner) > 0 && inner[0] != '}' {
						return true
					}
				}
			}
		}
		next := strings.Index(tail, target)
		if next == -1 {
			break
		}
		idx += len(target) + next
	}
	return false
}
