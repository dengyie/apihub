package setting

import (
	"strings"
	"sync"

	"github.com/dengyie/apihub/common"
)

var CheckSensitiveEnabled = false
var CheckSensitiveOnPromptEnabled = false

// StopOnSensitiveEnabled 如果检测到敏感词，是否立刻停止生成，否则替换敏感词
var StopOnSensitiveEnabled = true

// StreamCacheQueueLength 流模式缓存队列长度，0表示无缓存
var StreamCacheQueueLength = 0

// SensitiveWords 敏感词
var SensitiveWords = []string{}

// SensitiveWordsExemptGroups 敏感词豁免分组（默认包含 vip 与 svip）
var SensitiveWordsExemptGroups = []string{"vip", "svip"}
var sensitiveWordsExemptGroupsMutex sync.RWMutex

func SensitiveWordsToString() string {
	return strings.Join(SensitiveWords, "\n")
}

func SensitiveWordsFromString(s string) {
	SensitiveWords = []string{}
	sw := strings.SplitSeq(s, "\n")
	for w := range sw {
		w = strings.TrimSpace(w)
		if w != "" {
			SensitiveWords = append(SensitiveWords, w)
		}
	}
}

func SensitiveWordsExemptGroupsToString() string {
	sensitiveWordsExemptGroupsMutex.RLock()
	defer sensitiveWordsExemptGroupsMutex.RUnlock()
	return strings.Join(SensitiveWordsExemptGroups, ",")
}

func SensitiveWordsExemptGroupsFromString(s string) {
	sensitiveWordsExemptGroupsMutex.Lock()
	defer sensitiveWordsExemptGroupsMutex.Unlock()
	groups := []string{}
	for _, g := range strings.Split(s, ",") {
		g = strings.TrimSpace(strings.ToLower(g))
		if g != "" {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		SensitiveWordsExemptGroups = []string{"vip", "svip"}
	} else {
		SensitiveWordsExemptGroups = groups
	}
}

// IsUserExemptFromSensitiveCheck 判断用户是否豁免敏感词检查
// 规则 1：Root 用户（userId == 1）或管理员（userRole >= common.RoleAdminUser）自动豁免
// 规则 2：用户分组或当前请求使用的分组属于白名单分组（默认包含 vip, svip）自动豁免
func IsUserExemptFromSensitiveCheck(userId int, userRole int, userGroup string, usingGroup string) bool {
	// 方案 1：Root / 管理员自动免检
	if userId == 1 || (userRole != 0 && userRole >= common.RoleAdminUser) {
		return true
	}

	// 方案 2：VIP 分组免检
	sensitiveWordsExemptGroupsMutex.RLock()
	exemptGroups := append([]string(nil), SensitiveWordsExemptGroups...)
	sensitiveWordsExemptGroupsMutex.RUnlock()

	checkMatch := func(g string) bool {
		if g == "" {
			return false
		}
		gLower := strings.ToLower(strings.TrimSpace(g))
		for _, target := range exemptGroups {
			if gLower == target {
				return true
			}
		}
		return false
	}

	return checkMatch(userGroup) || checkMatch(usingGroup)
}

// ShouldCheckPromptSensitive 判断当前请求的提示词是否需要进行敏感词检查
func ShouldCheckPromptSensitive(userId int, userRole int, userGroup string, usingGroup string) bool {
	if !CheckSensitiveEnabled || !CheckSensitiveOnPromptEnabled {
		return false
	}
	if len(SensitiveWords) == 0 {
		return false
	}
	return !IsUserExemptFromSensitiveCheck(userId, userRole, userGroup, usingGroup)
}

// ShouldCheckPromptSensitiveGlobal 全局开关与词库状态检查
func ShouldCheckPromptSensitiveGlobal() bool {
	return CheckSensitiveEnabled && CheckSensitiveOnPromptEnabled && len(SensitiveWords) > 0
}
