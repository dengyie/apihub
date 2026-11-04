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
		c.Set("model_mapping", `{"gemini-3.8-flash":"gemini-3.8-flash-high"}`)

		info := &relaycommon.RelayInfo{
			OriginModelName: "Google-3.8-flash",
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		assert.True(t, info.IsModelMapped)
		assert.Equal(t, "gemini-3.8-flash-high", info.UpstreamModelName)
	})

	t.Run("normalizes model when mapping is empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		info := &relaycommon.RelayInfo{
			OriginModelName: "Google-3.8-flash",
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		assert.True(t, info.IsModelMapped)
		assert.Equal(t, "gemini-3.8-flash", info.UpstreamModelName)
	})

	t.Run("normalizes qwen-3.8-flash to qwen3.8-flash", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen-3.8-flash",
		}
		err := ModelMappedHelper(c, info, nil)
		require.NoError(t, err)
		assert.True(t, info.IsModelMapped)
		assert.Equal(t, "qwen3.8-flash", info.UpstreamModelName)
	})
}
