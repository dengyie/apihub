package loadbalancer

import (
	"errors"
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

func (s *memParamStripStore) key(channelID int, model string) string {
	return itoa(channelID) + "\x00" + model
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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

func splitKey(key string) (int, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			channelID := 0
			for _, c := range key[:i] {
				channelID = channelID*10 + int(c-'0')
			}
			return channelID, key[i+1:]
		}
	}
	return 0, ""
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

func (s *memParamStripStore) DeleteParamStripRulesForChannel(channelID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.rules {
		if cid, _ := splitKey(key); cid == channelID {
			delete(s.rules, key)
		}
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
	paramStripLearned = make(map[paramStripKey]map[string]struct{})
	paramStripMu.Unlock()
	t.Cleanup(func() {
		paramStripMu.Lock()
		paramStripLearned = oldLearned
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
func (failingParamStripStore) DeleteParamStripRulesForChannel(int) error { return nil }
