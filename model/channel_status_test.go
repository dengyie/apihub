package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-status",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	changed := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.True(t, changed)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      "original-model",
		Group:       "default",
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)

	stale, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:           true,
		MultiKeySize:         2,
		MultiKeyMode:         constant.MultiKeyModePolling,
		MultiKeyPollingIndex: 1,
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       "concurrent-model",
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]any{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, stale.saveStatusState(DB))

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, "concurrent-model", stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}

// TestUpdateChannelStatusRollsBackWhenAbilitiesWriteFails 证明 channels 与
// abilities 写在同一个事务里。
//
// 这两张表是同一份事实的两张表。之前 abilities 的更新挂在 defer 上、失败只打
// 日志（common.SysLog），于是 channels.status 已经禁用、abilities.enabled 仍为
// true 的漂移可以永久留存：数据库选路读到那条陈旧的 enabled 能力行，就会继续
// 选中一个已经禁用的渠道。读路径的二次校验只能兜住这一次，写路径才是根因。
func TestUpdateChannelStatusRollsBackWhenAbilitiesWriteFails(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "transactional-status",
		Key:         "key",
		Status:      common.ChannelStatusEnabled,
		Models:      "tx-model",
		Group:       "default",
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "tx-model", ChannelId: channel.Id, Enabled: true}).Error)

	// 让 abilities 更新失败：只在下一条 ability 更新上报错。
	injected := errors.New("injected abilities write failure")
	var once sync.Once
	const name = "test:fail_ability_update"
	require.NoError(t, DB.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "abilities" {
			once.Do(func() { tx.AddError(injected) })
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove(name) })

	changed := UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "upstream auth rejected")
	assert.False(t, changed, "事务失败必须让整次状态变更失败")

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status,
		"abilities 写失败时 channels.status 必须回滚，否则两张表漂移")

	var ability Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
	assert.True(t, ability.Enabled, "回滚后能力行必须保持原状")
}
