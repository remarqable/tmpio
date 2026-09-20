package controllers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/assets"
	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/platform/ratelimit"
)

// SetupRouter wires every route. mcpHandler is mounted at /mcp.
func (d *Deps) SetupRouter(mcpHandler http.Handler) *gin.Engine {
	r := gin.New()
	// Only these addresses may speak for someone else. Rate limit keys, the
	// /metrics loopback gate and the client registration quota all read
	// ClientIP, so trusting every proxy (gin's default) would let any caller
	// set X-Forwarded-For and claim to be 127.0.0.1 or a fresh address per
	// request.
	if err := r.SetTrustedProxies(d.Cfg.TrustedProxies); err != nil {
		// config.Load validates the list, so this is unreachable. Fail closed.
		_ = r.SetTrustedProxies(nil)
	}
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = true
	r.Use(middleware.RequestID())
	r.Use(gin.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.SecurityHeaders(d.Cfg))
	r.Use(middleware.BodyLimit(d.Ops.Quotas.MaxAssetBytes + 1<<20))

	// Immutable platform assets: the only cacheable responses.
	r.Group("/static", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Next()
	}).StaticFS("/", http.FS(assets.StaticFS()))

	r.GET("/healthz", d.Healthz)
	r.GET("/readyz", d.Readyz)
	r.GET("/metrics", d.Metrics)
	// These four are registered routes, so they never reach the catch-all
	// where the operator's static files are offered. A front page is expected
	// to own them, so offer the files here too.
	publicOr := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			if d.servePublicSite(c) {
				return
			}
			h(c)
		}
	}
	r.GET("/robots.txt", publicOr(d.Robots))
	r.GET("/favicon.ico", publicOr(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Status(http.StatusNoContent)
	}))
	r.GET("/sitemap.xml", publicOr(func(c *gin.Context) { c.Status(404) }))
	r.GET("/llms.txt", publicOr(func(c *gin.Context) { c.Status(404) }))

	authLimit := ratelimit.New(30)
	writeLimit := ratelimit.New(60)
	readLimit := ratelimit.New(300)
	shareWrite := ratelimit.New(30)
	shareRead := ratelimit.New(120)

	// Credentials: bearer first (fails closed), then cookie session.
	r.Use(middleware.Bearer(d.Cfg))
	r.Use(middleware.Session())

	// Sign-in
	r.GET("/login", d.Login)
	r.GET("/auth/google", middleware.RateLimit(authLimit, middleware.KeyByIP), d.GoogleBegin)
	r.GET("/auth/google/callback", middleware.RateLimit(authLimit, middleware.KeyByIP), d.GoogleCallback)
	r.POST("/auth/dev", middleware.RateLimit(authLimit, middleware.KeyByIP), d.DevLogin)
	// The local owner's password form: a tighter limit than the rest of auth,
	// because this is the one route where guessing a secret is the attack.
	r.POST("/auth/local", middleware.RateLimit(ratelimit.New(10), middleware.KeyByIP), d.LocalLogin)
	r.POST("/logout", middleware.CSRF(d.Cfg), d.Logout)

	// OAuth authorization server for AI clients
	r.GET("/.well-known/oauth-authorization-server", d.ASMetadata)
	r.GET("/.well-known/oauth-protected-resource", d.PRMetadata)
	r.GET("/.well-known/oauth-protected-resource/mcp", d.PRMetadata)
	r.GET("/oauth/authorize", d.Authorize)
	r.POST("/oauth/authorize", middleware.CSRF(d.Cfg), d.AuthorizePost)
	r.POST("/oauth/register", middleware.RateLimit(ratelimit.New(10), middleware.KeyByIP), d.Register)
	r.POST("/oauth/token", middleware.RateLimit(authLimit, middleware.KeyByIP), d.Token)
	r.POST("/oauth/revoke", middleware.RateLimit(authLimit, middleware.KeyByIP), d.Revoke)

	// MCP
	mcpLimit := ratelimit.New(300)
	r.Any("/mcp", middleware.RateLimit(mcpLimit, middleware.KeyByIP), gin.WrapH(mcpHandler))
	r.Any("/mcp/*rest", middleware.RateLimit(mcpLimit, middleware.KeyByIP), gin.WrapH(mcpHandler))

	// Owner content operations: verb-prefixed URLs rendered inside the site shell.
	ops := r.Group("/", d.RequireOwner(), middleware.CSRF(d.Cfg))
	{
		ops.GET("/edit/*rest", d.EditPage)
		ops.POST("/edit/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.SaveEdit)
		ops.GET("/new/*rest", d.NewPage)
		ops.POST("/new/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.CreateEntry)
		ops.POST("/preview", d.Preview)
		ops.POST("/organize", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.QuickAdd)
		ops.GET("/history/*rest", d.HistoryPage)
		ops.POST("/history/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.RestoreEntry)
		ops.GET("/share/*rest", d.SharePage)
		ops.POST("/share/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.ShareAction)
		ops.GET("/move/*rest", d.MovePage)
		ops.POST("/move/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.MoveEntry)
		ops.POST("/delete/*rest", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.DeleteEntry)
		ops.POST("/bulk", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.BulkEntry)
		ops.GET("/trash", d.TrashPage)
		ops.POST("/trash", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.TrashRestore)
		ops.GET("/format", d.FormatGuide)
	}

	// Admin: account and site administration only.
	admin := r.Group("/admin", d.RequireOwner(), middleware.CSRF(d.Cfg))
	{
		admin.GET("", d.AdminOverview)
		admin.GET("/", d.AdminOverview)
		admin.GET("/connections", d.AdminConnections)
		admin.POST("/connections/revoke", d.AdminConnectionRevoke)
		admin.POST("/tokens", d.AdminTokenCreate)
		admin.POST("/tokens/revoke", d.AdminTokenRevoke)
		admin.GET("/links", d.AdminLinks)
		admin.POST("/links/revoke", d.AdminLinkRevoke)
		admin.GET("/settings", d.AdminSettings)
		admin.POST("/settings/org", d.AdminSettingsOrg)
		admin.POST("/settings/ai", d.AdminSettingsAI)
		admin.POST("/settings/delete-account", middleware.RateLimit(ratelimit.New(5), middleware.KeyByPrincipalOrIP), d.AdminDeleteAccount)
		admin.GET("/server", d.AdminServer)
		admin.POST("/server/ai", d.AdminServerAI)
		admin.GET("/export", d.AdminExport)
		admin.GET("/export.zip", middleware.RateLimit(ratelimit.New(5), middleware.KeyByPrincipalOrIP), d.AdminExportDownload)
	}
	// The old dashboard URLs redirect to their new homes.
	r.GET("/app", d.LegacyAppRedirect)
	r.GET("/app/*rest", d.LegacyAppRedirect)

	// REST
	api := r.Group("/api/v1/orgs/:org", middleware.CSRF(d.Cfg))
	{
		api.GET("/entries", middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), d.APIGetEntry)
		api.PUT("/entries", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIPutEntry)
		api.DELETE("/entries", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIDeleteEntry)
		api.GET("/tree", middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), d.APITree)
		api.POST("/directories", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIMkdir)
		api.POST("/moves", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIMove)
		api.GET("/search", middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), d.APISearch)
		api.POST("/organize", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIOrganize)
		api.GET("/history", middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), d.APIHistory)
		api.POST("/restores", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIRestore)
		api.POST("/assets", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIPutAsset)
		api.GET("/export", middleware.RateLimit(ratelimit.New(5), middleware.KeyByPrincipalOrIP), d.APIExport)
		api.POST("/share-links", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIShareCreate)
		api.GET("/share-links", middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), d.APIShareList)
		api.DELETE("/share-links/:id", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIShareRevoke)
		api.POST("/share-links/:id/assets/refresh", middleware.RateLimit(writeLimit, middleware.KeyByPrincipalOrIP), d.APIShareRefreshAssets)
	}

	// Secret links
	s := r.Group("/s/:token")
	{
		s.GET("", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareHTML)
		s.HEAD("", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareHTML)
		s.GET("/raw", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareRaw)
		s.HEAD("/raw", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareRaw)
		s.PUT("/raw", middleware.RateLimit(shareWrite, middleware.KeyByIP), d.SharePutRaw)
		s.GET("/json", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareJSON)
		s.GET("/edit", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareEdit)
		s.POST("/edit", middleware.RateLimit(shareWrite, middleware.KeyByIP), d.ShareEditPost)
		s.POST("/preview", middleware.RateLimit(shareWrite, middleware.KeyByIP), d.SharePreview)
		s.GET("/assets/:id", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareAsset)
		s.HEAD("/assets/:id", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareAsset)
		s.GET("/instructions", middleware.RateLimit(shareRead, middleware.KeyByIP), d.ShareInstructions)
	}
	r.NoMethod(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/s/") {
			shareHeaders(c)
		}
		c.Header("Allow", "GET, HEAD")
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": gin.H{"code": "method_not_allowed", "message": "method not allowed"}})
	})

	// Root and content: reserved routes above win; then /o:{org}; then personal namespace.
	r.GET("/", d.Landing)
	r.HEAD("/", d.Landing)
	r.NoRoute(middleware.RateLimit(readLimit, middleware.KeyByPrincipalOrIP), func(c *gin.Context) {
		// Anything under /s/ that is not a defined link route is an invalid selector: 404, never content routing.
		if strings.HasPrefix(c.Request.URL.Path, "/s/") {
			d.shareNotFound(c)
			return
		}
		// An operator's own pages answer strangers before the application does.
		if d.servePublicSite(c) {
			return
		}
		d.Content(c)
	})
	return r
}
