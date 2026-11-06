package channel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dengyie/apihub/constant"
	"github.com/dengyie/apihub/loadbalancer"
	relaycommon "github.com/dengyie/apihub/relay/common"
	relayconstant "github.com/dengyie/apihub/relay/constant"
	"github.com/dengyie/apihub/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestNewTaskAPIRequestInheritsClientCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)

	upstream, err := newTaskAPIRequest(c, "https://provider.example/tasks", nil)
	require.NoError(t, err)
	cancel()

	require.ErrorIs(t, upstream.Context().Err(), context.Canceled)
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}

func TestProcessHeaderOverride_OpencodeSessionPlaceholder(t *testing.T) {
	t.Parallel()

	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			IsChannelTest: false,
			ChannelMeta: &relaycommon.ChannelMeta{
				HeadersOverride: map[string]any{
					"x-opencode-session": opencodeSessionPlaceholder,
				},
			},
		}
	}
	ctxFor := func(userID int) *gin.Context {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Set("id", userID)
		return ctx
	}

	headers, err := processHeaderOverride(newInfo(), ctxFor(7))
	require.NoError(t, err)
	first := headers["x-opencode-session"]
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, first)

	// 同一用户确定性：再次解析得到相同 UUID，保证上游会话粘性。
	headersAgain, err := processHeaderOverride(newInfo(), ctxFor(7))
	require.NoError(t, err)
	require.Equal(t, first, headersAgain["x-opencode-session"])

	// 不同用户得到不同 UUID。
	headersOther, err := processHeaderOverride(newInfo(), ctxFor(8))
	require.NoError(t, err)
	require.NotEqual(t, first, headersOther["x-opencode-session"])

	// 与指纹分支的 session_id 同源：同用户同值，渠道内会话标识一致。
	fpInfo := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.5",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI},
	}
	upstreamReq, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)
	applyUpstreamClientFingerprint(ctxFor(7), fpInfo, upstreamReq)
	require.Equal(t, first, upstreamReq.Header.Get("session_id"))
}

func TestIsClientCanceled(t *testing.T) {
	t.Parallel()

	// 1. context.Canceled 判定为客户端断开
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.True(t, isClientCanceled(nil, canceledCtx))
	assert.True(t, isClientCanceled(context.Canceled, context.Background()))

	// 2. DeadlineExceeded 绝不是客户端断开（属于网关侧预算/TTFT）
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer deadlineCancel()
	<-deadlineCtx.Done()
	assert.False(t, isClientCanceled(nil, deadlineCtx))
	assert.False(t, isClientCanceled(context.DeadlineExceeded, context.Background()))

	// 3. 一般网络错误
	assert.False(t, isClientCanceled(assert.AnError, context.Background()))
}

func TestToWebSocketURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.openai.com/v1/responses":             "wss://api.openai.com/v1/responses",
		"http://127.0.0.1:3000/v1/responses":              "ws://127.0.0.1:3000/v1/responses",
		"wss://chatgpt.com/backend-api/codex/responses":   "wss://chatgpt.com/backend-api/codex/responses",
		"ws://127.0.0.1:3000/backend-api/codex/responses": "ws://127.0.0.1:3000/backend-api/codex/responses",
	} {
		assert.Equal(t, want, toWebSocketURL(input), input)
	}
}

func TestDoRequest_HeaderTTFTTimeout(t *testing.T) {
	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer hangServer.Close()

	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Default.TTFTTimeoutMs = 80
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body"))

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 68,
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, hangServer.URL, strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.Error(t, err)
	require.Nil(t, resp)

	assert.True(t, loadbalancer.IsTTFTTimeout(err))
	var ttftErr *loadbalancer.TTFTTimeoutError
	require.ErrorAs(t, err, &ttftErr)
	assert.Equal(t, 68, ttftErr.ChannelID)
	assert.Equal(t, int64(80), ttftErr.TimeoutMs)
}

func TestDoRequest_IndependentHeaderTimeout(t *testing.T) {
	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer hangServer.Close()

	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	// TTFT 是 60 秒，但 HeaderTimeout 是 70 毫秒
	policy.Default.TTFTTimeoutMs = 60000
	policy.Default.HeaderTimeoutMs = 70
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body"))

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 99,
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, hangServer.URL, strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.Error(t, err)
	require.Nil(t, resp)

	assert.True(t, loadbalancer.IsTTFTTimeout(err))
	var ttftErr *loadbalancer.TTFTTimeoutError
	require.ErrorAs(t, err, &ttftErr)
	assert.Equal(t, 99, ttftErr.ChannelID)
	assert.Equal(t, int64(70), ttftErr.TimeoutMs)
}

func TestDoRequest_HeaderFastSuccess(t *testing.T) {
	fastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer fastServer.Close()

	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Default.TTFTTimeoutMs = 500
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body"))

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 75,
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, fastServer.URL, strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDoRequest_ClientCanceledReturnsClientAborted(t *testing.T) {
	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer hangServer.Close()

	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Default.TTFTTimeoutMs = 500
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	clientCtx, clientCancel := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body")).WithContext(clientCtx)

	time.AfterFunc(30*time.Millisecond, clientCancel)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 68,
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, hangServer.URL, strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.Error(t, err)
	require.Nil(t, resp)

	assert.False(t, loadbalancer.IsTTFTTimeout(err))
	assert.True(t, types.IsClientAbortedError(err))
}

func TestDoRequest_StreamingBodyReadAfterHeaderTimeoutCancel(t *testing.T) {
	streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer streamServer.Close()

	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Default.TTFTTimeoutMs = 500
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body"))

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 75,
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, streamServer.URL, strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	buf := make([]byte, 1024)
	n, readErr := resp.Body.Read(buf)
	// io.Reader is explicitly allowed to return data and io.EOF from the same
	// call, and the chunked decoder does exactly that here: the handler flushed
	// the payload and then returned, so the terminating zero-length chunk is
	// already sitting in the read buffer when this lands. Asserting readErr was
	// nil asserted a scheduling accident, not the behaviour under test -- it
	// failed 20 times out of 20 on a correct implementation. Production reads
	// through bufio.Scanner in relay/helper, which honours the (n>0, EOF) pair.
	if readErr != nil && !(errors.Is(readErr, io.EOF) && n > 0) {
		require.NoError(t, readErr)
	}
	assert.Equal(t, "data: hello\n\n", string(buf[:n]))
}

func TestDoRequest_UpstreamErrorWhenClientConnectedDoesNotAbort(t *testing.T) {
	policy := loadbalancer.DefaultPolicy()
	policy.Enabled = true
	policy.Default.TTFTTimeoutMs = 5000
	loadbalancer.SetPolicy(policy)
	defer loadbalancer.SetPolicy(nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("req-body"))

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 77,
		},
	}

	// 指向一个未监听的端口，产生常规的 dial 失败错误
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "http://127.0.0.1:54321/v1/chat", strings.NewReader("req-body"))
	require.NoError(t, err)

	resp, err := DoRequest(c, req, info)
	require.Error(t, err)
	require.Nil(t, resp)

	// 客户端依然在线，上游网络失败绝不能被判定为 ClientAbortedError (499)
	assert.False(t, types.IsClientAbortedError(err))
	assert.False(t, loadbalancer.IsTTFTTimeout(err))
}

func TestApplyChannelTestHeaders(t *testing.T) {
	t.Run("Codex test context sets official codex headers", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(channelTestUserAgentKey, "codex-cli/0.44.0")
		c.Set(channelTestIsCodexKey, true)

		req, err := http.NewRequest(http.MethodPost, "https://api666.zzzcoding.org/v1/responses", nil)
		require.NoError(t, err)

		info := &relaycommon.RelayInfo{
			IsChannelTest: true,
			RelayMode:     relayconstant.RelayModeResponses,
		}

		applyChannelTestHeaders(c, info, req)

		assert.Equal(t, "codex-cli/0.44.0", req.Header.Get("User-Agent"))
		assert.Equal(t, "codex_cli_rs", req.Header.Get("originator"))
		assert.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
		assert.Equal(t, "remote_compaction_v2", req.Header.Get("X-Codex-Beta-Features"))
		assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	})

	t.Run("Non-codex test does not set originator", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(channelTestUserAgentKey, "claude-code/2.0.0")

		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		require.NoError(t, err)

		info := &relaycommon.RelayInfo{
			IsChannelTest: true,
			RelayMode:     relayconstant.RelayModeChatCompletions,
		}

		applyChannelTestHeaders(c, info, req)

		assert.Equal(t, "claude-code/2.0.0", req.Header.Get("User-Agent"))
		assert.Empty(t, req.Header.Get("originator"))
		assert.Empty(t, req.Header.Get("OpenAI-Beta"))
		assert.Empty(t, req.Header.Get("X-Codex-Beta-Features"))
	})
}

func TestSetupApiRequestHeader_ForwardsCodexHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("X-Codex-Beta-Features", "remote_compaction_v2")

	info := &relaycommon.RelayInfo{
		IsStream:  true,
		RelayMode: relayconstant.RelayModeResponses,
	}

	headers := http.Header{}
	SetupApiRequestHeader(info, c, &headers)

	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "text/event-stream", headers.Get("Accept"))
	assert.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
	assert.Equal(t, "codex_cli_rs", headers.Get("originator"))
	assert.Equal(t, "remote_compaction_v2", headers.Get("X-Codex-Beta-Features"))
}

func TestApplyUpstreamClientFingerprint(t *testing.T) {
	codexClient := map[string]string{
		"User-Agent":        "codex_cli_rs/0.80.0 (Linux 6.8; x86_64) xterm",
		"originator":        "codex_cli_rs",
		"session_id":        "client-session",
		"X-Codex-Window-Id": "w1",
	}
	tests := []struct {
		name           string
		channelType    int
		channelBaseURL string
		model          string
		relayFormat    types.RelayFormat
		clientHeaders  map[string]string
		upstreamPreset map[string]string
		want           map[string]string
		wantPresent    []string
		wantAbsent     []string
	}{
		{
			name:          "codex client headers pass through even without affinity",
			channelType:   constant.ChannelTypeOpenAI,
			model:         "gpt-5.5",
			clientHeaders: codexClient,
			want: map[string]string{
				"User-Agent":            codexClient["User-Agent"],
				"originator":            "codex_cli_rs",
				"session_id":            "client-session",
				"X-Codex-Window-Id":     "w1",
				"OpenAI-Beta":           codexFingerprintOpenAIBeta,
				"X-Codex-Beta-Features": codexFingerprintBetaFeatures,
			},
			wantAbsent: []string{"x-opencode-session"},
		},
		{
			name:          "non codex client gets default codex fingerprint",
			channelType:   constant.ChannelTypeOpenAI,
			model:         "GPT-5.6-luna",
			clientHeaders: map[string]string{"User-Agent": "OpenAI/Python 1.99.0"},
			want: map[string]string{
				"User-Agent":            codexFingerprintUserAgent,
				"originator":            codexFingerprintOriginator,
				"OpenAI-Beta":           codexFingerprintOpenAIBeta,
				"X-Codex-Beta-Features": codexFingerprintBetaFeatures,
			},
			wantAbsent: []string{"x-opencode-session"},
		},
		{
			name:        "opencode sdk client keeps its explicit session header on gpt requests",
			channelType: constant.ChannelTypeOpenAI,
			model:       "gpt-5.5",
			clientHeaders: map[string]string{
				"User-Agent":         "OpenCode/1.0",
				"x-opencode-session": "client-opencode-session",
			},
			want: map[string]string{
				"User-Agent":            codexFingerprintUserAgent,
				"originator":            codexFingerprintOriginator,
				"OpenAI-Beta":           codexFingerprintOpenAIBeta,
				"X-Codex-Beta-Features": codexFingerprintBetaFeatures,
				"x-opencode-session":    "client-opencode-session",
			},
		},
		{
			name:           "header override keeps priority",
			channelType:    constant.ChannelTypeOpenAI,
			model:          "gpt-5.5",
			clientHeaders:  codexClient,
			upstreamPreset: map[string]string{"User-Agent": "override-ua"},
			want:           map[string]string{"User-Agent": "override-ua", "originator": "codex_cli_rs"},
		},
		{
			name:          "non gpt model keeps opencode session only",
			channelType:   constant.ChannelTypeOpenAI,
			model:         "claude-sonnet-4",
			clientHeaders: codexClient,
			wantAbsent:    []string{"originator", "session_id", "User-Agent"},
		},
		{
			name:          "gpt model on non openai channel keeps opencode session only",
			channelType:   constant.ChannelTypeAnthropic,
			model:         "gpt-5.5",
			clientHeaders: codexClient,
			wantAbsent:    []string{"originator", "session_id"},
		},
		{
			name:          "claude protocol request gets default claude code fingerprint",
			channelType:   constant.ChannelTypeAnthropic,
			model:         "claude-sonnet-5",
			relayFormat:   types.RelayFormatClaude,
			clientHeaders: map[string]string{"User-Agent": "OpenAI/Python 1.99.0"},
			want: map[string]string{
				"User-Agent":                  claudeFingerprintUserAgent,
				"anthropic-client-platform":   claudeFingerprintClientPlatform,
				"anthropic-version":           claudeFingerprintAnthropicVersion,
				"X-Stainless-Lang":            claudeFingerprintStainlessLang,
				"X-Stainless-Package-Version": claudeFingerprintStainlessPkgVer,
				"X-Stainless-OS":              claudeFingerprintStainlessOS,
				"X-Stainless-Arch":            claudeFingerprintStainlessArch,
				"X-Stainless-Runtime":         claudeFingerprintStainlessRuntime,
				"X-Stainless-Runtime-Version": claudeFingerprintStainlessRuntmVer,
				"X-App":                       claudeFingerprintApp,
				"X-Session-UUID":              UserSessionUUID(7),
			},
			wantPresent: []string{"X-Client-Request-Id"},
			wantAbsent:  []string{"x-opencode-session", "anthropic-beta"},
		},
		{
			name:        "real claude code client headers pass through on top of defaults",
			channelType: constant.ChannelTypeAnthropic,
			model:       "claude-sonnet-5",
			relayFormat: types.RelayFormatClaude,
			clientHeaders: map[string]string{
				"User-Agent":                  "claude-cli/2.1.290 (Darwin 25.0.0; arm64)",
				"X-Stainless-Package-Version": "0.41.0",
				"Anthropic-Beta":              "context-1m-2025-08-07",
				"X-Stainless-Retry-Count":     "0",
			},
			want: map[string]string{
				"User-Agent":                  "claude-cli/2.1.290 (Darwin 25.0.0; arm64)",
				"X-Stainless-Package-Version": "0.41.0",
				"Anthropic-Beta":              "context-1m-2025-08-07",
				"X-Stainless-Retry-Count":     "0",
				"anthropic-client-platform":   claudeFingerprintClientPlatform,
				"anthropic-version":           claudeFingerprintAnthropicVersion,
				"X-Session-UUID":              UserSessionUUID(7),
			},
			wantAbsent: []string{"x-opencode-session"},
		},
		{
			name:           "coding plan special base gets claude fingerprint and anthropic-version",
			channelType:    constant.ChannelTypeVolcEngine,
			channelBaseURL: "doubao-coding-plan",
			model:          "deepseek-v4-flash",
			relayFormat:    types.RelayFormatClaude,
			clientHeaders:  map[string]string{"User-Agent": "curl/8.7.1"},
			want: map[string]string{
				"User-Agent":                claudeFingerprintUserAgent,
				"anthropic-client-platform": claudeFingerprintClientPlatform,
				"anthropic-version":         claudeFingerprintAnthropicVersion,
				"X-Session-UUID":            UserSessionUUID(7),
			},
			wantPresent: []string{"X-Client-Request-Id"},
			wantAbsent:  []string{"x-opencode-session"},
		},
		{
			name:          "claude envelope on openai channel keeps opencode session only",
			channelType:   constant.ChannelTypeOpenAI,
			model:         "claude-sonnet-4",
			relayFormat:   types.RelayFormatClaude,
			clientHeaders: map[string]string{"User-Agent": "OpenAI/Python 1.99.0"},
			want:          map[string]string{"x-opencode-session": UserSessionUUID(7)},
			wantAbsent:    []string{"anthropic-client-platform", "User-Agent"},
		},
		{
			name:           "claude fingerprint header override keeps priority",
			channelType:    constant.ChannelTypeAnthropic,
			model:          "claude-sonnet-5",
			relayFormat:    types.RelayFormatClaude,
			clientHeaders:  map[string]string{},
			upstreamPreset: map[string]string{"User-Agent": "override-ua"},
			want:           map[string]string{"User-Agent": "override-ua", "anthropic-client-platform": claudeFingerprintClientPlatform},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("id", 7)
			for k, v := range tt.clientHeaders {
				c.Request.Header.Set(k, v)
			}
			req, err := http.NewRequest(http.MethodPost, "https://relay.example/v1/responses", nil)
			require.NoError(t, err)
			for k, v := range tt.upstreamPreset {
				req.Header.Set(k, v)
			}
			info := &relaycommon.RelayInfo{
				OriginModelName: tt.model,
				RelayFormat:     tt.relayFormat,
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: tt.channelType, ChannelBaseUrl: tt.channelBaseURL},
			}

			applyUpstreamClientFingerprint(c, info, req)

			for k, v := range tt.want {
				assert.Equal(t, v, req.Header.Get(k), k)
			}
			for _, k := range tt.wantPresent {
				assert.NotEmpty(t, req.Header.Get(k), k)
			}
			for _, k := range tt.wantAbsent {
				assert.Empty(t, req.Header.Get(k), k)
			}
			if usesClaudeFingerprint(info) {
				assert.Empty(t, req.Header.Get("x-opencode-session"))
				assert.NotEmpty(t, req.Header.Get("X-Session-UUID"))
			} else if usesCodexFingerprint(info) {
				assert.NotEmpty(t, req.Header.Get("session_id"))
			} else {
				assert.NotEmpty(t, req.Header.Get("x-opencode-session"))
			}
		})
	}
}
