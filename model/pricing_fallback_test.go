package model

import (
	"testing"

	"github.com/dengyie/apihub/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GetModelRatio answers an unknown model with the 37.5 self-use fallback
// instead of failing. That fallback is a billing safety net for the relay, not
// a price the operator configured, so the public pricing API must not publish
// it as model_ratio: API consumers would read $75/M for a model billed by
// expression (or not priced at all).
func TestGetPricingDoesNotPublishSelfUseFallbackRatio(t *testing.T) {
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
