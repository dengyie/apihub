package controller

import (
	"context"
	"testing"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testChannel 有两条返回**没有 context** 的 testResult 的路径，而
// testChannelForHealthCheck 随后无条件解引用它。
//
// 一条是渠道类型不支持测活（testChannel 开头那份名单直接 return，只有
// localErr）；另一条是取不到用户缓存。这两条都不是「理论上的可能」：
//
//  1. 名单在函数体内定义，随 constant 新增视频/任务类渠道自动扩大；
//  2. 「取不到用户缓存」与渠道类型完全无关 —— 缓存未预热、用户被删、
//     Redis 抖动，任何一条都会命中。
//
// 代价不对称得离谱：解引用失败是 panic，而 runChannelTestWorkers 的
// worker 没有 recover，一次就把整个进程带走 —— 一条渠道测不出来，
// 代价是全站下线。同函数末尾的复核分支本来就写了 result.context != nil，
// 说明作者知道它可能是 nil，只是漏了紧邻的这一处。

// TestHealthCheckSurvivesNilContextForUnsupportedType 测活撞上不支持测活的
// 渠道类型时必须正常返回，不得 panic。
//
// 用 Midjourney 走这条路：testChannel 在函数最开头就返回，连网络请求都
// 不会发出，所以这条用例是纯内存的、不依赖任何上游。
func TestHealthCheckSurvivesNilContextForUnsupportedType(t *testing.T) {
	for _, channelType := range []int{
		constant.ChannelTypeMidjourney,
		constant.ChannelTypeSunoAPI,
		constant.ChannelTypeTaskPlugin,
	} {
		t.Run(constant.GetChannelTypeName(channelType), func(t *testing.T) {
			channel := &model.Channel{
				Id:     9001,
				Type:   channelType,
				Name:   "unsupported-for-test",
				Status: 1,
				Models: "m1,m2",
			}

			var summary channelTestSummary
			require.NotPanics(t, func() {
				summary = testChannelForHealthCheck(context.Background(), channel, 1, false, 0)
			}, "testChannel 返回的 result.context 可能为 nil，解引用它会把整个进程带走")

			assert.Equal(t, 1, summary.Tested, "测过了就该计数")
			assert.Equal(t, 1, summary.Failed, "测不出来算失败，不算成功 —— 计入成功会让报表失真")
			assert.Zero(t, summary.Succeeded)
			assert.Zero(t, summary.Disabled,
				"测不出结果的渠道不得被摘掉：证据不足，且摘掉是不可逆的")
			assert.Zero(t, summary.Enabled)
		})
	}
}

// TestHealthCheckNilContextDoesNotDisableChannel 上面那条更重要的推论：
// 拿不到 context 时不得产生任何禁用副作用。
//
// 禁用是不可逆的，而「这条渠道测不出来」完全不构成它坏了的证据。把这种情况
// 计入禁用，等于让一次配置问题（多了一条不支持测活的渠道类型）摘掉整批渠道。
func TestHealthCheckNilContextDoesNotDisableChannel(t *testing.T) {
	channel := &model.Channel{
		Id:     9002,
		Type:   constant.ChannelTypeMidjourney,
		Name:   "unsupported-for-test",
		Status: 1,
		Models: "m1,m2",
	}

	summary := testChannelForHealthCheck(context.Background(), channel, 1, true, 1)
	assert.Zero(t, summary.Disabled,
		"拿不到 context 说明这次测试没给出任何证据，不得据此禁用渠道")
}

// TestHealthCheckNilContextCountsAgainstTotal 计数口径：nil context 那次仍要
// 占掉 Tested 的名额。
//
// 报表是按 Tested 对账的；若这里返回 Tested=0，一次「测了 200 条、失败 0 条」
// 的记录会掩盖掉「其中 3 条根本没测成」。
func TestHealthCheckNilContextCountsAgainstTotal(t *testing.T) {
	channel := &model.Channel{
		Id:     9003,
		Type:   constant.ChannelTypeVidu,
		Name:   "unsupported-for-test",
		Status: 1,
		Models: "m1",
	}
	summary := testChannelForHealthCheck(context.Background(), channel, 1, false, 0)
	assert.Equal(t, 1, summary.Tested)
	assert.Equal(t, 1, summary.Failed)
}
