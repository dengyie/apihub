package common

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Integrity floors for a built frontend bundle. A real build produces an
// index.html of roughly 1KB (1047 bytes measured in production) and a
// static/ tree with well over ten files.
//
// These same numbers are the ones that guard the embedded copy at build time;
// they are defined once here so the on-disk bundle cannot be judged by a
// different, laxer standard than the binary's fallback. See main.go's
// verifyEmbeddedFrontend for how that failure presented itself: a placeholder
// index.html builds fine, starts fine, answers 200, and renders a blank page.
const (
	MinIndexBytes  = 200
	MinStaticFiles = 10
)

// StaticBundle is a frontend build published on disk next to the binary,
// independent of the copy compiled into it.
type StaticBundle struct {
	// Dir is the absolute directory holding index.html and static/.
	Dir string
	// IndexHTML is the raw index.html, before analytics injection.
	IndexHTML []byte
}

// LoadStaticBundle reads and validates the bundle in dir.
//
// Every failure is returned rather than silently ignored, because the caller
// falls back to the embedded copy and an operator needs to know why the
// separated frontend is not the one being served.
func LoadStaticBundle(dir string) (*StaticBundle, error) {
	if dir == "" {
		return nil, fmt.Errorf("no static directory configured")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dir, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("static dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("static dir %s is not a directory", abs)
	}

	index, err := os.ReadFile(filepath.Join(abs, "index.html"))
	if err != nil {
		return nil, fmt.Errorf("read %s/index.html: %w", abs, err)
	}
	if len(index) < MinIndexBytes {
		return nil, fmt.Errorf("index.html is only %d bytes (need >= %d)", len(index), MinIndexBytes)
	}

	staticDir := filepath.Join(abs, "static")
	count := 0
	_ = filepath.WalkDir(staticDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			count++
		}
		return nil
	})
	if count < MinStaticFiles {
		return nil, fmt.Errorf("static tree has only %d files (need >= %d)", count, MinStaticFiles)
	}

	return &StaticBundle{Dir: abs, IndexHTML: index}, nil
}
