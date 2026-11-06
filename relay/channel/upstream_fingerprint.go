package channel

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/types"
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

// 用户定制：claude 协议上游（Anthropic 原生渠道与 coding-plan 特殊 base）的请求，
// 上游必须始终看到 Claude Code CLI 形态，不论下游客户端是什么。真 Claude Code 客户端
// 透传其指纹头；其余客户端补一套默认指纹。头集合与版本对齐知识库
// [[Claude 客户端协议伪装与指纹规范]]（CLI 2.1.284 + Stainless SDK 0.39.0），版本升级只改这里。
const (
	claudeFingerprintUserAgent         = "claude-code/2.1.284 (Darwin 24.6.0; arm64)"
	claudeFingerprintClientPlatform    = "claude_code_cli"
	claudeFingerprintAnthropicVersion  = "2023-06-01"
	claudeFingerprintStainlessLang     = "js"
	claudeFingerprintStainlessPkgVer   = "0.39.0"
	claudeFingerprintStainlessOS       = "MacOS"
	claudeFingerprintStainlessArch     = "arm64"
	claudeFingerprintStainlessRuntime  = "node"
	claudeFingerprintStainlessRuntmVer = "v22.14.0"
	claudeFingerprintApp               = "cli"
)

// 官方 UA 校验正则（伪装规范同款）：claude-cli/<ver> (...) 或 claude-code/<ver> (...)。
var claudeCodeClientUARegex = regexp.MustCompile(`(?i)^claude-(?:cli|code)/\S+ \(`)

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

// UserSessionUUID returns the deterministic per-user session UUID (UUID v5/SHA-1):
// the same user id always maps to the same UUID, keeping one user's requests
// session-sticky on upstreams that route or cache by per-user keys. Shared by
// the codex fingerprint (session_id / x-opencode-session), the claude
// fingerprint (X-Session-UUID) and the chat→responses conversion (prompt_cache_key).
func UserSessionUUID(userID int) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("opencode-session-%d", userID))).String()
}

// usesClaudeFingerprint reports whether the upstream request must look like a
// Claude Code CLI request: it fires when the request will reach the upstream in
// the Anthropic /v1/messages envelope — native Anthropic channels and the
// coding-plan special bases (doubao/glm/kimi) whose adaptors delegate to the
// claude adaptor. Channels that convert the claude envelope away (e.g. OpenAI
// type=1) are deliberately excluded, mirroring the codex branch's type gate.
// Keyed on the envelope rather than the model name so upstream aliases served
// over /v1/messages (e.g. historical cc-format models) are covered too.
func usesClaudeFingerprint(info *common.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || info.RelayFormat != types.RelayFormatClaude {
		return false
	}
	if info.ChannelType == constant.ChannelTypeAnthropic {
		return true
	}
	_, ok := constant.ChannelSpecialBases[info.ChannelBaseUrl]
	return ok
}

// isClaudeCodeClient reports whether the inbound request was sent by a real
// Claude Code CLI: it identifies itself via its UA (official validation regex
// ^claude-(?:cli|code)/\S+ \(…)) or via the anthropic-client-platform header.
func isClaudeCodeClient(clientHeaders http.Header) bool {
	if clientHeaders == nil {
		return false
	}
	if clientHeaders.Get("anthropic-client-platform") != "" {
		return true
	}
	return claudeCodeClientUARegex.MatchString(clientHeaders.Get("User-Agent"))
}

// applyClaudeCodeFingerprint fills the Claude Code identity headers for
// anthropic-protocol upstream requests. Fill-only: header_override and anything
// already set (adaptor defaults, channel tests) always win. Real Claude Code
// clients get their own fingerprint forwarded first; the defaults then fill the
// remaining gaps so upstream never sees a bare Go client. anthropic-beta is
// intentionally not defaulted: the claude adaptor already forwards the client's
// value, and injecting context-1m would change official-API billing above 200k
// input — add it per-channel via header_override when an upstream demands it.
func applyClaudeCodeFingerprint(c *gin.Context, req *http.Request, userSession string) {
	var clientHeaders http.Header
	if c.Request != nil {
		clientHeaders = c.Request.Header
	}
	if isClaudeCodeClient(clientHeaders) {
		for _, name := range operation_setting.ClaudeCliPassThroughHeaders() {
			if req.Header.Get(name) != "" {
				continue
			}
			if value := clientHeaders.Get(name); value != "" {
				req.Header.Set(name, value)
			}
		}
	}
	setIfEmpty := func(name, value string) {
		if req.Header.Get(name) == "" {
			req.Header.Set(name, value)
		}
	}
	setIfEmpty("User-Agent", claudeFingerprintUserAgent)
	setIfEmpty("anthropic-client-platform", claudeFingerprintClientPlatform)
	setIfEmpty("anthropic-version", claudeFingerprintAnthropicVersion)
	setIfEmpty("X-Stainless-Lang", claudeFingerprintStainlessLang)
	setIfEmpty("X-Stainless-Package-Version", claudeFingerprintStainlessPkgVer)
	setIfEmpty("X-Stainless-OS", claudeFingerprintStainlessOS)
	setIfEmpty("X-Stainless-Arch", claudeFingerprintStainlessArch)
	setIfEmpty("X-Stainless-Runtime", claudeFingerprintStainlessRuntime)
	setIfEmpty("X-Stainless-Runtime-Version", claudeFingerprintStainlessRuntmVer)
	setIfEmpty("X-App", claudeFingerprintApp)
	// X-Client-Request-Id 按官方 SDK 语义逐请求刷新；X-Session-UUID 按用户稳定派生，
	// 避免跨渠道共享同一会话标识触发风控（知识库 2026-10-15 伪装渠道审计教训）。
	setIfEmpty("X-Client-Request-Id", uuid.NewString())
	setIfEmpty("X-Session-UUID", userSession)
}

// applyUpstreamClientFingerprint sets the client identity headers the upstream sees.
// It runs after header_override and only fills headers that are still missing, so an
// explicit channel header_override always wins.
func applyUpstreamClientFingerprint(c *gin.Context, info *common.RelayInfo, req *http.Request) {
	userSession := UserSessionUUID(c.GetInt("id"))
	if usesClaudeFingerprint(info) {
		applyClaudeCodeFingerprint(c, req, userSession)
		return
	}
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
