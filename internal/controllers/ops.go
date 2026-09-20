package controllers

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// Owner content operations. Each lives at a verb-prefixed URL of the page it
// acts on (/edit/research/circle) and renders inside the site shell, so
// editing never leaves the site.

// EditPage shows the Markdown editor for a page or tmp.yaml.
func (d *Deps) EditPage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	if !e.HasSource() {
		d.fail(c, errors.New(errors.CodeValidationFailed, i18n.T("en", "editor.not_text")))
		return
	}
	doc, err := d.Ops.Read(c.Request.Context(), a.Prin, e.Path, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "editor.title") + " " + doc.Entry.Path
	sv.Entry = doc.Entry
	sv.CurrentHTML = e.HTMLPath()
	d.render(c, http.StatusOK, "pages/ops/editor.html", "layout/site", gin.H{
		"Site": sv, "Path": doc.Entry.Path, "Content": doc.Source, "Expected": doc.Entry.CurrentRevision, "Entry": doc.Entry,
		"RequestID": uuidV4(), "Action": verbURL("edit", doc.Entry), "Saved": c.Query("saved"), "IsConfig": doc.Entry.Kind == models.KindConfig, "IsFile": doc.Entry.Kind == models.KindFile,
	})
}

// SaveEdit handles the editor form. Unsaved text is preserved on any error.
func (d *Deps) SaveEdit(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	content := strings.ReplaceAll(c.PostForm("content"), "\r\n", "\n")
	expected, _ := strconv.ParseInt(c.PostForm("expected_revision"), 10, 64)
	reqID := c.PostForm("request_id")
	if !models.ValidUUID(reqID) {
		reqID = uuidV4()
	}
	res, err := d.Ops.Write(c.Request.Context(), a.Prin, models.WriteInput{Path: e.Path, Content: content, ExpectedRevision: expected, Summary: strings.TrimSpace(c.PostForm("summary")), RequestID: reqID})
	if err != nil {
		ae := errors.As(err)
		if ae.HTTPStatus() >= 500 {
			d.fail(c, err)
			return
		}
		sv.Title = i18n.T("en", "editor.title") + " " + e.Path
		sv.Entry = e
		data := gin.H{"Site": sv, "Path": e.Path, "Content": c.PostForm("content"), "Expected": expected, "Entry": e, "Error": ae, "RequestID": uuidV4(), "Summary": c.PostForm("summary"), "Action": verbURL("edit", e), "IsConfig": e.Kind == models.KindConfig, "IsFile": e.Kind == models.KindFile}
		if ae.Code == errors.CodeRevisionConflict {
			data["Conflict"] = ae.CurrentRevision
		}
		d.render(c, ae.HTTPStatus(), "pages/ops/editor.html", "layout/site", data)
		return
	}
	if res.Entry.Kind == models.KindConfig {
		c.Redirect(http.StatusSeeOther, "/admin/settings?saved=1")
		return
	}
	c.Redirect(http.StatusSeeOther, res.Entry.HTMLPath+"?saved="+strconv.FormatInt(res.Entry.Revision, 10))
}

// NewPage shows the create form (page, folder or image) for a directory.
func (d *Deps) NewPage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	dir := opDir(c)
	if _, err := models.ValidatePath(dir); err != nil {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return
	}
	if dir != "/" {
		if doc, err := d.Ops.Read(c.Request.Context(), a.Prin, dir, 0); err != nil || doc.Entry.Kind != models.KindDirectory {
			d.fail(c, errors.New(errors.CodeNotFound, ""))
			return
		}
	}
	sv.Title = i18n.T("en", "editor.new_title")
	sv.CurrentDir = dir
	d.render(c, http.StatusOK, "pages/ops/new.html", "layout/site", gin.H{
		"Site": sv, "Dir": dir, "Kind": c.Query("kind"), "Content": "# \n\n", "RequestID": uuidV4(), "Action": "/new" + dirHTML(dir), "Error": c.Query("error"),
	})
}

func dirHTML(dir string) string {
	if dir == "/" {
		return "/"
	}
	return dir + "/"
}

// CreateEntry handles the create form: kind=page (name + content), kind=folder (name), kind=image (file).
func (d *Deps) CreateEntry(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	dir := opDir(c)
	name := strings.ToLower(strings.TrimSpace(c.PostForm("name")))
	base := strings.TrimSuffix(dir, "/")
	back := "/new" + dirHTML(dir)
	switch c.PostForm("kind") {
	case "folder":
		res, err := d.Ops.Mkdir(c.Request.Context(), a.Prin, base+"/"+name, uuidV4())
		if err != nil {
			d.flashFail(c, err, back)
			return
		}
		c.Redirect(http.StatusSeeOther, res.Entry.HTMLPath)
	case "image":
		fh, err := c.FormFile("file")
		if err != nil {
			d.flashFail(c, errors.New(errors.CodeValidationFailed, i18n.T("en", "upload.choose")), back)
			return
		}
		if fh.Size > d.Ops.Quotas.MaxAssetBytes {
			d.flashFail(c, errors.New(errors.CodeTooLarge, i18n.T("en", "upload.too_large")), back)
			return
		}
		f, err := fh.Open()
		if err != nil {
			d.fail(c, err)
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, d.Ops.Quotas.MaxAssetBytes+1))
		if err != nil {
			d.fail(c, err)
			return
		}
		if name == "" {
			name = strings.ToLower(fh.Filename)
		}
		if _, err := d.Ops.PutAsset(c.Request.Context(), a.Prin, models.AssetInput{Path: base + "/" + name, Data: data, RequestID: uuidV4()}); err != nil {
			d.flashFail(c, err, back)
			return
		}
		c.Redirect(http.StatusSeeOther, dirHTML(dir))
	default:
		if name != "" && !strings.HasSuffix(name, ".md") && models.FileMIME(name) == "" {
			name += ".md" // a page unless a known file extension was given
		}
		path := base + "/" + name
		content := strings.ReplaceAll(c.PostForm("content"), "\r\n", "\n")
		reqID := c.PostForm("request_id")
		if !models.ValidUUID(reqID) {
			reqID = uuidV4()
		}
		res, err := d.Ops.Write(c.Request.Context(), a.Prin, models.WriteInput{Path: path, Content: content, ExpectedRevision: 0, Summary: strings.TrimSpace(c.PostForm("summary")), RequestID: reqID})
		if err != nil {
			ae := errors.As(err)
			if ae.HTTPStatus() >= 500 {
				d.fail(c, err)
				return
			}
			sv.Title = i18n.T("en", "editor.new_title")
			sv.CurrentDir = dir
			d.render(c, ae.HTTPStatus(), "pages/ops/new.html", "layout/site", gin.H{"Site": sv, "Dir": dir, "Name": c.PostForm("name"), "Content": c.PostForm("content"), "RequestID": uuidV4(), "Action": "/new" + dirHTML(dir), "FieldError": ae})
			return
		}
		c.Redirect(http.StatusSeeOther, res.Entry.HTMLPath+"?saved="+strconv.FormatInt(res.Entry.Revision, 10))
	}
}

// Preview renders Markdown with the publishing renderer without saving (HTMX fragment).
func (d *Deps) Preview(c *gin.Context) {
	p, t := owner(c)
	content := strings.ReplaceAll(c.PostForm("content"), "\r\n", "\n")
	path := c.PostForm("path")
	if path == "" && c.PostForm("name") != "" {
		path = "/" + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.PostForm("name"))), ".md") + ".md"
	}
	info, err := models.ValidatePath(path)
	if err != nil || info.Kind != models.KindPage {
		info = &models.PathInfo{Path: "/untitled.md", Name: "untitled.md"}
	}
	res, err := d.Ops.RenderPage(c.Request.Context(), p.TenantID, "", info.Path, content)
	_ = t
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

func renderErrorPublic(err error) error {
	if ve, ok := err.(*render.ValidationError); ok {
		ae := errors.New(errors.CodeValidationFailed, i18n.T("en", "editor.invalid"))
		for _, fe := range ve.Errors {
			ae.WithField(fe.Field, fe.Line, fe.Message)
		}
		return ae
	}
	return errors.As(err)
}

// flashFail redirects back with an error message in the query string.
func (d *Deps) flashFail(c *gin.Context, err error, back string) {
	ae := errors.As(err)
	if ae.HTTPStatus() >= 500 {
		d.fail(c, err)
		return
	}
	msg := ae.Message
	for _, fe := range ae.FieldErrors {
		msg += "; " + fe.Message
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	c.Redirect(http.StatusSeeOther, back+sep+"error="+url.QueryEscape(msg))
}

// HistoryPage lists revisions of an entry; ?rev=N adds a comparison with the current one.
func (d *Deps) HistoryPage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	page, err := d.Ops.History(c.Request.Context(), a.Prin, e.Path, c.Query("cursor"), 50)
	if err != nil {
		d.fail(c, err)
		return
	}
	data := gin.H{"Site": sv, "Items": page.Items, "Entry": e, "Next": page.NextCursor, "Path": e.Path, "Action": verbURL("history", e), "Restored": c.Query("restored"), "Error": c.Query("error")}
	if rev, _ := strconv.ParseInt(c.Query("rev"), 10, 64); rev > 0 && e.Kind != models.KindDirectory {
		old, err := d.Ops.Read(c.Request.Context(), a.Prin, e.Path, rev)
		if err != nil {
			d.fail(c, err)
			return
		}
		cur, err := d.Ops.Read(c.Request.Context(), a.Prin, e.Path, 0)
		if err != nil {
			d.fail(c, err)
			return
		}
		data["Rev"], data["Source"], data["Diff"] = rev, old.Source, lineDiff(old.Source, cur.Source)
	}
	sv.Title = i18n.T("en", "history.title") + " " + e.Path
	sv.Entry = e
	sv.CurrentHTML = e.HTMLPath()
	d.render(c, http.StatusOK, "pages/ops/history.html", "layout/site", data)
}

// RestoreEntry restores a revision (POST /history/...).
func (d *Deps) RestoreEntry(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	rev, _ := strconv.ParseInt(c.PostForm("revision"), 10, 64)
	expected, _ := strconv.ParseInt(c.PostForm("expected_revision"), 10, 64)
	res, err := d.Ops.Restore(c.Request.Context(), a.Prin, e.Path, rev, expected, uuidV4())
	if err != nil {
		d.flashFail(c, err, verbURL("history", e))
		return
	}
	c.Redirect(http.StatusSeeOther, verbURL("history", e)+"?restored="+strconv.FormatInt(res.Entry.Revision, 10))
}

// SharePage lists and manages the sharing links of one page.
func (d *Deps) SharePage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	links, err := d.Ops.ListShareGrants(c.Request.Context(), a.Prin, e.ID)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "share.title") + " " + e.Path
	sv.Entry = e
	sv.CurrentHTML = e.HTMLPath()
	d.render(c, http.StatusOK, "pages/ops/share.html", "layout/site", gin.H{
		"Site": sv, "Entry": e, "Links": links, "Action": verbURL("share", e), "OrgPrefix": orgPrefix(a.Tenant),
		"NewLink": c.Query("new"), "Error": c.Query("error"), "Notice": c.Query("notice"),
	})
}

// ShareAction creates, revokes or refreshes a link for the page (POST /share/...).
func (d *Deps) ShareAction(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	back := verbURL("share", e)
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	switch c.PostForm("action") {
	case "revoke":
		if err := d.Ops.RevokeShareGrant(c.Request.Context(), a.Prin, id); err != nil {
			d.flashFail(c, err, back)
			return
		}
		c.Redirect(http.StatusSeeOther, back+"?notice="+url.QueryEscape(i18n.T("en", "share.revoked")))
	case "refresh":
		if _, err := d.Ops.RefreshShareGrantAssets(c.Request.Context(), a.Prin, id); err != nil {
			d.flashFail(c, err, back)
			return
		}
		c.Redirect(http.StatusSeeOther, back+"?notice="+url.QueryEscape(i18n.T("en", "share.refreshed")))
	default:
		token, _, err := d.Ops.CreateShareGrant(c.Request.Context(), a.Prin, e.ID, c.PostForm("label"))
		if err != nil {
			d.flashFail(c, err, back)
			return
		}
		// The token travels once through this redirect and is never stored in plaintext.
		c.Redirect(http.StatusSeeOther, back+"?new="+url.QueryEscape(d.abs("/s/"+token)))
	}
}

// MovePage shows the rename/move form.
func (d *Deps) MovePage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	sv.Title = i18n.T("en", "content.move") + " " + e.Path
	sv.Entry = e
	sv.CurrentHTML = e.HTMLPath()
	d.render(c, http.StatusOK, "pages/ops/move.html", "layout/site", gin.H{"Site": sv, "Entry": e, "Action": verbURL("move", e), "Error": c.Query("error")})
}

// MoveEntry performs the move (POST /move/...).
func (d *Deps) MoveEntry(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	expected, _ := strconv.ParseInt(c.PostForm("expected_revision"), 10, 64)
	res, err := d.Ops.Move(c.Request.Context(), a.Prin, models.MoveInput{From: e.Path, To: strings.TrimSpace(c.PostForm("to")), ExpectedRevision: expected, RequestID: uuidV4()})
	if err != nil {
		d.flashFail(c, err, verbURL("move", e))
		return
	}
	c.Redirect(http.StatusSeeOther, res.Entry.HTMLPath)
}

// DeleteEntry soft-deletes (POST /delete/...).
func (d *Deps) DeleteEntry(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	e, ok := d.opEntry(c, a)
	if !ok {
		return
	}
	expected, _ := strconv.ParseInt(c.PostForm("expected_revision"), 10, 64)
	if _, err := d.Ops.Delete(c.Request.Context(), a.Prin, e.Path, expected, uuidV4()); err != nil {
		d.flashFail(c, err, e.HTMLPath())
		return
	}
	c.Redirect(http.StatusSeeOther, dirHTML(parentDir(e.Path)))
}

// TrashPage lists deleted entries; POST restores one.
func (d *Deps) TrashPage(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	rows, err := d.Ops.Trash(c.Request.Context(), a.Tenant.ID)
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "trash.title")
	sv.CurrentHTML = "/trash"
	d.render(c, http.StatusOK, "pages/ops/trash.html", "layout/site", gin.H{"Site": sv, "Rows": rows, "Error": c.Query("error")})
}

// TrashRestore revives a deleted entry from the trash.
func (d *Deps) TrashRestore(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	rev, _ := strconv.ParseInt(c.PostForm("revision"), 10, 64)
	res, err := d.Ops.Restore(c.Request.Context(), a.Prin, c.PostForm("path"), rev, rev, uuidV4())
	if err != nil {
		d.flashFail(c, err, "/trash")
		return
	}
	c.Redirect(http.StatusSeeOther, res.Entry.HTMLPath)
}

// FormatGuide shows the content format reference inside the shell.
func (d *Deps) FormatGuide(c *gin.Context) {
	sv, _, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	sv.Title = i18n.T("en", "format.title")
	d.render(c, http.StatusOK, "pages/ops/format.html", "layout/site", gin.H{"Site": sv, "Title": sv.Title})
}

// uuidV4 generates a random UUID string for browser-originated mutations.
func uuidV4() string {
	b := []byte(models.NewToken())[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hex[b[i]>>4]
		out[j+1] = hex[b[i]&0x0f]
		j += 2
	}
	return string(out)
}

// QuickAdd takes pasted text from the sidebar dialog, asks the placement model
// where it belongs and shows the create form pre-filled at that location so the
// owner confirms with one click. Nothing is written here.
func (d *Deps) QuickAdd(c *gin.Context) {
	sv, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	content := c.PostForm("content")
	pl, err := d.Ops.SuggestPlacement(c.Request.Context(), a.Prin, models.PlacementInput{Content: content, Filename: c.PostForm("filename"), Hint: c.PostForm("hint")})
	if err != nil {
		if errors.Is(err, errors.CodeValidationFailed) {
			c.Redirect(http.StatusSeeOther, "/new/?error="+url.QueryEscape(errors.As(err).Message))
			return
		}
		d.fail(c, err)
		return
	}
	dir := pl.Directory
	sv.Title = i18n.T("en", "editor.new_title")
	sv.CurrentDir = dir
	name := strings.TrimSuffix(pl.Name, ".md")
	target := pl.UniquePlacementPath()
	if target != pl.Path {
		name = strings.TrimSuffix(strings.TrimPrefix(target, dirHTML(dir)), ".md")
	}
	if pl.Kind == models.KindPage && strings.TrimSpace(content) != "" && pl.Title != "" {
		if meta, _, _, perr := render.ParseFrontmatter(content); perr == nil && !meta.HasFrontmatter && !strings.HasPrefix(strings.TrimSpace(content), "#") {
			content = "# " + pl.Title + "\n\n" + content
		}
	}
	d.render(c, http.StatusOK, "pages/ops/new.html", "layout/site", gin.H{
		"Site": sv, "Dir": dir, "Kind": "page", "Name": name, "Content": content, "RequestID": uuidV4(), "Action": "/new" + dirHTML(dir), "Placement": pl, "AIEnabled": d.Cfg.AI.Enabled(),
	})
}

// BulkEntry applies one action to the rows selected in a directory listing
// (POST /bulk). Each selected row carries the revision the browser displayed,
// so a page that changed underneath the listing is refused rather than moved
// blind. Failures do not abort the run: every item is attempted and the
// redirect reports how many did not make it.
func (d *Deps) BulkEntry(c *gin.Context) {
	_, a, err := d.ownerShell(c, "ops")
	if err != nil {
		d.fail(c, err)
		return
	}
	back := dirHTML(strings.TrimSpace(c.PostForm("dir")))
	paths := c.PostFormArray("path")
	if len(paths) == 0 {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	revs := c.PostFormMap("rev")
	action := c.PostForm("action")
	to := strings.TrimSpace(c.PostForm("to"))
	// "travel" and "/travel" mean the same thing to anyone typing in a hurry.
	if to != "" && !strings.HasPrefix(to, "/") {
		to = "/" + to
	}

	// Deepest first, so selecting a folder and its contents still empties the
	// folder before trying to remove it.
	sorted := append([]string(nil), paths...)
	sort.Slice(sorted, func(i, j int) bool {
		if n, m := strings.Count(sorted[i], "/"), strings.Count(sorted[j], "/"); n != m {
			return n > m
		}
		return sorted[i] > sorted[j]
	})

	var failed []string
	var firstErr error
	done := 0
	for _, p := range sorted {
		rev, _ := strconv.ParseInt(revs[p], 10, 64)
		var e error
		switch action {
		case "move":
			e = func() error {
				if to == "" {
					return errors.New(errors.CodeValidationFailed, "a destination folder is required")
				}
				dest := strings.TrimSuffix(to, "/") + "/" + path.Base(p)
				_, err := d.Ops.Move(c.Request.Context(), a.Prin, models.MoveInput{From: p, To: dest, ExpectedRevision: rev, RequestID: uuidV4()})
				return err
			}()
		case "delete":
			_, e = d.Ops.Delete(c.Request.Context(), a.Prin, p, rev, uuidV4())
		default:
			d.flashFail(c, errors.New(errors.CodeValidationFailed, "unknown bulk action"), back)
			return
		}
		if e != nil {
			failed = append(failed, path.Base(p))
			if firstErr == nil {
				firstErr = e
			}
			continue
		}
		done++
	}
	if len(failed) > 0 {
		ae := errors.As(firstErr)
		msg := fmt.Sprintf("%d of %d items could not be %sd (%s): %s",
			len(failed), len(sorted), action, strings.Join(failed, ", "), ae.Message)
		c.Redirect(http.StatusSeeOther, back+"?error="+url.QueryEscape(msg))
		return
	}
	c.Redirect(http.StatusSeeOther, back)
}
