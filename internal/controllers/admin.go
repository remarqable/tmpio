package controllers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/obs"
)

// The admin area holds only account and site administration. Content
// operations live on the site itself (see ops.go).

func (d *Deps) adminShell(c *gin.Context, active string) (*SiteView, *addr, bool) {
	c.Set("admin_active", active)
	sv, a, err := d.ownerShell(c, "admin")
	if err != nil {
		d.fail(c, err)
		return nil, nil, false
	}
	return sv, a, true
}

// AdminOverview is /admin.
func (d *Deps) AdminOverview(c *gin.Context) {
	sv, a, ok := d.adminShell(c, "overview")
	if !ok {
		return
	}
	recent, err := d.Ops.RecentChanges(c.Request.Context(), a.Tenant.ID, 15)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "admin.overview")
	aiSum, _ := d.Ops.AISummary(c.Request.Context(), a.Tenant.ID)
	d.render(c, http.StatusOK, "pages/admin/overview.html", "layout/site", gin.H{"Site": sv, "Recent": recent, "MCPURL": d.abs("/mcp"), "OrgPrefix": orgPrefix(a.Tenant), "AIEnabled": d.Cfg.AI.Enabled(), "AIModel": d.Cfg.AI.Model, "AI": aiSum,
		"Tidied": c.Query("tidied"), "Kept": c.Query("kept"), "More": c.Query("more")})
}

// AdminConnections shows connected clients and development API tokens.
func (d *Deps) AdminConnections(c *gin.Context) {
	sv, a, ok := d.adminShell(c, "connections")
	if !ok {
		return
	}
	conns, err := models.ListConnections(c.Request.Context(), a.Tenant.ID)
	if err != nil {
		d.fail(c, err)
		return
	}
	tokens, err := models.ListAPITokens(c.Request.Context(), a.Tenant.ID)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "connections.title")
	d.render(c, http.StatusOK, "pages/admin/connections.html", "layout/site", gin.H{
		"Site": sv, "Connections": conns, "Tokens": tokens, "MCPURL": d.abs("/mcp"),
		"NewToken": c.Query("token"), "Error": c.Query("error"), "Scopes": models.AllScopes,
	})
}

// AdminConnectionRevoke revokes a client connection.
func (d *Deps) AdminConnectionRevoke(c *gin.Context) {
	p, _ := owner(c)
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err := models.RevokeConnection(c.Request.Context(), p, id); err != nil {
		d.flashFail(c, err, "/admin/connections")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/connections")
}

// AdminTokenCreate creates a development API token shown once.
func (d *Deps) AdminTokenCreate(c *gin.Context) {
	p, _ := owner(c)
	plain, _, err := models.CreateAPIToken(c.Request.Context(), p, c.PostForm("name"), c.PostFormArray("scopes"), 0)
	if err != nil {
		d.flashFail(c, err, "/admin/connections")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/connections?token="+url.QueryEscape(plain))
}

// AdminTokenRevoke revokes a development API token.
func (d *Deps) AdminTokenRevoke(c *gin.Context) {
	p, _ := owner(c)
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err := models.RevokeAPIToken(c.Request.Context(), p, id); err != nil {
		d.flashFail(c, err, "/admin/connections")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/connections")
}

// AdminLinks lists every sharing link across the site.
func (d *Deps) AdminLinks(c *gin.Context) {
	sv, a, ok := d.adminShell(c, "links")
	if !ok {
		return
	}
	links, err := d.Ops.ListShareGrants(c.Request.Context(), a.Prin, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "admin.links")
	d.render(c, http.StatusOK, "pages/admin/links.html", "layout/site", gin.H{"Site": sv, "Links": links, "Notice": c.Query("notice"), "Error": c.Query("error")})
}

// AdminLinkRevoke revokes a link from the site-wide list.
func (d *Deps) AdminLinkRevoke(c *gin.Context) {
	p, _ := owner(c)
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err := d.Ops.RevokeShareGrant(c.Request.Context(), p, id); err != nil {
		d.flashFail(c, err, "/admin/links")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/links?notice="+url.QueryEscape(i18n.T("en", "share.revoked")))
}

// AdminSettings shows organization naming and the tmp.yaml editor.
func (d *Deps) AdminSettings(c *gin.Context) {
	sv, a, ok := d.adminShell(c, "settings")
	if !ok {
		return
	}
	cfg, src, err := d.Ops.SiteConfigFor(c.Request.Context(), a.Tenant.ID)
	if err != nil {
		d.fail(c, err)
		return
	}
	doc, err := d.Ops.Read(c.Request.Context(), a.Prin, models.ConfigPath, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "settings.title")
	d.render(c, http.StatusOK, "pages/admin/settings.html", "layout/site", gin.H{
		"Site": sv, "Config": cfg, "ConfigSource": src, "Expected": doc.Entry.CurrentRevision, "Saved": c.Query("saved") != "", "Error": c.Query("error"), "RequestID": uuidV4(),
		// Whether the installation has a credential at all, wherever it came from.
		"AIAvailable": d.Ops.AI != nil && d.Ops.AI.Enabled(c.Request.Context()),
		"AIModel":     aiModelName(d),
		"AIFiling":    a.Tenant.AIFilingEnabled,
	})
}

// AdminSettingsAI saves the owner's opt-in to AI-assisted filing.
func (d *Deps) AdminSettingsAI(c *gin.Context) {
	_, t := owner(c)
	enabled := c.PostForm("ai_filing") == "1"
	if err := models.SetTenantAIFiling(c.Request.Context(), t.ID, enabled); err != nil {
		d.flashFail(c, err, "/admin/settings")
		return
	}
	obs.From(c.Request.Context()).Info().Str("event", "settings.ai_filing").Bool("enabled", enabled).Msg("")
	c.Redirect(http.StatusSeeOther, "/admin/settings?saved=1")
}

// AdminDeleteAccount deletes the owner's organization and account after the
// owner has typed the organization code. Everything goes in one transaction;
// the session cookie is cleared and the visitor lands on the public page.
func (d *Deps) AdminDeleteAccount(c *gin.Context) {
	p, t := owner(c)
	if strings.TrimSpace(strings.ToUpper(c.PostForm("confirm"))) != t.Code {
		d.flashFail(c, errors.New(errors.CodeValidationFailed, i18n.T("en", "settings.delete_mismatch")), "/admin/settings")
		return
	}
	if err := models.DeleteAccount(c.Request.Context(), p.UserID, t.ID); err != nil {
		d.flashFail(c, err, "/admin/settings")
		return
	}
	obs.From(c.Request.Context()).Info().Str("event", "account.deleted").Int64("tenant_id", t.ID).Msg("")
	d.setSessionCookie(c, "", -1)
	middleware.NoStore(c)
	c.Redirect(http.StatusSeeOther, "/?deleted=1")
}

// AdminSettingsOrg saves the optional organization name.
func (d *Deps) AdminSettingsOrg(c *gin.Context) {
	_, t := owner(c)
	if err := models.RenameTenant(c.Request.Context(), t.ID, c.PostForm("name")); err != nil {
		d.flashFail(c, err, "/admin/settings")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/settings?saved=1")
}

// AdminExport shows the export screen.
func (d *Deps) AdminExport(c *gin.Context) {
	sv, _, ok := d.adminShell(c, "export")
	if !ok {
		return
	}
	sv.Title = i18n.T("en", "export.title")
	d.render(c, http.StatusOK, "pages/admin/export.html", "layout/site", gin.H{"Site": sv})
}

// AdminExportDownload streams the ZIP snapshot.
func (d *Deps) AdminExportDownload(c *gin.Context) {
	p, t := owner(c)
	data, err := d.Ops.Export(c.Request.Context(), p, t.Code)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="tmp-`+t.Code+`.zip"`)
	c.Data(http.StatusOK, "application/zip", data)
}

// instanceAdmin returns the signed-in user when they administer the
// installation, and answers the request otherwise. Owning an organization is
// not enough: these settings spend the operator's money.
func (d *Deps) instanceAdmin(c *gin.Context) (*models.User, bool) {
	_, _, u, _, ok := middleware.OwnerSession(c)
	if !ok || u == nil || !u.IsInstanceAdmin {
		d.renderError(c, http.StatusNotFound, "")
		return nil, false
	}
	return u, true
}

// AdminServer shows the settings that belong to the installation rather than
// to one organization.
func (d *Deps) AdminServer(c *gin.Context) {
	if _, ok := d.instanceAdmin(c); !ok {
		return
	}
	c.Set("admin_active", "server")
	sv, _, err := d.ownerShell(c, "admin")
	if err != nil {
		d.fail(c, err)
		return
	}
	set, err := models.GetInstanceSetting(c.Request.Context())
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "server.title")
	d.render(c, http.StatusOK, "pages/admin/server.html", "layout/site", gin.H{
		"Site": sv,
		// The environment only seeds the stored key, so the field is always
		// editable; say where the current one came from and leave it at that.
		"FromEnv": d.Cfg.AI.APIKey != "" && d.Cfg.AI.APIKey == set.AIAPIKey,
		"HasKey":  set.AIAPIKey != "",
		// Shown in full. This page is for whoever runs the server, and a
		// credential you cannot read is a credential you cannot check.
		"APIKey":      set.AIAPIKey,
		"Model":       firstNonEmpty(set.AIModel, d.Cfg.AI.Model),
		"BaseURL":     set.AIBaseURL,
		"WorkspaceID": set.AIWorkspaceID,
		"Saved":       c.Query("saved") != "", "Error": c.Query("error"), "Verified": c.Query("verified") != "",
	})
}

// AdminServerAI saves the installation's model credentials and checks them
// against the provider, so that a wrong key is reported here rather than
// discovered later as filing that quietly stopped being intelligent.
func (d *Deps) AdminServerAI(c *gin.Context) {
	if _, ok := d.instanceAdmin(c); !ok {
		return
	}
	ctx := c.Request.Context()
	key := strings.TrimSpace(c.PostForm("api_key"))
	if err := models.SaveInstanceAI(ctx, key, c.PostForm("model"), c.PostForm("base_url"), c.PostForm("workspace_id")); err != nil {
		d.flashFail(c, err, "/admin/server")
		return
	}
	obs.From(ctx).Info().Str("event", "settings.instance_ai").Bool("key_set", key != "").Msg("")
	if key == "" {
		c.Redirect(http.StatusSeeOther, "/admin/server?saved=1")
		return
	}
	// One cheap call: either the credential works or the operator hears why.
	if err := d.Ops.VerifyAI(ctx); err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/server?saved=1&error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/server?saved=1&verified=1")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// aiModelName reports the model filing would use, for the tenant settings page.
func aiModelName(d *Deps) string {
	if d.Ops.AI != nil {
		if m := d.Ops.AI.Model(); m != "" {
			return m
		}
	}
	return d.Cfg.AI.Model
}
