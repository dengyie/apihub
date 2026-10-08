package service

import (
	"fmt"
	"html"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/oauth"
	"github.com/dengyie/apihub/setting/system_setting"
)

func UnbindAccountOAuth(identity AuthIdentity, providerID int) error {
	enabled := model.AccountLoginMethods{
		Password: common.PasswordLoginEnabled,
		Passkey:  system_setting.PasskeySettingsSnapshot().Enabled,
		WeChat:   common.WeChatAuthEnabled,
	}
	for _, provider := range oauth.GetAllProviders() {
		if !provider.IsEnabled() {
			continue
		}
		if custom, ok := provider.(*oauth.GenericOAuthProvider); ok {
			enabled.CustomProviderIDs = append(enabled.CustomProviderIDs, custom.GetProviderId())
		} else {
			enabled.OAuthColumns = append(enabled.OAuthColumns, provider.ProviderUserIDColumn())
		}
	}
	return model.UnbindUserOAuthForSession(identity, providerID, enabled)
}

// NotifyAccountSecurityChange never includes credentials or tokens. The caller
// records delivery failure independently from the already-committed change.
func NotifyAccountSecurityChange(email, event string) error {
	if email == "" {
		return nil
	}
	subject := common.SystemName + " — Account security notification"
	content := fmt.Sprintf("<p>Your account security settings have changed: %s.</p><p>If you did not make this change, open your account security settings, revoke other login sessions, and contact your administrator.</p>", html.EscapeString(event))
	return common.SendEmail(subject, email, content)
}
