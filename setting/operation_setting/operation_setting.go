package operation_setting

import "strings"

var DemoSiteEnabled = false
var SelfUseModeEnabled = false

var AutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
	"credit insufficient balance",
	"insufficient_user_quota",
	"insufficient_quota",
	"insufficient balance",
	"user quota not enough",
	"quota exhausted",
	"balance is not enough",
	"quota_exceeded",
	"user_quota_exhausted",
	"无权访问",
	"当前分组",
	"not supported by tokenplan",
	"is not supported by tokenplan",
	"user_group_no_permission",
}

func AutomaticDisableKeywordsToString() string {
	return strings.Join(AutomaticDisableKeywords, "\n")
}

func AutomaticDisableKeywordsFromString(s string) {
	AutomaticDisableKeywords = []string{}
	ak := strings.SplitSeq(s, "\n")
	for k := range ak {
		k = strings.TrimSpace(k)
		k = strings.ToLower(k)
		if k != "" {
			AutomaticDisableKeywords = append(AutomaticDisableKeywords, k)
		}
	}
}
