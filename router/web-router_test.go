package router

import (
	"bytes"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

//go:embed testdata/webdist
var testBuildFS embed.FS

// writeBundle materialises a frontend bundle on disk. It always writes a full
// 12-file static tree and a real-length index.html, because LoadStaticBundle
// applies the same thresholds as the embedded-copy guard — a fixture that
// slipped past that check would prove nothing.
func writeBundle(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "static", "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<html><head><title>APIHub</title></head><body>" + marker +
		strings.Repeat("<p>filler content so the index clears the size gate</p>", 5) +
		"\n<!--umami-->\n<!--Google Analytics-->\n</body></html>\n"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		name := filepath.Join(dir, "static", "js", fmt.Sprintf("d%d.js", i))
		if err := os.WriteFile(name, []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newWebEngine(t *testing.T, staticDir string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// APIHUB_STATIC_DIR is read once, in main.go, and handed over as
	// WebAssets.StaticDir. The env var is cleared here so a stray value in the
	// developer's shell cannot silently redirect these tests at a real bundle.
	t.Setenv("APIHUB_STATIC_DIR", "")
	SetWebRouter(engine, WebAssets{
		BuildFS:   testBuildFS,
		IndexPage: mustRead(t, "testdata/webdist/index.html"),
		StaticDir: staticDir,
	}, func(c *gin.Context) { c.Next() })
	return engine
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func do(engine *gin.Engine, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSetWebRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("APIHUB_STATIC_DIR", "")
	t.Setenv("UMAMI_WEBSITE_ID", "site-abc")
	t.Setenv("GOOGLE_ANALYTICS_ID", "")

	t.Run("serves the index page and assets from disk when configured", func(t *testing.T) {
		dir := writeBundle(t, t.TempDir(), "DISK_BUNDLE")
		engine := newWebEngine(t, dir)

		rec := do(engine, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "DISK_BUNDLE") {
			t.Errorf("index page did not come from disk:\n%s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "EMBEDDED_INDEX") {
			t.Error("index page came from the embedded copy even though a disk bundle was configured")
		}
		// Analytics must be injected into the disk copy too, not just the
		// embedded one; otherwise the slots stay build-time comments.
		if !strings.Contains(rec.Body.String(), `data-website-id="site-abc"`) {
			t.Errorf("analytics was not injected into the disk index page:\n%s", rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control = %q, want no-cache", got)
		}

		rec = do(engine, "/static/js/d0.js")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "DISK_BUNDLE") {
			t.Errorf("GET /static/js/d0.js = %d %q, want the disk asset", rec.Code, rec.Body.String())
		}
	})

	t.Run("falls back to the embedded copy when the disk bundle is unusable", func(t *testing.T) {
		// A directory that exists but holds no real build is the 2026-10-03
		// blank-page shape; it must not become what users see.
		broken := t.TempDir()
		if err := os.WriteFile(filepath.Join(broken, "index.html"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		engine := newWebEngine(t, broken)

		rec := do(engine, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "EMBEDDED_INDEX") {
			t.Errorf("expected the embedded index page, got:\n%s", rec.Body.String())
		}
		if rec := do(engine, "/static/js/e1.js"); rec.Code != http.StatusOK {
			t.Errorf("GET embedded asset = %d, want 200 from the fallback layer", rec.Code)
		}
	})

	t.Run("missing disk directory falls back instead of failing to serve", func(t *testing.T) {
		engine := newWebEngine(t, filepath.Join(t.TempDir(), "does-not-exist"))

		if rec := do(engine, "/"); !strings.Contains(rec.Body.String(), "EMBEDDED_INDEX") {
			t.Errorf("expected the embedded index page, got %d:\n%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("empty StaticDir serves the embedded copy", func(t *testing.T) {
		engine := newWebEngine(t, "")

		rec := do(engine, "/")
		if !strings.Contains(rec.Body.String(), "EMBEDDED_INDEX") {
			t.Errorf("an unset APIHUB_STATIC_DIR must behave exactly as before, got:\n%s", rec.Body.String())
		}
		if rec := do(engine, "/static/js/e2.js"); rec.Code != http.StatusOK {
			t.Errorf("GET embedded asset = %d, want 200", rec.Code)
		}
	})

	t.Run("API and relay prefixes still 404 instead of returning the SPA", func(t *testing.T) {
		// The static fallback must not swallow a missing API route: a client
		// asking for /api/... has to get the relay 404, not an HTML page.
		engine := newWebEngine(t, writeBundle(t, t.TempDir(), "DISK_BUNDLE"))

		for _, path := range []string{"/api/status/nope", "/v1/nope", "/assets/nope.js"} {
			rec := do(engine, path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, rec.Code)
			}
			if strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
				t.Errorf("GET %s returned HTML; the SPA fallback ate a real 404", path)
			}
			if strings.Contains(rec.Body.String(), "DISK_BUNDLE") {
				t.Errorf("GET %s returned the index page body", path)
			}
			if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
				t.Errorf("GET %s Cache-Control = %q, want the 404 no-store policy", path, got)
			}
		}
	})

	t.Run("a client-side route still serves the index page", func(t *testing.T) {
		engine := newWebEngine(t, "")

		rec := do(engine, "/console/token")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "EMBEDDED_INDEX") {
			t.Errorf("GET /console/token = %d, want 200 with the SPA shell", rec.Code)
		}
	})
}

// TestIndexPageIsASnapshotWhileAssetsAreLive pins an asymmetry that is easy to
// get backwards: static assets are re-read from disk on every request, but
// index.html is read once at startup and held for the life of the process.
//
// The practical consequence is that replacing files inside a bound directory
// swaps the JS while leaving the old HTML in place -- producing either a
// console that silently never updates or a new index paired with old chunks.
// Publishing a frontend is therefore "point a slot at a new directory and
// restart it", which is what deploy.sh does per version. If this test ever
// needs to change, the README and the SetWebRouter comment change with it.
func TestIndexPageIsASnapshotWhileAssetsAreLive(t *testing.T) {
	dir := writeBundle(t, t.TempDir(), "BUILD_ONE")
	engine := newWebEngine(t, dir)

	if rec := do(engine, "/"); !strings.Contains(rec.Body.String(), "BUILD_ONE") {
		t.Fatalf("precondition: index should start from the on-disk bundle, got:\n%s", rec.Body.String())
	}

	// Rotate the bundle in place, exactly what the README used to invite.
	index := filepath.Join(dir, "index.html")
	page, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, bytes.ReplaceAll(page, []byte("BUILD_ONE"), []byte("BUILD_TWO")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "static", "js", "d0.js"), []byte("BUILD_TWO"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rec := do(engine, "/static/js/d0.js"); !strings.Contains(rec.Body.String(), "BUILD_TWO") {
		t.Errorf("static assets must be re-read from disk per request, got:\n%s", rec.Body.String())
	}
	if rec := do(engine, "/"); !strings.Contains(rec.Body.String(), "BUILD_ONE") {
		t.Errorf("index.html must stay the startup snapshot, but it changed:\n%s", rec.Body.String())
	}
}
