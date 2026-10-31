package model

import (
	"testing"

	"github.com/dengyie/apihub/setting/config"
	"github.com/dengyie/apihub/setting/operation_setting"
	"github.com/dengyie/apihub/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GetModelRatio answers an unknown model with the 37.5 self-use fallback
// instead of failing. That fallback is a billing safety net for the relay, not
// a price the operator configured, so the public pricing API must not publish
// it as model_ratio: API consumers would read $75/M for a model billed by
// expression (or not priced at all).
func TestGetPricingDoesNotPublishSelfUseFallbackRatio(t *testing.T) {
	disableSelfUseMode(t)
	resetPricingEndpointTestTables(t)
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"pricing-fallback-probe":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"pricing-fallback-probe":"tier(\"standard\", p * 2 + c * 8)"}`,
	}))
	insertPricingEndpointChannel(t, 961, 1, pricingEndpointAdvancedCustomConfig())
	insertPricingEndpointAbility(t, 961, "pricing-fallback-probe")
	InvalidatePricingCache()

	pricing, exists := pricingByModel(GetPricing())["pricing-fallback-probe"]
	require.True(t, exists)
	assert.Nil(t, pricing.ModelRatio,
		"unconfigured models must not publish the 37.5 self-use fallback as model_ratio")
	assert.Equal(t, "tiered_expr", pricing.BillingMode)
	assert.Equal(t, `tier("standard", p * 2 + c * 8)`, pricing.BillingExpr)
}

// Self-use mode is where the flag trap lives: GetModelRatio's second return
// value then reports true for an unknown model, because the 37.5 safety ratio
// can be used. Reading that flag as "configured" publishes the fallback on
// precisely the hosts that enable self-use -- the production configuration --
// so publication has to be decided by map membership instead.
func TestGetPricingDoesNotPublishSelfUseFallbackInSelfUseMode(t *testing.T) {
	savedSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = savedSelfUse })

	resetPricingEndpointTestTables(t)
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"pricing-selfuse-tiered-probe":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"pricing-selfuse-tiered-probe":"tier(\"standard\", p * 2 + c * 8)"}`,
	}))
	// The ratio map is normally seeded by InitRatioSettings at startup, which
	// the test binary does not run; configure it explicitly and restore it.
	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios)) })
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":1.25}`))
	insertPricingEndpointChannel(t, 962, 1, pricingEndpointAdvancedCustomConfig())
	insertPricingEndpointAbility(t, 962, "pricing-selfuse-tiered-probe")
	// An advertised model with no price, ratio or expression at all: the relay
	// bills it at the fallback, but the catalog must not call that a price.
	insertPricingEndpointAbility(t, 962, "pricing-selfuse-unpriced-probe")
	// A model with a real built-in ratio, so the fix cannot pass by publishing
	// nothing at all.
	insertPricingEndpointAbility(t, 962, "gpt-4o")
	InvalidatePricingCache()

	byModel := pricingByModel(GetPricing())

	tiered, exists := byModel["pricing-selfuse-tiered-probe"]
	require.True(t, exists)
	assert.Nil(t, tiered.ModelRatio,
		"self-use mode must not turn the 37.5 safety ratio into a published price")
	assert.Equal(t, "tiered_expr", tiered.BillingMode)

	unpriced, exists := byModel["pricing-selfuse-unpriced-probe"]
	require.True(t, exists)
	assert.Nil(t, unpriced.ModelRatio,
		"an unpriced model must advertise no ratio rather than the self-use fallback")

	configured, _, _ := ratio_setting.GetModelRatio("gpt-4o")
	configuredPricing, exists := byModel["gpt-4o"]
	require.True(t, exists)
	require.NotNil(t, configuredPricing.ModelRatio,
		"a model with a real ratio must keep publishing it")
	assert.Equal(t, configured, *configuredPricing.ModelRatio)
}

func disableSelfUseMode(t *testing.T) {
	t.Helper()
	saved := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = saved })
}
