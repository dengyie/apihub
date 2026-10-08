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
	_, err := e.Open(path)
	if err != nil {
		return false
	}
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
	_, err := d.Open(path)
	return err == nil
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
	if l.disk != nil {
		if f, err := l.disk.Open(name); err == nil {
			return f, nil
		}
	}
	return l.embed.Open(name)
}
