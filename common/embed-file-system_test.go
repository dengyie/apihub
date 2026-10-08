package common

import (
	"embed"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-contrib/static"
)

//go:embed testdata/embedfs
var testEmbedFS embed.FS

//go:embed testdata/embedfs/index.html
var testEmbedIndex []byte

func embedded() static.ServeFileSystem {
	return EmbedFolder(testEmbedFS, "testdata/embedfs")
}

func openAndRead(t *testing.T, fsys static.ServeFileSystem, path string) string {
	t.Helper()
	f, err := fsys.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(http.File(f))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLayeredFileSystem(t *testing.T) {
	t.Run("serves the embedded copy when no disk layer is configured", func(t *testing.T) {
		fsys := LayeredFileSystem(nil, embedded())
		if !fsys.Exists("", "/static/js/embedded.js") {
			t.Fatal("embedded file should exist")
		}
	})

	t.Run("disk wins when the same path exists in both layers", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "static", "js"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "static", "js", "embedded.js"),
			[]byte("DISK_VERSION"), 0o644); err != nil {
			t.Fatal(err)
		}

		fsys := LayeredFileSystem(DiskFolder(dir), embedded())
		if got := openAndRead(t, fsys, "/static/js/embedded.js"); got != "DISK_VERSION" {
			t.Errorf("expected the disk copy to win, got %q", got)
		}
	})

	t.Run("falls back to the embedded copy for files only the binary has", func(t *testing.T) {
		dir := t.TempDir()
		fsys := LayeredFileSystem(DiskFolder(dir), embedded())
		if !fsys.Exists("", "/static/js/embed-only.js") {
			t.Fatal("a file absent from disk must still resolve from the embedded layer")
		}
	})

	t.Run("does not resolve a path neither layer has", func(t *testing.T) {
		dir := t.TempDir()
		fsys := LayeredFileSystem(DiskFolder(dir), embedded())
		if fsys.Exists("", "/static/js/nope.js") {
			t.Error("a path in neither layer must not exist")
		}
	})

	t.Run("root must miss so the index handler runs", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "index.html"), testEmbedIndex, 0o644); err != nil {
			t.Fatal(err)
		}
		// If "/" resolved, gin's static middleware would serve the raw file
		// and the analytics-injected page would never be reached.
		if DiskFolder(dir).Exists("", "/") {
			t.Error("the disk layer must not resolve /")
		}
		if LayeredFileSystem(DiskFolder(dir), embedded()).Exists("", "/") {
			t.Error("the layered view must not resolve / either")
		}
	})
}
