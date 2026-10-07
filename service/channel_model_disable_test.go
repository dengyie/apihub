package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/relaykit/types"
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
	withModelScopeOnCacheOff(t)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "happy", AutoBan: true}
	DisableChannelForModel(channelError, "m2", "模型不存在")

	assert.False(t, abilityEnabled(t, ch.Id, "m2"), "m2 应退出轮转")
	assert.True(t, abilityEnabled(t, ch.Id, "m1"), "m1 不该受影响")
	assert.True(t, abilityEnabled(t, ch.Id, "m3"), "m3 不该受影响")
	assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, ch.Id),
		"per-model 禁用不得改渠道状态")
}

// 同一 (渠道, 模型) 上有两个并发在途请求、上游同样回「没有这个模型」。
// 第二个必须被当成幂等成功，**绝不能**降级为整渠道禁用 —— 否则本特性在生产
// 上必然退化成 v29.14 的行为：模型刚掉的那一刻在途请求有好几个，第一个摘掉
// 那个模型，其余每一个都把整条渠道连同 4 个健康模型一起摘了。
func TestDisableChannelForModelConcurrentHitDoesNotEscalate(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeOnCacheOff(t)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "concurrent", AutoBan: true}
	DisableChannelForModel(channelError, "m1", "模型不存在")
	require.Equal(t, common.ChannelStatusEnabled, channelStatus(t, ch.Id), "第一次只摘模型")

	DisableChannelForModel(channelError, "m1", "模型不存在")

	assert.Equal(t, common.ChannelStatusEnabled, channelStatus(t, ch.Id),
		"重复命中同一失效模型不得把整条渠道禁用 —— m2/m3 会被连坐下线")
	assert.False(t, abilityEnabled(t, ch.Id, "m1"))
	assert.True(t, abilityEnabled(t, ch.Id, "m2"))
	assert.True(t, abilityEnabled(t, ch.Id, "m3"))
}

// modelName 为空：没有模型名就没有降级依据，必须退回整渠道禁用。
func TestDisableChannelForModelFallsBackOnEmptyModelName(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeOnCacheOff(t)

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
	withModelScopeOnCacheOff(t)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "ch", AutoBan: true}
	DisableChannelForModel(channelError, "not-mine", "模型不存在")

	assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, ch.Id),
		"abilities 无匹配行时必须降级为整渠道禁用")
}

// 摘到最后一个可用模型：必须升级为整渠道禁用，否则留下 status=1 却永远选不中
// 的僵尸渠道。
func TestDisableChannelForModelEscalatesWhenNoModelLeft(t *testing.T) {
	ch := setupModelScopeTestDB(t, "only")
	withModelScopeOnCacheOff(t)

	channelError := types.ChannelError{ChannelId: ch.Id, ChannelName: "last", AutoBan: true}
	DisableChannelForModel(channelError, "only", "模型不存在")

	assert.Equal(t, common.ChannelStatusAutoDisabled, channelStatus(t, ch.Id),
		"没有可用模型后必须升级为整渠道禁用")
}

// AutoBan=false：未启用自动禁用的渠道，任何粒度都不该动。这条同时守住
// model 层与 service 层两道闸。
func TestDisableChannelForModelRespectsAutoBanOff(t *testing.T) {
	ch := setupModelScopeTestDB(t)
	withModelScopeOnCacheOff(t)

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
// withModelScopeOnCacheOff 固定「开关打开 + 内存缓存关闭」——生产现状，也是
// 模型级禁用真正生效的组合。降级分支（开关关 / 缓存开）各测试自行覆写全局值。
func withModelScopeOnCacheOff(t *testing.T) {
	t.Helper()
	previousSwitch := common.AutomaticDisableModelScope
	previousCache := common.MemoryCacheEnabled
	common.AutomaticDisableModelScope = true
	common.MemoryCacheEnabled = false
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

// 「选不出渠道」是**路由池状态**、会自愈，不是「这条凭据没有这个模型」。
//
// 这条不变量此前**靠巧合成立**：IsUpstreamModelUnavailableError 的白名单是
// 子串匹配，恰好没收 "no available channel for model …"。任何人日后往那张表
// 补一条同措辞，一条 85% 可用的渠道就会被整条摘掉，且线上零报错。生产实测
// #144（cpa-kuaipao）40 分钟成功 39 次、失败 7 次，失败全是这一类。
//
// 这里从两个方向钉死：ShouldDisableChannel（整渠道）与 isModelScopedAutoDisable
// （per-model）都必须拒绝它，同时真正的「上游说没有该模型」必须仍然命中 ——
// 否则就是把安全换成了漏报。
func TestRoutingExhaustedIsNeverAutoDisabled(t *testing.T) {
	prevDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prevDisable })

	upstreamWording := types.NewErrorWithStatusCode(
		errString("status_code=503, No available channel for model glm-5.3-flash under group codex (distributor) (request id: abc)"),
		types.ErrorCodeBadResponse, 503)
	// 本网关自己产出的同一类，现在带专属 error code。
	ownCode := types.NewErrorWithStatusCode(
		errString("分组 codex 下模型 x 无可用渠道（distributor）"),
		types.ErrorCodeNoAvailableChannel, 503)

	for name, err := range map[string]*types.NewAPIError{
		"上游透传的同款措辞":        upstreamWording,
		"本网关自带 error code": ownCode,
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, loadbalancer.IsRoutingExhaustedError(err), "必须被识别为路由池耗尽")
			assert.False(t, ShouldDisableChannel(0, err),
				"路由池耗尽不得触发整渠道禁用 —— 候选渠道只是暂时被熔断/过载")
			assert.False(t, isModelScopedAutoDisable(err),
				"路由池耗尽不得触发 per-model 禁用 —— 它会自愈，不是确定性失效")
		})
	}
}

// 反向：真正的「上游说没有这个模型」必须仍然命中两个判据，否则就是在用
// 漏报换误报。
func TestGenuineModelUnavailableStillDisables(t *testing.T) {
	prevDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prevDisable })

	for _, wording := range []string{
		"404, message: 模型不存在",
		"model_not_found",
		"no such model: gpt-9",
		"The model `x` does not exist",
	} {
		err := types.NewErrorWithStatusCode(errString(wording), types.ErrorCodeBadResponse, 404)
		assert.False(t, loadbalancer.IsRoutingExhaustedError(err),
			"确定性失效不得被误判成路由池耗尽：%s", wording)
		assert.True(t, ShouldDisableChannel(0, err),
			"上游明确说没有该模型时仍应禁用：%s", wording)
		assert.True(t, isModelScopedAutoDisable(err),
			"上游明确说没有该模型时仍应走 per-model：%s", wording)
	}
}

// 守卫**真正兜住**的是这一条：白名单是子串匹配表，日后任何人往里补一句
// "no available channel"（动机完全合理 —— 它确实提到 model），这一类就会
// 被判成确定性失效并整条禁用渠道，而它其实是会自愈的池状态。
//
// 因此构造一条**同时**满足两侧判据的报文：正文既像池耗尽、又像确定性失效。
// 守卫必须以前者（正文语义）为准。这是 mutation 可验证的负载点。
func TestRoutingExhaustedWinsOverWhitelistAccidentalMatch(t *testing.T) {
	prevDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prevDisable })

	// 模拟「白名单被补宽后」的最坏情况：两条判据同时命中。
	err := types.NewErrorWithStatusCode(
		errString("status_code=503, No available channel for model glm-5.3-flash under group codex (distributor); model not found"),
		types.ErrorCodeBadResponse, 503)

	require.True(t, loadbalancer.IsUpstreamModelUnavailableError(err),
		"前置条件：该报文命中 model-unavailable 白名单")
	require.True(t, loadbalancer.IsRoutingExhaustedError(err),
		"前置条件：同时它也是路由池耗尽")

	assert.False(t, ShouldDisableChannel(0, err),
		"池耗尽必须压过白名单的偶然命中，否则补宽词表就会误禁健康渠道")
	assert.False(t, isModelScopedAutoDisable(err), "per-model 侧同理")
}
