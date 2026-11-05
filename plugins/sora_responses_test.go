package plugins_test

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/pkg/jsplugin"
	builtinplugins "github.com/dengyie/apihub/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSoraResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "sora",
		model:     "sora-2-pro",
		requestBody: map[string]any{
			"model":   "sora-2-pro",
			"input":   "waves at sunset",
			"seconds": 8,
			"size":    "1792x1024",
		},
		wantAction: "text_to_video",
		wantRequest: map[string]any{
			"model":   "sora-2-pro",
			"prompt":  "waves at sunset",
			"seconds": float64(8),
			"size":    "1792x1024",
		},
		wantUsageKeys:  []string{"seconds", "size"},
		wantVendorName: "sora",
	})
}

// Unknown video models used to fall back to TaskSubmitReq, which drops unknown
// image_url fields and rejects the native Grok image object before submission.
func TestGrokVideoNativeReferenceSurvivesGateway(t *testing.T) {
	source, err := builtinplugins.Source("sora")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "sora"})
	require.NoError(t, err)
	for _, model := range []string{"grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview"} {
		t.Run(model, func(t *testing.T) {
			binding, found := registry.Generation().LookupEndpoint("POST", "/v1/videos", model)
			require.True(t, found, "native Grok requests must bypass the lossy generic task DTO")
			assert.Same(t, plugin, binding.Plugin)
			_, responsesClaimed := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
			assert.False(t, responsesClaimed, "Grok Responses must keep the existing media relay")
			request := map[string]any{"model": model, "prompt": "Animate this exact cat", "image": map[string]any{"url": "data:image/png;base64,aGVsbG8="}, "duration": 2, "aspect_ratio": "16:9", "resolution": "720p"}
			value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": model, "body": map[string]any{"kind": "json", "value": request}})
			require.NoError(t, callErr)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var resolved map[string]any
			require.NoError(t, common.Unmarshal(encoded, &resolved))
			assert.Equal(t, "image_to_video", resolved["action"])
			value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"baseUrl": "https://grok.example", "apiKey": "test", "model": model, "upstreamModel": "grok-imagine-video", "action": resolved["action"], "requestBody": resolved["requestBody"]})
			require.NoError(t, err)
			encoded, err = common.Marshal(value)
			require.NoError(t, err)
			var descriptor map[string]any
			require.NoError(t, common.Unmarshal(encoded, &descriptor))
			assert.Equal(t, "https://grok.example/v1/videos", descriptor["url"])
			body, ok := descriptor["body"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, request["image"], body["image"])
			assert.Equal(t, "grok-imagine-video", body["model"])
			assert.EqualValues(t, 2, body["duration"])
			assert.Equal(t, "16:9", body["aspect_ratio"])
			assert.Equal(t, "720p", body["resolution"])
		})
	}
}
