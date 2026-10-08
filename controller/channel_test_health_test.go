package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/stretchr/testify/assert"
)

// 这组测试锁定 testChannelForHealthCheck 的判定不变式。此前该函数零测试，
// 2026-10-03 的 P1（5 秒时长阈值把拟真探针的正常生成耗时判成「该禁用」，
// scheduled_all 下一次「测试所有渠道」即可打掉大半健康池）正是从这个空白里
// 漏过去的。判定必须是纯函数，副作用留在执行方。

func errFromMessage(msg string) *types.NewAPIError {
	return types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponse, http.StatusInternalServerError)
}

func TestDecideChannelHealthAction(t *testing.T) {
	badResponse := errFromMessage("upstream boom")

	// 各用例共用：测活开始时渠道状态与是否在架一致（在架=1 / 自动禁用=3）。
	cases := []struct {
		name string
		in   decideChannelHealthInput
		want channelHealthAction
	}{
		{
			name: "被动复活：慢但成功必须恢复，绝不禁用",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: false, IsChannelEnabled: false,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusAutoDisabled,
			},
			want: channelHealthAction{Enable: true, Succeeded: true},
		},
		{
			name: "scheduled_all：慢但成功不得禁用（时长不再是禁用判据）",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{Succeeded: true},
		},
		{
			name: "scheduled_all：失败且判该禁 → 禁用",
			in: decideChannelHealthInput{
				LocalErr: badResponse, NewAPIError: badResponse, ShouldBan: true,
				AllowDisable: true, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{Ban: true},
		},
		{
			name: "被动复活：失败也绝不禁用",
			in: decideChannelHealthInput{
				LocalErr: badResponse, NewAPIError: badResponse, ShouldBan: true,
				AllowDisable: false, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{},
		},
		{
			name: "autoBan 白名单关闭不得禁用",
			in: decideChannelHealthInput{
				LocalErr: badResponse, NewAPIError: badResponse, ShouldBan: true,
				AllowDisable: true, IsChannelEnabled: true,
				AutoBan: false, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{},
		},
		{
			name: "ShouldBan 未判该禁不得禁用",
			in: decideChannelHealthInput{
				LocalErr: badResponse, NewAPIError: badResponse, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{},
		},
		{
			name: "成功 + 自动禁用 + 全局开关开 → 恢复",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: false,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusAutoDisabled,
			},
			want: channelHealthAction{Enable: true, Succeeded: true},
		},
		{
			name: "全局自动恢复开关关闭不得恢复",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: false,
				AutoBan: true, AutomaticEnable: false, Status: common.ChannelStatusAutoDisabled,
			},
			want: channelHealthAction{Succeeded: true},
		},
		{
			name: "手动禁用（status=2）不得自动恢复",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: false,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusManuallyDisabled,
			},
			want: channelHealthAction{Succeeded: true},
		},
		{
			name: "localErr 非 nil 时不得恢复（恢复闸门是双 nil，计数只看 newAPIError）",
			in: decideChannelHealthInput{
				LocalErr: errors.New("read body failed"), NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: false,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusAutoDisabled,
			},
			want: channelHealthAction{Succeeded: true},
		},
		{
			name: "在架渠道永不走恢复路径",
			in: decideChannelHealthInput{
				LocalErr: nil, NewAPIError: nil, ShouldBan: false,
				AllowDisable: true, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			},
			want: channelHealthAction{Succeeded: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, decideChannelHealthAction(tc.in))
		})
	}
}

// 时长阈值唯一的合法性来自「把病态慢的渠道拦在 allowDisable 模式里」，
// 且不得污染成功计数 —— 阈值凭空造出的 newAPIError 不得把恢复路径堵死。
func TestResponseTimeThresholdOnlyBansWithinAllowDisable(t *testing.T) {
	const threshold = int64(60_000)

	previousSwitch := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousSwitch })

	cases := []struct {
		name          string
		elapsedMs     int64
		allowDisable  bool
		expectBanned  bool
		expectSuccess bool
	}{
		{"scheduled_all 超阈值 → 病态慢判禁用", 61_000, true, true, false},
		{"scheduled_all 未超阈值 → 正常成功", 59_000, true, false, true},
		{"被动复活超阈值 → 不得判禁用", 61_000, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newAPIError := (*types.NewAPIError)(nil)
			shouldBan := false
			if tc.elapsedMs > threshold && tc.allowDisable && common.AutomaticDisableChannelEnabled {
				err := errors.New("响应时间超过阈值")
				newAPIError = types.NewOpenAIError(err, types.ErrorCodeChannelResponseTimeExceeded, http.StatusRequestTimeout)
				shouldBan = true
			}
			act := decideChannelHealthAction(decideChannelHealthInput{
				LocalErr: nil, NewAPIError: newAPIError, ShouldBan: shouldBan,
				AllowDisable: tc.allowDisable, IsChannelEnabled: true,
				AutoBan: true, AutomaticEnable: true, Status: common.ChannelStatusEnabled,
			})
			assert.Equal(t, tc.expectBanned, act.Ban)
			assert.Equal(t, tc.expectSuccess, act.Succeeded)
		})
	}
}
