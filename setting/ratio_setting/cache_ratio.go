package ratio_setting

import (
	"github.com/dengyie/apihub/types"
)

const (
	DefaultCacheRatio       = 1.0
	DefaultCreateCacheRatio = 1.25
)

var defaultCacheRatio = map[string]float64{
	"gemini-3-flash-preview":       0.1,
	"gemini-3-pro-preview":         0.1,
	"gemini-3.1-pro-preview":       0.1,
	"gpt-4":                        0.5,
	"o1":                           0.5,
	"o1-2024-12-17":                0.5,
	"o1-preview-2024-09-12":        0.5,
	"o1-preview":                   0.5,
	"o1-mini-2024-09-12":           0.5,
	"o1-mini":                      0.5,
	"o3-mini":                      0.5,
	"o3-mini-2025-01-31":           0.5,
	"gpt-4o-2024-11-20":            0.5,
	"gpt-4o-2024-08-06":            0.5,
	"gpt-4o":                       0.5,
	"gpt-4o-mini-2024-07-18":       0.5,
	"gpt-4o-mini":                  0.5,
	"gpt-4o-realtime-preview":      0.5,
	"gpt-4o-mini-realtime-preview": 0.5,
	"gpt-4.5-preview":              0.5,
	"gpt-4.5-preview-2025-02-27":   0.5,
	"gpt-4.1":                      0.25,
	"gpt-4.1-mini":                 0.25,
	"gpt-4.1-nano":                 0.25,
	// Models below also carry a legacy ModelRatio entry, so GetBillingMode
	// reports "ratio" and the built-in tiered expression (whose cr term is the
	// discounted cache-read price) never runs. Without an explicit cache ratio
	// the ratio path bills cache reads at DefaultCacheRatio = 1.0, i.e. full
	// input price. Values are cr / p from setting/billing_setting/builtin_billing.go.
	"gemini-2.5-pro":                      0.1, // tier("standard", p*1.25 + c*10 + cr*0.125)
	"gemini-2.5-pro-thinking-*":           0.1,
	"gpt-5.5":                             0.1,  // p*5 + c*30 + cr*0.5
	"gpt-5.6-luna":                        0.1,  // p*0.2 + c*1.2 + cr*0.02
	"gpt-5.6-sol":                         0.1,  // p*4 + c*20 + cr*0.4
	"gpt-5.6-terra":                       0.1,  // p*2 + c*12 + cr*0.2
	"gpt-image-1":                         0.25, // p*5 + cr*1.25 + img*8 + img_cr*2 + c*30
	"gpt-5":                               0.1,
	"gpt-5-2025-08-07":                    0.1,
	"gpt-5-chat-latest":                   0.1,
	"gpt-5-mini":                          0.1,
	"gpt-5-mini-2025-08-07":               0.1,
	"gpt-5-nano":                          0.1,
	"gpt-5-nano-2025-08-07":               0.1,
	"deepseek-chat":                       0.25,
	"deepseek-reasoner":                   0.25,
	"deepseek-coder":                      0.25,
	"claude-3-sonnet-20240229":            0.1,
	"claude-3-opus-20240229":              0.1,
	"claude-3-haiku-20240307":             0.1,
	"claude-3-5-haiku-20241022":           0.1,
	"claude-haiku-4-5-20251001":           0.1,
	"claude-3-5-sonnet-20240620":          0.1,
	"claude-3-5-sonnet-20241022":          0.1,
	"claude-3-7-sonnet-20250219":          0.1,
	"claude-3-7-sonnet-20250219-thinking": 0.1,
	"claude-sonnet-4-20250514":            0.1,
	"claude-sonnet-4-20250514-thinking":   0.1,
	"claude-opus-4-20250514":              0.1,
	"claude-opus-4-20250514-thinking":     0.1,
	"claude-opus-4-1-20250805":            0.1,
	"claude-opus-4-1-20250805-thinking":   0.1,
	"claude-sonnet-4-5-20250929":          0.1,
	"claude-sonnet-4-5-20250929-thinking": 0.1,
	"claude-opus-4-5-20251101":            0.1,
	"claude-opus-4-5-20251101-thinking":   0.1,
	"claude-opus-4-6":                     0.1,
	"claude-opus-4-6-thinking":            0.1,
	"claude-opus-4-6-max":                 0.1,
	"claude-opus-4-6-high":                0.1,
	"claude-opus-4-6-medium":              0.1,
	"claude-opus-4-6-low":                 0.1,
	"claude-opus-4-7":                     0.1,
	"claude-opus-4-7-thinking":            0.1,
	"claude-opus-4-7-max":                 0.1,
	"claude-opus-4-7-xhigh":               0.1,
	"claude-opus-4-7-high":                0.1,
	"claude-opus-4-7-medium":              0.1,
	"claude-opus-4-7-low":                 0.1,
	"claude-opus-4-8":                     0.1,
	"claude-opus-4-8-thinking":            0.1,
	"claude-opus-4-8-max":                 0.1,
	"claude-opus-4-8-xhigh":               0.1,
	"claude-opus-4-8-high":                0.1,
	"claude-opus-4-8-medium":              0.1,
	"claude-opus-4-8-low":                 0.1,
}

var defaultCreateCacheRatio = map[string]float64{
	"gpt-5.6-sol":                         1.25,
	"gpt-5.6-terra":                       1.25,
	"gpt-5.6-luna":                        1.25,
	"claude-3-sonnet-20240229":            1.25,
	"claude-3-opus-20240229":              1.25,
	"claude-3-haiku-20240307":             1.25,
	"claude-3-5-haiku-20241022":           1.25,
	"claude-haiku-4-5-20251001":           1.25,
	"claude-3-5-sonnet-20240620":          1.25,
	"claude-3-5-sonnet-20241022":          1.25,
	"claude-3-7-sonnet-20250219":          1.25,
	"claude-3-7-sonnet-20250219-thinking": 1.25,
	"claude-sonnet-4-20250514":            1.25,
	"claude-sonnet-4-20250514-thinking":   1.25,
	"claude-opus-4-20250514":              1.25,
	"claude-opus-4-20250514-thinking":     1.25,
	"claude-opus-4-1-20250805":            1.25,
	"claude-opus-4-1-20250805-thinking":   1.25,
	"claude-sonnet-4-5-20250929":          1.25,
	"claude-sonnet-4-5-20250929-thinking": 1.25,
	"claude-opus-4-5-20251101":            1.25,
	"claude-opus-4-5-20251101-thinking":   1.25,
	"claude-opus-4-6":                     1.25,
	"claude-opus-4-6-thinking":            1.25,
	"claude-opus-4-6-max":                 1.25,
	"claude-opus-4-6-high":                1.25,
	"claude-opus-4-6-medium":              1.25,
	"claude-opus-4-6-low":                 1.25,
	"claude-opus-4-7":                     1.25,
	"claude-opus-4-7-thinking":            1.25,
	"claude-opus-4-7-max":                 1.25,
	"claude-opus-4-7-xhigh":               1.25,
	"claude-opus-4-7-high":                1.25,
	"claude-opus-4-7-medium":              1.25,
	"claude-opus-4-7-low":                 1.25,
	"claude-opus-4-8":                     1.25,
	"claude-opus-4-8-thinking":            1.25,
	"claude-opus-4-8-max":                 1.25,
	"claude-opus-4-8-xhigh":               1.25,
	"claude-opus-4-8-high":                1.25,
	"claude-opus-4-8-medium":              1.25,
	"claude-opus-4-8-low":                 1.25,
}

//var defaultCreateCacheRatio = map[string]float64{}

var cacheRatioMap = types.NewRWMap[string, float64]()
var createCacheRatioMap = types.NewRWMap[string, float64]()

// GetCacheRatioMap returns a copy of the cache ratio map
func GetCacheRatioMap() map[string]float64 {
	return cacheRatioMap.ReadAll()
}

// CacheRatio2JSONString converts the cache ratio map to a JSON string
func CacheRatio2JSONString() string {
	return cacheRatioMap.MarshalJSONString()
}

// CreateCacheRatio2JSONString converts the create cache ratio map to a JSON string
func CreateCacheRatio2JSONString() string {
	return createCacheRatioMap.MarshalJSONString()
}

// UpdateCacheRatioByJSONString updates the cache ratio map from a JSON string
func UpdateCacheRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonStringWithCallback(cacheRatioMap, jsonStr, InvalidateExposedDataCache)
}

// UpdateCreateCacheRatioByJSONString updates the create cache ratio map from a JSON string
func UpdateCreateCacheRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonStringWithCallback(createCacheRatioMap, jsonStr, InvalidateExposedDataCache)
}

// GetCacheRatio returns the cache ratio for a model.
//
// Three layers, in order: the configured map, the built-in defaults in this
// file, then DefaultCacheRatio (1.0, i.e. no cache discount at all).
//
// The middle layer is not redundant. The configured map is loaded from the
// CacheRatio option as a whole -- types.LoadFromJsonStringWithCallback replaces
// it rather than merging -- so an installation whose option was written before
// a built-in entry existed has a map without it, and every built-in cache price
// added since then would be dead on that host while looking correct in the
// source. For a model that already carries a legacy ModelRatio the built-in
// tiered expression (and its cr term) never runs either, so this getter is the
// only thing left that can give cached input its discount; without the fallback
// it silently bills at full input price.
//
// A configured value always wins, including an explicit 1.0, so "no discount
// for this model" stays expressible.
//
// Like GetModelRatio and GetCompletionRatio, the name is wildcard-normalized
// first, so a request for e.g. gemini-2.5-pro-thinking-<budget> matches the
// gemini-2.5-pro-thinking-* entry instead of silently falling back to 1.0.
func GetCacheRatio(name string) (float64, bool) {
	return lookupCacheRatio(cacheRatioMap, defaultCacheRatio, DefaultCacheRatio, name)
}

// GetCreateCacheRatio mirrors GetCacheRatio, over the cache-write defaults.
func GetCreateCacheRatio(name string) (float64, bool) {
	return lookupCacheRatio(createCacheRatioMap, defaultCreateCacheRatio, DefaultCreateCacheRatio, name)
}

func lookupCacheRatio(configured *types.RWMap[string, float64], defaults map[string]float64, fallback float64, name string) (float64, bool) {
	formatted := FormatMatchingModelName(name)
	if ratio, ok := configured.Get(formatted); ok {
		return ratio, true
	}
	if ratio, ok := defaults[formatted]; ok {
		return ratio, true
	}
	return fallback, false
}

// ResolveCacheRatio resolves a cache-read ratio from a configuration snapshot
// rather than the live map. The console's saved-price view and its draft
// preview both read a snapshot, so they must apply the same built-in default
// the relay bills with -- otherwise the console shows 1.0 for a model the relay
// charges at 0.1. configured is nil when the snapshot has no entry.
func ResolveCacheRatio(name string, configured *float64) float64 {
	return resolveCacheRatio(defaultCacheRatio, DefaultCacheRatio, name, configured)
}

// ResolveCreateCacheRatio mirrors ResolveCacheRatio for cache-write prices.
func ResolveCreateCacheRatio(name string, configured *float64) float64 {
	return resolveCacheRatio(defaultCreateCacheRatio, DefaultCreateCacheRatio, name, configured)
}

func resolveCacheRatio(defaults map[string]float64, fallback float64, name string, configured *float64) float64 {
	if configured != nil {
		return *configured
	}
	if ratio, ok := defaults[FormatMatchingModelName(name)]; ok {
		return ratio
	}
	return fallback
}

func GetCacheRatioCopy() map[string]float64 {
	return cacheRatioMap.ReadAll()
}

func GetCreateCacheRatioCopy() map[string]float64 {
	return createCacheRatioMap.ReadAll()
}
