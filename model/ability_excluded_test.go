package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetChannelWithExcluded_DatabaseMode(t *testing.T) {
	truncateTables(t)
	oldCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	defer func() {
		common.MemoryCacheEnabled = oldCacheEnabled
	}()

	priority10 := int64(10)
	priority5 := int64(5)
	weight1 := uint(1)
	baseURL := "https://example.com"

	channels := []Channel{
		{Id: 800001, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-primary", Models: "gpt-test", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
		{Id: 800002, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-secondary", Models: "gpt-test", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
		{Id: 800003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-backup", Models: "gpt-test", Group: "default", Priority: &priority5, Weight: &weight1, BaseURL: &baseURL},
	}
	for i := range channels {
		require.NoError(t, channels[i].Insert())
	}

	// 1. Initial selection with no exclusions: should return one of the priority 10 channels
	ch, err := GetRandomSatisfiedChannelWithExcluded("default", "gpt-test", 0, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Contains(t, []int{800001, 800002}, ch.Id)

	// 2. Exclude ch-primary (800001): must return ch-secondary (800002)
	excluded1 := map[int]struct{}{800001: {}}
	ch, err = GetRandomSatisfiedChannelWithExcluded("default", "gpt-test", 0, nil, excluded1)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, 800002, ch.Id)

	// 3. Exclude both priority 10 channels: with retry=0, must automatically progress to priority 5 channel (800003)
	excludedTopPriority := map[int]struct{}{800001: {}, 800002: {}}
	ch, err = GetRandomSatisfiedChannelWithExcluded("default", "gpt-test", 0, nil, excludedTopPriority)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, 800003, ch.Id)

	// 4. Exclude all channels: must return nil, nil without database consistency error
	excludedAll := map[int]struct{}{800001: {}, 800002: {}, 800003: {}}
	ch, err = GetRandomSatisfiedChannelWithExcluded("default", "gpt-test", 0, nil, excludedAll)
	require.NoError(t, err)
	assert.Nil(t, ch)
}
