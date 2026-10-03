package loadbalancer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withPolicyFile 把 policyPath 指到一个临时文件，跑 fn 后恢复原状。
func withPolicyFile(t *testing.T, content string, fn func(path string)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	prevPath := policyPath
	prevMtime := policyMtime
	policyPath = path
	policyMtime = time.Time{}
	t.Cleanup(func() {
		policyPath = prevPath
		policyMtime = prevMtime
	})
	fn(path)
}

func TestPolicyReliableDefaultsTrue(t *testing.T) {
	if !PolicyReliable() {
		t.Fatal("未发生首次加载失败时 PolicyReliable 应为 true")
	}
}

// 回归：一次启动期的配置读取故障曾经让 PolicyReliable() 在整个进程生命周期内
// 恒为 false —— 自动禁用就此永久静默失效，熔断却照常工作，外部看不出异常。
// 标志的语义是「当前生效的策略不是从文件读出来的」，所以读成功一次就必须恢复。
func TestPolicyReliableRecoversAfterSuccessfulReload(t *testing.T) {
	restore := MarkPolicyLoadFailedForTest(true)
	defer restore()

	require.False(t, PolicyReliable(), "前置条件：模拟首次加载失败")

	withPolicyFile(t, "enabled: true\nmax_retries: 2\n", func(string) {
		require.NoError(t, reload())
		assert.True(t, PolicyReliable(),
			"热加载成功后 PolicyReliable 必须恢复，否则自动禁用永久静默失效")
		assert.True(t, GetPolicy().Enabled)
	})
}

// 读失败是瞬时的：不得推进 mtime，否则一次抖动就会把「文件刚被写好」这个
// 状态永久跳过，再也不会重试。
func TestReloadReadFailureDoesNotAdvanceMtime(t *testing.T) {
	restore := MarkPolicyLoadFailedForTest(false)
	defer restore()

	withPolicyFile(t, "enabled: true\n", func(path string) {
		// 先成功一次，让 mtime 有一个非零基准
		require.NoError(t, reload())
		baseline := policyMtime
		require.False(t, baseline.IsZero())

		require.NoError(t, os.Remove(path))
		require.Error(t, reload(), "文件已删除时 reload 必须报错")
		assert.Equal(t, baseline, policyMtime,
			"读失败不得推进 mtime：文件可能几秒后就恢复可读")
	})
}

// 解析失败是确定性的：坏 yaml 不会自己变好，必须推进 mtime 把重试压到
// 「文件再次被编辑」为止。此前不推进，于是每 5 秒刷一行日志、一天一万七千行。
func TestReloadParseFailureAdvancesMtime(t *testing.T) {
	restore := MarkPolicyLoadFailedForTest(false)
	defer restore()

	withPolicyFile(t, "enabled: true\n", func(path string) {
		require.NoError(t, reload())

		bad := "enabled: true\nchannels: [ this is not: valid yaml\n"
		require.NoError(t, os.WriteFile(path, []byte(bad), 0o600))

		require.Error(t, reload(), "坏 yaml 必须报错")
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.False(t, fi.ModTime().After(policyMtime),
			"解析失败必须推进 mtime，否则 watchLoop 每 5 秒空转一次")

		// 上一份可用策略必须原样保留，不能因为新文件坏了就退回默认策略。
		assert.True(t, GetPolicy().Enabled,
			"热加载失败时必须保留上一份可用策略")
	})
}

func TestReloadSuccessResetsFailureCounter(t *testing.T) {
	restore := MarkPolicyLoadFailedForTest(false)
	defer restore()

	withPolicyFile(t, "enabled: true\n", func(path string) {
		require.NoError(t, reload())

		require.NoError(t, os.WriteFile(path, []byte("channels: [oops\n"), 0o600))
		require.Error(t, reload())
		assert.EqualValues(t, 1, policyReloadFailures.Load(),
			"解析失败必须累加连续失败计数")

		require.NoError(t, os.WriteFile(path, []byte("enabled: true\n"), 0o600))
		require.NoError(t, reload())
		assert.EqualValues(t, 0, policyReloadFailures.Load(),
			"成功一次即清零，计数只用于把失败日志降频")
	})
}
