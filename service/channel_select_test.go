package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPinnedTaskPluginChannelTypesUsesPinnedGenerationIndex(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectTaskPluginSource("legacy-select", constant.ChannelTypeKling), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "legacy-select")
	assert.Equal(t, []int{constant.ChannelTypeKling}, types)
	assert.Equal(t, []string{"legacy-select"}, keys)
	types, keys = pinnedTaskPluginIdentities(c, "another-plugin")
	assert.Empty(t, types)
	assert.Empty(t, keys)
	types, keys = pinnedTaskPluginIdentities(nil, "legacy-select")
	assert.Empty(t, types)
	assert.Empty(t, keys)
}

func TestPinnedTaskPluginChannelTypesLeavesGenericChannelsKeyed(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectTaskPluginSource("generic-select", constant.ChannelTypeTaskPlugin), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "generic-select")
	assert.Empty(t, types)
	assert.Equal(t, []string{"generic-select"}, keys)
}

func TestPinnedTaskPluginChannelTypesIncludesSharedEndpointProviders(t *testing.T) {
	registry := jsplugin.NewRegistry()
	_, err := registry.Register(channelSelectEndpointPluginSource("gemini-select", constant.ChannelTypeGemini), jsplugin.Options{})
	require.NoError(t, err)
	_, err = registry.Register(channelSelectEndpointPluginSource("vertex-select", constant.ChannelTypeVertexAi), jsplugin.Options{})
	require.NoError(t, err)
	candidates := registry.Generation().LookupEndpointCandidates("POST", "/v1/responses", "task-model")
	require.Len(t, candidates, 2)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     candidates[0].Plugin,
	})
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{
		Generation: registry.Generation(),
		Plugin:     candidates[0].Plugin,
		Protocol:   candidates[0].Protocol,
		Operation:  candidates[0].Operation,
		Model:      "task-model",
		Candidates: candidates,
	})

	AppendTaskPluginIdentityFilter(c, candidates[0].Plugin.Meta.Key)
	filters := GetChannelConstraints(c).Filters
	require.Len(t, filters, 1)
	assert.Equal(t, []int{constant.ChannelTypeGemini, constant.ChannelTypeVertexAi}, filters[0].TaskPluginChannelTypes)
	assert.Equal(t, []string{"gemini-select", "vertex-select"}, filters[0].TaskPluginKeys)
}

func channelSelectTaskPluginSource(key string, channelType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  %s
  models: ["task-model"],
  fetchMode: "per_task",
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`, key, key, channelSelectChannelTypesField(channelType))
}

func channelSelectEndpointPluginSource(key string, channelType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  %s
  models: ["task-model"],
  fetchMode: "per_task",
  protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
export const protocols = {openai_responses: {
  decodeRequest: function(ctx) { return {kind: "submit", model: "task-model", requestBody: ctx.body.value}; },
  renderEvents: function() { return {events: [], state: null, done: false}; },
  renderFinal: function() { return {output: []}; },
}};
`, key, key, channelSelectChannelTypesField(channelType))
}

func channelSelectChannelTypesField(channelType int) string {
	if channelType <= 0 || channelType == constant.ChannelTypeTaskPlugin {
		return ""
	}
	return fmt.Sprintf("channelTypes: [%d],", channelType)
}

func TestPinnedTaskPluginChannelTypesIncludesCompatibleTypes(t *testing.T) {
	registry := jsplugin.NewRegistry()
	plugin, err := registry.Register(channelSelectCompatiblePluginSource("sora-select", constant.ChannelTypeSora, constant.ChannelTypeOpenAI), jsplugin.Options{})
	require.NoError(t, err)

	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
		Generation: registry.Generation(),
		Plugin:     plugin,
	})

	types, keys := pinnedTaskPluginIdentities(c, "sora-select")
	assert.Equal(t, []int{constant.ChannelTypeSora, constant.ChannelTypeOpenAI}, types)
	assert.Equal(t, []string{"sora-select"}, keys)
}

func channelSelectCompatiblePluginSource(key string, channelType, compatibleType int) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1,
  key: %q,
  name: %q,
  version: "1.0.0",
  author: {name: "Test"},
  channelTypes: [%d, %d],
  models: ["task-model"],
  fetchMode: "per_task",
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`, key, key, channelType, compatibleType)
}

func TestSharedType61IdentityFilterContainsAllCandidateKeys(t *testing.T) {
	registry := jsplugin.NewRegistry()
	for _, key := range []string{"alpha", "beta"} {
		_, err := registry.Register(channelSelectEndpointPluginSource(key, 0), jsplugin.Options{})
		require.NoError(t, err)
	}
	generation := registry.Generation()
	candidates := generation.LookupEndpointCandidates("POST", "/v1/responses", "task-model")
	require.Len(t, candidates, 2)
	c, _ := gin.CreateTestContext(nil)
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{Generation: generation, Plugin: candidates[0].Plugin, Candidates: candidates})
	AppendTaskPluginIdentityFilter(c, "alpha")
	filters := GetChannelConstraints(c).Filters
	require.Len(t, filters, 1)
	assert.Equal(t, "alpha", filters[0].TaskPluginKey)
	assert.Equal(t, []string{"alpha", "beta"}, filters[0].TaskPluginKeys)
	assert.Empty(t, filters[0].TaskPluginChannelTypes)
}

// setupChannelSelectTest gives a selection test its own SQLite database. The
// reserved-word column names come from InitCol, which production reaches via
// InitDB; a test database built by hand has to ask for it explicitly.
func setupChannelSelectTest(t *testing.T) *gorm.DB {
	t.Helper()

	model.InitCol()
	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1, "vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createTestChannelForSelect(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func newSelectRetryParam(modelName string, excluded map[int]struct{}) (*gin.Context, *RetryParam) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return c, &RetryParam{
		Ctx:         c,
		TokenGroup:  "default",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
		ExcludedIDs: excluded,
	}
}

func TestSelectChannelForRequestOverloadedFallback(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-overload-fallback-model"

	const chID1, chID2 = 7001, 7002
	createTestChannelForSelect(t, db, chID1, "default", modelName)
	createTestChannelForSelect(t, db, chID2, "default", modelName)

	loadbalancer.SetPolicy(&loadbalancer.Policy{
		Enabled: true,
		Default: loadbalancer.ChannelPolicy{
			MaxInflight: 1,
			Breaker:     loadbalancer.BreakerPolicy{FailureThreshold: 5, CooldownSeconds: 60, HalfOpenProbes: 1},
		},
	})
	t.Cleanup(func() { loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: false}) })

	// Both channels reach max_inflight; chID2 is further past it. Selection
	// must fall back to the least loaded one instead of failing with 503.
	tr := loadbalancer.GlobalTracker()
	handles := []*loadbalancer.RequestHandle{tr.Begin(chID1), tr.Begin(chID2), tr.Begin(chID2), tr.Begin(chID2)}
	t.Cleanup(func() {
		for _, h := range handles {
			h.End(false, false)
		}
	})

	c, retry := newSelectRetryParam(modelName, nil)
	selected, selectGroup, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "default", selectGroup)
	assert.Equal(t, chID1, selected.Id, "should pick the least loaded overloaded channel")
}

func TestSelectChannelForRequestPrefersOverloadedOverDegradedFallback(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-degraded-vs-overloaded-model"

	const overloadedID, degradedID = 7051, 7052
	createTestChannelForSelect(t, db, overloadedID, "default", modelName)
	createTestChannelForSelect(t, db, degradedID, "default", modelName)

	loadbalancer.SetPolicy(&loadbalancer.Policy{
		Enabled: true,
		Default: loadbalancer.ChannelPolicy{
			MaxInflight: 1,
			Breaker:     loadbalancer.BreakerPolicy{FailureThreshold: 99, CooldownSeconds: 60, HalfOpenProbes: 1},
		},
	})
	t.Cleanup(func() { loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: false}) })

	tr := loadbalancer.GlobalTracker()
	inflight := tr.Begin(overloadedID)
	t.Cleanup(func() { inflight.End(false, false) })
	// Three consecutive slow attempts mark the channel as degraded: usable, but
	// only as a last resort.
	for i := 0; i < 3; i++ {
		tr.Begin(degradedID).End(true, false)
	}
	require.True(t, tr.IsDegraded(degradedID))

	c, retry := newSelectRetryParam(modelName, nil)
	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, overloadedID, selected.Id, "an overloaded channel beats a proven-slow one")
}

func TestSelectChannelForRequestDatabaseModeExcludedFilters(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-db-mode-excluded-model"

	const chID1, chID2 = 7101, 7102
	createTestChannelForSelect(t, db, chID1, "default", modelName)
	createTestChannelForSelect(t, db, chID2, "default", modelName)

	c, retry := newSelectRetryParam(modelName, map[int]struct{}{chID1: {}})
	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, chID2, selected.Id, "must select a non-excluded channel in database mode")
}

func TestSelectChannelForRequestExhaustsCandidates(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-many-channels-model"

	all := make([]int, 0, 8)
	for i := 1; i <= 8; i++ {
		createTestChannelForSelect(t, db, 7200+i, "default", modelName)
		all = append(all, 7200+i)
	}
	// Six of the eight are already known bad; the remaining two must stay
	// reachable however many skips precede them.
	excluded := make(map[int]struct{}, 6)
	for _, id := range all[:6] {
		excluded[id] = struct{}{}
	}

	c, retry := newSelectRetryParam(modelName, excluded)
	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Contains(t, []int{7207, 7208}, selected.Id)

	// Excluding every candidate is exhaustion, reported as "no available
	// channel" rather than as a database failure.
	for _, id := range all[6:] {
		excluded[id] = struct{}{}
	}
	c2, retry2 := newSelectRetryParam(modelName, excluded)
	selected, _, err = SelectChannelForRequest(c2, modelName, retry2)
	assert.Nil(t, selected)
	require.NotNil(t, err)
	assert.True(t, err.NoAvailableChannel)
}

// The candidate set must be resolved once, not once per skipped channel: the
// old per-attempt resolution issued two queries per skip and reached 150
// statements on a 57-channel model.
func TestSelectChannelForRequestResolvesCandidatesOnce(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-query-count-model"

	for i := 1; i <= 20; i++ {
		createTestChannelForSelect(t, db, 7300+i, "default", modelName)
	}
	excluded := make(map[int]struct{}, 19)
	for i := 1; i < 20; i++ {
		excluded[7300+i] = struct{}{}
	}

	queries := 0
	name := "test:count_queries"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(*gorm.DB) { queries++ }))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })

	c, retry := newSelectRetryParam(modelName, excluded)
	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 7320, selected.Id)
	assert.LessOrEqual(t, queries, 4, "the candidate set must be resolved once, not per skipped channel")
}

// A model name that carries a thought-level modifier routes to the abilities
// of its normalized base name, so a request for "gpt-4o-mini-high" still finds
// the channels registered for "gpt-4o-mini".
func TestSelectChannelForRequestFallsBackToNormalizedModelName(t *testing.T) {
	db := setupChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const requested = "gpt-4o-mini-high"
	const registered = "gpt-4o-mini"
	require.Equal(t, registered, ratio_setting.RoutingMatchModelName(requested))

	createTestChannelForSelect(t, db, 7401, "default", registered)

	c, retry := newSelectRetryParam(requested, nil)
	selected, _, err := SelectChannelForRequest(c, requested, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 7401, selected.Id)
}
