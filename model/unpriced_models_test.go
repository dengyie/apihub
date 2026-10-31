package model

import (
	"strconv"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/setting/config"
	"github.com/dengyie/apihub/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The audit exists because GetModelRatio answers a missing entry with the 37.5
// self-use fallback instead of an error: an unpriced model bills silently. These
// tests pin the two halves of that contract -- which models are considered
// routable, and what counts as having a price.

func seedAbilityForStatus(t *testing.T, channelStatus int, modelName string) *Channel {
	t.Helper()
	channel := Channel{
		Name:   "audit-" + modelName,
		Key:    "sk-test",
		Group:  "default",
		Models: modelName,
		Status: channelStatus,
	}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{
		Group:     "default",
		Model:     modelName,
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)
	return &channel
}

// seedTieredBillingExpr installs a tiered expression for one model and restores
// the previous billing setting afterwards.
func seedTieredBillingExpr(t *testing.T, modelName, expr string) {
	t.Helper()
	setting := config.GlobalConfig.Get("billing_setting")
	before := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		require.NoError(t, config.UpdateConfigFromMap(setting, map[string]string{
			"billing_mode": before["billing_setting.billing_mode"],
			"billing_expr": before["billing_setting.billing_expr"],
		}))
	})
	modeBefore := before["billing_setting.billing_mode"]
	if modeBefore == "" {
		modeBefore = "{}"
	}
	require.NoError(t, config.UpdateConfigFromMap(setting, map[string]string{
		"billing_mode": `{"` + modelName + `":"tiered_expr"}`,
		"billing_expr": `{"` + modelName + `":` + strconv.Quote(expr) + `}`,
	}))
}

// seedRatio installs a legacy ratio for one model and restores the map after.
func seedRatio(t *testing.T, modelName string, ratio float64) {
	t.Helper()
	previous := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previous))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(
		`{"`+modelName+`": `+strconv.FormatFloat(ratio, 'f', -1, 64)+`}`,
	))
}

func TestGetUnpricedModelsReportsModelsWithoutEntry(t *testing.T) {
	setupChannelStatusTest(t)
	seedAbilityForStatus(t, common.ChannelStatusEnabled, "audit-unpriced-model")

	unpriced, checked, err := GetUnpricedModels()
	require.NoError(t, err)
	assert.Equal(t, 1, checked)
	require.Len(t, unpriced, 1)
	assert.Equal(t, "audit-unpriced-model", unpriced[0].Model)
	assert.Equal(t, 1, unpriced[0].Channels)
}

// A designed price does not need the legacy ratio table: a tiered expression
// entry is just as much a price. Without this the audit would flag every
// expression-priced model in the deployment.
func TestGetUnpricedModelsAcceptsTieredBillingEntry(t *testing.T) {
	setupChannelStatusTest(t)
	seedAbilityForStatus(t, common.ChannelStatusEnabled, "audit-tiered-model")
	seedTieredBillingExpr(t, "audit-tiered-model", `tier("standard", p * 1 + c * 2)`)

	unpriced, _, err := GetUnpricedModels()
	require.NoError(t, err)
	assert.Empty(t, unpriced)
}

func TestGetUnpricedModelsAcceptsRatioEntry(t *testing.T) {
	setupChannelStatusTest(t)
	seedAbilityForStatus(t, common.ChannelStatusEnabled, "audit-ratio-model")
	seedRatio(t, "audit-ratio-model", 1.5)

	unpriced, _, err := GetUnpricedModels()
	require.NoError(t, err)
	assert.Empty(t, unpriced)
}

// A model whose channel was disabled is not routable, so a leftover ability row
// must not keep producing audit noise. This is the same status/enabled pair
// GetChannelCandidates requires.
func TestGetUnpricedModelsSkipsModelsOnDisabledChannels(t *testing.T) {
	setupChannelStatusTest(t)
	seedAbilityForStatus(t, common.ChannelStatusManuallyDisabled, "audit-disabled-channel-model")
	seedAbilityForStatus(t, common.ChannelStatusAutoDisabled, "audit-auto-disabled-channel-model")

	unpriced, checked, err := GetUnpricedModels()
	require.NoError(t, err)
	assert.Zero(t, checked)
	assert.Empty(t, unpriced)
}

// The billing resolver strips trailing @key:value modifiers, so a priced base
// name has to satisfy the audit for every suffixed spelling a client can send.
func TestGetUnpricedModelsMatchesBaseNameOfModifiedModel(t *testing.T) {
	setupChannelStatusTest(t)
	seedAbilityForStatus(t, common.ChannelStatusEnabled, "audit-suffixed@thinking:high")
	seedRatio(t, "audit-suffixed", 2)

	unpriced, _, err := GetUnpricedModels()
	require.NoError(t, err)
	assert.Empty(t, unpriced)
}
