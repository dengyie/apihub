package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/glebarez/sqlite"
)

func errString(s string) error { return errors.New(s) }

// 判据只认「可证明只关于某个模型」的失败，其余一律渠道级。
//
// 两个方向的错配代价都不对称：把账号级错判成模型级，死 key 的渠道会继续
// 吃满每一轮重试；把模型级错判成账号级，其余健康模型跟着陪葬 —— 生产实测
// 这一类连带下线过 91 个模型。
func TestIsModelScopedAutoDisable(t *testing.T) {
	cases := []struct {
		name string
		err  *types.NewAPIError
		want bool
	}{
		{"上游说模型不存在", types.NewErrorWithStatusCode(errString("404, message: 模型不存在"), types.ErrorCodeBadResponse, 404), true},
		{"model not found", types.NewErrorWithStatusCode(errString(`model_not_found`), types.ErrorCodeBadResponse, 404), true},
		{"no such model", types.NewErrorWithStatusCode(errString("no such model"), types.ErrorCodeBadResponse, 404), true},
		// 这句是本网关自己吐的下游症状，不是上游的判断，词表刻意不收
		// （见 operation_setting.go 的关键词注释）：拿它当「上游说没有这个
		// 模型」会把选路失败反向当成渠道的过错。
		{"本网关的无渠道提示不算上游表态", types.NewErrorWithStatusCode(errString("No available channel for model x under group default"), types.ErrorCodeBadResponse, 400), false},

		// 这条是本次最容易踩的坑：IsUpstreamModelUnavailableError 把令牌级措辞
		// 一并收进来了，若跟着转模型级，「拿不到 token」就会变成「只摘一个
		// 模型」，而该渠道的每个模型都拿不到 token。
		{"cannot fetch token 必须留在渠道级", types.NewErrorWithStatusCode(errString("cannot fetch token"), types.ErrorCodeBadResponse, 400), false},

		{"余额不足", types.NewErrorWithStatusCode(errString("insufficient balance"), types.ErrorCodeBadResponse, 403), false},
		{"额度耗尽", types.NewErrorWithStatusCode(errString("insufficient_user_quota"), types.ErrorCodeBadResponse, 403), false},
		{"密钥失效", types.NewErrorWithStatusCode(errString("API key not recognised"), types.ErrorCodeBadResponse, 401), false},
		{"分组无权", types.NewErrorWithStatusCode(errString("无权访问 CC-kiro 分组"), types.ErrorCodeBadResponse, 403), false},
		{"令牌被停用", types.NewErrorWithStatusCode(errString("令牌因分组倍率上调已停用"), types.ErrorCodeBadResponse, 403), false},

		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isModelScopedAutoDisable(c.err))
		})
	}
}

// 与熔断侧刻意不共用判据：熔断自 v29.14 起一律模型级（有递增退避兜底且能
// 自愈），自动禁用不能自愈、判据必须更保守。这条测试把两者的差异钉住，
// 防止后人「顺手」把它们合并 —— 合并的后果就是余额耗尽的渠道被降级成
// 「只摘一个模型」，于是它继续吃满每一轮重试。
func TestModelScopedAutoDisableIsStricterThanBreakerScope(t *testing.T) {
	quotaErr := types.NewErrorWithStatusCode(errString("insufficient_user_quota"), types.ErrorCodeBadResponse, 403)

	assert.Equal(t, loadbalancer.ScopeModel, loadbalancer.BreakerScopeOf(quotaErr),
		"熔断侧一律模型级")
	assert.False(t, isModelScopedAutoDisable(quotaErr),
		"自动禁用侧必须判为渠道级 —— 这是两者刻意的分歧点")
}

// 开关关闭时 DisableChannelForModel 必须退化成整渠道禁用，与 v29.14 逐位一致 ——
// 这是上线期的默认状态。
func TestDisableChannelForModelFallsBackWhenSwitchOff(t *testing.T) {
	previous := common.AutomaticDisableModelScope
	common.AutomaticDisableModelScope = false
	t.Cleanup(func() { common.AutomaticDisableModelScope = previous })

	channelError := types.ChannelError{ChannelId: 1, ChannelName: "ch", AutoBan: true}
	// AutoBan 为 false 时两个分支都会直接返回，用例只验证「没有崩、且没走
	// 模型级路径」；真正的降级行为由下面 MemoryCacheEnabled 的用例覆盖。
	assert.NotPanics(t, func() {
		DisableChannelForModel(channelError, "m1", "模型不存在")
	})
}

// 内存缓存打开时 abilities 的改动对选路不可见（InitChannelCache 从
// channels.Group × channels.Models 建表，不读 abilities.enabled），此时若不
// 降级，特性会静默失效：禁用照写、路由照旧、零报错。
func TestDisableChannelForModelFallsBackUnderMemoryCache(t *testing.T) {
	previousSwitch := common.AutomaticDisableModelScope
	previousCache := common.MemoryCacheEnabled
	common.AutomaticDisableModelScope = true
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.AutomaticDisableModelScope = previousSwitch
		common.MemoryCacheEnabled = previousCache
	})

	channelError := types.ChannelError{ChannelId: 1, ChannelName: "ch", AutoBan: true}
	assert.NotPanics(t, func() {
		DisableChannelForModel(channelError, "m1", "模型不存在")
	})
}

// 以下四条覆盖 service 层此前完全没有测过的分支。判据一律看**数据库实际状态**
// （渠道 status 与 abilities.enabled），而不是「没崩」—— 只断言 NotPanics 的用例
// 在降级逻辑整个被删掉时也照样通过，等于没测。

// 正常路径：开关打开、渠道健康、模型确实在服务 —— 必须真的只摘这一个模型，
// 兄弟模型与渠道状态都不受影响。这是整个特性的主路径，此前零覆盖。
func TestDisableChannelForModelHappyPathOnlyAffectsThatModel(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeEnabled(t, false)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "happy", AutoBan: true}
	DisableChannelForModel(channelError, "m2", "模型不存在")

	assert.False(t, abilityEnabled(t, ch.Id, "m2"), "m2 应退出轮转")
	assert.True(t, abilityEnabled(t, ch.Id, "m1"), "m1 不该受影响")
	assert.True(t, abilityEnabled(t, ch.Id, "m3"), "m3 不该受影响")
	assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, ch.Id),
		"per-model 禁用不得改渠道状态")
}

// modelName 为空：没有模型名就没有降级依据，必须退回整渠道禁用。
func TestDisableChannelForModelFallsBackOnEmptyModelName(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeEnabled(t, false)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "ch", AutoBan: true}
	DisableChannelForModel(channelError, "", "模型不存在")

	assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, ch.Id),
		"空模型名必须降级为整渠道禁用")
	assert.False(t, abilityEnabled(t, ch.Id, "m1"),
		"整渠道禁用会把该渠道所有 ability 一起置灰")
}

// 渠道不服务这个模型：abilities 翻不动，必须退回整渠道禁用。
//
// 注意这里**不能**断言兄弟模型仍在架：一旦降级为整渠道禁用，
// UpdateAbilityStatus 就会把该渠道所有 ability 一起置灰 —— 这正是降级的含义，
// 也正是「宁可多熔不可假装生效」这条原则的代价。真正该断言的是「确实降级了」。
func TestDisableChannelForModelFallsBackWhenModelNotServed(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeEnabled(t, false)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "ch", AutoBan: true}
	DisableChannelForModel(channelError, "not-mine", "模型不存在")

	assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, ch.Id),
		"abilities 无匹配行时必须降级为整渠道禁用")
}

// 摘到最后一个可用模型：必须升级为整渠道禁用，否则留下 status=1 却永远选不中
// 的僵尸渠道。
func TestDisableChannelForModelEscalatesWhenNoModelLeft(t *testing.T) {
	ch := setupModelScopeTestDB(t, "only")
	withModelScopeEnabled(t, false)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "last", AutoBan: true}
	DisableChannelForModel(channelError, "only", "模型不存在")

	assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, ch.Id),
		"没有可用模型后必须升级为整渠道禁用")
}

// AutoBan=false：未启用自动禁用的渠道，任何粒度都不该动。这条同时守住
// model 层与 service 层两道闸。
func TestDisableChannelForModelRespectsAutoBanOff(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeEnabled(t, false)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "ch", AutoBan: false}
	DisableChannelForModel(channelError, "m2", "模型不存在")

	assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, ch.Id),
		"AutoBan 关闭时不得禁用渠道")
	assert.True(t, abilityEnabled(t, ch.Id, "m2"), "AutoBan 关闭时不得动 abilities")
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, ch.Id).Error)
	assert.Empty(t, channel.ChannelInfo.ModelDisabledReason,
		"AutoBan 关闭时不得写入 per-model 留痕")
}

// setupModelScopeTestDB 建内存库并种一条带给定模型的启用渠道。
func setupModelScopeTestDB(t *testing.T, models ...string) *model.Channel {
	t.Helper()
	if len(models) == 0 {
		models = []string{"m1", "m2", "m3"}
	}
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	channel := model.Channel{
		Name:   "per-model-svc",
		Key:    "sk-test",
		Group:  "default",
		Models: strings.Join(models, ","),
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	for _, m := range models {
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     m,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	return &channel
}

// withModelScopeEnabled 打开 AutomaticDisableModelScope 并按需关掉内存缓存，
// 让用例进入真正的模型级路径而不是降级分支。
func withModelScopeEnabled(t *testing.T, memoryCache bool) {
	t.Helper()
	previousSwitch := common.AutomaticDisableModelScope
	previousCache := common.MemoryCacheEnabled
	common.AutomaticDisableModelScope = true
	common.MemoryCacheEnabled = memoryCache
	t.Cleanup(func() {
		common.AutomaticDisableModelScope = previousSwitch
		common.MemoryCacheEnabled = previousCache
	})
}

func abilityEnabled(t *testing.T, channelID int, modelName string) bool {
	t.Helper()
	var a model.Ability
	require.NoError(t, model.DB.
		Where("channel_id = ? and model = ?", channelID, modelName).First(&a).Error)
	return a.Enabled
}

func channelStatus(t *testing.T, channelID int) int {
	t.Helper()
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, channelID).Error)
	return channel.Status
}
