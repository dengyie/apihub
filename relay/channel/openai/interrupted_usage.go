package openai

import (
	"github.com/gin-gonic/gin"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/service"
)

// interruptedStreamUsage 给断流的那一次尝试估出已交付部分的用量，返回值交给
// 调用方转交 controller 结算。
//
// 判据是真正累积到的产出而非「收到了帧」：上游错误同样以数据帧送达，空产出会
// 被估成整段 prompt，等于给一次失败的请求收一遍全款 —— 所以没有累积到正文时
// 返回 nil，不计费。上游自带 usage 时直接用它（比按正文估算准）。
//
// 只适用于边收边转发的流式 handler。缓冲型 handler 不适用：它把整个响应攒完
// 才写给客户端，出错时客户端一个字节都没拿到，那种情况本就不该计费。
func interruptedStreamUsage(c *gin.Context, info *relaycommon.RelayInfo, state *relayconvert.ResponseStreamState) *dto.Usage {
	if state == nil || state.UsageText() == "" {
		return nil
	}
	if u := state.Usage(); u != nil && u.TotalTokens != 0 {
		return u
	}
	return service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
}

// interruptedImageUsage 是 interruptedStreamUsage 在图像流上的对应物。
//
// 判据换成真正生成的张数：上游的错误帧同样以数据帧送达，而 usage 帧里带着一
// 份完整的 prompt 计数 —— 只看「拿到 usage 了」的话，一次失败的请求反而会被按
// 整段 prompt 收一遍全款。一张都没生成出来就返回 nil，不计费。
func interruptedImageUsage(usage *dto.Usage, completedImages int64) *dto.Usage {
	if completedImages <= 0 {
		return nil
	}
	return usage
}
