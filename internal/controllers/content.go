package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
)

// addr describes the resolved address form of a content request.
type addr struct {
	Tenant   *models.Tenant
	Prefix   string // "" personal, "/o:CODE" explicit
	Explicit bool
	Path     string // site-relative decoded path (may end with /)
	Prin     models.Principal
}

// Content is the catch-all for site content in both address forms: HTML,
// raw Markdown, JSON, assets, config and search.
func (d *Deps) Content(c *gin.Context) {
	middleware.NoStore(c)
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		if wantsJSON(c) {
			jsonError(c, errors.New(errors.CodeValidationFailed, "content URLs support GET and HEAD only"))
			return
		}
		c.String(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a, ok := d.resolveAddress(c)
	if !ok {
		return
	}
	d.serveContent(c, a)
}

// resolveAddress applies the section 4 resolution order and authorization.
func (d *Deps) resolveAddress(c *gin.Context) (*addr, bool) {
	reqPath, err := models.RequestPath(c.Request.URL.EscapedPath())
	if err != nil {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return nil, false
	}
	code, rest, explicit := models.SplitOrgPrefix(reqPath)
	prin, hasPrin := middleware.GetPrincipal(c)
	a := &addr{Path: rest, Explicit: explicit}
	if explicit {
		if code == "" {
			d.fail(c, errors.New(errors.CodeNotFound, ""))
			return nil, false
		}
		// Answer an anonymous browser before looking the code up. Deciding after
		// the lookup would redirect for a code that exists and 404 for one that
		// does not, which tells a stranger which organization codes are real.
		if !hasPrin && isNavigation(c) {
			d.redirectToLogin(c)
			return nil, false
		}
		t, err := models.GetTenantByCode(c.Request.Context(), code)
		if err != nil {
			d.fail(c, errors.New(errors.CodeNotFound, ""))
			return nil, false
		}
		if !hasPrin || prin.TenantID != t.ID || prin.IsShare() {
			// Unauthorized explicit org URLs are indistinguishable from unknown ones.
			d.fail(c, errors.New(errors.CodeNotFound, ""))
			return nil, false
		}
		a.Tenant, a.Prefix, a.Prin = t, "/o:"+t.Code, prin
		return a, true
	}
	a.Path = reqPath
	if !hasPrin || prin.IsShare() {
		if isNavigation(c) {
			d.redirectToLogin(c)
			return nil, false
		}
		c.Header("WWW-Authenticate", `Bearer realm="tmp"`)
		jsonError(c, errors.New(errors.CodeUnauthorized, "authentication required"))
		return nil, false
	}
	t, err := models.GetTenant(c.Request.Context(), prin.TenantID)
	if err != nil {
		d.fail(c, err)
		return nil, false
	}
	a.Tenant, a.Prefix, a.Prin = t, "", prin
	return a, true
}

// ownerVerbs are the operation prefixes handled by explicit routes in the personal form.
var ownerVerbs = map[string]bool{"edit": true, "new": true, "history": true, "share": true, "move": true, "delete": true, "trash": true, "format": true, "admin": true}

func (d *Deps) serveContent(c *gin.Context, a *addr) {
	ctx := c.Request.Context()
	p := a.Path
	if a.Explicit {
		if first := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]; ownerVerbs[first] {
			// Owner operations live at the personal address form only.
			c.Redirect(http.StatusTemporaryRedirect, p)
			return
		}
	}
	if !a.Prin.Can(models.ScopeRead) {
		jsonError(c, errors.New(errors.CodeInsufficientScope, "content:read scope is required"))
		return
	}
	// Search
	if p == "/search" || p == "/search.json" {
		d.siteSearch(c, a, strings.HasSuffix(p, ".json"))
		return
	}
	// Config
	if p == models.ConfigPath {
		doc, err := d.Ops.Read(ctx, a.Prin, models.ConfigPath, 0)
		if err != nil {
			d.fail(c, err)
			return
		}
		writeRaw(c, doc, "application/yaml; charset=utf-8")
		return
	}
	lower := strings.ToLower(p)
	if lower != p {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return
	}
	if models.FileMIME(p) != "" {
		if res, err := d.Ops.ResolveRaw(ctx, a.Tenant.ID, p); err == nil {
			if res.Redirect != "" {
				c.Redirect(http.StatusTemporaryRedirect, a.Prefix+res.Redirect)
				return
			}
			if res.Entry.Kind == models.KindFile {
				d.serveFile(c, a, res.Entry)
				return
			}
		}
	}
	switch {
	case strings.HasSuffix(p, ".md"):
		res, err := d.Ops.ResolveRaw(ctx, a.Tenant.ID, p)
		if err != nil {
			d.fail(c, err)
			return
		}
		if res.Redirect != "" {
			c.Redirect(http.StatusTemporaryRedirect, a.Prefix+res.Redirect)
			return
		}
		doc, err := d.Ops.Read(ctx, a.Prin, res.Entry.Path, 0)
		if err != nil {
			d.fail(c, err)
			return
		}
		writeRaw(c, doc, "text/markdown; charset=utf-8")
	case strings.HasSuffix(p, ".json"):
		d.serveJSON(c, a, strings.TrimSuffix(p, ".json"))
	case isAssetPath(p):
		res, err := d.Ops.ResolveRaw(ctx, a.Tenant.ID, p)
		if err != nil {
			d.fail(c, err)
			return
		}
		if res.Redirect != "" {
			c.Redirect(http.StatusTemporaryRedirect, a.Prefix+res.Redirect)
			return
		}
		d.serveBlob(c, a.Tenant.ID, res.Entry)
	default:
		d.serveHTML(c, a, p)
	}
}

func isAssetPath(p string) bool {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func writeRaw(c *gin.Context, doc *models.Document, contentType string) {
	c.Header("Content-Type", contentType)
	c.Header("ETag", etag(doc.Entry))
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
	c.String(http.StatusOK, doc.Source)
}

// etag is opaque: entry identity plus revision.
func etag(e *models.Entry) string {
	return `"e` + strconv.FormatInt(e.ID, 36) + `-r` + strconv.FormatInt(e.CurrentRevision, 10) + `"`
}

func (d *Deps) serveBlob(c *gin.Context, tenantID int64, e *models.Entry) {
	if e.Kind != models.KindAsset || e.BlobID == nil {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return
	}
	b, err := d.Ops.ReadBlob(c.Request.Context(), tenantID, *e.BlobID)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("ETag", etag(e))
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
	c.Header("Content-Disposition", "inline")
	c.Data(http.StatusOK, b.MIME, b.Data)
}

// serveJSON returns the JSON representation of a page (or a directory listing).
func (d *Deps) serveJSON(c *gin.Context, a *addr, rendered string) {
	ctx := c.Request.Context()
	if rendered == "" || rendered == "/index" {
		rendered = "/"
	} else if strings.HasSuffix(rendered, "/index") {
		rendered = strings.TrimSuffix(rendered, "index")
	}
	res, err := d.Ops.Resolve(ctx, a.Tenant.ID, rendered)
	if err != nil {
		d.fail(c, err)
		return
	}
	if res.Redirect != "" && res.Entry != nil {
		c.Redirect(http.StatusTemporaryRedirect, a.Prefix+res.Entry.JSONPath())
		return
	}
	if res.Redirect != "" {
		c.Redirect(http.StatusTemporaryRedirect, a.Prefix+strings.TrimSuffix(res.Redirect, "/")+"/index.json")
		return
	}
	if res.Directory != nil {
		page, err := d.Ops.List(ctx, a.Prin, res.Directory.Path, "", 100)
		if err != nil {
			d.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"org": a.Tenant.Code, "directory": d.publicEntry(a, page.Directory, ""), "entries": d.publicEntries(a, page.Entries)})
		return
	}
	doc, err := d.Ops.Read(ctx, a.Prin, res.Entry.Path, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("ETag", etag(doc.Entry))
	c.JSON(http.StatusOK, d.publicDocument(a, doc))
}

// publicEntry is the JSON representation without internal IDs or audit data.
func (d *Deps) publicEntry(a *addr, info models.EntryInfo, content string) gin.H {
	h := gin.H{
		"org": a.Tenant.Code, "path": info.Path, "kind": info.Kind, "title": info.Title,
		"description": info.Description, "tags": info.Tags, "revision": info.Revision,
		"created_at": info.CreatedAt, "updated_at": info.UpdatedAt,
		"urls": gin.H{"html": d.abs(a.Prefix + info.HTMLPath), "raw": d.abs(a.Prefix + info.RawPath), "json": d.abs(a.Prefix + info.JSONPath)},
	}
	if info.Order != nil {
		h["order"] = *info.Order
	}
	if info.Kind == models.KindPage || info.Kind == models.KindConfig || info.Kind == models.KindFile {
		h["content"] = content
	}
	return h
}

func (d *Deps) publicEntries(a *addr, infos []models.EntryInfo) []gin.H {
	out := make([]gin.H, 0, len(infos))
	for _, i := range infos {
		h := d.publicEntry(a, i, "")
		delete(h, "content")
		out = append(out, h)
	}
	return out
}

func (d *Deps) publicDocument(a *addr, doc *models.Document) gin.H {
	return d.publicEntry(a, models.Info(doc.Entry), doc.Source)
}

// serveHTML renders a page or generated directory listing.
func (d *Deps) serveHTML(c *gin.Context, a *addr, rendered string) {
	ctx := c.Request.Context()
	res, err := d.Ops.Resolve(ctx, a.Tenant.ID, rendered)
	if err != nil {
		d.fail(c, err)
		return
	}
	if res.Redirect != "" {
		c.Redirect(http.StatusTemporaryRedirect, a.Prefix+res.Redirect)
		return
	}
	site, err := d.siteModel(c, a)
	if err != nil {
		d.fail(c, err)
		return
	}
	site.Mode = "site"
	if res.Directory != nil {
		site.Directory = res.Directory
		site.CurrentDir = res.Directory.Path
		site.Title = res.Directory.Title
		if site.Title == "" {
			site.Title = siteName(site.Config, a.Tenant)
		}
		site.Listing = d.listingFor(ctx, site, res.Directory)
		site.Folders = folderPaths(site.Sidebar)
		site.Breadcrumbs = titled(breadcrumbs(a.Prefix, res.Directory.Path, true), res.Directory.Title)
		site.CurrentHTML = a.Prefix + res.Directory.HTMLPath()
		data := gin.H{"Site": site, "Error": c.Query("error")}
		if site.IsOwner {
			children, _ := d.Ops.Children(ctx, a.Tenant.ID, res.Directory.ID)
			var assets []models.Entry
			for _, ch := range children {
				if ch.Kind == models.KindAsset {
					assets = append(assets, ch)
				}
			}
			data["Assets"] = assets
		}
		d.render(c, http.StatusOK, "pages/site/directory.html", "layout/site", data)
		return
	}
	e := res.Entry
	if e.Kind == models.KindAsset {
		d.serveBlob(c, a.Tenant.ID, e)
		return
	}
	if e.Kind == models.KindConfig {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return
	}
	doc, err := d.Ops.Read(ctx, a.Prin, e.Path, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	page, err := d.renderPage(c, a, doc)
	if err != nil {
		d.fail(c, err)
		return
	}
	site.Page = page
	site.Entry = e
	site.CurrentDir = parentDir(e.Path)
	site.Title = page.Title
	site.Breadcrumbs = titled(breadcrumbs(a.Prefix, e.Path, false), site.Page.Title)
	site.CurrentHTML = a.Prefix + e.HTMLPath()
	c.Header("ETag", etag(e))
	d.render(c, http.StatusOK, "pages/site/page.html", "layout/site", gin.H{"Site": site, "Saved": c.Query("saved"), "Refiled": c.Query("refiled")})
}

func (d *Deps) siteSearch(c *gin.Context, a *addr, asJSON bool) {
	q := strings.TrimSpace(c.Query("q"))
	var page *models.SearchPage
	var err error
	if q != "" {
		page, err = d.Ops.Search(c.Request.Context(), a.Prin, q, c.Query("path_prefix"), c.Query("cursor"), 20)
		if err != nil && !errors.Is(err, errors.CodeValidationFailed) {
			d.fail(c, err)
			return
		}
	}
	if asJSON {
		hits := []gin.H{}
		if page != nil {
			for _, h := range page.Hits {
				hits = append(hits, gin.H{"path": h.Path, "title": h.Title, "snippet": h.Snippet, "revision": h.Revision,
					"urls": gin.H{"html": d.abs(a.Prefix + h.HTMLPath), "raw": d.abs(a.Prefix + h.RawPath), "json": d.abs(a.Prefix + h.JSONPath)}})
			}
		}
		out := gin.H{"query": q, "hits": hits}
		if page != nil && page.NextCursor != "" {
			out["next_cursor"] = page.NextCursor
		}
		c.JSON(http.StatusOK, out)
		return
	}
	site, err := d.siteModel(c, a)
	if err != nil {
		d.fail(c, err)
		return
	}
	site.Title = i18n.T("en", "site.search")
	site.Query = q
	site.Search = page
	site.CurrentHTML = a.Prefix + "/search"
	d.render(c, http.StatusOK, "pages/site/search.html", "layout/site", gin.H{"Site": site})
}
