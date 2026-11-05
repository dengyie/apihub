package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteGoroutineDumpWritesFullStacksIntoDir(t *testing.T) {
	dir := t.TempDir()

	path, err := WriteGoroutineDump(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, filepath.Dir(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// debug=2 是 panic 式的完整栈格式，能看到每个 goroutine 的阻塞原因与时长。
	assert.Contains(t, string(data), "goroutine ")
	assert.Contains(t, string(data), "TestWriteGoroutineDumpWritesFullStacksIntoDir")
}
