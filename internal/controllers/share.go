package controllers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// shareHeaders applies the disclosure-reducing headers of section 6.
func shareHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
}

// shareCtx validates the token before any content is touched. Failures are 404.
func (d *Deps) shareCtx(c *gin.Context) (*models.ShareContext, string, bool) {
	shareHeaders(c)
	token := c.Param("token")
	if strings.ContainsAny(token, "/.%") || len(token) < 20 || len(token) > 128 {
		d.shareNotFound(c)
		return nil, "", false
	}
	sc, err := d.Ops.ResolveShareToken(c.Request.Context(), token)
	if err != nil {
		d.shareNotFound(c)
		return nil, "", false
	}
	// Per-grant limits apply in addition to the per-IP limits on the route.
	reads, writes := d.limiters()
	l := reads
	if c.Request.Method == http.MethodPost || c.Request.Method == http.MethodPut {
		l = writes
	}
	if allowed, retry := l.Allow(strconv.FormatInt(sc.Grant.ID, 10)); !allowed {
		c.Header("Retry-After", strconv.Itoa(retry))
		jsonError(c, &errors.AppError{Code: errors.CodeRateLimited, Message: "too many requests for this link; retry after " + strconv.Itoa(retry) + " seconds", RetryAfter: retry})
		return nil, "", false
	}
	// A logged-in visitor still acts as the link principal, never with account permissions.
	c.Set(middleware.KeyPrincipal, sc.Principal)
	return sc, "/s/" + token, true
}

func (d *Deps) shareNotFound(c *gin.Context) {
	shareHeaders(c)
	if wantsJSON(c) || strings.HasSuffix(c.Request.URL.Path, "/raw") || strings.HasSuffix(c.Request.URL.Path, "/json") || strings.Contains(c.Request.URL.Path, "/assets/") {
		jsonError(c, errors.New(errors.CodeNotFound, "not found"))
		return
	}
	d.render(c, http.StatusNotFound, "pages/errors/error.html", "layout/bare", gin.H{"Status": 404, "Title": i18n.T("en", "error.404"), "Detail": i18n.T("en", "error.404_detail")})
}

// shareNonce is a short-lived CSRF nonce bound to the grant.
func (d *Deps) shareNonce(grantID int64) string {
	exp := time.Now().Add(2 * time.Hour).Unix()
	msg := strconv.FormatInt(grantID, 10) + "." + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, d.Cfg.SessionSecret)
	mac.Write([]byte("share-csrf:" + msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:20])
}

func (d *Deps) checkShareNonce(grantID int64, nonce string) bool {
	parts := strings.Split(nonce, ".")
	if len(parts) != 3 || parts[0] != strconv.FormatInt(grantID, 10) {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, d.Cfg.SessionSecret)
	mac.Write([]byte("share-csrf:" + parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:20])
	return hmac.Equal([]byte(want), []byte(parts[2]))
}

func (d *Deps) shareView(c *gin.Context, sc *models.ShareContext, base string) (*SiteView, *RenderedPage, error) {
	t, err := models.GetTenant(c.Request.Context(), sc.Principal.TenantID)
	if err != nil {
		return nil, nil, err
	}
	cfg, _, err := d.Ops.SiteConfigFor(c.Request.Context(), t.ID)
	if err != nil {
		return nil, nil, err
	}
	res, err := render.Render(sc.Source, render.Options{
		SourcePath: sc.Entry.Path, FilenameFallbackTitle: strings.TrimSuffix(sc.Entry.Name(), ".md"),
		Prefix:       "/o:" + t.Code, // internal links remain private org-qualified links
		ResolveAsset: d.Ops.ShareAssetResolver(c.Request.Context(), sc, base),
		MaxBytes:     int(d.Ops.Quotas.MaxPageBytes),
	})
	page := &RenderedPage{Title: sc.Entry.Title, Revision: sc.Entry.CurrentRevision, UpdatedAt: sc.Entry.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC"), RawURL: base + "/raw", JSONURL: base + "/json"}
	if err != nil {
		page.HTML = template.HTML("<pre>" + template.HTMLEscapeString(sc.Source) + "</pre>")
	} else {
		page.Title, page.Description, page.Tags, page.HTML, page.TOC = res.Title, res.Meta.Description, res.Meta.Tags, template.HTML(res.HTML), res.TOC
		for _, w := range res.Warnings {
			page.Warnings = append(page.Warnings, w.Message)
		}
	}
	// Shared views use the theme but hide navigation, org metadata and search.
	sv := &SiteView{Config: cfg, Name: page.Title, Title: page.Title, Page: page, Entry: sc.Entry, Shared: true, ShareBase: base, CurrentHTML: base}
	return sv, page, nil
}

// ShareHTML renders the shared document.
func (d *Deps) ShareHTML(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	sv, _, err := d.shareView(c, sc, base)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("ETag", etag(sc.Entry))
	d.render(c, http.StatusOK, "pages/share/page.html", "layout/site", gin.H{"Site": sv, "Base": base})
}

// ShareRaw returns the exact current Markdown.
func (d *Deps) ShareRaw(c *gin.Context) {
	sc, _, ok := d.shareCtx(c)
	if !ok {
		return
	}
	c.Header("Content-Type", "text/markdown; charset=utf-8")
	c.Header("ETag", etag(sc.Entry))
	c.String(http.StatusOK, sc.Source)
}

// ShareJSON returns the token-scoped JSON representation.
func (d *Deps) ShareJSON(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	c.Header("ETag", etag(sc.Entry))
	c.JSON(http.StatusOK, gin.H{
		"title": sc.Entry.Title, "description": sc.Entry.Description, "tags": []string(sc.Entry.Tags), "content": sc.Source,
		"revision": sc.Entry.CurrentRevision, "etag": etag(sc.Entry), "updated_at": sc.Entry.UpdatedAt,
		"urls":   gin.H{"html": d.abs(base), "raw": d.abs(base + "/raw"), "json": d.abs(base + "/json"), "write": d.abs(base + "/raw"), "edit": d.abs(base + "/edit"), "instructions": d.abs(base + "/instructions")},
		"access": "read+write via sharing link",
	})
}

// SharePutRaw replaces the whole source over HTTP. If-Match and Idempotency-Key are required.
func (d *Deps) SharePutRaw(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	if !middleware.OriginAllowed(c, d.Cfg.AppOrigin) {
		jsonError(c, errors.New(errors.CodeForbidden, "cross-origin write refused"))
		return
	}
	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if !strings.HasPrefix(ct, "text/markdown") && !strings.HasPrefix(ct, "text/plain") {
		jsonError(c, errors.New(errors.CodeValidationFailed, "Content-Type must be text/markdown"))
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		jsonError(c, errors.As(err))
		return
	}
	ifMatch := c.GetHeader("If-Match")
	if ifMatch == "" {
		jsonError(c, errors.New(errors.CodePreconditionReq, "If-Match with the current ETag is required"))
		return
	}
	expected, err := revisionFromETag(ifMatch)
	if err != nil || !strings.HasPrefix(strings.Trim(ifMatch, `"`), "e"+strconv.FormatInt(sc.Entry.ID, 36)+"-r") {
		jsonError(c, &errors.AppError{Code: errors.CodeRevisionConflict, Message: "If-Match is not the ETag of this document", CurrentRevision: sc.Entry.CurrentRevision})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, d.Ops.Quotas.MaxPageBytes+1))
	if err != nil {
		jsonError(c, errors.New(errors.CodeValidationFailed, "could not read body"))
		return
	}
	if int64(len(body)) > d.Ops.Quotas.MaxPageBytes {
		jsonError(c, errors.New(errors.CodeTooLarge, "document exceeds the size limit"))
		return
	}
	p := sc.Principal
	p.AuthorName = sanitizeName(c.GetHeader("X-Author-Name"))
	res, err := d.Ops.Write(c.Request.Context(), p, models.WriteInput{Path: sc.Entry.Path, Content: string(body), ExpectedRevision: expected, Summary: strings.TrimSpace(c.GetHeader("X-Change-Summary")), RequestID: key})
	if err != nil {
		if errors.Is(err, errors.CodeNotFound) {
			d.shareNotFound(c)
			return
		}
		jsonError(c, errors.As(err))
		return
	}
	tag := `"e` + strconv.FormatInt(res.Entry.ID, 36) + `-r` + strconv.FormatInt(res.Entry.Revision, 10) + `"`
	c.Header("ETag", tag)
	c.JSON(http.StatusOK, gin.H{"revision": res.Entry.Revision, "etag": tag, "unchanged": res.Unchanged, "warnings": res.Warnings,
		"urls": gin.H{"html": d.abs(base), "raw": d.abs(base + "/raw"), "json": d.abs(base + "/json")}})
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}

// ShareEdit shows the signed-out editor.
func (d *Deps) ShareEdit(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	sv, _, err := d.shareView(c, sc, base)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Page.TOC = nil // the editor has no rendered headings to jump to
	d.render(c, http.StatusOK, "pages/share/edit.html", "layout/site", gin.H{
		"Site": sv, "Base": base, "Content": sc.Source, "Expected": sc.Entry.CurrentRevision, "Nonce": d.shareNonce(sc.Grant.ID), "RequestID": uuidV4(),
		"Saved": c.Query("saved"),
	})
}

// ShareEditPost saves the browser form.
func (d *Deps) ShareEditPost(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	if !middleware.OriginAllowed(c, d.Cfg.AppOrigin) || !d.checkShareNonce(sc.Grant.ID, c.PostForm("nonce")) {
		d.renderError(c, http.StatusForbidden, i18n.T("en", "shared.form_expired"))
		return
	}
	content := strings.ReplaceAll(c.PostForm("content"), "\r\n", "\n")
	expected, _ := strconv.ParseInt(c.PostForm("expected_revision"), 10, 64)
	reqID := c.PostForm("request_id")
	if !models.ValidUUID(reqID) {
		reqID = uuidV4()
	}
	p := sc.Principal
	p.AuthorName = sanitizeName(c.PostForm("author_name"))
	res, err := d.Ops.Write(c.Request.Context(), p, models.WriteInput{Path: sc.Entry.Path, Content: content, ExpectedRevision: expected, Summary: strings.TrimSpace(c.PostForm("summary")), RequestID: reqID})
	if err != nil {
		if errors.Is(err, errors.CodeNotFound) {
			d.shareNotFound(c)
			return
		}
		ae := errors.As(err)
		sv, _, verr := d.shareView(c, sc, base)
		if verr != nil {
			d.fail(c, verr)
			return
		}
		sv.Page.TOC = nil
		data := gin.H{"Site": sv, "Base": base, "Content": c.PostForm("content"), "Expected": expected, "Nonce": d.shareNonce(sc.Grant.ID), "RequestID": uuidV4(), "Error": ae, "AuthorName": p.AuthorName, "Summary": c.PostForm("summary")}
		if ae.Code == errors.CodeRevisionConflict {
			data["Conflict"] = ae.CurrentRevision
		}
		d.render(c, ae.HTTPStatus(), "pages/share/edit.html", "layout/site", data)
		return
	}
	c.Redirect(http.StatusSeeOther, base+"/edit?saved="+strconv.FormatInt(res.Entry.Revision, 10))
}

// SharePreview renders supplied Markdown under the link's restrictions without saving.
func (d *Deps) SharePreview(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	if !middleware.OriginAllowed(c, d.Cfg.AppOrigin) {
		c.String(http.StatusForbidden, "cross-origin request refused")
		return
	}
	content := strings.ReplaceAll(c.PostForm("content"), "\r\n", "\n")
	t, _ := models.GetTenant(c.Request.Context(), sc.Principal.TenantID)
	prefix := ""
	if t != nil {
		prefix = "/o:" + t.Code
	}
	res, err := render.Render(content, render.Options{SourcePath: sc.Entry.Path, FilenameFallbackTitle: strings.TrimSuffix(sc.Entry.Name(), ".md"), Prefix: prefix, ResolveAsset: d.Ops.ShareAssetResolver(c.Request.Context(), sc, base), MaxBytes: int(d.Ops.Quotas.MaxPageBytes)})
	if err != nil {
		d.renderPartial(c, http.StatusUnprocessableEntity, "partials/_preview.html", gin.H{"Error": errors.As(renderErrorPublic(err))})
		return
	}
	warnings := make([]string, 0, len(res.Warnings))
	for _, w := range res.Warnings {
		warnings = append(warnings, w.Message)
	}
	d.renderPartial(c, http.StatusOK, "partials/_preview.html", gin.H{"Title": res.Title, "HTML": template.HTML(res.HTML), "Warnings": warnings})
}

// ShareAsset serves an approved, currently embedded asset.
func (d *Deps) ShareAsset(c *gin.Context) {
	sc, _, ok := d.shareCtx(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		d.shareNotFound(c)
		return
	}
	blob, err := d.Ops.ShareAsset(c.Request.Context(), sc, id)
	if err != nil {
		d.shareNotFound(c)
		return
	}
	c.Header("Content-Disposition", "inline")
	c.Data(http.StatusOK, blob.MIME, blob.Data)
}

// ShareInstructions documents the read→edit→write contract for this link only.
func (d *Deps) ShareInstructions(c *gin.Context) {
	sc, base, ok := d.shareCtx(c)
	if !ok {
		return
	}
	u := d.abs(base)
	c.Header("Content-Type", "text/markdown; charset=utf-8")
	c.String(http.StatusOK, `# Editing this document over HTTP

This link grants read and write access to exactly one Markdown document. No account, login or API key is needed. It does not grant access to any other page, search, history, or site configuration.

Current document: %d bytes, revision %d.

## Read

    GET %s/raw
    → text/markdown body, ETag header (keep it)

    GET %s/json
    → {"content": ..., "revision": N, "etag": "...", "urls": {...}}

## Write (replace the whole document)

    PUT %s/raw
    Content-Type: text/markdown
    If-Match: <ETag from the read>
    Idempotency-Key: <new UUID v4>
    X-Author-Name: <optional display name, unverified>

    <entire new Markdown source>

Responses: 200 with {"revision","etag"} on success; 412 if the document changed since your read (read again, merge, retry); 428 if If-Match or Idempotency-Key is missing; 422 if the Markdown is invalid (the body lists line-numbered errors); 413 if too large; 429 with Retry-After when throttled; 404 if the link was revoked.

Repeating a PUT with the same Idempotency-Key and body returns the original result for 24 hours. A different body with the same key is rejected with 409.

## Format

Markdown with optional YAML frontmatter (title, description, tags, order, extra). Three extensions: :::callout type="note|tip|warning|important", :::cards around a list of links, :::details title="...". Raw HTML is rejected. Images must be already-approved local assets or external https URLs.

Human editor: %s/edit
`, len(sc.Source), sc.Entry.CurrentRevision, u, u, u, u)
}
