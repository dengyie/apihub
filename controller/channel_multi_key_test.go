package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiKeyEnableRestoresOnlyExhaustedChannels(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
	})
	t.Setenv("SQL_DSN", os.Getenv("TEST_CHANNEL_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "channel.db")
	require.NoError(t, model.InitDB())
	database := model.DB
	sqlDB, err := database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = database
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Log{}, &model.AuditLog{}))
	root := &model.User{Username: "multi-key-review-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: "multi-key-review-root-aff"}
	require.NoError(t, database.Create(root).Error)
	t.Cleanup(func() { require.NoError(t, database.Unscoped().Delete(root).Error) })
	versionQuery := "SELECT VERSION()"
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, database.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database=%s version=%s", common.MainDatabaseType(), version)

	for _, cacheEnabled := range []bool{false, true} {
		for _, action := range []string{"enable_key", "enable_all_keys"} {
			for _, tc := range []struct {
				name           string
				initialStatus  int
				manualOverride string
				wantStatus     int
			}{
				{name: "key exhaustion restores", initialStatus: common.ChannelStatusEnabled, wantStatus: common.ChannelStatusEnabled},
				{name: "manual disable is preserved", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "manual disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "status", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "tag disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "tag", wantStatus: common.ChannelStatusManuallyDisabled},
			} {
				t.Run(fmt.Sprintf("cache=%t/%s/%s", cacheEnabled, action, tc.name), func(t *testing.T) {
					common.MemoryCacheEnabled = cacheEnabled
					tag := t.Name()
					channel := &model.Channel{Name: t.Name(), Type: 1, Key: "key-one\nkey-two", Status: tc.initialStatus, Models: "test-model", Group: "default", Tag: &tag,
						ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
					}
					require.NoError(t, channel.Insert())
					t.Cleanup(func() {
						require.NoError(t, channel.Delete())
						model.InitChannelCache()
					})
					for _, operation := range []string{"disable_key", action} {
						if operation == action {
							if tc.manualOverride == "status" {
								model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual operation")
							} else if tc.manualOverride == "tag" {
								require.NoError(t, model.DisableChannelByTag(tag))
							}
						}
						payload, err := common.Marshal(MultiKeyManageRequest{ChannelId: channel.Id, Action: operation, KeyIndex: common.GetPointer(0)})
						require.NoError(t, err)
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Set("id", root.Id)
						c.Set("role", common.RoleRootUser)
						c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key", bytes.NewReader(payload))
						c.Request.Header.Set("Content-Type", "application/json")
						ManageMultiKeys(c)
						var result struct {
							Success bool `json:"success"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
						require.True(t, result.Success, recorder.Body.String())
					}
						loaded, err := model.GetChannelById(channel.Id, true)
						require.NoError(t, err)
						assert.Equal(t, tc.wantStatus, loaded.Status)
						assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, 0)
						assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledReason, 0)
						assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledTime, 0)
						var ability model.Ability
						require.NoError(t, database.Where("channel_id = ?", channel.Id).First(&ability).Error)
						assert.Equal(t, tc.wantStatus == common.ChannelStatusEnabled, ability.Enabled)
					})
				}
			}
		}

		t.Run("update_key_proxy requires sensitive write and records audit", func(t *testing.T) {
		channel := &model.Channel{
			Name: "test-proxy-authz", Type: 1, Key: "key-0\nkey-1", Status: common.ChannelStatusEnabled, Models: "test-model", Group: "default",
			ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
		}
		require.NoError(t, channel.Insert())
		t.Cleanup(func() {
			_ = channel.Delete()
			model.InitChannelCache()
		})

		// Common user without ChannelSensitiveWrite should be rejected
		nonAdmin := &model.User{Username: "non-admin-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "non-admin-user-aff"}
		require.NoError(t, database.Create(nonAdmin).Error)
		t.Cleanup(func() { _ = database.Unscoped().Delete(nonAdmin).Error })

		proxyURL := "socks5://u:p@127.0.0.1:2080"
		payload, err := common.Marshal(MultiKeyManageRequest{
			ChannelId: channel.Id,
			Action:    "update_key_proxy",
			KeyIndex:  common.GetPointer(0),
			Proxy:     &proxyURL,
		})
		require.NoError(t, err)

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", nonAdmin.Id)
		c.Set("role", nonAdmin.Role)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		ManageMultiKeys(c)

		var deniedResult struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &deniedResult))
		assert.False(t, deniedResult.Success, "non-admin user must be rejected without ChannelSensitiveWrite")

		// Root user should succeed
		recorderRoot := httptest.NewRecorder()
		cRoot, _ := gin.CreateTestContext(recorderRoot)
		cRoot.Set("id", root.Id)
		cRoot.Set("role", root.Role)
		cRoot.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(payload))
		cRoot.Request.Header.Set("Content-Type", "application/json")
		ManageMultiKeys(cRoot)

		var successResult struct {
			Success bool `json:"success"`
		}
		require.NoError(t, common.Unmarshal(recorderRoot.Body.Bytes(), &successResult))
		assert.True(t, successResult.Success, recorderRoot.Body.String())

		// Verify proxy was saved
		reloaded, err := model.GetChannelById(channel.Id, true)
		require.NoError(t, err)
		assert.Equal(t, proxyURL, reloaded.ChannelInfo.MultiKeyProxyList[0])

		// Verify audit log recorded redacted proxy
		var auditLogs []model.AuditLog
		require.NoError(t, database.Where("action = ? AND user_id = ?", "channel.multi_key_manage", root.Id).Find(&auditLogs).Error)
		require.NotEmpty(t, auditLogs)
		lastLog := auditLogs[len(auditLogs)-1]
		require.NotNil(t, lastLog.Other.Op)
		rawParams, err := common.Marshal(lastLog.Other.Op.Params)
		require.NoError(t, err)
		assert.Contains(t, string(rawParams), `"action":"update_key_proxy"`)
		assert.Contains(t, string(rawParams), `"proxy":"socks5://u:xxxxx@127.0.0.1:2080"`)
		assert.NotContains(t, string(rawParams), "socks5://u:p@127.0.0.1:2080")
	})
}
