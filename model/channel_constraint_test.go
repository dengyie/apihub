package model

import (
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/loadbalancer"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestFilterCandidateIDs(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	alpha := &Channel{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &alphaSetting}
	beta := &Channel{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &betaSetting}
	ordinary := &Channel{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	kling := &Channel{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled}
	jimeng := &Channel{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled}
	matchingCustom := &Channel{Id: 900010, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	matchingCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})
	otherCustom := &Channel{Id: 900011, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	otherCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/responses",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	pathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: "/v1/chat/completions"}
	emptyPathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: ""}

	tests := []struct {
		name      string
		ids       []int
		modelName string
		filters   []dto.ChannelFilter
		wantKept  []int
		wantEmpty dto.ChannelFilterKind
	}{
		{
			name:      "identity keeps matching type-59 key",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("alpha", nil),
			wantKept:  []int{900001},
		},
		{
			name:      "identity empty key drops all type-59",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity empty key keeps ordinary channel",
			ids:       []int{900003},
			modelName: "ordinary",
			filters:   identityFilters("", nil),
			wantKept:  []int{900003},
		},
		{
			name:      "identity keeps matching legacy type",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "identity keeps all listed legacy types",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}),
			wantKept:  []int{900004, 900005},
		},
		{
			name:      "identity keyed with no types drops legacy",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity drops missing cache entry",
			ids:       []int{900004, 999999},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "empty request path is a passthrough including missing ids",
			ids:       []int{900003, 900010, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{emptyPathFilter},
			wantKept:  []int{900003, 900010, 999999},
		},
		{
			name:      "request path keeps missing cache entry for consistency",
			ids:       []int{900003, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 999999},
		},
		{
			name:      "request path keeps matching type-58 and ordinary",
			ids:       []int{900003, 900010, 900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 900010},
		},
		{
			name:      "request path empties when only unmatched type-58 remains",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
		{
			name:      "intersection attributes empty set to identity after path keeps candidates",
			ids:       []int{900001, 900010},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter, identityFilters("missing", nil)[0]},
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "intersection attributes empty set to path when path runs first",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{identityFilters("", nil)[0], pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
	}

	channelSyncLock.Lock()
	previous := channelsIDM
	channelsIDM = map[int]*Channel{
		900001: alpha,
		900002: beta,
		900003: ordinary,
		900004: kling,
		900005: jimeng,
		900010: matchingCustom,
		900011: otherCustom,
	}
	t.Cleanup(func() {
		channelsIDM = previous
		channelSyncLock.Unlock()
	})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			kept, emptiedBy := filterCandidateIDs(testCase.ids, testCase.modelName, testCase.filters)
			if testCase.wantKept == nil {
				assert.Nil(t, kept)
			} else {
				assert.Equal(t, testCase.wantKept, kept)
			}
			assert.Equal(t, testCase.wantEmpty, emptiedBy)
		})
	}
}

func TestChannelSatisfiesFilters(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	alpha := &Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Setting: &alphaSetting}
	ordinary := &Channel{Id: 2, Type: constant.ChannelTypeOpenAI}
	custom := &Channel{Id: 3, Type: constant.ChannelTypeAdvancedCustom}
	custom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	ok, kind := ChannelSatisfiesFilters(nil, "gpt-4", nil)
	assert.False(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("alpha", nil))
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("beta", nil))
	assert.False(t, ok)
	assert.Equal(t, dto.FilterTaskPluginIdentity, kind)

	ok, kind = ChannelSatisfiesFilters(ordinary, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/chat/completions",
	}})
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(custom, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/responses",
	}})
	assert.False(t, ok)
	assert.Equal(t, dto.FilterRequestPath, kind)
}

// TestChannelCandidatesExclusionDatabaseMode covers the database-backed
// candidate set, which must honour excluded channels just like the memory
// cache does: it used to ignore them and re-pick the channel that had just
// failed.
func TestChannelCandidatesExclusionDatabaseMode(t *testing.T) {
	truncateTables(t)
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCache })

	priority10 := int64(10)
	priority5 := int64(5)
	weight1 := uint(1)
	baseURL := "https://example.com"
	for _, channel := range []Channel{
		{Id: 800001, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-primary", Models: "gpt-test", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
		{Id: 800002, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-secondary", Models: "gpt-test", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
		{Id: 800003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-backup", Models: "gpt-test", Group: "default", Priority: &priority5, Weight: &weight1, BaseURL: &baseURL},
	} {
		channel := channel
		require.NoError(t, channel.Insert())
	}

	pick := func(excluded map[int]struct{}) *Channel {
		t.Helper()
		candidates, err := GetChannelCandidates("default", "gpt-test", nil)
		require.NoError(t, err)
		return candidates.Pick(0, excluded, "")
	}

	// Without exclusions the top priority tier wins.
	assert.Contains(t, []int{800001, 800002}, pick(nil).Id)
	// Excluding one of them leaves the other.
	assert.Equal(t, 800002, pick(map[int]struct{}{800001: {}}).Id)
	// Excluding the whole top tier falls through to the next priority.
	assert.Equal(t, 800003, pick(map[int]struct{}{800001: {}, 800002: {}}).Id)
	// Excluding everything exhausts the set rather than erroring.
	assert.Nil(t, pick(map[int]struct{}{800001: {}, 800002: {}, 800003: {}}))
}

// TestChannelCandidatesDatabaseMatrix runs the database-backed candidate
// resolution against real SQLite, MySQL and PostgreSQL instances.
//
// The reason this cannot be a SQLite-only test is the reserved-word quoting:
// abilities are filtered with `commonGroupCol`, which is `"group"` on
// PostgreSQL and backticked elsewhere. A statement that is valid on SQLite and
// MySQL is a syntax error on PostgreSQL, so a green SQLite run says nothing
// about the dialect production actually runs on.
//
// The subtests also pin the behaviours the refactor depends on — priority-tier
// fallthrough, exclusion, exhaustion, and the normalized-model-name fallback —
// against each dialect rather than only the one the test suite defaults to.
func TestChannelCandidatesDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			switch dialect {
			case "sqlite":
				dialector = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				dialector = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialector, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "candidates_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)

			previousDB := DB
			previousMainType := common.MainDatabaseType()
			previousMemoryCache := common.MemoryCacheEnabled
			DB = db
			common.MemoryCacheEnabled = false
			// initCol derives commonGroupCol from the main database type, so it
			// has to be re-run for every dialect or the backtick form leaks into
			// the PostgreSQL subtest and the failure looks like a code bug.
			switch dialect {
			case "sqlite":
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			}
			initCol()
			t.Cleanup(func() {
				DB = previousDB
				common.MemoryCacheEnabled = previousMemoryCache
				common.SetMainDatabaseType(previousMainType)
				initCol()
				require.NoError(t, db.Migrator().DropTable(&Channel{}, &Ability{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))

			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)

			priority10 := int64(10)
			priority5 := int64(5)
			weight1 := uint(1)
			baseURL := "https://example.com"
			for _, channel := range []Channel{
				{Id: 810001, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "matrix-primary", Models: "gpt-matrix", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
				{Id: 810002, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "matrix-secondary", Models: "gpt-matrix", Group: "default", Priority: &priority10, Weight: &weight1, BaseURL: &baseURL},
				{Id: 810003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "matrix-backup", Models: "gpt-matrix", Group: "default", Priority: &priority5, Weight: &weight1, BaseURL: &baseURL},
			} {
				channel := channel
				require.NoError(t, channel.Insert())
			}

			pick := func(group, modelName string, retry int, excluded map[int]struct{}) *Channel {
				t.Helper()
				candidates, err := GetChannelCandidates(group, modelName, nil)
				require.NoError(t, err)
				return candidates.Pick(retry, excluded, "")
			}

			// A group with no abilities resolves to an empty set instead of an
			// error, so the caller's skip loop terminates rather than retrying.
			assert.Nil(t, pick("group-without-channels", "gpt-matrix", 0, nil))
			// The reserved-word `group` filter above only proves anything if it
			// actually matched rows for the group that does exist.
			assert.Contains(t, []int{810001, 810002}, pick("default", "gpt-matrix", 0, nil).Id)
			assert.Equal(t, 810002, pick("default", "gpt-matrix", 0, map[int]struct{}{810001: {}}).Id)
			assert.Equal(t, 810003, pick("default", "gpt-matrix", 0, map[int]struct{}{810001: {}, 810002: {}}).Id)
			assert.Nil(t, pick("default", "gpt-matrix", 0, map[int]struct{}{810001: {}, 810002: {}, 810003: {}}))
			// A second retry tier reaches the lower-priority channel directly.
			assert.Equal(t, 810003, pick("default", "gpt-matrix", 1, nil).Id)

			// A suffixed model name falls back to its base name, which is what
			// makes "-high"/"-low" variants routable without per-variant abilities.
			assert.Contains(t, []int{810001, 810002}, pick("default", "gpt-matrix-high", 0, nil).Id)

			// A channel disabled in channels must not be selectable even when its
			// abilities row is stale. UpdateChannelStatus syncs abilities from a
			// defer whose error is only logged, so the two tables can diverge; the
			// memory-cache path filters on channel.Status and the database path
			// has to agree with it. Runs last because it mutates shared rows.
			require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 810003).
				Update("status", common.ChannelStatusManuallyDisabled).Error)
			for retry := range 3 {
				picked := pick("default", "gpt-matrix", retry, nil)
				if picked == nil {
					continue
				}
				assert.NotEqual(t, 810003, picked.Id,
					"a channel disabled in channels must not be picked even with a stale enabled ability")
			}
		})
	}
}

// TestChannelCandidatesRetryLeavesStickyChannel pins the boundary between
// sticky routing and channel failover.
//
// Sticky routing hashes the prompt prefix to a fixed index so repeat requests
// hit one channel and reuse the upstream prompt cache. That is the right
// behaviour for the first attempt and exactly wrong for a retry: after a
// channel fails, "pick consistently" means "pick the channel that just failed
// again", deterministically, for every remaining retry in the budget.
//
// It used to happen because the retry index was clamped into range before the
// sticky check. A pool with a single priority tier has len(tiers)==1, so every
// retry>0 was clamped back to 0 and re-entered the sticky branch.
func TestChannelCandidatesRetryLeavesStickyChannel(t *testing.T) {
	priority := int64(10)
	// Weight 0 on the sticky channel and 100 on the other one makes the
	// weighted-random branch deterministic: with sumWeight=100 the zero-weight
	// channel never wins the draw. Without the fix this test would instead get
	// the sticky channel every time, so the assertion below discriminates.
	stickyWeight := uint(0)
	otherWeight := uint(100)
	candidates := &ChannelCandidates{channels: []*Channel{
		{Id: 1, Priority: &priority, Weight: &stickyWeight},
		{Id: 2, Priority: &priority, Weight: &otherWeight},
	}}

	// Pick a prompt prefix whose sticky index is the first channel, so that a
	// retry which wrongly reuses sticky routing is visibly returning channel 1.
	stickyKey := ""
	for i := 0; i < 1000 && stickyKey == ""; i++ {
		if key := fmt.Sprintf("prompt-%d", i); loadbalancer.StickyIndex(key, 2) == 0 {
			stickyKey = key
		}
	}
	require.NotEmpty(t, stickyKey, "no sticky key maps to index 0 of a 2-channel tier")

	assert.Equal(t, 1, candidates.Pick(0, nil, stickyKey).Id, "first attempt must honour sticky routing")
	assert.Equal(t, 2, candidates.Pick(1, nil, stickyKey).Id, "a retry must leave the sticky channel it failed on")
	assert.Equal(t, 2, candidates.Pick(3, nil, stickyKey).Id, "clamping into range must not re-arm sticky routing")

	// A retry still lands on a usable channel when the sticky one is excluded,
	// and a pool of one still resolves rather than dead-ending the retry loop.
	assert.Equal(t, 2, candidates.Pick(1, map[int]struct{}{1: {}}, stickyKey).Id)
	sole := &ChannelCandidates{channels: []*Channel{{Id: 9, Priority: &priority, Weight: &otherWeight}}}
	assert.Equal(t, 9, sole.Pick(2, nil, stickyKey).Id, "exhausting the sticky preference must not return nil")
}
