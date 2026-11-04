package helper

import (
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RewriteResponseModelBytes rewrites the top-level "model" field in a JSON byte slice
// to info.OriginModelName, so downstream callers only ever observe the model name
// they requested, masking upstream redirects and provider-specific names.
func RewriteResponseModelBytes(info *relaycommon.RelayInfo, payload []byte) []byte {
	if info == nil || info.OriginModelName == "" || len(payload) == 0 {
		return payload
	}
	modelNode := gjson.GetBytes(payload, "model")
	if !modelNode.Exists() || modelNode.Str == "" || modelNode.Str == info.OriginModelName {
		return payload
	}
	if patched, err := sjson.SetBytes(payload, "model", info.OriginModelName); err == nil {
		return patched
	}
	return payload
}

// RewriteResponseModelString rewrites the top-level "model" field in a JSON string
// (e.g. an SSE stream frame) to info.OriginModelName.
func RewriteResponseModelString(info *relaycommon.RelayInfo, data string) string {
	if info == nil || info.OriginModelName == "" || data == "" {
		return data
	}
	modelNode := gjson.Get(data, "model")
	if !modelNode.Exists() || modelNode.Str == "" || modelNode.Str == info.OriginModelName {
		return data
	}
	if patched, err := sjson.Set(data, "model", info.OriginModelName); err == nil {
		return patched
	}
	return data
}

// RewriteResponsesResponseModelString rewrites the model field in an OpenAI Responses API SSE frame.
func RewriteResponsesResponseModelString(info *relaycommon.RelayInfo, data string) string {
	if info == nil || info.OriginModelName == "" || data == "" {
		return data
	}
	if modelNode := gjson.Get(data, "response.model"); modelNode.Exists() && modelNode.Str != "" && modelNode.Str != info.OriginModelName {
		if patched, err := sjson.Set(data, "response.model", info.OriginModelName); err == nil {
			data = patched
		}
	}
	if modelNode := gjson.Get(data, "model"); modelNode.Exists() && modelNode.Str != "" && modelNode.Str != info.OriginModelName {
		if patched, err := sjson.Set(data, "model", info.OriginModelName); err == nil {
			data = patched
		}
	}
	return data
}

// RewriteResponsesResponseModelBytes rewrites the model field in an OpenAI Responses API body.
func RewriteResponsesResponseModelBytes(info *relaycommon.RelayInfo, payload []byte) []byte {
	if info == nil || info.OriginModelName == "" || len(payload) == 0 {
		return payload
	}
	if modelNode := gjson.GetBytes(payload, "response.model"); modelNode.Exists() && modelNode.Str != "" && modelNode.Str != info.OriginModelName {
		if patched, err := sjson.SetBytes(payload, "response.model", info.OriginModelName); err == nil {
			payload = patched
		}
	}
	if modelNode := gjson.GetBytes(payload, "model"); modelNode.Exists() && modelNode.Str != "" && modelNode.Str != info.OriginModelName {
		if patched, err := sjson.SetBytes(payload, "model", info.OriginModelName); err == nil {
			payload = patched
		}
	}
	return payload
}
