package common

import (
	"embed"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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

// TestExistsDoesNotLeakFileDescriptors guards a defect that no behavioural test
// could have caught: Exists opened a file and threw the handle away. Over
// http.Dir that is a real OS descriptor, and gin-contrib/static calls Exists once
// per request, so every static request left one outstanding until GC's finalizer
// happened to run.
//
// Measuring the descriptor count is platform-specific, so this skips where the
// count cannot be read rather than pretending to have verified the fix.
func TestExistsDoesNotLeakFileDescriptors(t *testing.T) {
	// /proc/self/fd on Linux, /dev/fd on Darwin. os.ReadDir is not usable on
	// /dev/fd: it stats entries that the directory handle has already closed,
	// so Readdirnames is the portable way to count.
	fdDir := "/proc/self/fd"
	if _, err := os.Stat(fdDir); err != nil {
		fdDir = "/dev/fd"
	}
	openDescriptors := func() (int, bool) {
		d, err := os.Open(fdDir)
		if err != nil {
			return 0, false
		}
		defer d.Close()
		names, err := d.Readdirnames(-1)
		if err != nil && len(names) == 0 {
			return 0, false
		}
		// The handle just opened is itself one of the entries.
		return len(names) - 1, true
	}

	if _, ok := openDescriptors(); !ok {
		t.Skipf("cannot count file descriptors under %s on this platform", fdDir)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	disk := DiskFolder(dir)
	layered := LayeredFileSystem(disk, embedded())

	// Warm up so one-off allocations are not counted as growth, then force a GC
	// so the measurement starts from a settled baseline rather than from
	// whatever the previous test happened to leave behind.
	for i := 0; i < 200; i++ {
		disk.Exists("", "/a.js")
	}
	runtime.GC()
	base, _ := openDescriptors()

	const iterations = 5000
	for i := 0; i < iterations; i++ {
		disk.Exists("", "/a.js")
	}
	afterDisk, _ := openDescriptors()
	if grew := afterDisk - base; grew > 50 {
		t.Errorf("DiskFolder.Exists leaked descriptors: %d -> %d after %d calls",
			base, afterDisk, iterations)
	}

	// The layered view opens the disk layer first, so it leaked at least as much.
	runtime.GC()
	baseLayered, _ := openDescriptors()
	for i := 0; i < iterations; i++ {
		layered.Exists("", "/a.js")
	}
	afterLayered, _ := openDescriptors()
	if grew := afterLayered - baseLayered; grew > 50 {
		t.Errorf("LayeredFileSystem.Exists leaked descriptors: %d -> %d after %d calls",
			baseLayered, afterLayered, iterations)
	}
}
