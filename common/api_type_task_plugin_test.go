package common

import (
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/stretchr/testify/assert"
)

func TestTaskPluginChannelHasNoOrdinaryAPIType(t *testing.T) {
	apiType, ok := ChannelType2APIType(constant.ChannelTypeTaskPlugin)
	assert.Equal(t, -1, apiType)
	assert.False(t, ok)
}
