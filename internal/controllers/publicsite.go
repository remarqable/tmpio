package controllers

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
)

// The public site is a directory of static files an operator drops beside the
// application: their own front page, about page, pricing, whatever they
// publish. It is served only to visitors who are not signed in, and only for
// paths the platform does not own, so it can never shadow sign-in, a sharing
// link, the API or an owner's site.
//
// The files run on the application's origin, which is what makes this
// convenient and also what makes it consequential: script served from here can
// act as a signed-in visitor. Whoever can write to the directory is trusted at
// the level of whoever deploys the application.

// publicSiteIndex is the file answered for "/".
const publicSiteIndex = "index.html"

// publicRootExceptions are platform paths a front page still expects to own.
// Search engines and browsers ask for these at the root, and a site that
// cannot answer them is not really a site.
var publicRootExceptions = map[string]bool{
	"robots.txt": true, "sitemap.xml": true, "favicon.ico": true, "llms.txt": true,
}

// servePublicSite answers the request from the operator's static directory and
// reports whether it did. Everything it declines falls through to the
// application unchanged.
func (d *Deps) servePublicSite(c *gin.Context) bool {
	if d.Cfg.PublicSiteDir == "" {
		return false
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		return false
	}
	// A signed-in visitor is here for their own site, not for marketing.
	if _, ok := middleware.GetPrincipal(c); ok {
		return false
	}
	name, ok := publicFilePath(c.Request.URL.Path)
	if !ok {
		return false
	}
	root := os.DirFS(d.Cfg.PublicSiteDir)
	candidates := []string{name}
	if !strings.Contains(path.Base(name), ".") {
		// Pretty URLs: /pricing is pricing.html or pricing/index.html.
		candidates = []string{name + ".html", path.Join(name, publicSiteIndex)}
	}
	for _, candidate := range candidates {
		f, err := root.Open(candidate)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			f.Close()
			continue
		}
		defer f.Close()
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			continue
		}
		d.publicSiteHeaders(c, candidate)
		http.ServeContent(c.Writer, c.Request, path.Base(candidate), info.ModTime(), rs)
		c.Abort()
		return true
	}
	return false
}

// publicFilePath maps a request path to a path inside the directory, or
// reports that the platform owns it. fs.ValidPath rejects traversal, absolute
// paths and empty segments, so the only way out of the directory is closed
// before any file is opened.
func publicFilePath(urlPath string) (string, bool) {
	clean := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if clean == "" || clean == "." {
		return publicSiteIndex, true
	}
	first := clean
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	if models.IsReservedRoot(first) && !publicRootExceptions[first] {
		return "", false
	}
	// An organization address is the application's, whatever else it looks like.
	if strings.HasPrefix(first, "o:") {
		return "", false
	}
	if !fs.ValidPath(clean) {
		return "", false
	}
	return clean, true
}

// publicSiteHeaders replaces the headers that assume application content.
func (d *Deps) publicSiteHeaders(c *gin.Context, name string) {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		c.Header("Content-Type", ct)
	}
	c.Header("X-Content-Type-Options", "nosniff")
	// Static files are public by definition; the application's no-store is for
	// tenant content and does not apply here.
	c.Header("Cache-Control", "public, max-age=300")
	if d.Cfg.PublicSiteCSP != "" {
		c.Header("Content-Security-Policy", d.Cfg.PublicSiteCSP)
	}
}
