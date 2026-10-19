package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dengyie/apihub/common"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/dengyie/apihub/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestPrepareRequestBilling_SensitiveCheckExemption(t *testing.T) {
	origEnabled := setting.CheckSensitiveEnabled
	origPromptEnabled := setting.CheckSensitiveOnPromptEnabled
	origWords := setting.SensitiveWords
	origGroups := setting.SensitiveWordsExemptGroups
	defer func() {
		setting.CheckSensitiveEnabled = origEnabled
		setting.CheckSensitiveOnPromptEnabled = origPromptEnabled
		setting.SensitiveWords = origWords
		setting.SensitiveWordsExemptGroups = origGroups
	}()

	setting.CheckSensitiveEnabled = true
	setting.CheckSensitiveOnPromptEnabled = true
	setting.SensitiveWords = []string{"intercept_this_keyword"}
	setting.SensitiveWordsExemptGroups = []string{"vip", "svip"}

	createRequest := func() dto.Request {
		return &dto.GeneralOpenAIRequest{
			Model: "gpt-4o",
			Messages: []dto.Message{
				{
					Role:    "user",
					Content: "Please process intercept_this_keyword now",
				},
			},
		}
	}

	t.Run("normal user is intercepted", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

		info := &relaycommon.RelayInfo{
			UserId:     100,
			UserRole:   common.RoleCommonUser,
			UserGroup:  "default",
			UsingGroup: "default",
			Request:    createRequest(),
		}

		err := PrepareRequestBilling(c, info)
		assert.NotNil(t, err)
		assert.Equal(t, types.ErrorCodeSensitiveWordsDetected, err.GetErrorCode())
	})

	t.Run("root user (id=1) is exempt", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

		info := &relaycommon.RelayInfo{
			UserId:     1, // Root user
			UserRole:   common.RoleCommonUser,
			UserGroup:  "default",
			UsingGroup: "default",
			Request:    createRequest(),
		}

		err := PrepareRequestBilling(c, info)
		if err != nil {
			assert.NotEqual(t, types.ErrorCodeSensitiveWordsDetected, err.GetErrorCode(), "Root user must not be blocked by sensitive word detection")
		}
	})

	t.Run("admin role user is exempt", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

		info := &relaycommon.RelayInfo{
			UserId:     200,
			UserRole:   common.RoleAdminUser, // Admin role
			UserGroup:  "default",
			UsingGroup: "default",
			Request:    createRequest(),
		}

		err := PrepareRequestBilling(c, info)
		if err != nil {
			assert.NotEqual(t, types.ErrorCodeSensitiveWordsDetected, err.GetErrorCode(), "Admin user must not be blocked by sensitive word detection")
		}
	})

	t.Run("vip userGroup is exempt", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

		info := &relaycommon.RelayInfo{
			UserId:     300,
			UserRole:   common.RoleCommonUser,
			UserGroup:  "vip", // VIP user group
			UsingGroup: "default",
			Request:    createRequest(),
		}

		err := PrepareRequestBilling(c, info)
		if err != nil {
			assert.NotEqual(t, types.ErrorCodeSensitiveWordsDetected, err.GetErrorCode(), "VIP user must not be blocked by sensitive word detection")
		}
	})

	t.Run("vip usingGroup token is exempt", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

		info := &relaycommon.RelayInfo{
			UserId:     400,
			UserRole:   common.RoleCommonUser,
			UserGroup:  "default",
			UsingGroup: "vip", // Token uses VIP group
			Request:    createRequest(),
		}

		err := PrepareRequestBilling(c, info)
		if err != nil {
			assert.NotEqual(t, types.ErrorCodeSensitiveWordsDetected, err.GetErrorCode(), "VIP token must not be blocked by sensitive word detection")
		}
	})
}
