package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetCatalogVisibilityTestTables(t *testing.T) {
	t.Helper()
	InvalidateCatalogMetadataCache()
	require.NoError(t, DB.AutoMigrate(&Model{}, &Option{}, &Vendor{}))
	require.NoError(t, DB.Exec("DELETE FROM models").Error)
	t.Cleanup(func() {
		InvalidateCatalogMetadataCache()
		require.NoError(t, DB.Exec("DELETE FROM models").Error)
	})
}

func insertCatalogVisibilityRow(t *testing.T, name string, nameRule int, status int) {
	t.Helper()
	row := &Model{ModelName: name, NameRule: nameRule}
	require.NoError(t, DB.Create(row).Error)
	// status has a gorm default of 1, so a zero value must be written explicitly.
	require.NoError(t, DB.Model(&Model{}).Where("id = ?", row.Id).Update("status", status).Error)
	InvalidateCatalogMetadataCache()
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

func TestCatalogMetadataCacheAndInvalidate(t *testing.T) {
	resetCatalogVisibilityTestTables(t)

	insertCatalogVisibilityRow(t, "cached-model", NameRuleExact, 0)
	assert.Equal(t, map[string]struct{}{"cached-model": {}}, GetCatalogHiddenModels([]string{"cached-model"}))

	// Directly update DB without invalidating cache: cached result must persist
	require.NoError(t, DB.Model(&Model{}).Where("model_name = ?", "cached-model").Update("status", 1).Error)
	assert.Equal(t, map[string]struct{}{"cached-model": {}}, GetCatalogHiddenModels([]string{"cached-model"}))

	// Invalidate cache: fresh DB value must be seen
	InvalidateCatalogMetadataCache()
	assert.Empty(t, GetCatalogHiddenModels([]string{"cached-model"}))

	// Model.Insert invalidates cache
	newRow := &Model{ModelName: "new-hidden-model", NameRule: NameRuleExact, Status: 2}
	require.NoError(t, newRow.Insert())
	assert.Equal(t, map[string]struct{}{"new-hidden-model": {}}, GetCatalogHiddenModels([]string{"new-hidden-model"}))

	// DeleteModelMetadata invalidates cache
	_, err := DeleteModelMetadata([]int{newRow.Id}, false, false)
	require.NoError(t, err)
	assert.Empty(t, GetCatalogHiddenModels([]string{"new-hidden-model"}))
}
