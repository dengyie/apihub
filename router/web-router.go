package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/controller"
	"github.com/dengyie/apihub/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the dashboard frontend: the copy embedded in this binary,
// and optionally a bundle published separately on disk.
//
// StaticDir is the per-slot frontend directory. It is deliberately per-slot
// rather than a single shared "current" symlink: the blue/green handover is
// process-scoped, so two processes serving one shared directory would hand
// some requests an index.html from the new build and some a script bundle
// from the old one. Binding each slot to its own version's directory keeps the
// two in step, and deploy.sh sets the path to match the binary in that slot.
//
// Empty StaticDir means "serve the embedded copy", which is the pre-existing
// behaviour and the safe setting: unsetting it disables separation entirely.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
	StaticDir string
}

// resolvedFrontend is what SetWebRouter actually serves: a validated asset
// source and the index page it came from.
//
// indexPage is read once, here, and held for the lifetime of the process. The
// asset files behind `files` are re-read from disk on every request, but this
// one is not -- see the comment on SetWebRouter before assuming otherwise.
type resolvedFrontend struct {
	files     static.ServeFileSystem
	indexPage []byte
}

// SetWebRouter wires the dashboard route. Everything about which copy to serve
// is decided here, once, at startup: a request never branches on where the
// frontend came from.
//
// One consequence is worth stating plainly, because it is easy to assume the
// opposite. Static assets are live -- http.Dir re-opens them per request -- but
// index.html is a startup snapshot. Replacing the files inside the bound
// directory therefore swaps the JS while leaving the old HTML in place, which
// yields either a console that silently never updates or a new index paired
// with old chunks. Publishing a frontend is consequently a matter of pointing a
// slot at a different directory and restarting it, which is exactly what
// deploy.sh does per version.
func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	embedded := common.EmbedFolder(assets.BuildFS, "web/dist")

	var disk static.ServeFileSystem
	indexPage := assets.IndexPage
	if dir := assets.StaticDir; dir != "" {
		if bundle, err := common.LoadStaticBundle(dir); err != nil {
			// A bad bundle must not take the console down: the binary still
			// carries a working copy, so say what happened and carry on.
			common.SysLog("frontend separation disabled: " + err.Error() +
				"; falling back to the copy embedded in this binary")
		} else {
			disk = common.DiskFolder(bundle.Dir)
			indexPage = bundle.IndexHTML
			common.SysLog("frontend served from " + bundle.Dir +
				" (separate build); the embedded copy remains the fallback")
		}
	}

	frontend := resolvedFrontend{
		files:     common.LayeredFileSystem(disk, embedded),
		indexPage: common.InjectAnalytics(indexPage),
	}

	router.NoRoute(
		pluginDispatcher,
		middleware.RouteTag("web"),
		gzip.Gzip(gzip.DefaultCompression),
		middleware.AccessTokenAudit(),
		middleware.GlobalWebRateLimit(),
		middleware.Cache(),
		static.Serve("/", frontend.files),
		func(c *gin.Context) {
			if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
				controller.RelayNotFound(c)
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", frontend.indexPage)
		},
	)
}
