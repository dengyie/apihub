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
