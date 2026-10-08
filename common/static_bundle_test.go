package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBundle lays out a directory shaped like a real frontend build: an
// index.html above the size floor and a static/ tree with enough files.
func writeBundle(t *testing.T, dir string, indexBytes int, staticFiles int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "static", "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<!doctype html><html><head>"
	if indexBytes > 0 {
		body += strings.Repeat("x", indexBytes)
	}
	body += "</head><body></body></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < staticFiles; i++ {
		p := filepath.Join(dir, "static", "js", fmt.Sprintf("index.%d.js", i))
		if err := os.WriteFile(p, []byte("// chunk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadStaticBundle(t *testing.T) {
	t.Run("accepts a well-formed bundle", func(t *testing.T) {
		dir := t.TempDir()
		writeBundle(t, dir, 512, 12)
		b, err := LoadStaticBundle(dir)
		if err != nil {
			t.Fatalf("expected the bundle to load, got %v", err)
		}
		if !strings.HasPrefix(b.Dir, "/") {
			t.Errorf("Dir should be absolute, got %q", b.Dir)
		}
		if len(b.IndexHTML) == 0 {
			t.Error("IndexHTML should carry the raw index page")
		}
	})

	t.Run("rejects a placeholder index, the 2026-10-03 blank-page mode", func(t *testing.T) {
		dir := t.TempDir()
		writeBundle(t, dir, 0, 12)
		_, err := LoadStaticBundle(dir)
		if err == nil {
			t.Fatal("a 60-byte index.html must not be accepted")
		}
		if !strings.Contains(err.Error(), "index.html is only") {
			t.Errorf("error should name the size problem, got %v", err)
		}
	})

	t.Run("rejects a bundle with too few static files", func(t *testing.T) {
		dir := t.TempDir()
		writeBundle(t, dir, 512, 3)
		_, err := LoadStaticBundle(dir)
		if err == nil {
			t.Fatal("3 static files must not be accepted")
		}
		if !strings.Contains(err.Error(), "static tree has only") {
			t.Errorf("error should name the static problem, got %v", err)
		}
	})

	t.Run("reports a missing directory rather than panicking", func(t *testing.T) {
		_, err := LoadStaticBundle(filepath.Join(t.TempDir(), "nope"))
		if err == nil {
			t.Fatal("a missing directory must be an error, not a panic")
		}
	})

	t.Run("rejects an empty setting", func(t *testing.T) {
		if _, err := LoadStaticBundle(""); err == nil {
			t.Fatal("an empty dir must be an error")
		}
	})
}
