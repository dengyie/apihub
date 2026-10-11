package model

import (
	"testing"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelCandidates_TopAvailablePriority(t *testing.T) {
	loadbalancer.SetPolicy(&loadbalancer.Policy{
		Enabled: true,
		Default: loadbalancer.ChannelPolicy{
			Breaker: loadbalancer.BreakerPolicy{
				FailureThreshold: 5,
				CooldownSeconds:  60,
			},
		},
	})
	t.Cleanup(func() {
		loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: false})
	})

	const modelName = "test-model-priority"
	p10 := int64(10)
	p8 := int64(8)
	p5 := int64(5)

	ch1 := &Channel{Id: 801, Status: common.ChannelStatusEnabled, Priority: &p10}
	ch2 := &Channel{Id: 802, Status: common.ChannelStatusEnabled, Priority: &p8}
	ch3 := &Channel{Id: 803, Status: common.ChannelStatusEnabled, Priority: &p5}

	candidates := &ChannelCandidates{channels: []*Channel{ch1, ch2, ch3}}

	// 1. 全部渠道健康时，返回最高优先级 10
	top, ok := candidates.TopAvailablePriority(nil, modelName)
	require.True(t, ok)
	assert.Equal(t, int64(10), top)

	// 2. 模拟 ch1 (优先级 10) 连续慢 3 次降级
	tr := loadbalancer.GlobalTracker()
	for i := 0; i < 3; i++ {
		tr.Begin(ch1.Id, modelName).End(true, false)
	}
	require.True(t, tr.IsDegraded(ch1.Id, modelName))

	// 降级后，最高健康优先级应为 ch2 (优先级 8)
	top, ok = candidates.TopAvailablePriority(nil, modelName)
	require.True(t, ok)
	assert.Equal(t, int64(8), top, "当高优先级渠道处于降级状态时，应优先使用健康渠道的最高优先级")

	// 3. 排除 ch2
	top, ok = candidates.TopAvailablePriority(map[int]struct{}{ch2.Id: {}}, modelName)
	require.True(t, ok)
	assert.Equal(t, int64(5), top)

	// 4. 当全部健康渠道排除/不可用，仅剩降级渠道时，回退到降级渠道的最高优先级
	top, ok = candidates.TopAvailablePriority(map[int]struct{}{ch2.Id: {}, ch3.Id: {}}, modelName)
	require.True(t, ok)
	assert.Equal(t, int64(10), top, "无任何健康渠道可用时，回退使用降级渠道的最高优先级")

	// 5. 渠道硬熔断后不可用
	tr.TripBreakerUntil(ch1.Id, modelName, time.Now().Add(time.Hour))
	t.Cleanup(func() {
		tr.TripBreakerUntil(ch1.Id, modelName, time.Now().Add(-time.Hour))
	})
	top, ok = candidates.TopAvailablePriority(map[int]struct{}{ch2.Id: {}, ch3.Id: {}}, modelName)
	assert.False(t, ok)
	assert.Equal(t, int64(0), top)
}

func TestCachedChannelCandidates_CaseInsensitive(t *testing.T) {
	channelSyncLock.Lock()
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	oldGroup2model2channels := group2model2channels
	oldChannelsIDM := channelsIDM
	channelSyncLock.Unlock()

	defer func() {
		channelSyncLock.Lock()
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		group2model2channels = oldGroup2model2channels
		channelsIDM = oldChannelsIDM
		channelSyncLock.Unlock()
	}()

	common.MemoryCacheEnabled = true

	p9 := int64(9)
	w10 := uint(10)
	ch90 := &Channel{
		Id:       90,
		Status:   common.ChannelStatusEnabled,
		Priority: &p9,
		Weight:   &w10,
	}

	p5 := int64(5)
	ch50 := &Channel{
		Id:       50,
		Status:   common.ChannelStatusEnabled,
		Priority: &p5,
		Weight:   &w10,
	}

	channelSyncLock.Lock()
	channelsIDM = map[int]*Channel{
		90: ch90,
		50: ch50,
	}
	group2model2channels = map[string]map[string][]int{
		"default": {
			"Space-Bunny": {90}, // CamelCase in channel config
			"other-model": {50},
		},
	}
	channelSyncLock.Unlock()

	// 1. Lowercase request "space-bunny" matches "Space-Bunny"
	candidates, err := GetChannelCandidates("default", "space-bunny", nil)
	require.NoError(t, err)
	require.NotNil(t, candidates)
	require.Len(t, candidates.channels, 1)
	assert.Equal(t, 90, candidates.channels[0].Id)

	// 2. Uppercase request "SPACE-BUNNY" matches "Space-Bunny"
	candidatesUpper, err := GetChannelCandidates("default", "SPACE-BUNNY", nil)
	require.NoError(t, err)
	require.NotNil(t, candidatesUpper)
	require.Len(t, candidatesUpper.channels, 1)
	assert.Equal(t, 90, candidatesUpper.channels[0].Id)

	// 3. Exact match takes precedence if an exact lowercase channel exists
	p8 := int64(8)
	ch80 := &Channel{
		Id:       80,
		Status:   common.ChannelStatusEnabled,
		Priority: &p8,
		Weight:   &w10,
	}
	channelSyncLock.Lock()
	channelsIDM[80] = ch80
	group2model2channels["default"]["space-bunny"] = []int{80}
	channelSyncLock.Unlock()

	candidatesExact, err := GetChannelCandidates("default", "space-bunny", nil)
	require.NoError(t, err)
	require.NotNil(t, candidatesExact)
	require.Len(t, candidatesExact.channels, 1)
	assert.Equal(t, 80, candidatesExact.channels[0].Id, "精确匹配存在时必须优先走精确匹配")
}

func TestIsChannelEnabledForGroupModel_CaseInsensitive(t *testing.T) {
	channelSyncLock.Lock()
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	oldGroup2model2channels := group2model2channels
	channelSyncLock.Unlock()

	defer func() {
		channelSyncLock.Lock()
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		group2model2channels = oldGroup2model2channels
		channelSyncLock.Unlock()
	}()

	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	group2model2channels = map[string]map[string][]int{
		"default": {
			"Space-Bunny": {90},
		},
	}
	channelSyncLock.Unlock()

	// 1. Match case-insensitively
	assert.True(t, IsChannelEnabledForGroupModel("default", "space-bunny", 90))
	assert.True(t, IsChannelEnabledForGroupModel("default", "SPACE-BUNNY", 90))
	assert.True(t, IsChannelEnabledForGroupModel("default", "Space-Bunny", 90))

	// 2. Different model or channel returns false
	assert.False(t, IsChannelEnabledForGroupModel("default", "space-bunny", 91))
	assert.False(t, IsChannelEnabledForGroupModel("default", "non-existent", 90))
	assert.False(t, IsChannelEnabledForGroupModel("other-group", "space-bunny", 90))
}
