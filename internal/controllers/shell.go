package controllers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
)

// NavLink is one entry of the admin navigation.
type NavLink struct {
	Title  string
	URL    string
	Active bool
	Key    string
	Icon   string
}

// adminNav lists the admin sections; nothing content-related lives here.
// adminNav lists the admin sections. The server section appears only for the
// account that administers the installation, because what it holds — the model
// credential that pays for every organization's calls — belongs to whoever runs
// the server rather than to any one owner.
func adminNav(active string, instanceAdmin bool) []NavLink {
	items := []NavLink{
		{Key: "overview", Title: i18n.T("en", "admin.overview"), URL: "/admin", Icon: "home"},
		{Key: "connections", Title: i18n.T("en", "nav.connections"), URL: "/admin/connections", Icon: "plug"},
		{Key: "links", Title: i18n.T("en", "admin.links"), URL: "/admin/links", Icon: "link"},
		{Key: "settings", Title: i18n.T("en", "nav.settings"), URL: "/admin/settings", Icon: "settings"},
		{Key: "export", Title: i18n.T("en", "nav.export"), URL: "/admin/export", Icon: "export"},
	}
	if instanceAdmin {
		items = append(items, NavLink{Key: "server", Title: i18n.T("en", "server.title"), URL: "/admin/server", Icon: "settings"})
	}
	for i := range items {
		items[i].Active = items[i].Key == active
	}
	return items
}

// RequireOwner gates owner operations and the admin area on an owner browser session.
func (d *Deps) RequireOwner() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, _, _, _, ok := middleware.OwnerSession(c); !ok {
			if isNavigation(c) {
				d.redirectToLogin(c)
				c.Abort()
				return
			}
			jsonError(c, errors.New(errors.CodeUnauthorized, "sign in required"))
			return
		}
		middleware.NoStore(c)
		c.Next()
	}
}

func owner(c *gin.Context) (models.Principal, *models.Tenant) {
	p, _, _, t, _ := middleware.OwnerSession(c)
	return p, t
}

// ownerShell builds the site view model for the signed-in owner in the
// personal address form, ready for the shared shell layout.
func (d *Deps) ownerShell(c *gin.Context, mode string) (*SiteView, *addr, error) {
	p, t := owner(c)
	a := &addr{Tenant: t, Prefix: "", Path: c.Request.URL.Path, Prin: p}
	sv, err := d.siteModel(c, a)
	if err != nil {
		return nil, nil, err
	}
	sv.Mode = mode
	sv.Wide = mode != "site"
	if mode == "admin" {
		_, _, u, _, _ := middleware.OwnerSession(c)
		sv.AdminNav = adminNav(c.GetString("admin_active"), u != nil && u.IsInstanceAdmin)
	}
	return sv, a, nil
}

// opEntry resolves the wildcard remainder of a verb URL to a live entry.
// The remainder is a rendered path: /research/circle, /research/, /tmp.yaml, /assets/x.png.
func (d *Deps) opEntry(c *gin.Context, a *addr) (*models.Entry, bool) {
	rest := c.Param("rest")
	if rest == "" {
		rest = "/"
	}
	rendered, err := models.RequestPath(rest)
	if err != nil {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return nil, false
	}
	res, err := d.Ops.Resolve(c.Request.Context(), a.Tenant.ID, rendered)
	if err != nil {
		d.fail(c, err)
		return nil, false
	}
	if res.Redirect != "" {
		verb := strings.SplitN(strings.TrimPrefix(c.Request.URL.Path, "/"), "/", 2)[0]
		c.Redirect(http.StatusTemporaryRedirect, "/"+verb+res.Redirect)
		return nil, false
	}
	if res.Directory != nil {
		return res.Directory, true
	}
	return res.Entry, true
}

// opDir resolves the remainder of /new/... and /upload/... to a directory source path.
func opDir(c *gin.Context) string {
	rest := c.Param("rest")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "/"
	}
	return rest
}

// verbURL builds an owner operation URL for an entry.
func verbURL(verb string, e *models.Entry) string {
	switch e.Kind {
	case models.KindDirectory:
		return "/" + verb + e.HTMLPath()
	case models.KindPage:
		return "/" + verb + e.HTMLPath()
	default:
		return "/" + verb + e.Path
	}
}

// LegacyAppRedirect maps the old /app dashboard URLs to their new homes.
func (d *Deps) LegacyAppRedirect(c *gin.Context) {
	middleware.NoStore(c)
	rest := strings.TrimPrefix(c.Param("rest"), "/")
	path := c.Query("path")
	target := "/admin"
	switch rest {
	case "", "/":
		target = "/admin"
	case "content":
		// The query value reaches Location, so it must be a local path. "//evil"
		// and "/\evil" are protocol-relative and would leave the origin.
		dir := safeReturnPath(c.DefaultQuery("path", "/"))
		if dir == "" {
			dir = "/"
		}
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		target = dir
	case "edit":
		target = "/edit" + renderedOf(path)
	case "history":
		if path == "" {
			target = "/admin"
		} else {
			target = "/history" + renderedOf(path)
		}
	case "share":
		target = "/share" + renderedOf(path)
	case "new":
		target = "/new" + c.DefaultQuery("dir", "/")
	case "trash":
		target = "/trash"
	case "format":
		target = "/format"
	case "settings":
		target = "/admin/settings"
	case "connections":
		target = "/admin/connections"
	case "export":
		target = "/admin/export"
	}
	c.Redirect(http.StatusMovedPermanently, target)
}

// renderedOf converts a source path (query param) to its rendered path.
func renderedOf(p string) string {
	if p == "" {
		return "/"
	}
	e := models.Entry{Kind: models.KindPage, Path: p}
	if strings.HasSuffix(p, ".md") {
		return e.HTMLPath()
	}
	return p
}
