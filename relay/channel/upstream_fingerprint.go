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
	// 部分上游（如 zzzcoding）缺这个头就以 403 "only allows Codex official clients" 拒绝。
	codexFingerprintBetaFeatures = "remote_compaction_v2"
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

// userSessionUUID returns the deterministic per-user session UUID (UUID v5/SHA-1):
// the same user id always maps to the same UUID, keeping one user's requests
// session-sticky on upstreams that route by session headers.
func userSessionUUID(userID int) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("opencode-session-%d", userID))).String()
}

// applyUpstreamClientFingerprint sets the client identity headers the upstream sees.
// It runs after header_override and only fills headers that are still missing, so an
// explicit channel header_override always wins.
func applyUpstreamClientFingerprint(c *gin.Context, info *common.RelayInfo, req *http.Request) {
	userSession := userSessionUUID(c.GetInt("id"))
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
	// OpenCode 系 SDK 会显式携带 x-opencode-session 做会话路由（如 ioll.pp.ua 一类
	// 中继缺失它直接 400 并触发熔断）。真 Codex CLI 从不发送该头，因此转发客户端
	// 显式值不影响 Codex 指纹形态；header_override 里配的 {opencode_session} 占位符
	// 则覆盖没有该头的客户端。
	if value := clientHeaders.Get("x-opencode-session"); value != "" && req.Header.Get("x-opencode-session") == "" {
		req.Header.Set("x-opencode-session", value)
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
	if req.Header.Get("X-Codex-Beta-Features") == "" {
		req.Header.Set("X-Codex-Beta-Features", codexFingerprintBetaFeatures)
	}
	if req.Header.Get("session_id") == "" {
		req.Header.Set("session_id", userSession)
	}
}
