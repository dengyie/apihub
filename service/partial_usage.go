package service

import (
	"github.com/gin-gonic/gin"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// InterruptedTextUsage prices a streaming attempt that failed after producing
// output, using the text the client actually received. It returns nil when
// nothing was delivered, which is the load-bearing half of the contract.
//
// Why nil matters: an upstream error is delivered as an ordinary SSE data frame,
// so "a frame arrived" does not mean "the client got content". Pricing an empty
// attempt still yields a full prompt estimate from GetEstimatePromptTokens, which
// would bill the user in full for a request that failed. A nil result bills
// nothing, matching the non-streaming path where an errored request is free.
//
// Callers pass the content they accumulated, so the "was anything delivered?"
// decision stays where the evidence is — next to the accumulator that produced
// it — instead of being re-derived from a frame count that cannot tell the two
// apart.
func InterruptedTextUsage(c *gin.Context, info *relaycommon.RelayInfo, text string) *dto.Usage {
	if info == nil || text == "" {
		return nil
	}
	return ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
}

// DeliveredTextUsage is the success-path counterpart: it prices a stream that
// ended without an upstream usage frame, so the prompt has to come from the local
// estimate rather than from the provider's own accounting.
//
// The prompt estimate is charged only when the client actually received output.
// A stream the client abandoned before the first token never reached the model —
// the scanner classifies client_gone as a normal end (it is the client's
// decision, not a channel fault), so it walks the success path and used to bill
// the full prompt against nothing. Production evidence: 19 such records in 24h,
// each charging a 36k~120k token prompt with zero output, every one carrying
// frt=-1000, i.e. the first token never arrived. The provider was never charged
// for those either, so the estimate was the gateway's own invention.
//
// A stream that ended normally keeps the old behaviour: an upstream answering 200
// with an empty body did consume the prompt, and that cost is real.
//
// The two independent conditions are deliberate. "Delivered nothing" alone would
// also zero out a legitimately empty-but-successful response; "did not end
// normally" alone would also fire on a partial response the client did read,
// which is real output worth charging for.
func DeliveredTextUsage(c *gin.Context, info *relaycommon.RelayInfo, text string) *dto.Usage {
	if info == nil {
		// 没有 info 就连模型名都拿不到，无法定价。返回零而不是崩溃 —— 这里绝不能
		// 成为一处新的 panic 点。
		return &dto.Usage{}
	}
	if text == "" && info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
		return &dto.Usage{}
	}
	return ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
}
