package channel

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 用户定制：GPT 模型走 OpenAI 类渠道时，上游必须始终看到 Codex CLI 形态的请求，
// 不论下游是 Codex 还是普通 SDK。真 Codex 客户端（带 originator）原样透传其指纹头；
// 其余客户端补一套默认 Codex 指纹，避免 Go 默认 UA 等非 Codex 特征外泄。
const (
	codexFingerprintOriginator = "codex_cli_rs"
	codexFingerprintUserAgent  = "codex_cli_rs/0.77.0 (Mac OS 26.1.0; arm64) iTerm.app/3.6.5"
	codexFingerprintOpenAIBeta = "responses=experimental"
)

// usesCodexFingerprint reports whether the upstream request must look like a Codex CLI request.
func usesCodexFingerprint(info *common.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeOpenAI {
		return false
	}
	model := info.UpstreamModelName
	if model == "" {
		model = info.OriginModelName
	}
	return strings.HasPrefix(strings.ToLower(model), "gpt-")
}

// applyUpstreamClientFingerprint sets the client identity headers the upstream sees.
// It runs after header_override and only fills headers that are still missing, so an
// explicit channel header_override always wins.
func applyUpstreamClientFingerprint(c *gin.Context, info *common.RelayInfo, req *http.Request) {
	// UUID v5（SHA-1）：相同 user_id 总是生成相同 UUID，保证同一用户的会话粘性。
	userSession := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("opencode-session-%d", c.GetInt("id")))).String()
	if !usesCodexFingerprint(info) {
		// 部分上游（如 ioll.pp.ua）要求 x-opencode-session 头做会话路由，缺失时报 400。
		// Codex 指纹请求不带它：真实 Codex CLI 不会发送该头。
		if req.Header.Get("x-opencode-session") == "" {
			req.Header.Set("x-opencode-session", userSession)
		}
		return
	}

	var clientHeaders http.Header
	if c.Request != nil {
		clientHeaders = c.Request.Header
	}
	if clientHeaders.Get("originator") != "" {
		for _, name := range operation_setting.CodexCliPassThroughHeaders() {
			if req.Header.Get(name) != "" {
				continue
			}
			if value := clientHeaders.Get(name); value != "" {
				req.Header.Set(name, value)
			}
		}
	}
	if req.Header.Get("originator") == "" {
		req.Header.Set("originator", codexFingerprintOriginator)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", codexFingerprintUserAgent)
	}
	if req.Header.Get("OpenAI-Beta") == "" {
		req.Header.Set("OpenAI-Beta", codexFingerprintOpenAIBeta)
	}
	if req.Header.Get("session_id") == "" {
		req.Header.Set("session_id", userSession)
	}
}
