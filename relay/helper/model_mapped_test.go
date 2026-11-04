package helper

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelMappedHelper_CaseInsensitive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Model mapping has CamelCase source key
	c.Set("model_mapping", `{"Space-Bunny":"upstream-space-bunny"}`)

	// Client request has lowercase origin model
	info := &relaycommon.RelayInfo{
		OriginModelName: "space-bunny",
	}

	err := ModelMappedHelper(c, info, nil)
	require.NoError(t, err)
	assert.True(t, info.IsModelMapped)
	assert.Equal(t, "upstream-space-bunny", info.UpstreamModelName)
}

func TestModelMappedHelper_AliasNormalizedFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("resolves mapping via normalized key", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		// 渠道显式映射以规范名 qwen-3.8-flash 为 key，
		// 下游用无连字符变体 qwen3.8-flash 请求时也能命中。
		c.Set("model_mapping", `{"qwen-3.8-flash":"Qwen3.8-Flash-Next"}`)

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3.8-flash",
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		assert.True(t, info.IsModelMapped)
		assert.Equal(t, "Qwen3.8-Flash-Next", info.UpstreamModelName)
	})

	t.Run("normalizes model when mapping is empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3.8-flash",
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		assert.True(t, info.IsModelMapped)
		assert.Equal(t, "qwen-3.8-flash", info.UpstreamModelName)
	})

	t.Run("keeps canonical qwen-3.8-flash as-is", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen-3.8-flash",
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "qwen-3.8-flash"},
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		// 规范名无需归一化：既不标记映射，也不改写上游模型名。
		assert.False(t, info.IsModelMapped)
		assert.Equal(t, "qwen-3.8-flash", info.UpstreamModelName)
	})
}

// 渠道 model_mapping 若被写成脏 JSON（例如编辑器残留的
// `{ "duplicate_source_models": `），不能把请求打成 500：跳过映射、按请求名
// 原样转发，让脏数据只影响它自己那条重定向，而不是让所有落在该渠道上的模型
// 全部不可用。
func TestModelMappedHelper_InvalidMappingFallsBackToOriginModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dirtyMappings := map[string]string{
		"duplicate sentinel": `{ "duplicate_source_models": `,
		"truncated object":   `{"gemini-3.8-flash": "gemini-3.8-flash-high"`,
		"not an object":      `["gemini-3.8-flash-high"]`,
	}

	for name, mapping := range dirtyMappings {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("model_mapping", mapping)

			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3.8-flash-high",
			}
			err := ModelMappedHelper(c, info, nil)
			require.NoError(t, err)
			// 不报错、也不把映射表本身当成重定向结果；剩下的规范化
			// （gemini-3.8-flash-high → gemini-3.8-flash）由评分名逻辑负责，
			// 与「没有配 model_mapping」的渠道完全同一条路径。
			assert.Equal(t, "gemini-3.8-flash", info.UpstreamModelName)
		})
	}
}

// 成环的 model_mapping 与脏 JSON 同类：都是持久配置错误。同样不能把整条
// 渠道打成 500，按「没有配置重定向」处理，行为与未配 mapping 的渠道一致。
func TestModelMappedHelper_CyclicMappingFallsBackToOriginModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("model_mapping", `{"gemini-3.8-flash-high":"gemini-3.8-flash","gemini-3.8-flash":"gemini-3.8-flash-high"}`)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3.8-flash-high",
	}
	err := ModelMappedHelper(c, info, nil)
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash", info.UpstreamModelName)
}
