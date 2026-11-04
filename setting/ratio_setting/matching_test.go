package ratio_setting

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatMatchingModelNameDoesNotStripBase(t *testing.T) {
	// qwen-3.8-flash 是下游唯一规范名，其余写法归一化到它。
	assert.Equal(t, "qwen-3.8-flash", FormatMatchingModelName("qwen-3.8-flash"))
	assert.Equal(t, "qwen-3.8-flash", FormatMatchingModelName("qwen3.8-flash"))
	assert.Equal(t, "qwen-3.8-flash", FormatMatchingModelName("qwen-3.8flash"))
	// 厂商名不是模型名：Google-3.8-flash 不得被解析成 gemini 模型。
	assert.Equal(t, "Google-3.8-flash", FormatMatchingModelName("Google-3.8-flash"))
	assert.Equal(t, "gemini-3.8-flash", FormatMatchingModelName("gemini-3.8-flash"))
	assert.Equal(t, "gemini-3.8-flash-high", FormatMatchingModelName("gemini-3.8-flash-high"))
	assert.Equal(t, "qwen3-max@thinking:on", FormatMatchingModelName("qwen3-max@thinking:on"))
	assert.Equal(t, "claude-3-7-sonnet-thinking", FormatMatchingModelName("claude-3-7-sonnet-thinking"))
	assert.Equal(t, "gemini-2.5-flash-thinking-*", FormatMatchingModelName("gemini-2.5-flash-thinking-8192"))
	assert.Equal(t, "gpt-4-gizmo-*", FormatMatchingModelName("gpt-4-gizmo-abc"))
}

func TestRoutingMatchModelNameStripsThenWildcards(t *testing.T) {
	assert.Equal(t, "qwen3-max", RoutingMatchModelName("qwen3-max@thinking:on@temperature:0.2"))
	assert.Equal(t, "claude-3-7-sonnet", RoutingMatchModelName("claude-3-7-sonnet-thinking"))
	assert.Equal(t, "gemini-2.5-flash-thinking-*", RoutingMatchModelName("gemini-2.5-flash-thinking-8192"))
	assert.Equal(t, "gpt-5.1-codex-max", RoutingMatchModelName("gpt-5.1-codex-max"))

	geminiSettings := model_setting.GetGeminiSettings()
	old := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = old })
	assert.Equal(t, "gemini-2.5-flash", RoutingMatchModelName("gemini-2.5-flash-thinking-8192"))
}

func TestRoutingMatchModelNamePreservesExemptAtName(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, "re:.*@sha256:.*")

	assert.Equal(t, "opaque@sha256:deadbeef", RoutingMatchModelName("opaque@sha256:deadbeef"))
	assert.Equal(t, "kimi-k2-thinking", RoutingMatchModelName("kimi-k2-thinking"))
}

// The relay hands GetCacheRatio the request's model name, not the
// wildcard-normalized one, so a lookup that skipped normalization would miss
// every "*"-suffixed entry and silently bill cached input at 1.0.
func TestCacheRatioLookupNormalizesWildcard(t *testing.T) {
	InitRatioSettings()

	ratio, ok := GetCacheRatio("gemini-2.5-pro-thinking-8192")
	assert.True(t, ok)
	assert.Equal(t, 0.1, ratio)

	ratio, ok = GetCacheRatio("gemini-2.5-pro")
	assert.True(t, ok)
	assert.Equal(t, 0.1, ratio)

	created, ok := GetCreateCacheRatio("gpt-5.6-sol")
	assert.True(t, ok)
	assert.Equal(t, 1.25, created)
}

// The CacheRatio option is loaded as a whole snapshot -- LoadFromJsonStringWithCallback
// replaces the map instead of merging -- so a host whose option was written before a
// built-in entry existed keeps a map without it. Production was exactly that: 73 keys,
// none of the cache prices added for ratio-mode models. The source looked right and
// every cached request still billed at 1.0, because the configured layer is the only
// place the getter used to look.
func TestCacheRatioFallsBackToBuiltinWhenOptionSnapshotIsStale(t *testing.T) {
	stale := make(map[string]float64, len(defaultCacheRatio))
	for name, ratio := range defaultCacheRatio {
		stale[name] = ratio
	}
	for _, added := range []string{
		"gemini-2.5-pro", "gemini-2.5-pro-thinking-*", "gpt-5.5",
		"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-image-1",
	} {
		delete(stale, added)
	}
	saved := CacheRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateCacheRatioByJSONString(saved)) })
	encoded, err := common.Marshal(stale)
	require.NoError(t, err)
	require.NoError(t, UpdateCacheRatioByJSONString(string(encoded)))
	_, configured := cacheRatioMap.Get("gpt-5.5")
	require.False(t, configured, "the stale snapshot must really lack the entry")

	for _, name := range []string{"gpt-5.5", "gemini-2.5-pro", "gemini-2.5-pro-thinking-8192", "gpt-5.6-sol"} {
		ratio, ok := GetCacheRatio(name)
		assert.Truef(t, ok, "%s: a stale option snapshot must not hide the built-in cache price", name)
		assert.Equalf(t, 0.1, ratio, "%s", name)
	}
	ratio, ok := GetCacheRatio("gpt-image-1")
	assert.True(t, ok)
	assert.Equal(t, 0.25, ratio)

	// A configured value still wins, including an explicit 1.0: the built-in
	// layer is a default, not an override of what the operator set.
	require.NoError(t, UpdateCacheRatioByJSONString(`{"gpt-5.5":1}`))
	ratio, ok = GetCacheRatio("gpt-5.5")
	assert.True(t, ok)
	assert.Equal(t, 1.0, ratio, "an explicit configured ratio must not be replaced by the built-in default")

	// And an unknown model still gets "no discount" rather than an empty answer.
	ratio, ok = GetCacheRatio("not-a-configured-model")
	assert.False(t, ok)
	assert.Equal(t, DefaultCacheRatio, ratio)
}
