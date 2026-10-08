package common

import (
	"embed"
	"io/fs"
	"net/http"
	"os"

	"github.com/gin-contrib/static"
)

// Credit: https://github.com/gin-contrib/static/issues/19

type embedFileSystem struct {
	http.FileSystem
}

func (e *embedFileSystem) Exists(prefix string, path string) bool {
	f, err := e.Open(path)
	if err != nil {
		return false
	}
	// Closed explicitly even though embed.FS hands back an in-memory file that
	// holds no OS descriptor. The sibling implementation over http.Dir opens a
	// real one, and leaving the close implicit here is what made the two copies
	// of this method differ in cost while reading as identical.
	f.Close()
	return true
}

func (e *embedFileSystem) Open(name string) (http.File, error) {
	if name == "/" {
		// This will make sure the index page goes to NoRouter handler,
		// which will use the replaced index bytes with analytic codes.
		return nil, os.ErrNotExist
	}
	return e.FileSystem.Open(name)
}

func EmbedFolder(fsEmbed embed.FS, targetPath string) static.ServeFileSystem {
	efs, err := fs.Sub(fsEmbed, targetPath)
	if err != nil {
		panic(err)
	}
	return &embedFileSystem{
		FileSystem: http.FS(efs),
	}
}

// diskFileSystem serves a directory from the filesystem using the same trick as
// embedFileSystem: opening "/" must fail so the request falls through to the
// handler that serves the analytics-injected index page rather than the raw
// file straight off disk.
type diskFileSystem struct {
	http.FileSystem
}

func (d *diskFileSystem) Exists(prefix string, path string) bool {
	f, err := d.Open(path)
	if err != nil {
		return false
	}
	// This close is load-bearing, not stylistic. Unlike the embed.FS layer, http.Dir
	// returns an *os.File backed by a real OS descriptor, and gin-contrib/static
	// calls Exists once per request before it opens the file again to serve it.
	// Dropping the handle here would leave one descriptor outstanding per static
	// request, to be reclaimed only by GC's finalizer -- so the process would hold a
	// transient backlog of descriptors proportional to request rate between GC
	// cycles.
	f.Close()
	return true
}

func (d *diskFileSystem) Open(name string) (http.File, error) {
	if name == "/" {
		// Same reason as embedFileSystem.Open: force "/" onto the index handler.
		return nil, os.ErrNotExist
	}
	return d.FileSystem.Open(name)
}

// DiskFolder returns a ServeFileSystem rooted at dir. dir is used as given; the
// caller is responsible for having validated it.
func DiskFolder(dir string) static.ServeFileSystem {
	return &diskFileSystem{FileSystem: http.Dir(dir)}
}

// layeredFileSystem serves from a published on-disk bundle when one is
// configured and readable, and otherwise from the copy embedded in the binary.
//
// The disk layer is optional because an operator can always unset
// APIHUB_STATIC_DIR to fall back to pure embedded serving, and because a
// bundle that fails validation at startup must not take the whole UI down with
// it.
type layeredFileSystem struct {
	disk  static.ServeFileSystem
	embed static.ServeFileSystem
}

func LayeredFileSystem(disk static.ServeFileSystem, embedded static.ServeFileSystem) static.ServeFileSystem {
	return &layeredFileSystem{disk: disk, embed: embedded}
}

func (l *layeredFileSystem) Exists(prefix string, path string) bool {
	if l.disk != nil && l.disk.Exists(prefix, path) {
		return true
	}
	return l.embed.Exists(prefix, path)
}

func (l *layeredFileSystem) Open(name string) (http.File, error) {
	// Disk first, and the order must not be reversed. The published bundle is the
	// newer artifact by definition -- that is the whole point of separating the
	// frontend -- so falling through to the embedded copy first would make the
	// published bundle unreachable for any path the binary also happens to carry.
	// The failure mode is silent: every request still returns 200 from a healthy
	// process, the console just never updates again.
	if l.disk != nil {
		if f, err := l.disk.Open(name); err == nil {
			return f, nil
		}
	}
	return l.embed.Open(name)
}
