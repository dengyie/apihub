package operation_setting

import (
	"fmt"
	"sync"
	"testing"

	"github.com/dengyie/apihub/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestParseHTTPStatusCodeRanges_CommaSeparated(t *testing.T) {
	ranges, err := ParseHTTPStatusCodeRanges("401,403,500-599")
	require.NoError(t, err)
	require.Equal(t, []StatusCodeRange{
		{Start: 401, End: 401},
		{Start: 403, End: 403},
		{Start: 500, End: 599},
	}, ranges)
}

func TestParseHTTPStatusCodeRanges_MergeAndNormalize(t *testing.T) {
	ranges, err := ParseHTTPStatusCodeRanges("500-505,504,401,403,402")
	require.NoError(t, err)
	require.Equal(t, []StatusCodeRange{
		{Start: 401, End: 403},
		{Start: 500, End: 505},
	}, ranges)
}

func TestParseHTTPStatusCodeRanges_Invalid(t *testing.T) {
	_, err := ParseHTTPStatusCodeRanges("99,600,foo,500-400,500-")
	require.Error(t, err)
}

func TestParseHTTPStatusCodeRanges_NoComma_IsInvalid(t *testing.T) {
	_, err := ParseHTTPStatusCodeRanges("401 403")
	require.Error(t, err)
}

func TestShouldDisableByStatusCode(t *testing.T) {
	orig := AutomaticDisableStatusCodesToString()
	t.Cleanup(func() { require.NoError(t, AutomaticDisableStatusCodesFromString(orig)) })

	require.NoError(t, AutomaticDisableStatusCodesFromString("401-403,500-599"))

	require.True(t, ShouldDisableByStatusCode(401))
	require.True(t, ShouldDisableByStatusCode(403))
	require.False(t, ShouldDisableByStatusCode(404))
	require.True(t, ShouldDisableByStatusCode(500))
	require.False(t, ShouldDisableByStatusCode(200))
}

func TestShouldRetryByStatusCode(t *testing.T) {
	orig := AutomaticRetryStatusCodesToString()
	t.Cleanup(func() { require.NoError(t, AutomaticRetryStatusCodesFromString(orig)) })

	require.NoError(t, AutomaticRetryStatusCodesFromString("429,500-599"))

	require.True(t, ShouldRetryByStatusCode(429))
	require.True(t, ShouldRetryByStatusCode(500))
	require.True(t, ShouldRetryByStatusCode(504))
	require.True(t, ShouldRetryByStatusCode(524))
	require.False(t, ShouldRetryByStatusCode(400))
	require.False(t, ShouldRetryByStatusCode(200))
}

func TestShouldRetryByStatusCode_DefaultMatchesLegacyBehavior(t *testing.T) {
	require.False(t, ShouldRetryByStatusCode(200))
	require.False(t, ShouldRetryByStatusCode(400))
	require.True(t, ShouldRetryByStatusCode(401))
	require.False(t, ShouldRetryByStatusCode(408))
	require.True(t, ShouldRetryByStatusCode(429))
	require.True(t, ShouldRetryByStatusCode(500))
	// 504/524 曾随上游遗留实现对慢上游网关超时（Cloudflare 504/524）永不重试。
	// 生产实证（v29.3 部署后 1 小时 15 次终态 502）证明这类失败换渠道即可恢复，
	// 且客户端断开守卫与即时熔断已封住双倍等待的代价，故放开为可重试。
	require.True(t, ShouldRetryByStatusCode(504))
	require.True(t, ShouldRetryByStatusCode(524))
	require.True(t, ShouldRetryByStatusCode(599))
}

func TestIsAlwaysSkipRetryStatusCode(t *testing.T) {
	// 必跳过清单默认为空：不再有绕过一切启发式的状态码。保留机制供将来配置。
	require.False(t, IsAlwaysSkipRetryStatusCode(504))
	require.False(t, IsAlwaysSkipRetryStatusCode(524))
	require.False(t, IsAlwaysSkipRetryStatusCode(500))
}

// TestStatusCodeRulesConcurrentAccess pins the atomicity of the status-code
// rule snapshot.
//
// The rules are written at runtime through the admin option API and read on
// the relay hot path (every failed request walks DecideRelayRetry). They used
// to be plain package variables: writes happened under OptionMapRWMutex, but
// hot-path readers took no lock at all — a genuine data race the race detector
// flags the moment this test runs against the old implementation.
func TestStatusCodeRulesConcurrentAccess(t *testing.T) {
	origRetry := AutomaticRetryStatusCodesToString()
	origDisable := AutomaticDisableStatusCodesToString()
	t.Cleanup(func() {
		require.NoError(t, AutomaticRetryStatusCodesFromString(origRetry))
		require.NoError(t, AutomaticDisableStatusCodesFromString(origDisable))
	})

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				require.NoError(t, AutomaticRetryStatusCodesFromString(fmt.Sprintf("%d,%d-599", 500+w, 505+i%50)))
				require.NoError(t, AutomaticDisableStatusCodesFromString("401,403"))
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				// 读取结果本身无所谓：这一步在 -race 下验证的是
				// 「读侧永远看到完整一致的快照，绝不与写侧撕裂」。
				ShouldRetryByStatusCode(500)
				ShouldRetryByStatusCode(504)
				ShouldDisableByStatusCode(401)
				IsAlwaysSkipRetryStatusCode(504)
				IsAlwaysSkipRetryCode(types.ErrorCodeBadResponseBody)
				AutomaticRetryStatusCodesToString()
			}
		}()
	}
	wg.Wait()

	// 并发写全部落地后，最后一次写必须可见。
	require.NoError(t, AutomaticRetryStatusCodesFromString("504,524"))
	require.True(t, ShouldRetryByStatusCode(504))
	require.True(t, ShouldRetryByStatusCode(524))
}
