package model

import (
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/relaykit/relayconvert/reasoning"
	"github.com/dengyie/apihub/setting/billing_setting"
	"github.com/dengyie/apihub/setting/ratio_setting"
	hostreasoning "github.com/dengyie/apihub/setting/reasoning"
)

// UnpricedModel is one model that is routable right now but that has no price,
// ratio or tiered billing entry anywhere in the pricing configuration.
type UnpricedModel struct {
	Model string `json:"model"`
	// Channels is how many enabled channels currently advertise the model, so
	// the operator can tell a real, load-bearing model from a leftover row.
	Channels int `json:"channels"`
}

// GetUnpricedModels returns the routable models that have no billing entry,
// plus how many routable models were checked.
//
// Why this exists: GetModelRatio answers a missing entry with the 37.5 self-use
// fallback ratio (= $75/M at the default QuotaPerUnit) instead of failing, so an
// unpriced model does not error -- it quietly bills every request at a price
// nobody chose. A model only reaches that path when a channel advertises it, so
// the audit walks the routing table rather than request logs: nothing needs
// traffic to be mispriced for this to be worth fixing.
//
// A model counts as routable when it has an enabled ability row *and* its
// channel is in the enabled state, which is the same pair
// GetChannelCandidates checks. A model counts as priced when the name the relay
// would bill under has an entry: the name itself, one of its canonical billing
// spellings, or its base name with reasoning/thinking suffixes stripped --
// mirroring resolveBillingModelName in relay/helper/price.go.
func GetUnpricedModels() ([]UnpricedModel, int, error) {
	var rows []UnpricedModel
	err := DB.Table("abilities").
		Select("abilities.model as model, COUNT(DISTINCT abilities.channel_id) as channels").
		Joins("join channels on channels.id = abilities.channel_id").
		Where("abilities.enabled = ? and channels.status = ?", true, common.ChannelStatusEnabled).
		Group("abilities.model").
		Order("abilities.model").
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	unpriced := make([]UnpricedModel, 0, len(rows))
	for _, row := range rows {
		if !hasBillingEntry(row.Model) {
			unpriced = append(unpriced, row)
		}
	}
	return unpriced, len(rows), nil
}

// hasBillingEntry reports whether any billing name derived from origin has a
// configured price. Callers must pass an unformatted model name: the candidate
// list is what the relay itself would try, in the same order.
func hasBillingEntry(origin string) bool {
	for _, name := range billingCandidateNames(origin) {
		if hasPriceOrRatioEntry(name) {
			return true
		}
	}
	return false
}

// billingCandidateNames returns the names the relay tries, most specific first:
// the request name (when it carries no reasoning modifier), the canonical
// vendor spellings, and finally the base name.
func billingCandidateNames(origin string) []string {
	candidates := make([]string, 0, 3)
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	candidates = append(candidates, hostreasoning.BaseModelName(origin))

	seen := make(map[string]struct{}, len(candidates))
	unique := candidates[:0]
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	return unique
}

// hasPriceOrRatioEntry mirrors relay/helper.HasPriceOrRatioEntry. It is
// duplicated rather than imported because relay/helper depends on this package.
//
// A tiered_expr entry only counts when an expression actually exists. A model
// left in tiered mode with an empty expression is *not* priced: the relay
// refuses those requests outright, so reporting it as priced would hide a
// broken configuration behind a passing audit.
func hasPriceOrRatioEntry(name string) bool {
	formatted := ratio_setting.FormatMatchingModelName(name)
	if _, ok := ratio_setting.GetModelPrice(formatted, false); ok {
		return true
	}
	if ratio_setting.HasConfiguredModelRatio(formatted) {
		return true
	}
	if billing_setting.GetBillingMode(formatted) != billing_setting.BillingModeTieredExpr {
		return false
	}
	expr, ok := billing_setting.GetBillingExpr(formatted)
	return ok && strings.TrimSpace(expr) != ""
}
