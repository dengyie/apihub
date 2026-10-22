package loadbalancer

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memParamStripStore 是一个内存版的后端，用来在没有数据库的情况下验证
// 「学到 → 落库 → 重启（清内存）→ 读回 → 生效」这条链路。
type memParamStripStore struct {
	mu    sync.Mutex
	rules map[string]map[string]struct{}
	// loads 记录被读回了几次，用来验证 LoadParamStripLearned 真的走了后端。
	loads int
}

func newMemParamStripStore() *memParamStripStore {
	return &memParamStripStore{rules: map[string]map[string]struct{}{}}
}

// key 用与生产一致的编组键：strconv 数字 + \x00 + 模型名。
func (s *memParamStripStore) key(channelID int, model string) string {
	return strconv.Itoa(channelID) + "\x00" + model
}

func (s *memParamStripStore) LoadParamStripRules() ([]ParamStripRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	grouped := map[string][]string{}
	for key, params := range s.rules {
		grouped[key] = nil
		for p := range params {
			grouped[key] = append(grouped[key], p)
		}
	}
	out := make([]ParamStripRule, 0, len(grouped))
	for key, params := range grouped {
		channelID, model := splitKey(key)
		if channelID == 0 {
			continue
		}
		out = append(out, ParamStripRule{ChannelID: channelID, Model: model, Params: params})
	}
	return out, nil
}

// splitKey 反解编组键，与生产的 SplitN 一致：只拆第一个 \x00。
func splitKey(key string) (int, string) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return 0, ""
	}
	channelID, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, ""
	}
	return channelID, parts[1]
}

func (s *memParamStripStore) SaveParamStripRule(rule ParamStripRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.key(rule.ChannelID, rule.Model)
	set := s.rules[key]
	if set == nil {
		set = map[string]struct{}{}
		s.rules[key] = set
	}
	for _, p := range rule.Params {
		set[p] = struct{}{}
	}
	return nil
}

// withParamStripStore 注册一个内存后端，并在测试结束时还原。
func withParamStripStore(t *testing.T) *memParamStripStore {
	t.Helper()
	store := newMemParamStripStore()

	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = store
	paramStripStoreMu.Unlock()

	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})
	return store
}

// clearParamStripLearned 清空内存里的学习集合，等价于「新进程的初始状态」。
//
// 可以多次调用：每次都把当前值记下来，cleanup 时按相反顺序还原，最终回到
// 测试开始时的状态。
func clearParamStripLearned(t *testing.T) {
	t.Helper()
	paramStripMu.Lock()
	oldLearned := paramStripLearned
	oldPersisted := paramStripPersisted
	paramStripLearned = make(map[paramStripKey]map[string]struct{})
	paramStripPersisted = make(map[paramStripKey]map[string]struct{})
	paramStripMu.Unlock()
	t.Cleanup(func() {
		paramStripMu.Lock()
		paramStripLearned = oldLearned
		paramStripPersisted = oldPersisted
		paramStripMu.Unlock()
	})
}

// TestParamStripLearnedSurvivesRestart 是本次修复的核心断言。
//
// 背景：paramStripLearned 曾是纯内存 map。网关在生产里一天重启 8 次
// （蓝绿交接），每次重启都把学到的规则清零，于是同一个渠道同一个模型的
// 同一个参数在每个新进程里都要先错一次、再学一次。
//
// 这里模拟一次重启：学习 → 落库 → 清空内存 → 从库里读回 → 规则必须还在，
// 而且 GetStripParams 必须真的返回它（能被裁掉）。
func TestParamStripLearnedSurvivesRestart(t *testing.T) {
	installPolicy(t, testPolicy())
	store := withParamStripStore(t)
	clearParamStripLearned(t)

	// 第一次进程：学到一个参数。
	MarkParamUnsupported(84, "gpt-5.3-codex", "enable_thinking")
	require.Contains(t, GetStripParams(84, "gpt-5.3-codex"), "enable_thinking")

	// 落库确实发生了。
	rules, err := store.LoadParamStripRules()
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, 84, rules[0].ChannelID)
	assert.Equal(t, "gpt-5.3-codex", rules[0].Model)
	assert.Equal(t, []string{"enable_thinking"}, rules[0].Params)

	// 新进程：内存是空的。
	clearParamStripLearned(t)
	assert.Empty(t, GetStripParams(84, "gpt-5.3-codex"),
		"新进程开始时内存里当然没有规则")

	// 从库里读回。
	require.NoError(t, LoadParamStripLearned())
	assert.Contains(t, GetStripParams(84, "gpt-5.3-codex"), "enable_thinking",
		"重启后学到的规则必须还在，否则每个新进程都要先错一次再学一次")
}

// TestParamStripLearnedScopeStaysPerModel 钉住「重启后作用域不变」。
//
// 规则是按 (渠道, 模型) 存的：同一个渠道的另一个模型不该被牵连。
// 持久化会把这条语义带到磁盘上，读回时必须原样还原。
func TestParamStripLearnedScopeStaysPerModel(t *testing.T) {
	installPolicy(t, testPolicy())
	withParamStripStore(t)
	clearParamStripLearned(t)

	MarkParamUnsupported(238, "gemini-3.8-flash", "enable_thinking")

	clearParamStripLearned(t)
	require.NoError(t, LoadParamStripLearned())

	assert.Contains(t, GetStripParams(238, "gemini-3.8-flash"), "enable_thinking")
	assert.Empty(t, GetStripParams(238, "kimi-k3"),
		"另一个模型不该被同渠道的观测牵连")
	assert.Empty(t, GetStripParams(239, "gemini-3.8-flash"),
		"另一个渠道不该被牵连")
}

// TestMarkParamUnsupportedWritesOnce 钉住「同一个参数只落库一次」。
//
// 一个参数被反复遇到是最常见的情形：每次都写库会把写放大到每个请求一次。
func TestMarkParamUnsupportedWritesOnce(t *testing.T) {
	installPolicy(t, testPolicy())
	store := withParamStripStore(t)
	clearParamStripLearned(t)

	for i := 0; i < 5; i++ {
		MarkParamUnsupported(52, "gpt-6-luna", "enable_thinking")
	}

	saves := 0
	store.mu.Lock()
	for _, set := range store.rules {
		saves += len(set)
	}
	store.mu.Unlock()
	assert.Equal(t, 1, saves, "同一个参数只应落库一次")
}

// TestMarkParamUnsupportedWithoutStoreIsSafe 钉住「没有后端时学习照常工作」。
//
// 后端是可选的（单机测试、单进程部署可能不注册）。没有它时，行为必须退回到
// 改动前的样子：内存里学到，进程内生效，只是不跨重启。
func TestMarkParamUnsupportedWithoutStoreIsSafe(t *testing.T) {
	installPolicy(t, testPolicy())
	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = nil
	paramStripStoreMu.Unlock()
	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})
	clearParamStripLearned(t)

	MarkParamUnsupported(52, "gpt-6-luna", "enable_thinking")
	assert.Contains(t, GetStripParams(52, "gpt-6-luna"), "enable_thinking")

	// 模拟重启：内存清空，而没有后端可以把它读回来。
	clearParamStripLearned(t)
	require.NoError(t, LoadParamStripLearned())
	assert.Empty(t, GetStripParams(52, "gpt-6-luna"),
		"没有后端时规则不跨重启 —— 这正是改动前的行为")
}

// TestRegisterParamStripStoreIsOnce 钉住「后端只能注册一次」。
//
// 重复注册意味着有两个后端在写同一份数据，重启后哪一份生效取决于时序。
func TestRegisterParamStripStoreIsOnce(t *testing.T) {
	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = nil
	paramStripStoreMu.Unlock()
	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})

	assert.True(t, RegisterParamStripStore(newMemParamStripStore()))
	assert.False(t, RegisterParamStripStore(newMemParamStripStore()),
		"重复注册必须被拒绝")
}

// TestForgetParamStripLearnedForChannel 钉住「渠道删除时内存里的规则也清掉」。
//
// 渠道 id 会被复用：不清的话，复用同一个 id 的新渠道会立刻继承上一条渠道
// 学到的参数限制。
func TestForgetParamStripLearnedForChannel(t *testing.T) {
	installPolicy(t, testPolicy())
	withParamStripStore(t)
	clearParamStripLearned(t)

	MarkParamUnsupported(272, "bge-m3", "thinking")
	require.Contains(t, GetStripParams(272, "bge-m3"), "thinking")

	ForgetParamStripLearnedForChannel(272)
	assert.Empty(t, GetStripParams(272, "bge-m3"),
		"渠道删除后它的规则必须从内存里清掉")
}

// TestForgetParamStripLearnedRule 钉住「人工纠正只删指定那一条」。
//
// 误学纠正到 (渠道, 模型, 参数) 粒度，不能把同渠道同模型的其他规则误伤。
func TestForgetParamStripLearnedRule(t *testing.T) {
	installPolicy(t, testPolicy())
	withParamStripStore(t)
	clearParamStripLearned(t)

	MarkParamUnsupported(84, "gpt-5.3-codex", "enable_thinking")
	MarkParamUnsupported(84, "gpt-5.3-codex", "thinking")
	require.Contains(t, GetStripParams(84, "gpt-5.3-codex"), "thinking")

	ForgetParamStripLearnedRule(84, "gpt-5.3-codex", "thinking")

	got := GetStripParams(84, "gpt-5.3-codex")
	assert.NotContains(t, got, "thinking", "被撤销的那条规则必须消失")
	assert.Contains(t, got, "enable_thinking", "其他规则不能被误删")
}

// TestLoadParamStripLearnedToleratesStoreError 钉住「后端读失败不炸」。
//
// 落库是「让下次重启少错一次」的优化，不是请求正确性的前提。读失败时
// 退回「重新学」，也就是改动前的行为，比启动失败好。
func TestLoadParamStripLearnedToleratesStoreError(t *testing.T) {
	installPolicy(t, testPolicy())
	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = &failingParamStripStore{}
	paramStripStoreMu.Unlock()
	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})
	clearParamStripLearned(t)

	err := LoadParamStripLearned()
	assert.Error(t, err, "读失败必须把错误报出来，而不是静默吞掉")
}

type failingParamStripStore struct{}

func (failingParamStripStore) LoadParamStripRules() ([]ParamStripRule, error) {
	return nil, errors.New("boom")
}
func (failingParamStripStore) SaveParamStripRule(ParamStripRule) error { return nil }

// TestMarkParamUnsupportedRetriesAfterPersistFailure 钉住「一次落库失败不会让
// 规则永久失去持久化机会」。
//
// 旧实现拿内存里的存在与否当作「已经落库」的判据：一次失败的写之后，同一
// 参数再次遇到时 alreadyKnown 短路，永不再写库——这条规则在每次重启后都要
// 重新学。修复后「已落库」单独记账，失败的写下次遇到还会重试。
func TestMarkParamUnsupportedRetriesAfterPersistFailure(t *testing.T) {
	installPolicy(t, testPolicy())
	store := newFlakyParamStripStore()
	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = store
	paramStripStoreMu.Unlock()
	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})
	clearParamStripLearned(t)

	// 第一次学习：落库失败，规则只留在内存。
	MarkParamUnsupported(84, "gpt-5.3-codex", "enable_thinking")
	require.Contains(t, GetStripParams(84, "gpt-5.3-codex"), "enable_thinking")
	assert.Empty(t, store.saved(), "第一次落库失败，库里不该有这条规则")

	// 同一参数再次遇到：必须重试落库，而不是被内存里的存在短路。
	MarkParamUnsupported(84, "gpt-5.3-codex", "enable_thinking")
	saved := store.saved()
	require.Len(t, saved, 1, "失败的写必须在下次遇到同一参数时重试")
	assert.Equal(t, "enable_thinking", saved[0])
}

// TestPersistLearnedRuleWithoutStoreReportsError 钉住「后端未注册时不谎报成功」。
//
// 旧实现里 persistLearnedRule 在 store == nil 时返回 nil，调用方以为写进去了，
// 把规则标成已落库——等后端注册好后也不会补写。现在必须返回
// ErrParamStripStoreUnavailable，让 MarkParamUnsupported 保持「未落库」状态，
// 下次遇到再试。
func TestPersistLearnedRuleWithoutStoreReportsError(t *testing.T) {
	paramStripStoreMu.Lock()
	old := paramStripStore
	paramStripStore = nil
	paramStripStoreMu.Unlock()
	t.Cleanup(func() {
		paramStripStoreMu.Lock()
		paramStripStore = old
		paramStripStoreMu.Unlock()
	})

	err := persistLearnedRule(52, "gpt-6-luna", "enable_thinking")
	require.ErrorIs(t, err, ErrParamStripStoreUnavailable)
}

// flakyParamStripStore 第一次 Save 失败、之后成功，用来验证落库失败会重试。
type flakyParamStripStore struct {
	mu       sync.Mutex
	failOnce bool
	rules    map[string]map[string]struct{}
}

func newFlakyParamStripStore() *flakyParamStripStore {
	return &flakyParamStripStore{failOnce: true, rules: map[string]map[string]struct{}{}}
}

func (s *flakyParamStripStore) LoadParamStripRules() ([]ParamStripRule, error) {
	return nil, nil
}

func (s *flakyParamStripStore) SaveParamStripRule(rule ParamStripRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOnce {
		s.failOnce = false
		return errors.New("injected persist failure")
	}
	key := strconv.Itoa(rule.ChannelID) + "\x00" + rule.Model
	set := s.rules[key]
	if set == nil {
		set = map[string]struct{}{}
		s.rules[key] = set
	}
	for _, p := range rule.Params {
		set[p] = struct{}{}
	}
	return nil
}

func (s *flakyParamStripStore) saved() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, set := range s.rules {
		for p := range set {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
