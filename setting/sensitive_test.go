package setting

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/stretchr/testify/assert"
)

func TestSensitiveExemptLogic(t *testing.T) {
	// Backup & restore global variables
	origCheckSensitiveEnabled := CheckSensitiveEnabled
	origCheckSensitiveOnPromptEnabled := CheckSensitiveOnPromptEnabled
	origSensitiveWords := SensitiveWords
	origExemptGroups := SensitiveWordsExemptGroups
	defer func() {
		CheckSensitiveEnabled = origCheckSensitiveEnabled
		CheckSensitiveOnPromptEnabled = origCheckSensitiveOnPromptEnabled
		SensitiveWords = origSensitiveWords
		SensitiveWordsExemptGroups = origExemptGroups
	}()

	t.Run("default configuration", func(t *testing.T) {
		assert.False(t, CheckSensitiveEnabled)
		assert.False(t, CheckSensitiveOnPromptEnabled)
		assert.Empty(t, SensitiveWords)
		assert.Contains(t, SensitiveWordsExemptGroups, "vip")
		assert.Contains(t, SensitiveWordsExemptGroups, "svip")
	})

	t.Run("exempt groups serialization", func(t *testing.T) {
		SensitiveWordsExemptGroupsFromString("vip, svip, internal")
		assert.Equal(t, "vip,svip,internal", SensitiveWordsExemptGroupsToString())

		// Reset with empty string falls back to default
		SensitiveWordsExemptGroupsFromString("")
		assert.Equal(t, "vip,svip", SensitiveWordsExemptGroupsToString())
	})

	t.Run("IsUserExemptFromSensitiveCheck", func(t *testing.T) {
		SensitiveWordsExemptGroups = []string{"vip", "svip"}

		// 1. Root user by ID (id=1)
		assert.True(t, IsUserExemptFromSensitiveCheck(1, common.RoleCommonUser, "default", "default"), "Root user (id=1) should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(1, 0, "", ""), "Root user (id=1) with 0 role/group should be exempt")

		// 2. Admin user by Role
		assert.True(t, IsUserExemptFromSensitiveCheck(2, common.RoleRootUser, "default", "default"), "RoleRootUser should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(2, common.RoleAdminUser, "default", "default"), "RoleAdminUser should be exempt")

		// 3. VIP Group
		assert.True(t, IsUserExemptFromSensitiveCheck(3, common.RoleCommonUser, "vip", "default"), "UserGroup=vip should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(4, common.RoleCommonUser, "VIP", "default"), "Case-insensitive UserGroup=VIP should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(5, common.RoleCommonUser, "svip", "default"), "UserGroup=svip should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(6, common.RoleCommonUser, "default", "vip"), "UsingGroup=vip should be exempt")
		assert.True(t, IsUserExemptFromSensitiveCheck(7, common.RoleCommonUser, "default", "SVIP"), "Case-insensitive UsingGroup=SVIP should be exempt")

		// 4. Regular common user
		assert.False(t, IsUserExemptFromSensitiveCheck(8, common.RoleCommonUser, "default", "default"), "Regular common user in default group should NOT be exempt")
		assert.False(t, IsUserExemptFromSensitiveCheck(9, 0, "other", "other"), "Regular user with non-exempt group should NOT be exempt")
	})

	t.Run("ShouldCheckPromptSensitive with switches and keywords", func(t *testing.T) {
		CheckSensitiveEnabled = true
		CheckSensitiveOnPromptEnabled = true
		SensitiveWords = []string{"blocked_word"}
		SensitiveWordsExemptGroups = []string{"vip", "svip"}

		// Regular user is checked
		assert.True(t, ShouldCheckPromptSensitive(10, common.RoleCommonUser, "default", "default"))

		// Root user is exempt
		assert.False(t, ShouldCheckPromptSensitive(1, common.RoleCommonUser, "default", "default"))

		// Admin user is exempt
		assert.False(t, ShouldCheckPromptSensitive(10, common.RoleAdminUser, "default", "default"))

		// VIP group is exempt
		assert.False(t, ShouldCheckPromptSensitive(10, common.RoleCommonUser, "vip", "default"))

		// If switches are turned off, no one is checked
		CheckSensitiveEnabled = false
		assert.False(t, ShouldCheckPromptSensitive(10, common.RoleCommonUser, "default", "default"))

		// If keywords are empty, no one is checked
		CheckSensitiveEnabled = true
		SensitiveWords = []string{}
		assert.False(t, ShouldCheckPromptSensitive(10, common.RoleCommonUser, "default", "default"))
	})
}
