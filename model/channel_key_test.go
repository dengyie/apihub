package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizeChannelKeyStripsControlChars 覆盖线上事故 #219 的形态：key 尾部
// 残留换行会让 Go 的 net/http 直接拒绝请求头（invalid header field value），
// 且 100% 必现，不是概率问题。
func TestSanitizeChannelKeyStripsControlChars(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"trailing lf", "sk-abc\n", "sk-abc"},
		{"trailing crlf", "sk-abc\r\n", "sk-abc"},
		{"trailing cr", "sk-abc\r", "sk-abc"},
		{"surrounding spaces", "  sk-abc  ", "sk-abc"},
		{"surrounding whitespace plus crlf", " \t sk-abc\r\n ", "sk-abc"},
		{"embedded lf", "sk\nabc", "skabc"},
		{"clean key untouched", "sk-abc", "sk-abc"},
		{"empty stays empty", "", ""},
		{"only control chars", "\r\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SanitizeChannelKey(tc.in))
		})
	}
}

// TestParseChannelKeyListPreservesNewlineSeparatedKeys 是「以后自动删除换行符，
// 但不要影响正常的多 key 场景」那条要求的正面守卫。
//
// 清洗规则如果哪天退化成「把整个 key 字段里的换行全删掉」，多 key 会被静默
// 拼成一条超长 key：所有子 key 同时失效，且没有任何报错。这里用换行分隔的
// 多 key 断言拆分语义仍然成立——每个 key 都要拿得到、且顺序不变。
func TestParseChannelKeyListPreservesNewlineSeparatedKeys(t *testing.T) {
	keys := parseChannelKeyList("sk-first\nsk-second\nsk-third")
	require.Len(t, keys, 3)
	assert.Equal(t, []string{"sk-first", "sk-second", "sk-third"}, keys)
}

// 换行分隔的多 key 在逐个清洗后必须仍然是全部 key，CRLF 录入与空行都不能吞 key。
func TestParseChannelKeyListCleansEachLineIndependently(t *testing.T) {
	t.Run("crlf line endings", func(t *testing.T) {
		keys := parseChannelKeyList("sk-first\r\nsk-second\r\n")
		assert.Equal(t, []string{"sk-first", "sk-second"}, keys)
	})

	t.Run("blank lines dropped not merged", func(t *testing.T) {
		keys := parseChannelKeyList("sk-first\n\n  \nsk-second\n")
		assert.Equal(t, []string{"sk-first", "sk-second"}, keys)
	})

	t.Run("single key with trailing newline stays one key", func(t *testing.T) {
		keys := parseChannelKeyList("sk-only\n")
		assert.Equal(t, []string{"sk-only"}, keys)
	})
}

// JSON 数组形态（Vertex AI 场景）逐个清洗，数组本身不能被换行规则误伤。
func TestParseChannelKeyListJSONArray(t *testing.T) {
	t.Run("per entry cleaned", func(t *testing.T) {
		keys := parseChannelKeyList(`["sk-first", " sk-second\n", "sk-third"]`)
		assert.Equal(t, []string{"sk-first", "sk-second", "sk-third"}, keys)
	})

	t.Run("blank entries dropped", func(t *testing.T) {
		keys := parseChannelKeyList(`["sk-first", "  ", "sk-second"]`)
		assert.Equal(t, []string{"sk-first", "sk-second"}, keys)
	})
}

// 单 key 渠道：GetNextEnabledKey 过去直接返回 channel.Key，尾部换行会原样进
// Authorization 头，该渠道 100% 必然失败。这里锁住修复后的行为。
func TestGetNextEnabledKeySingleKeySanitizes(t *testing.T) {
	channel := &Channel{Id: 900001, Key: "sk-single\n", ChannelInfo: ChannelInfo{IsMultiKey: false}}
	key, idx, err := channel.GetNextEnabledKey()
	// 注意：err 是 *types.NewAPIError。用 require.NoError 会踩 typed-nil 陷阱
	// ——nil 指针装进 error 接口后接口本身非空，断言必然失败（报错信息为空）。
	// 这里用 require.Nil，它按反射判断，能正确识别 typed nil。
	require.Nil(t, err)
	assert.Equal(t, 0, idx)
	assert.Equal(t, "sk-single", key)
}

// 单 key 渠道的库里却存了多条换行分隔的 key：清洗会把它们拼成一条超长 key，
// 上游回 401，而 401 在自动禁用状态码里 → 该渠道被自动禁用。这里锁住
// 「不静默吞掉、拼出来的结果仍然返回」这一侧，诊断日志由 GetNextEnabledKey 打出。
func TestGetNextEnabledKeySingleKeyWithEmbeddedNewlinesStillReturnsOneKey(t *testing.T) {
	channel := &Channel{Id: 900002, Key: "sk-a\nsk-b\n", ChannelInfo: ChannelInfo{IsMultiKey: false}}
	key, idx, err := channel.GetNextEnabledKey()
	require.Nil(t, err)
	assert.Equal(t, 0, idx)
	// 清洗 = 删掉换行而不是按行拆开，因此这里得到一条拼接 key（会被上游拒绝，
	// 这正是需要在日志里提示运维去核对渠道配置的原因）。
	assert.Equal(t, "sk-ask-b", key)
}

// 多 key 渠道：清洗后每次拿到的都必须是一条**完整且独立**的 key。
//
// 这条断言针对的回归是：清洗规则哪天退化成「把整个 key 字段的换行全删掉」，
// 多 key 会被静默拼成一条超长 key，所有子 key 同时失效且不报任何错。
//
// 轮询模式（MultiKeyModePolling）要走 CacheGetChannelInfo/SaveChannelInfo 访问
// 数据库，不是纯函数，单测里不覆盖；这里只锁「逐条取到真 key」这层语义。
func TestGetNextEnabledKeyMultiKeyYieldsWholeKeys(t *testing.T) {
	channel := &Channel{
		Id:          900002,
		Key:         "sk-first\nsk-second\n",
		ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}

	keys := channel.GetKeys()
	require.Equal(t, []string{"sk-first", "sk-second"}, keys)

	for i := 0; i < 4; i++ {
		key, _, err := channel.GetNextEnabledKey()
		require.Nil(t, err)
		require.Contains(t, []string{"sk-first", "sk-second"}, key)
		require.NotContains(t, key, "\n", "返回的 key 不应含换行，否则会被拼成一条")
	}
}

// GetKeys 与 Update 重算 MultiKeySize 共用同一份解析。清洗规则一旦在两处
// 分叉，MultiKeySize 会与真正可选的 key 数不一致，状态位数组直接错位。
func TestGetKeysMatchesParseChannelKeyList(t *testing.T) {
	keyStr := "sk-first\r\n  sk-second  \n\nsk-third\n"
	channel := &Channel{Id: 900003, Key: keyStr}
	assert.Equal(t, parseChannelKeyList(keyStr), channel.GetKeys())

	channel.Keys = []string{"cached-one", "cached-two"}
	assert.Equal(t, []string{"cached-one", "cached-two"}, channel.GetKeys(),
		"预加载的 Keys 缓存应优先于重新解析")
}
