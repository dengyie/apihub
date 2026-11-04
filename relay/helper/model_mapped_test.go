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
