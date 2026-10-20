package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
