package logger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPruneLogFilesKeepsNewestAndCurrent(t *testing.T) {
	names := []string{
		"oneapi-20261001000000.log",
		"oneapi-20261002000000.log",
		"oneapi-20261003000000.log",
		"oneapi-20261004000000.log",
		"goroutine-20261001-000000-1.txt",
		"apihub-green.out.log",
	}
	tests := []struct {
		name     string
		keep     int
		current  string
		expected []string
	}{
		{
			name:     "keeps newest including current",
			keep:     2,
			current:  "oneapi-20261004000000.log",
			expected: []string{"apihub-green.out.log", "goroutine-20261001-000000-1.txt", "oneapi-20261003000000.log", "oneapi-20261004000000.log"},
		},
		{
			// 时钟回拨时新文件名可能比旧文件小，current 仍不能被删。
			name:     "current survives even when it sorts oldest",
			keep:     2,
			current:  "oneapi-20261001000000.log",
			expected: []string{"apihub-green.out.log", "goroutine-20261001-000000-1.txt", "oneapi-20261001000000.log", "oneapi-20261004000000.log"},
		},
		{
			name:     "non-positive keep disables pruning",
			keep:     0,
			current:  "oneapi-20261004000000.log",
			expected: []string{"apihub-green.out.log", "goroutine-20261001-000000-1.txt", "oneapi-20261001000000.log", "oneapi-20261002000000.log", "oneapi-20261003000000.log", "oneapi-20261004000000.log"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, n := range names {
				require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644))
			}

			require.NoError(t, pruneLogFiles(dir, tt.keep, filepath.Join(dir, tt.current)))

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}
