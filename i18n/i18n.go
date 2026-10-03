package i18n

import (
	"embed"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	LangZhCN    = "zh-CN"
	LangZhTW    = "zh-TW"
	LangEn      = "en"
	DefaultLang = LangEn // Fallback to English if language not supported
)

//go:embed locales/*.yaml
var localeFS embed.FS

var (
	bundle     *i18n.Bundle
	localizers = make(map[string]*i18n.Localizer)
	mu         sync.RWMutex
	initOnce   sync.Once
)

// Init initializes the i18n bundle and loads all translation files
func Init() error {
	var initErr error
	initOnce.Do(func() {
		bundle = i18n.NewBundle(language.Chinese)
		bundle.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)

		// Load embedded translation files
		files := []string{"locales/zh-CN.yaml", "locales/zh-TW.yaml", "locales/en.yaml"}
		for _, file := range files {
			_, err := bundle.LoadMessageFileFS(localeFS, file)
			if err != nil {
				initErr = err
				return
			}
		}

		// Pre-create localizers for supported languages
		localizers[LangZhCN] = i18n.NewLocalizer(bundle, LangZhCN)
		localizers[LangZhTW] = i18n.NewLocalizer(bundle, LangZhTW)
		localizers[LangEn] = i18n.NewLocalizer(bundle, LangEn)

		// Set the TranslateMessage function in common package
		common.TranslateMessage = T
	})
	return initErr
}

// GetLocalizer returns a localizer for the specified language
func GetLocalizer(lang string) *i18n.Localizer {
	// bundle 为 nil 说明 Init 失败或尚未调用。此时**必须**返回 nil，而不是
	// 造一个 Localizer{bundle: nil}：
	//
	// go-i18n 的 Localizer.Localize 会无判空地解引用 l.bundle.matcher，
	// 于是一次「翻译文件加载失败」会变成之后每一次翻译调用上的空指针 panic，
	// 而且 Translate 里那个 `err != nil` 的降级分支根本走不到 —— panic
	// 发生在返回 err 之前。
	//
	// 这不是假设：main.go 对 Init 失败的处理是记一条日志后继续启动，
	// 注释写着「i18n is not critical」。那句话要成立，调用方就必须能优雅
	// 降级，而 relay.selectResponsesWSChannel 等处是直接调 i18n.T 的。
	mu.RLock()
	b := bundle
	mu.RUnlock()
	if b == nil {
		return nil
	}

	lang = normalizeLang(lang)

	mu.RLock()
	loc, ok := localizers[lang]
	mu.RUnlock()

	if ok {
		return loc
	}

	// Create new localizer for unknown language (fallback to default)
	mu.Lock()
	defer mu.Unlock()

	// Double-check after acquiring write lock
	if loc, ok = localizers[lang]; ok {
		return loc
	}

	loc = i18n.NewLocalizer(b, lang, DefaultLang)
	localizers[lang] = loc
	return loc
}

// T translates a message key using the language from gin context
func T(c *gin.Context, key string, args ...map[string]any) string {
	lang := GetLangFromContext(c)
	return Translate(lang, key, args...)
}

// Translate translates a message key for the specified language
func Translate(lang, key string, args ...map[string]any) string {
	loc := GetLocalizer(lang)
	if loc == nil {
		// bundle 未初始化：降级到原始 key，和下面「翻译不存在」的降级一致。
		// 一条英文 key 远好过一次空指针 panic —— 后者会把整条错误路径变成 500。
		return key
	}

	config := &i18n.LocalizeConfig{
		MessageID: key,
	}

	if len(args) > 0 && args[0] != nil {
		config.TemplateData = args[0]
	}

	msg, err := loc.Localize(config)
	if err != nil {
		// Return key as fallback if translation not found
		return key
	}
	return msg
}

// userLangLoaderFunc is a function that loads user language from database/cache
// It's set by the model package to avoid circular imports
var userLangLoaderFunc func(userId int) string

// SetUserLangLoader sets the function to load user language (called from model package)
func SetUserLangLoader(loader func(userId int) string) {
	userLangLoaderFunc = loader
}

// GetLangFromContext extracts the language setting from gin context
// It checks multiple sources in priority order:
// 1. User settings (ContextKeyUserSetting) - if already loaded (e.g., by TokenAuth)
// 2. Lazy load user language from cache/DB using user ID
// 3. Language set by middleware (ContextKeyLanguage) - from Accept-Language header
// 4. Default language (English)
func GetLangFromContext(c *gin.Context) string {
	if c == nil {
		return DefaultLang
	}

	// 1. Try to get language from user settings (if already loaded by TokenAuth or other middleware)
	if userSetting, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting); ok {
		if userSetting.Language != "" {
			normalized := normalizeLang(userSetting.Language)
			if IsSupported(normalized) {
				return normalized
			}
		}
	}

	// 2. Lazy load user language using user ID (for session-based auth where full settings aren't loaded)
	if userLangLoaderFunc != nil {
		if userId, exists := c.Get("id"); exists {
			if uid, ok := userId.(int); ok && uid > 0 {
				lang := userLangLoaderFunc(uid)
				if lang != "" {
					normalized := normalizeLang(lang)
					if IsSupported(normalized) {
						return normalized
					}
				}
			}
		}
	}

	// 3. Try to get language from context (set by I18n middleware from Accept-Language)
	if lang := c.GetString(string(constant.ContextKeyLanguage)); lang != "" {
		normalized := normalizeLang(lang)
		if IsSupported(normalized) {
			return normalized
		}
	}

	// 4. Try Accept-Language header directly (fallback if middleware didn't run)
	if acceptLang := c.GetHeader("Accept-Language"); acceptLang != "" {
		lang := ParseAcceptLanguage(acceptLang)
		if IsSupported(lang) {
			return lang
		}
	}

	return DefaultLang
}

// ParseAcceptLanguage parses the Accept-Language header and returns the preferred language
func ParseAcceptLanguage(header string) string {
	if header == "" {
		return DefaultLang
	}

	// Simple parsing: take the first language tag
	parts := strings.Split(header, ",")
	if len(parts) == 0 {
		return DefaultLang
	}

	// Get the first language and remove quality value
	firstLang := strings.TrimSpace(parts[0])
	if idx := strings.Index(firstLang, ";"); idx > 0 {
		firstLang = firstLang[:idx]
	}

	return normalizeLang(firstLang)
}

// normalizeLang normalizes language code to supported format
func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))

	// Handle common variations
	switch {
	case strings.HasPrefix(lang, "zh-tw"):
		return LangZhTW
	case strings.HasPrefix(lang, "zh"):
		return LangZhCN
	case strings.HasPrefix(lang, "en"):
		return LangEn
	default:
		return DefaultLang
	}
}

// SupportedLanguages returns a list of supported language codes
func SupportedLanguages() []string {
	return []string{LangZhCN, LangZhTW, LangEn}
}

// IsSupported checks if a language code is supported
func IsSupported(lang string) bool {
	lang = normalizeLang(lang)
	for _, supported := range SupportedLanguages() {
		if lang == supported {
			return true
		}
	}
	return false
}
