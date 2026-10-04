package relay

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DoResponse 返回的是 any，各家 handler 的实际类型并不统一（websocket 那边是
// *dto.RealtimeUsage，自己结算，不走这里）。这个类型闸门就是防止别的类型被误
// 当成计费用量塞进 InterruptedStreamUsage —— 那会按一个空结构体给用户计费。
func TestForwardInterruptedUsageRejectsForeignTypes(t *testing.T) {
	info := &relaycommon.RelayInfo{}

	ForwardInterruptedUsage(info, "some other shape")
	ForwardInterruptedUsage(info, dto.Usage{})
	ForwardInterruptedUsage(info, 42)

	assert.Nil(t, info.InterruptedStreamUsage,
		"非 *dto.Usage 的返回值不是计费用量，必须被挡在闸门外")
}

// handler 返回 nil 是「本次尝试没有产出」的约定，于是零产出的断流一路传到这里
// 也必须是空操作 —— 这正是上一版过收缺陷的形状。
func TestForwardInterruptedUsageIgnoresNil(t *testing.T) {
	info := &relaycommon.RelayInfo{}

	ForwardInterruptedUsage(info, (*dto.Usage)(nil))
	ForwardInterruptedUsage(info, nil)

	assert.Nil(t, info.InterruptedStreamUsage, "没有产出就没有待结算用量")
}

// 正常路径：handler 交上来的用量必须落到 InterruptedStreamUsage 上，controller
// 整轮重试失败后据此结算。
func TestForwardInterruptedUsageForwardsHandlerUsage(t *testing.T) {
	info := &relaycommon.RelayInfo{}

	ForwardInterruptedUsage(info, &dto.Usage{PromptTokens: 120, CompletionTokens: 37, TotalTokens: 157})

	require.NotNil(t, info.InterruptedStreamUsage, "断流已送达的输出必须被转交给 controller 结算")
	assert.EqualValues(t, 120, info.InterruptedStreamUsage.PromptTokens)
	assert.EqualValues(t, 37, info.InterruptedStreamUsage.CompletionTokens)
	assert.EqualValues(t, 157, info.InterruptedStreamUsage.TotalTokens)
}

// info 为 nil 不能炸：它是纯转交，不该成为一处新的 panic 点。
func TestForwardInterruptedUsageNilInfo(t *testing.T) {
	assert.NotPanics(t, func() {
		ForwardInterruptedUsage(nil, &dto.Usage{TotalTokens: 10})
	})
}
