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
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupLBChannelSelectTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createTestChannel(t *testing.T, db *gorm.DB, id int, group, modelName string, priority int64) {
	t.Helper()
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

func TestSelectChannelForRequest_OverloadedFallback(t *testing.T) {
	db := setupLBChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-overload-fallback-model"

	chID1 := 7001
	chID2 := 7002
	createTestChannel(t, db, chID1, "default", modelName, 0)
	createTestChannel(t, db, chID2, "default", modelName, 0)

	// Set loadbalancer policy: max_inflight = 1 for both
	policy := &loadbalancer.Policy{
		Enabled: true,
		Default: loadbalancer.ChannelPolicy{
			MaxInflight: 1,
			Breaker: loadbalancer.BreakerPolicy{
				FailureThreshold: 5,
				CooldownSeconds:  60,
				HalfOpenProbes:   1,
			},
		},
	}
	loadbalancer.SetPolicy(policy)
	defer func() {
		loadbalancer.SetPolicy(&loadbalancer.Policy{Enabled: false})
	}()

	tr := loadbalancer.GlobalTracker()

	// Make chID2 more overloaded (inflight = 3)
	h2a := tr.Begin(chID2)
	h2b := tr.Begin(chID2)
	h2c := tr.Begin(chID2)
	defer func() {
		h2a.End(false, false)
		h2b.End(false, false)
		h2c.End(false, false)
	}()

	// Make chID1 less overloaded (inflight = 1 >= max_inflight 1)
	h1 := tr.Begin(chID1)
	defer h1.End(false, false)

	// Both channels are now overloaded (inflight >= 1).
	// Under previous implementation, this would abort with 503 NoAvailableChannel.
	// With overloadedFallback, it must select chID1 (least-loaded: inflight 1 vs 3).
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	retry := &RetryParam{
		Ctx:        c,
		TokenGroup: "default",
		ModelName:  modelName,
		Retry:      common.GetPointer(0),
	}

	selected, selectGroup, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "default", selectGroup)
	assert.Equal(t, chID1, selected.Id, "should select chID1 because it has lower inflight (1 vs 3)")
}

func TestSelectChannelForRequest_DatabaseModeExcludedFilters(t *testing.T) {
	db := setupLBChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-db-mode-excluded-model"

	chID1 := 7101
	chID2 := 7102
	createTestChannel(t, db, chID1, "default", modelName, 0)
	createTestChannel(t, db, chID2, "default", modelName, 0)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// Exclude chID1
	retry := &RetryParam{
		Ctx:         c,
		TokenGroup:  "default",
		ModelName:   modelName,
		Retry:       common.GetPointer(0),
		ExcludedIDs: map[int]struct{}{chID1: {}},
	}

	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, chID2, selected.Id, "must select non-excluded channel in database mode")
}

func TestSelectChannelForRequest_AttemptsBeyondFive(t *testing.T) {
	db := setupLBChannelSelectTest(t)
	common.MemoryCacheEnabled = false
	const modelName = "test-many-channels-model"

	// Create 8 channels
	for i := 1; i <= 8; i++ {
		createTestChannel(t, db, 7200+i, "default", modelName, 0)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// Exclude channels 7201 through 7206 (6 channels excluded).
	// With old maxLBAttempts=5, the loop would give up after 5 attempts and fail.
	// With maxLBAttempts=50, it iterates and finds 7207 or 7208.
	excluded := make(map[int]struct{})
	for i := 1; i <= 6; i++ {
		excluded[7200+i] = struct{}{}
	}

	retry := &RetryParam{
		Ctx:         c,
		TokenGroup:  "default",
		ModelName:   modelName,
		Retry:       common.GetPointer(0),
		ExcludedIDs: excluded,
	}

	selected, _, err := SelectChannelForRequest(c, modelName, retry)
	require.Nil(t, err)
	require.NotNil(t, selected)
	assert.Contains(t, []int{7207, 7208}, selected.Id, "should find channels beyond the 5th attempt")
}
