package relay

import (
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
)

// ForwardInterruptedUsage routes a stream handler's partial usage to the
// controller, which settles it once the whole retry loop has failed.
//
// Every relay helper has the same shape — take (usage, err) from DoResponse and
// return early on err — and that early return is where partial usage used to die,
// once per helper, with no shared rule. This function is the single place that
// drop now passes through.
//
// The convention it completes: a streaming handler returns a non-nil usage
// alongside its error if and only if the client received billable content before
// the failure, and nil otherwise. Because the caller forwards rather than
// decides, a handler that forgets to return usage bills nothing — the safe
// direction to be wrong in.
//
// The controller settles InterruptedStreamUsage only after every retry has
// failed, so a successful retry is covered by that attempt's normal settlement
// and is never charged twice.
func ForwardInterruptedUsage(info *relaycommon.RelayInfo, usage any) {
	if info == nil {
		return
	}
	u, ok := usage.(*dto.Usage)
	if !ok || u == nil {
		return
	}
	info.RecordInterruptedUsage(u)
}
