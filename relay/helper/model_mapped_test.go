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
