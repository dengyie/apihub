package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetCatalogVisibilityTestTables(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Model{}))
	require.NoError(t, DB.Exec("DELETE FROM models").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Exec("DELETE FROM models").Error)
	})
}

func insertCatalogVisibilityRow(t *testing.T, name string, nameRule int, status int) {
	t.Helper()
	row := &Model{ModelName: name, NameRule: nameRule}
	require.NoError(t, DB.Create(row).Error)
	// status has a gorm default of 1, so a zero value must be written explicitly.
	require.NoError(t, DB.Model(&Model{}).Where("id = ?", row.Id).Update("status", status).Error)
}

func TestFilterCatalogHiddenModels(t *testing.T) {
	resetCatalogVisibilityTestTables(t)

	insertCatalogVisibilityRow(t, "gpt-6-astra", NameRuleExact, 1)
	insertCatalogVisibilityRow(t, "gpt-5.5", NameRuleExact, 0)
	insertCatalogVisibilityRow(t, "legacy-", NameRulePrefix, 0)

	names := []string{"gpt-6-astra", "gpt-5.5", "legacy-chat", "unlisted-model"}

	// Visibility follows the catalog: an exact row hides its own name, a prefix
	// rule hides matching names, and a name without metadata stays visible.
	assert.Equal(t, map[string]struct{}{"gpt-5.5": {}, "legacy-chat": {}}, GetCatalogHiddenModels(names))
	assert.Equal(t, []string{"gpt-6-astra", "unlisted-model"}, FilterCatalogHiddenModels(names))

	// Order is preserved and hiding must not mutate the caller's slice.
	filtered := FilterCatalogHiddenModels([]string{"b", "a", "c"})
	assert.Equal(t, []string{"b", "a", "c"}, filtered)
}

func TestFilterCatalogHiddenModelsEmptyInput(t *testing.T) {
	resetCatalogVisibilityTestTables(t)
	assert.Empty(t, GetCatalogHiddenModels(nil))
	assert.Empty(t, FilterCatalogHiddenModels(nil))
}
