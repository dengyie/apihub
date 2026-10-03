package i18n

import (
	"testing"

	i18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withNilBundle 临时把 bundle 置空，返回还原函数。
//
// 必须直接改包级变量而不是 Init()：Init 用 sync.Once，本进程只能成功一次，
// 没法用来构造「Init 从未调用」这个状态 —— 而那恰恰是要测的状态。
func withNilBundle(t *testing.T) func() {
	t.Helper()
	mu.Lock()
	previousBundle := bundle
	previousLocalizers := localizers
	bundle = nil
	localizers = make(map[string]*i18n.Localizer)
	mu.Unlock()
	return func() {
		mu.Lock()
		bundle = previousBundle
		localizers = previousLocalizers
		mu.Unlock()
	}
}

// TestTranslateWithoutInitializedBundle 钉住「翻译不可用时降级到原始 key」。
//
// 这条不是洁癖：main.go 对 i18n.Init() 失败的处理是记一条日志后**继续启动**
// （注释写着「i18n is not critical」）。要让那句话成立，翻译层就必须能优雅
// 降级 —— 而事实是 go-i18n 的 Localizer.Localize 会无判空地解引用
// l.bundle.matcher，于是每一次 i18n.T 都是一次空指针 panic。
//
// 后果已经不只是测试：relay.selectResponsesWSChannel 在「选不出渠道」时直接
// 调 i18n.T 拼错误消息。i18n 一旦没初始化，这条错误路径就从「返回一条英文
// key」退化成「panic，被 recover 成 500」—— 一个本该可诊断的路由错误
// 变成了不可诊断的内部错误。
func TestTranslateWithoutInitializedBundle(t *testing.T) {
	defer withNilBundle(t)()

	assert.Equal(t, "some.key", Translate("zh-CN", "some.key"),
		"bundle 未初始化时必须降级到原始 key，而不是 panic")
	assert.Equal(t, "some.key", Translate("en", "some.key"),
		"未支持的语言同样走降级分支")
	assert.Equal(t, "some.key", Translate("", "some.key"),
		"空语言同样走降级分支")
}

// TestGetLocalizerReturnsNilWithoutBundle 单独钉住 GetLocalizer：它必须是
// nil 而不是「一个 bundle 为 nil 的 Localizer」。后者是个定时炸弹 ——
// 造它的时候不报错，用它的第一行 Localize 才炸，且栈里完全看不出
// 根因是这个 Localizer 从一开始就是坏的。
func TestGetLocalizerReturnsNilWithoutBundle(t *testing.T) {
	defer withNilBundle(t)()

	assert.Nil(t, GetLocalizer("zh-CN"),
		"bundle 为 nil 时必须返回 nil，不能返回一个坏掉的 Localizer")
	assert.Nil(t, GetLocalizer("en"))
}

// TestTranslateDegradesWhenBundleInitializedButKeyMissing 确认另一条降级
// 路径没被这次改动破坏：bundle 正常、只是这个 key 不存在时，仍然返回 key。
func TestTranslateDegradesWhenBundleInitializedButKeyMissing(t *testing.T) {
	require.NoError(t, Init(), "i18n 应当能正常初始化")
	got := Translate(LangEn, "definitely.not.a.real.key")
	assert.Equal(t, "definitely.not.a.real.key", got,
		"翻译缺失时降级到 key")
}

// TestInitIsIdempotent 确认 sync.Once 的语义没被破坏：第二次 Init 不该
// 把已经建好的 localizers 冲掉（否则热路径上会周期性丢翻译）。
func TestInitIsIdempotent(t *testing.T) {
	require.NoError(t, Init())
	before := GetLocalizer(LangEn)
	require.NotNil(t, before)
	require.NoError(t, Init())
	assert.Same(t, before, GetLocalizer(LangEn),
		"重复 Init 不应重建 localizer")
}
