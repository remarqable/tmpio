package controllers

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// apiCtx authenticates a REST request and binds it to the org in the URL.
func (d *Deps) apiCtx(c *gin.Context) (models.Principal, *models.Tenant, bool) {
	middleware.NoStore(c)
	p, ok := middleware.GetPrincipal(c)
	if !ok || p.IsShare() {
		c.Header("WWW-Authenticate", `Bearer realm="tmp"`)
		jsonError(c, errors.New(errors.CodeUnauthorized, "authentication required"))
		return p, nil, false
	}
	code := strings.ToUpper(c.Param("org"))
	t, err := models.GetTenantByCode(c.Request.Context(), code)
	if err != nil || t.ID != p.TenantID {
		jsonError(c, errors.New(errors.CodeNotFound, "organization not found"))
		return p, nil, false
	}
	return p, t, true
}

// preconditions extracts If-Match / If-None-Match into an expected revision.
func preconditions(c *gin.Context, e *models.Entry, create bool) (int64, error) {
	ifMatch := c.GetHeader("If-Match")
	ifNone := c.GetHeader("If-None-Match")
	switch {
	case ifNone == "*":
		return 0, nil
	case ifMatch != "":
		rev, err := revisionFromETag(ifMatch)
		if err != nil {
			return 0, errors.New(errors.CodeRevisionConflict, "If-Match does not match a known ETag")
		}
		return rev, nil
	case create:
		return 0, errors.New(errors.CodePreconditionReq, "supply If-None-Match: * to create or If-Match: <etag> to replace")
	default:
		return 0, errors.New(errors.CodePreconditionReq, "If-Match with the current ETag is required")
	}
}

// revisionFromETag parses the opaque ETag ("e<id36>-r<rev>").
func revisionFromETag(tag string) (int64, error) {
	tag = strings.Trim(strings.TrimPrefix(tag, "W/"), `"`)
	i := strings.LastIndex(tag, "-r")
	if i < 0 {
		return 0, errors.New(errors.CodeRevisionConflict, "bad etag")
	}
	return strconv.ParseInt(tag[i+2:], 10, 64)
}

func idempotencyKey(c *gin.Context) (string, error) {
	k := c.GetHeader("Idempotency-Key")
	if k == "" {
		return "", errors.New(errors.CodePreconditionReq, "Idempotency-Key header (a fresh UUID) is required for mutations")
	}
	if !models.ValidUUID(k) {
		return "", errors.New(errors.CodeValidationFailed, "Idempotency-Key must be a UUID")
	}
	return k, nil
}

// checkETagEntry enforces the entry-identity half of an If-Match ETag: the
// ETag must have been issued for the entry at this path.
func (d *Deps) checkETagEntry(c *gin.Context, p models.Principal, path string) error {
	tag := c.GetHeader("If-Match")
	if tag == "" {
		return nil
	}
	tag = strings.Trim(strings.TrimPrefix(tag, "W/"), `"`)
	if !strings.HasPrefix(tag, "e") {
		return errors.New(errors.CodeRevisionConflict, "If-Match is not a known ETag")
	}
	i := strings.LastIndex(tag, "-r")
	if i < 1 {
		return errors.New(errors.CodeRevisionConflict, "If-Match is not a known ETag")
	}
	id, err := strconv.ParseInt(tag[1:i], 36, 64)
	if err != nil {
		return errors.New(errors.CodeRevisionConflict, "If-Match is not a known ETag")
	}
	doc, err := d.Ops.Read(c.Request.Context(), p, path, 0)
	if err != nil {
		return err
	}
	if doc.Entry.ID != id {
		return &errors.AppError{Code: errors.CodeRevisionConflict, Message: "If-Match was issued for a different entry", CurrentRevision: doc.Entry.CurrentRevision}
	}
	return nil
}

func (d *Deps) mutationJSON(c *gin.Context, t *models.Tenant, res *models.MutationResult, status int) {
	out := gin.H{"entry": res.Entry, "urls": d.entryURLs(t, res.Entry), "created": res.Created, "unchanged": res.Unchanged}
	if res.Warnings != nil {
		out["warnings"] = res.Warnings
	}
	if res.Rewrites != nil {
		out["rewrites"] = res.Rewrites
	}
	if res.Aliases != nil {
		out["aliases"] = res.Aliases
	}
	c.Header("ETag", `"e`+strconv.FormatInt(res.Entry.ID, 36)+`-r`+strconv.FormatInt(res.Entry.Revision, 10)+`"`)
	c.JSON(status, out)
}

// APIGetEntry reads a page/config (with content) or entry metadata.
func (d *Deps) APIGetEntry(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	rev, _ := strconv.ParseInt(c.Query("revision"), 10, 64)
	doc, err := d.Ops.Read(c.Request.Context(), p, c.Query("path"), rev)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("ETag", etag(doc.Entry))
	out := gin.H{"entry": models.Info(doc.Entry), "urls": d.entryURLs(t, models.Info(doc.Entry))}
	if doc.Entry.HasSource() {
		out["content"] = doc.Source
	}
	if doc.Revision != nil {
		out["revision"] = doc.Revision.Seq
	}
	c.JSON(http.StatusOK, out)
}

// APIPutEntry creates or replaces a page or config.
func (d *Deps) APIPutEntry(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	var body struct {
		Content string `json:"content"`
		Summary string `json:"summary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		d.fail(c, errors.New(errors.CodeValidationFailed, "body must be JSON {content, summary}"))
		return
	}
	expected, err := preconditions(c, nil, c.GetHeader("If-None-Match") == "*")
	if err != nil {
		d.fail(c, err)
		return
	}
	if err := d.checkETagEntry(c, p, c.Query("path")); err != nil {
		d.fail(c, err)
		return
	}
	res, err := d.Ops.Write(c.Request.Context(), p, models.WriteInput{Path: c.Query("path"), Content: body.Content, ExpectedRevision: expected, Summary: body.Summary, RequestID: key})
	if err != nil {
		d.fail(c, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	d.mutationJSON(c, t, res, status)
}

// APIDeleteEntry soft-deletes.
func (d *Deps) APIDeleteEntry(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	expected, err := preconditions(c, nil, false)
	if err != nil {
		d.fail(c, err)
		return
	}
	if err := d.checkETagEntry(c, p, c.Query("path")); err != nil {
		d.fail(c, err)
		return
	}
	res, err := d.Ops.Delete(c.Request.Context(), p, c.Query("path"), expected, key)
	if err != nil {
		d.fail(c, err)
		return
	}
	d.mutationJSON(c, t, res, http.StatusOK)
}

// APITree lists children.
func (d *Deps) APITree(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	page, err := d.Ops.List(c.Request.Context(), p, c.DefaultQuery("path", "/"), c.Query("cursor"), limit)
	if err != nil {
		d.fail(c, err)
		return
	}
	entries := make([]gin.H, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, gin.H{"entry": e, "urls": d.entryURLs(t, e)})
	}
	c.JSON(http.StatusOK, gin.H{"directory": page.Directory, "entries": entries, "next_cursor": page.NextCursor})
}

// APIMkdir creates a directory.
func (d *Deps) APIMkdir(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		d.fail(c, errors.New(errors.CodeValidationFailed, "body must be JSON {path}"))
		return
	}
	res, err := d.Ops.Mkdir(c.Request.Context(), p, body.Path, key)
	if err != nil {
		d.fail(c, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	d.mutationJSON(c, t, res, status)
}

// APIMove moves a page.
func (d *Deps) APIMove(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		d.fail(c, errors.New(errors.CodeValidationFailed, "body must be JSON {from, to}"))
		return
	}
	expected, err := preconditions(c, nil, false)
	if err != nil {
		d.fail(c, err)
		return
	}
	if err := d.checkETagEntry(c, p, body.From); err != nil {
		d.fail(c, err)
		return
	}
	res, err := d.Ops.Move(c.Request.Context(), p, models.MoveInput{From: body.From, To: body.To, ExpectedRevision: expected, RequestID: key})
	if err != nil {
		d.fail(c, err)
		return
	}
	d.mutationJSON(c, t, res, http.StatusOK)
}

// APISearch searches current content.
func (d *Deps) APISearch(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	page, err := d.Ops.Search(c.Request.Context(), p, c.Query("q"), c.Query("path_prefix"), c.Query("cursor"), limit)
	if err != nil {
		d.fail(c, err)
		return
	}
	hits := make([]gin.H, 0, len(page.Hits))
	for _, h := range page.Hits {
		hits = append(hits, gin.H{"path": h.Path, "title": h.Title, "snippet": h.Snippet, "revision": h.Revision, "urls": d.entryURLs(t, models.EntryInfo{HTMLPath: h.HTMLPath, RawPath: h.RawPath, JSONPath: h.JSONPath})})
	}
	c.JSON(http.StatusOK, gin.H{"query": page.Query, "hits": hits, "next_cursor": page.NextCursor})
}

// APIHistory lists revisions.
func (d *Deps) APIHistory(c *gin.Context) {
	p, _, ok := d.apiCtx(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	page, err := d.Ops.History(c.Request.Context(), p, c.Query("path"), c.Query("cursor"), limit)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// APIRestore restores a revision.
func (d *Deps) APIRestore(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	var body struct {
		Path     string `json:"path"`
		Revision int64  `json:"revision"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		d.fail(c, errors.New(errors.CodeValidationFailed, "body must be JSON {path, revision}"))
		return
	}
	expected, err := preconditions(c, nil, false)
	if err != nil {
		d.fail(c, err)
		return
	}
	if err := d.checkETagEntry(c, p, body.Path); err != nil {
		d.fail(c, err)
		return
	}
	res, err := d.Ops.Restore(c.Request.Context(), p, body.Path, body.Revision, expected, key)
	if err != nil {
		d.fail(c, err)
		return
	}
	d.mutationJSON(c, t, res, http.StatusOK)
}

// APIPutAsset uploads or replaces an image (multipart "file" or raw body).
func (d *Deps) APIPutAsset(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	key, err := idempotencyKey(c)
	if err != nil {
		d.fail(c, err)
		return
	}
	expected, err := preconditions(c, nil, c.GetHeader("If-None-Match") == "*")
	if err != nil {
		d.fail(c, err)
		return
	}
	var data []byte
	if fh, ferr := c.FormFile("file"); ferr == nil {
		f, err := fh.Open()
		if err != nil {
			d.fail(c, err)
			return
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, d.Ops.Quotas.MaxAssetBytes+1))
		if err != nil {
			d.fail(c, err)
			return
		}
	} else {
		data, err = io.ReadAll(io.LimitReader(c.Request.Body, d.Ops.Quotas.MaxAssetBytes+1))
		if err != nil {
			d.fail(c, err)
			return
		}
	}
	if int64(len(data)) > d.Ops.Quotas.MaxAssetBytes {
		d.fail(c, errors.New(errors.CodeTooLarge, "image exceeds the upload limit"))
		return
	}
	if err := d.checkETagEntry(c, p, c.Query("path")); err != nil {
		d.fail(c, err)
		return
	}
	res, err := d.Ops.PutAsset(c.Request.Context(), p, models.AssetInput{Path: c.Query("path"), Data: data, ExpectedRevision: expected, RequestID: key})
	if err != nil {
		d.fail(c, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	d.mutationJSON(c, t, res, status)
}

// APIExport is the owner-session-only ZIP export.
func (d *Deps) APIExport(c *gin.Context) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return
	}
	if !p.IsOwnerSession() {
		d.fail(c, errors.New(errors.CodeForbidden, "export requires the owner's browser session"))
		return
	}
	data, err := d.Ops.Export(c.Request.Context(), p, t.Code)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="tmp-`+t.Code+`.zip"`)
	c.Data(http.StatusOK, "application/zip", data)
}

// ---- owner-session-only share-link management ----

func (d *Deps) ownerOnlyAPI(c *gin.Context) (models.Principal, *models.Tenant, bool) {
	p, t, ok := d.apiCtx(c)
	if !ok {
		return p, t, false
	}
	if !p.IsOwnerSession() {
		d.fail(c, errors.New(errors.CodeForbidden, "sharing links are managed from the owner's browser session only"))
		return p, t, false
	}
	return p, t, true
}

// APIShareCreate mints a link (returns the secret URL exactly once).
func (d *Deps) APIShareCreate(c *gin.Context) {
	p, _, ok := d.ownerOnlyAPI(c)
	if !ok {
		return
	}
	var body struct {
		EntryID int64  `json:"entry_id"`
		Path    string `json:"path"`
		Label   string `json:"label"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		d.fail(c, errors.New(errors.CodeValidationFailed, "body must be JSON {entry_id | path, label}"))
		return
	}
	if body.EntryID == 0 && body.Path != "" {
		doc, err := d.Ops.Read(c.Request.Context(), p, body.Path, 0)
		if err != nil {
			d.fail(c, err)
			return
		}
		body.EntryID = doc.Entry.ID
	}
	token, info, err := d.Ops.CreateShareGrant(c.Request.Context(), p, body.EntryID, body.Label)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"link": info, "url": d.abs("/s/" + token), "warning": "Anyone with this link can view and edit this document."})
}

// APIShareList lists metadata only.
func (d *Deps) APIShareList(c *gin.Context) {
	p, _, ok := d.ownerOnlyAPI(c)
	if !ok {
		return
	}
	entryID, _ := strconv.ParseInt(c.Query("entry_id"), 10, 64)
	links, err := d.Ops.ListShareGrants(c.Request.Context(), p, entryID)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"links": links})
}

// APIShareRevoke revokes a link.
func (d *Deps) APIShareRevoke(c *gin.Context) {
	p, _, ok := d.ownerOnlyAPI(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := d.Ops.RevokeShareGrant(c.Request.Context(), p, id); err != nil {
		d.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// APIShareRefreshAssets re-approves embedded assets.
func (d *Deps) APIShareRefreshAssets(c *gin.Context) {
	p, _, ok := d.ownerOnlyAPI(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	info, err := d.Ops.RefreshShareGrantAssets(c.Request.Context(), p, id)
	if err != nil {
		d.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"link": info})
}

// APIOrganize suggests where content belongs: POST /api/v1/orgs/{org}/organize
// with {"content", "filename", "hint"}. Read scope; nothing is written.
func (d *Deps) APIOrganize(c *gin.Context) {
	p, _, ok := d.apiCtx(c)
	if !ok {
		return
	}
	var in struct {
		Content  string `json:"content"`
		Filename string `json:"filename"`
		Hint     string `json:"hint"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonError(c, errors.New(errors.CodeValidationFailed, "body must be JSON with content, filename and hint"))
		return
	}
	pl, err := d.Ops.SuggestPlacement(c.Request.Context(), p, models.PlacementInput{Content: in.Content, Filename: in.Filename, Hint: in.Hint})
	if err != nil {
		d.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"placement": pl, "write_with": gin.H{"path": pl.UniquePlacementPath(), "expected_revision": 0}})
}
