package billing_setting_test

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/dengyie/apihub/setting/billing_setting"
	"github.com/dengyie/apihub/setting/config"
	"github.com/dengyie/apihub/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A model can carry both a built-in tiered expression and an administrator's
// legacy ratio. GetBillingMode deliberately lets the legacy ratio win, which
// makes the expression's cr term dead code -- so the only thing that gives
// cache reads their discount is ratio_setting.GetCacheRatio. Without an entry
// there the ratio path bills cache hits at DefaultCacheRatio (= full input
// price), a silent overcharge on every cached request.
//
// This test pins the two layers together: for every model the ratio path can
// reach that has a cache-read price in its expression, a cache ratio must exist
// and must equal cr / p.
func TestRatioModeModelsPriceCachedInput(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	savedSetting := *settings
	savedRatios, savedCacheRatios := ratio_setting.ModelRatio2JSONString(), ratio_setting.CacheRatio2JSONString()
	t.Cleanup(func() {
		*settings = savedSetting
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(savedCacheRatios))
	})
	settings.BillingMode = map[string]string{}
	settings.BillingExpr = map[string]string{}
	// The built-in defaults are what production starts from before the operator
	// override layer replaces the maps; test that layer, not a stale snapshot.
	ratio_setting.InitRatioSettings()

	pPrice := regexp.MustCompile(`\bp\s*\*\s*(\d+(?:\.\d+)?)`)
	crPrice := regexp.MustCompile(`\bcr\s*\*\s*(\d+(?:\.\d+)?)`)

	checked := 0
	for _, configured := range []string{"defaults loaded", "configured map empty"} {
		// The two layers that can answer this question have to agree. An
		// installation whose CacheRatio option predates a built-in entry only has
		// the built-in layer for that model, and production was exactly that: the
		// option held 73 keys, none of the cache prices added below.
		if configured == "configured map empty" {
			require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString("{}"),
				"clearing the configured cache ratios must succeed")
		} else {
			// The built-in defaults are what production starts from before the
			// operator override layer replaces the maps; test that layer, not a
			// stale snapshot.
			ratio_setting.InitRatioSettings()
		}

		for model, expr := range billing_setting.GetBuiltinBillingExprCopy() {
			if billing_setting.GetBillingMode(model) != billing_setting.BillingModeRatio {
				continue
			}
			prices := pPrice.FindAllStringSubmatch(expr, -1)
			cachedPrices := crPrice.FindAllStringSubmatch(expr, -1)
			if len(cachedPrices) == 0 {
				continue
			}
			require.Len(t, prices, len(cachedPrices),
				"%s: every tier with a cr term must also have a p term in %q", model, expr)
			for i, cached := range cachedPrices {
				p, err := strconv.ParseFloat(prices[i][1], 64)
				require.NoError(t, err)
				cr, err := strconv.ParseFloat(cached[1], 64)
				require.NoError(t, err)
				require.Positivef(t, p, "%s: input price must be positive", model)

				got, ok := ratio_setting.GetCacheRatio(model)
				require.Truef(t, ok,
					"%s bills under the ratio path but has no cache ratio (%s); cached input would bill at %.2fx the input price instead of %.4fx",
					model, configured, ratio_setting.DefaultCacheRatio, cr/p)
				assert.InDeltaf(t, cr/p, got, 1e-9,
					"%s (%s): cache ratio %v does not match cr/p from %q", model, configured, got, expr)
				checked++
			}
		}
	}
	require.Positive(t, checked,
		"no shadowed ratio-mode model with a cr term was found; the invariant would go unenforced")
}
