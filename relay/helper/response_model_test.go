package helper

import (
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/stretchr/testify/assert"
)

func TestRewriteResponseModelBytes(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-flash",
	}

	// Different model -> rewrites to OriginModelName
	input := []byte(`{"id":"chatcmpl-1","model":"qwen3.8-27b-coder-plus-step1940","choices":[]}`)
	expected := `{"id":"chatcmpl-1","model":"qwen3.8-flash","choices":[]}`
	assert.JSONEq(t, expected, string(RewriteResponseModelBytes(info, input)))

	// Same model -> unchanged
	sameInput := []byte(`{"id":"chatcmpl-1","model":"qwen3.8-flash","choices":[]}`)
	assert.Equal(t, string(sameInput), string(RewriteResponseModelBytes(info, sameInput)))

	// Nil info -> unchanged
	assert.Equal(t, string(input), string(RewriteResponseModelBytes(nil, input)))

	// Empty OriginModelName -> unchanged
	emptyInfo := &relaycommon.RelayInfo{OriginModelName: ""}
	assert.Equal(t, string(input), string(RewriteResponseModelBytes(emptyInfo, input)))

	// No model field -> unchanged
	noModel := []byte(`{"id":"chatcmpl-1","choices":[]}`)
	assert.Equal(t, string(noModel), string(RewriteResponseModelBytes(info, noModel)))

	// Empty model field -> patched to OriginModelName
	emptyModel := []byte(`{"id":"chatcmpl-1","model":"","choices":[]}`)
	expectedEmptyPatched := `{"id":"chatcmpl-1","model":"qwen3.8-flash","choices":[]}`
	assert.JSONEq(t, expectedEmptyPatched, string(RewriteResponseModelBytes(info, emptyModel)))
}

func TestRewriteResponseModelString(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-flash",
	}

	frame := `{"id":"chatcmpl-1","model":"qwen3.8-27b-free","choices":[{"delta":{"content":"hi"}}]}`
	expected := `{"id":"chatcmpl-1","model":"qwen3.8-flash","choices":[{"delta":{"content":"hi"}}]}`
	assert.JSONEq(t, expected, RewriteResponseModelString(info, frame))

	emptyModelFrame := `{"id":"chatcmpl-1","model":"","choices":[{"delta":{"content":"hi"}}]}`
	assert.JSONEq(t, expected, RewriteResponseModelString(info, emptyModelFrame))

	// Nil info or empty OriginModelName
	assert.Equal(t, frame, RewriteResponseModelString(nil, frame))
	assert.Equal(t, frame, RewriteResponseModelString(&relaycommon.RelayInfo{}, frame))
}

func TestRewriteResponsesResponseModel(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-flash",
	}

	frame := `{"type":"response.created","response":{"id":"resp_1","model":"upstream-model"}}`
	expected := `{"type":"response.created","response":{"id":"resp_1","model":"qwen3.8-flash"}}`
	assert.JSONEq(t, expected, RewriteResponsesResponseModelString(info, frame))
	assert.JSONEq(t, expected, string(RewriteResponsesResponseModelBytes(info, []byte(frame))))
}
