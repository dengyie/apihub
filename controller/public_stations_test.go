package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// /api/public_stations is unauthenticated: its JSON must be a strict whitelist
// projection. The channel key, admin settings and accounting fields must never
// leak even though the DB rows carry them.
func TestGetPublicStationsWhitelistProjection(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	baseURL := "https://yiming.example.com/"
	secretTag := "private-paid"
	priority := int64(10)
	weight := uint(5)
	require.NoError(t, db.Create(&model.Channel{
		Id:      151,
		Name:    "cpa-agentrouter-1",
		Type:    60,
		Status:  common.ChannelStatusEnabled,
		Key:     "sk-UPSTREAM-SECRET-KEY",
		BaseURL: &baseURL,
		Models:  "claude-sonnet-5,gpt-5.5,",
		Group:   "default",
		Tag:     &[]string{PublicStationsTag}[0],
		// Fields that must NOT survive the projection:
		Other:         `{"azure_api_version":"2024"}`,
		OtherInfo:     `{"balance": 99.5}`,
		OtherSettings: `{"proxy":"http://127.0.0.1:7890"}`,
		Priority:      &priority,
		Weight:        &weight,
		ResponseTime:  320,
		TestTime:      1730000000,
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id:     900,
		Name:   "private-paid-channel",
		Type:   1,
		Status: common.ChannelStatusEnabled,
		Key:    "sk-PRIVATE-KEY",
		Models: "gpt-5.5",
		Group:  "default",
		Tag:    &secretTag,
	}).Error)
	// Loopback sidecar channels (internal gateways) must never be published...
	loopback := "http://127.0.0.1:8317"
	require.NoError(t, db.Create(&model.Channel{
		Id:      8,
		Name:    "xAI-sidecar",
		Type:    48,
		Status:  common.ChannelStatusEnabled,
		Key:     "sk-SIDECAR-KEY",
		BaseURL: &loopback,
		Models:  "grok-4",
		Group:   "default",
	}).Error)
	// ...and neither may disabled channels.
	disabledBaseURL := "https://dead-station.example.com"
	require.NoError(t, db.Create(&model.Channel{
		Id:      901,
		Name:    "disabled-station",
		Type:    1,
		Status:  common.ChannelStatusManuallyDisabled,
		Key:     "sk-DISABLED-KEY",
		BaseURL: &disabledBaseURL,
		Models:  "gpt-5.5",
		Group:   "default",
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/public_stations", nil)
	GetPublicStations(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Success bool                `json:"success"`
		Data    []publicStationItem `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.True(t, body.Success)

	// Only the whitelisted tag is published; private channels stay hidden.
	require.Len(t, body.Data, 1)
	item := body.Data[0]
	assert.Equal(t, 151, item.ID)
	assert.Equal(t, "cpa-agentrouter-1", item.Name)
	assert.Equal(t, 60, item.Type)
	assert.Equal(t, common.ChannelStatusEnabled, item.Status)
	assert.Equal(t, "https://yiming.example.com", item.BaseURL, "trailing slash stripped")
	assert.Equal(t, []string{"claude-sonnet-5", "gpt-5.5"}, item.Models, "trailing comma tolerated")
	assert.Equal(t, int64(10), item.Priority)
	assert.Equal(t, 320, item.ResponseTime)

	// Raw-JSON level guard: no sensitive key or field name may appear anywhere.
	raw := recorder.Body.String()
	assert.NotContains(t, raw, "sk-UPSTREAM-SECRET-KEY")
	assert.NotContains(t, raw, "sk-PRIVATE-KEY")
	for _, forbidden := range []string{"\"key\"", "\"other\"", "\"other_info\"", "\"settings\"", "\"balance\""} {
		assert.NotContains(t, raw, forbidden, "projection must not expose %s", forbidden)
	}
}

func TestGetPublicStationsEmptyWhenTagMissing(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/public_stations", nil)
	GetPublicStations(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Success bool                `json:"success"`
		Data    []publicStationItem `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.True(t, body.Success)
	assert.Empty(t, body.Data, "no rows for the tag must yield an empty list, not an error")
}

// PUBLIC_STATIONS_MODE=all publishes every enabled channel with a public
// base_url regardless of tag; loopback sidecars stay filtered and disabled
// channels stay hidden.
func TestGetPublicStationsAllMode(t *testing.T) {
	previousDB := model.DB
	previousMode, hadMode := os.LookupEnv("PUBLIC_STATIONS_MODE")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	require.NoError(t, os.Setenv("PUBLIC_STATIONS_MODE", "all"))
	t.Cleanup(func() {
		model.DB = previousDB
		if hadMode {
			_ = os.Setenv("PUBLIC_STATIONS_MODE", previousMode)
		} else {
			_ = os.Unsetenv("PUBLIC_STATIONS_MODE")
		}
	})

	publicURL := "https://nailao.example.com"
	loopback := "http://127.0.0.1:8318"
	for _, ch := range []*model.Channel{
		{Id: 10, Name: "cpa-nailao", Type: 1, Status: common.ChannelStatusEnabled, Key: "sk-A", BaseURL: &publicURL, Models: "gpt-5.5", Group: "default"},
		{Id: 95, Name: "cpa-opencode.zen", Type: 1, Status: common.ChannelStatusEnabled, Key: "sk-B", BaseURL: &loopback, Models: "gpt-5.5", Group: "default"},
	} {
		ch := ch
		require.NoError(t, db.Create(ch).Error)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/public_stations", nil)
	GetPublicStations(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Success bool                `json:"success"`
		Data    []publicStationItem `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.True(t, body.Success)
	require.Len(t, body.Data, 1, "untagged public channel is published in all mode; loopback is not")
	assert.Equal(t, 10, body.Data[0].ID)
	assert.Equal(t, "https://nailao.example.com", body.Data[0].BaseURL)
	assert.NotContains(t, recorder.Body.String(), "127.0.0.1")
	assert.NotContains(t, recorder.Body.String(), "sk-A")
}
