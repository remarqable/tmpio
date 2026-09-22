package models

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/ai"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// ReceiptLifetime is how long an idempotent result is replayed.
const ReceiptLifetime = 24 * time.Hour

// Ops holds the configuration the filesystem operations need. One instance is
// shared by the browser, REST and MCP adapters, which never implement their own
// permission or storage logic.
type Ops struct {
	Quotas    config.Quotas
	CursorKey []byte

	// AI is the optional model used by SuggestPlacement; nil means heuristics only.
	AI                ai.Completer
	AIMaxCallsPerHour int64
}

// EntryInfo is the adapter-neutral description of an entry after an operation.
type EntryInfo struct {
	ID          int64     `json:"id,omitempty"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Order       *int      `json:"order,omitempty"`
	Revision    int64     `json:"revision"`
	Deleted     bool      `json:"deleted,omitempty"`
	SizeBytes   int64     `json:"size_bytes,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
	HTMLPath    string    `json:"html_path"`
	RawPath     string    `json:"raw_path"`
	JSONPath    string    `json:"json_path"`
}

// Info converts an entry.
func Info(e *Entry) EntryInfo {
	tags := []string(e.Tags)
	if tags == nil {
		tags = []string{}
	}
	return EntryInfo{
		ID: e.ID, Path: e.Path, Kind: e.Kind, Title: e.Title, Description: e.Description, Tags: tags,
		Order: e.OrderIndex, Revision: e.CurrentRevision, Deleted: e.Deleted(), SizeBytes: e.SizeBytes,
		UpdatedAt: e.UpdatedAt, CreatedAt: e.CreatedAt,
		HTMLPath: e.HTMLPath(), RawPath: e.Path, JSONPath: e.JSONPath(),
	}
}

// WriteInput is a full-content page or config replacement.
type WriteInput struct {
	Path             string
	Content          string
	ExpectedRevision int64 // 0 = create only
	Summary          string
	RequestID        string
}

// MutationResult is returned by every mutation and cached in receipts.
type MutationResult struct {
	Entry     EntryInfo `json:"entry"`
	Created   bool      `json:"created,omitempty"`
	Unchanged bool      `json:"unchanged,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	Rewrites  []string  `json:"rewrites,omitempty"`
	Aliases   []string  `json:"aliases,omitempty"`
}

// Write creates or replaces a page or the site configuration.
func (o *Ops) Write(ctx context.Context, p Principal, in WriteInput) (*MutationResult, error) {
	if !p.Can(ScopeWrite) {
		return nil, errors.New(errors.CodeInsufficientScope, "content:write scope is required")
	}
	info, err := ValidatePath(in.Path)
	if err != nil {
		return nil, err
	}
	switch info.Kind {
	case KindPage:
		if int64(len(in.Content)) > o.Quotas.MaxPageBytes {
			return nil, errors.Newf(errors.CodeTooLarge, "page exceeds %d bytes", o.Quotas.MaxPageBytes)
		}
	case KindConfig:
		if p.IsShare() {
			return nil, errors.New(errors.CodeInsufficientScope, "sharing links cannot edit site configuration")
		}
		if int64(len(in.Content)) > o.Quotas.MaxConfigBytes {
			return nil, errors.Newf(errors.CodeTooLarge, "tmp.yaml exceeds %d bytes", o.Quotas.MaxConfigBytes)
		}
		if _, err := ParseSiteConfig(in.Content); err != nil {
			return nil, err
		}
	case KindFile:
		if int64(len(in.Content)) > o.Quotas.MaxFileBytes {
			return nil, errors.Newf(errors.CodeTooLarge, "file exceeds %d bytes", o.Quotas.MaxFileBytes)
		}
		if err := ValidateFileContent(info, in.Content); err != nil {
			return nil, err
		}
	case KindAsset:
		return nil, errors.New(errors.CodeInvalidPath, "images and PDFs are uploaded through the asset endpoint")
	default:
		return nil, errors.New(errors.CodeInvalidPath, "a page path must end in .md; use mkdir for directories")
	}
	if len(in.Summary) > 500 {
		return nil, errors.New(errors.CodeValidationFailed, "summary must be 500 characters or fewer")
	}
	if !strings.HasSuffix(in.Content, "\n") && in.Content != "" {
		in.Content += "\n"
	}
	if !ValidUTF8(in.Content) {
		return nil, errors.New(errors.CodeValidationFailed, "content must be valid UTF-8")
	}

	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "write", in.RequestID, digest(in.Path, in.Content, in.ExpectedRevision)); err != nil || cached != nil {
			out = cached
			return err
		}
		if p.IsShare() {
			// Lock and recheck the grant so revocation and this write have a defined order:
			// once a revocation commits, no later mutation commits under the grant.
			var g ShareGrant
			if err := tx.Clauses(forUpdate).First(&g, p.ShareGrantID).Error; err != nil {
				return errors.New(errors.CodeNotFound, "not found")
			}
			if g.RevokedAt != nil || g.EntryID != p.ShareEntryID {
				return errors.New(errors.CodeAccessEnded, "access to this document has ended")
			}
			e, err := entryByID(tx, p.ShareEntryID, false)
			if err != nil {
				return err
			}
			if e == nil || e.Deleted() || e.Path != info.Path {
				return errors.New(errors.CodeNotFound, "not found")
			}
		}
		e, err := entryByPath(tx, info.Path, true)
		if err != nil {
			return err
		}
		res, err := o.writeLocked(tx, p, info, e, in)
		if err != nil {
			return err
		}
		out = res
		return saveReceipt(tx, p, "write", in.RequestID, digest(in.Path, in.Content, in.ExpectedRevision), res)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeLocked performs the compare-and-swap write. e is the locked existing
// entry (live or tombstone) or nil.
func (o *Ops) writeLocked(tx *gorm.DB, p Principal, info *PathInfo, e *Entry, in WriteInput) (*MutationResult, error) {
	created := false
	revived := false
	switch {
	case e == nil:
		if in.ExpectedRevision != 0 {
			return nil, &errors.AppError{Code: errors.CodeNotFound, Message: "page does not exist; use expected_revision 0 to create it", ExpectedRevision: in.ExpectedRevision}
		}
		created = true
	case e.Deleted():
		if in.ExpectedRevision != 0 && in.ExpectedRevision != e.CurrentRevision {
			return nil, conflict(e.CurrentRevision, in.ExpectedRevision)
		}
		if e.Kind != info.Kind {
			return nil, errors.New(errors.CodePathConflict, "a deleted entry of a different kind occupies this path")
		}
		revived = true
	default:
		if e.Kind != info.Kind {
			return nil, errors.Newf(errors.CodePathConflict, "%s already exists at this path as a %s", info.Path, e.Kind)
		}
		if in.ExpectedRevision == 0 {
			return nil, &errors.AppError{Code: errors.CodeRevisionConflict, Message: "page already exists; read it and pass its current revision as expected_revision", CurrentRevision: e.CurrentRevision}
		}
		if in.ExpectedRevision != e.CurrentRevision {
			return nil, conflict(e.CurrentRevision, in.ExpectedRevision)
		}
	}

	// Validate and derive fields with the publishing renderer.
	var meta PageMeta
	var plain string
	var warnings []string
	if info.Kind == KindPage {
		res, err := render.Render(in.Content, render.Options{
			SourcePath:            info.Path,
			FilenameFallbackTitle: strings.TrimSuffix(info.Name, ".md"),
			ResolveAsset:          assetResolver(tx, ""),
			ResolveLink:           linkResolver(tx),
			MaxBytes:              int(o.Quotas.MaxPageBytes),
		})
		if err != nil {
			return nil, renderError(err)
		}
		meta = PageMeta{Title: res.Title, Description: res.Meta.Description, Tags: res.Meta.Tags, Order: res.Meta.Order, Extra: res.Meta.Extra}
		plain = res.PlainText
		for _, w := range res.Warnings {
			warnings = append(warnings, w.Message)
		}
	} else if info.Kind == KindFile {
		meta = PageMeta{Title: info.Name}
		plain = filePlainText(in.Content)
	} else {
		cfg, err := ParseSiteConfig(in.Content)
		if err != nil {
			return nil, err
		}
		meta = PageMeta{Title: "tmp.yaml", Description: cfg.Description}
		plain = ""
	}

	if e != nil && !e.Deleted() {
		cur, _, err := currentSource(tx, e)
		if err != nil {
			return nil, err
		}
		if cur == in.Content {
			r := &MutationResult{Entry: Info(e), Unchanged: true, Warnings: warnings}
			return r, nil
		}
	}

	if created || revived {
		if err := o.checkPageQuota(tx, info.Kind); err != nil {
			return nil, err
		}
	}
	if err := o.checkRetainedQuota(tx, int64(len(in.Content))); err != nil {
		return nil, err
	}

	if created {
		parentID, err := o.ensureParents(tx, p, info.Parent, in.RequestID)
		if err != nil {
			return nil, err
		}
		if err := checkRenderedFree(tx, info); err != nil {
			return nil, err
		}
		e = &Entry{TenantID: p.TenantID, Kind: info.Kind, Path: info.Path, RenderedPath: info.Rendered, ParentID: parentID}
		if err := tx.Create(e).Error; err != nil {
			return nil, err
		}
	} else if revived {
		parentID, err := o.ensureParents(tx, p, info.Parent, in.RequestID)
		if err != nil {
			return nil, err
		}
		e.ParentID = parentID
		e.DeletedAt = nil
	}

	op := "update"
	if created {
		op = "create"
	} else if revived {
		op = "restore"
	}
	src := in.Content
	rev, err := o.appendRevision(tx, p, e, op, &src, nil, meta, in.Summary, in.RequestID, nil)
	if err != nil {
		return nil, err
	}
	e.CurrentRevision = rev.Seq
	e.Title, e.Description, e.Tags, e.OrderIndex, e.PlainText, e.SizeBytes = meta.Title, meta.Description, StringArray(meta.Tags), meta.Order, plain, int64(len(in.Content))
	if e.Tags == nil {
		e.Tags = StringArray{}
	}
	if err := tx.Model(e).Select("current_revision", "title", "description", "tags", "order_index", "plain_text", "size_bytes", "deleted_at", "parent_id").Updates(map[string]any{
		"current_revision": e.CurrentRevision, "title": e.Title, "description": e.Description, "tags": e.Tags,
		"order_index": e.OrderIndex, "plain_text": e.PlainText, "size_bytes": e.SizeBytes, "deleted_at": nil, "parent_id": e.ParentID,
	}).Error; err != nil {
		return nil, err
	}
	if err := bumpGeneration(tx, p.TenantID); err != nil {
		return nil, err
	}
	if err := audit(tx, p, "entry."+op, &e.ID, &rev.Seq, in.RequestID); err != nil {
		return nil, err
	}
	fresh, err := entryByID(tx, e.ID, false)
	if err != nil {
		return nil, err
	}
	return &MutationResult{Entry: Info(fresh), Created: created || revived, Warnings: warnings}, nil
}

// Mkdir creates a directory, and any missing parents, atomically.
func (o *Ops) Mkdir(ctx context.Context, p Principal, path, requestID string) (*MutationResult, error) {
	if !p.Can(ScopeWrite) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:write scope is required")
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if info.Kind != KindDirectory {
		return nil, errors.New(errors.CodeInvalidPath, "directory paths must not carry a file extension")
	}
	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "mkdir", requestID, digest(info.Path)); err != nil || cached != nil {
			out = cached
			return err
		}
		e, err := entryByPath(tx, info.Path, true)
		if err != nil {
			return err
		}
		created := false
		if e != nil && !e.Deleted() {
			if e.Kind != KindDirectory {
				return errors.Newf(errors.CodePathConflict, "%s exists and is a %s", info.Path, e.Kind)
			}
		} else {
			id, err := o.ensureParents(tx, p, info.Path, requestID)
			if err != nil {
				return err
			}
			e, err = entryByID(tx, *id, false)
			if err != nil {
				return err
			}
			created = true
		}
		out = &MutationResult{Entry: Info(e), Created: created}
		return saveReceipt(tx, p, "mkdir", requestID, digest(info.Path), out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ensureParents creates every missing directory on dir (inclusive) and returns
// the ID of dir. Tombstoned directories are revived.
func (o *Ops) ensureParents(tx *gorm.DB, p Principal, dir string, requestID string) (*int64, error) {
	chain := append(Ancestors(dir), dir)
	var parentID *int64
	for _, d := range chain {
		e, err := entryByPath(tx, d, true)
		if err != nil {
			return nil, err
		}
		if e != nil && !e.Deleted() {
			if e.Kind != KindDirectory {
				return nil, errors.Newf(errors.CodePathConflict, "%s exists and is a %s, not a directory", d, e.Kind)
			}
			parentID = &e.ID
			continue
		}
		if d != "/" {
			if err := o.checkPageQuota(tx, KindDirectory); err != nil {
				return nil, err
			}
		}
		if e == nil {
			info := &PathInfo{Path: d, Rendered: d, Kind: KindDirectory}
			if d == "/" {
				info.Rendered = "/"
			}
			if err := checkRenderedFree(tx, info); err != nil {
				return nil, err
			}
			e = &Entry{TenantID: p.TenantID, Kind: KindDirectory, Path: d, RenderedPath: info.Rendered, ParentID: parentID, Title: dirTitle(d)}
			if err := tx.Create(e).Error; err != nil {
				return nil, err
			}
			rev, err := o.appendRevision(tx, p, e, "create", nil, nil, PageMeta{Title: e.Title}, "", requestID, nil)
			if err != nil {
				return nil, err
			}
			if err := tx.Model(e).Update("current_revision", rev.Seq).Error; err != nil {
				return nil, err
			}
			if err := audit(tx, p, "entry.mkdir", &e.ID, &rev.Seq, requestID); err != nil {
				return nil, err
			}
		} else {
			rev, err := o.appendRevision(tx, p, e, "restore", nil, nil, PageMeta{Title: e.Title}, "", requestID, nil)
			if err != nil {
				return nil, err
			}
			if err := tx.Model(e).Updates(map[string]any{"deleted_at": nil, "parent_id": parentID, "current_revision": rev.Seq}).Error; err != nil {
				return nil, err
			}
		}
		if err := bumpGeneration(tx, p.TenantID); err != nil {
			return nil, err
		}
		parentID = &e.ID
	}
	return parentID, nil
}

func dirTitle(d string) string {
	if d == "/" {
		return ""
	}
	name := d[strings.LastIndexByte(d, '/')+1:]
	name = strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " ")
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// checkRenderedFree ensures neither an entry nor an alias occupies the rendered path.
func checkRenderedFree(tx *gorm.DB, info *PathInfo) error {
	var n int64
	if err := tx.Model(&Entry{}).Where("rendered_path = ? AND path <> ?", info.Rendered, info.Path).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return errors.Newf(errors.CodePathConflict, "%s would collide with an existing entry in the rendered namespace", info.Path)
	}
	if err := tx.Model(&PathAlias{}).Where("rendered_path = ? OR path = ?", info.Rendered, info.Path).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return errors.Newf(errors.CodePathConflict, "%s is a redirect left by a previous move and cannot be reused", info.Path)
	}
	return nil
}

func (o *Ops) checkPageQuota(tx *gorm.DB, kind string) error {
	var n int64
	if err := tx.Model(&Entry{}).Where("kind = ? AND deleted_at IS NULL", kind).Count(&n).Error; err != nil {
		return err
	}
	switch kind {
	case KindPage:
		if n >= o.Quotas.MaxPages {
			return errors.Newf(errors.CodeQuotaExceeded, "page quota of %d reached; export and delete pages or ask the operator to purge history", o.Quotas.MaxPages)
		}
	case KindDirectory:
		if n-1 >= o.Quotas.MaxDirectories { // root is not counted
			return errors.Newf(errors.CodeQuotaExceeded, "directory quota of %d reached", o.Quotas.MaxDirectories)
		}
	}
	return nil
}

func (o *Ops) checkRetainedQuota(tx *gorm.DB, adding int64) error {
	var rev, blobs int64
	if err := tx.Raw(`SELECT COALESCE(SUM(size_bytes),0) FROM revision`).Scan(&rev).Error; err != nil {
		return err
	}
	if err := tx.Raw(`SELECT COALESCE(SUM(size_bytes),0) FROM asset_blob`).Scan(&blobs).Error; err != nil {
		return err
	}
	if rev+blobs+adding > o.Quotas.MaxRetainedBytes {
		return errors.Newf(errors.CodeQuotaExceeded, "retained history quota of %d bytes would be exceeded; export your site or request an operator purge", o.Quotas.MaxRetainedBytes)
	}
	return nil
}

func (o *Ops) appendRevision(tx *gorm.DB, p Principal, e *Entry, op string, source *string, blobID *int64, meta PageMeta, summary, requestID string, priorPath *string) (*Revision, error) {
	mb, _ := json.Marshal(meta)
	var size int64
	if source != nil {
		size = int64(len(*source))
	}
	rev := &Revision{
		TenantID: p.TenantID, EntryID: e.ID, Seq: e.CurrentRevision + 1, Operation: op, Path: e.Path, PriorPath: priorPath,
		Source: source, BlobID: blobID, Meta: mb, ActorKind: p.Kind, AuthorName: p.AuthorName, Summary: summary, RequestID: requestID, SizeBytes: size,
	}
	if p.UserID != 0 && !p.IsShare() {
		rev.UserID = &p.UserID
	}
	if p.OAuthGrantID != 0 {
		rev.OAuthGrantID = &p.OAuthGrantID
	}
	if p.APITokenID != 0 {
		rev.APITokenID = &p.APITokenID
	}
	if p.ShareGrantID != 0 {
		rev.ShareGrantID = &p.ShareGrantID
	}
	if err := tx.Create(rev).Error; err != nil {
		return nil, err
	}
	return rev, nil
}

func bumpGeneration(tx *gorm.DB, tenantID int64) error {
	return tx.Exec(`UPDATE tenant SET generation = generation + 1 WHERE id = ?`, tenantID).Error
}

func audit(tx *gorm.DB, p Principal, action string, entryID *int64, seq *int64, requestID string) error {
	ev := &AuditEvent{TenantID: p.TenantID, ActorKind: p.Kind, Action: action, EntryID: entryID, RevisionSeq: seq, RequestID: requestID}
	if p.UserID != 0 && !p.IsShare() {
		ev.UserID = &p.UserID
	}
	if p.OAuthGrantID != 0 {
		ev.OAuthGrantID = &p.OAuthGrantID
	}
	if p.APITokenID != 0 {
		ev.APITokenID = &p.APITokenID
	}
	if p.ShareGrantID != 0 {
		ev.ShareGrantID = &p.ShareGrantID
	}
	return tx.Create(ev).Error
}

func conflict(current, expected int64) error {
	return &errors.AppError{Code: errors.CodeRevisionConflict, Message: "the page changed since you read it; read the current revision, merge your changes and retry with expected_revision set to it", CurrentRevision: current, ExpectedRevision: expected}
}

func renderError(err error) error {
	var ve *render.ValidationError
	if e, ok := err.(*render.ValidationError); ok {
		ve = e
	}
	if ve == nil {
		return errors.Wrap(errors.CodeValidationFailed, err.Error(), err)
	}
	ae := errors.New(errors.CodeValidationFailed, "The content is invalid.")
	for _, fe := range ve.Errors {
		ae.WithField(fe.Field, fe.Line, fe.Message)
	}
	return ae
}

// digest binds a receipt to its payload.
func digest(parts ...any) []byte {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, p := range parts {
		_ = enc.Encode(p)
	}
	return h.Sum(nil)
}

func loadReceipt(tx *gorm.DB, p Principal, op, requestID string, d []byte) (*MutationResult, error) {
	if requestID == "" {
		return nil, errors.New(errors.CodeValidationFailed, "request_id (UUID) is required for every mutation")
	}
	if !ValidUUID(requestID) {
		return nil, errors.New(errors.CodeValidationFailed, "request_id must be a UUID")
	}
	var r MutationReceipt
	err := tx.Where("principal_key = ? AND request_id = ?", p.Key(), requestID).First(&r).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	if time.Now().After(r.ExpiresAt) {
		return nil, nil
	}
	if r.Operation != op || !bytesEqual(r.RequestDigest, d) {
		return nil, errors.New(errors.CodeIdempotencyMismatch, "request_id was already used with a different operation or payload")
	}
	var res MutationResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func saveReceipt(tx *gorm.DB, p Principal, op, requestID string, d []byte, res *MutationResult) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	r := &MutationReceipt{TenantID: p.TenantID, PrincipalKey: p.Key(), RequestID: requestID, Operation: op, RequestDigest: d, Result: b, ExpiresAt: time.Now().Add(ReceiptLifetime)}
	if p.ShareGrantID != 0 {
		r.ShareGrantID = &p.ShareGrantID
	}
	return tx.Create(r).Error
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assetResolver resolves a local image site path to an asset URL under prefix.
func assetResolver(tx *gorm.DB, prefix string) func(string) (string, bool) {
	return func(sitePath string) (string, bool) {
		e, err := liveEntryByPath(tx, sitePath)
		if err != nil || e == nil || e.Kind != KindAsset {
			return "", false
		}
		return prefix + e.Path, true
	}
}

// linkResolver reports whether an internal link target exists (entries or aliases).
func linkResolver(tx *gorm.DB) func(string) bool {
	return func(sitePath string) bool {
		if sitePath == "/" {
			return true
		}
		candidates := []string{sitePath}
		if strings.HasSuffix(sitePath, "/") {
			candidates = append(candidates, strings.TrimSuffix(sitePath, "/"), sitePath+"index.md")
		} else if !strings.Contains(sitePath[strings.LastIndexByte(sitePath, '/')+1:], ".") {
			candidates = append(candidates, sitePath+".md", sitePath+"/index.md")
		}
		for _, c := range candidates {
			if e, _ := liveEntryByPath(tx, c); e != nil {
				return true
			}
			if a, _ := aliasByPath(tx, c); a != nil {
				return true
			}
		}
		return false
	}
}

// initialFilesystem creates /, /tmp.yaml and /index.md for a new tenant.
func initialFilesystem(tx *gorm.DB, tenantID, userID int64, cfgSrc, indexSrc string) error {
	// Creating a new organization's first files, on behalf of its owner.
	p := Principal{Kind: PrincipalOwner, TenantID: tenantID, UserID: userID, Scopes: AllScopes, Role: RoleOwner}
	o := &Ops{Quotas: config.Quotas{MaxPages: 10, MaxDirectories: 10, MaxPageBytes: 1 << 20, MaxConfigBytes: 1 << 16, MaxRetainedBytes: 1 << 30}}
	rootID, err := o.ensureParents(tx, p, "/", "")
	if err != nil {
		return err
	}
	_ = rootID
	cfgInfo, _ := ValidatePath(ConfigPath)
	if _, err := o.writeLocked(tx, p, cfgInfo, nil, WriteInput{Path: ConfigPath, Content: cfgSrc}); err != nil {
		return err
	}
	idxInfo, _ := ValidatePath("/index.md")
	if _, err := o.writeLocked(tx, p, idxInfo, nil, WriteInput{Path: "/index.md", Content: indexSrc}); err != nil {
		return err
	}
	return nil
}

// PriorWritePath returns the path a previous write with this request id
// committed to. A retry must be placed once and only once: deciding a fresh
// location on the second attempt would file the same document twice and turn
// a safe retry into a duplicate.
func (o *Ops) PriorWritePath(ctx context.Context, p Principal, requestID string) (string, bool, error) {
	if requestID == "" || !ValidUUID(requestID) {
		return "", false, nil
	}
	var (
		path  string
		found bool
	)
	err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		var r MutationReceipt
		err := tx.Where("principal_key = ? AND request_id = ? AND operation = ?", p.Key(), requestID, "write").First(&r).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if time.Now().After(r.ExpiresAt) {
			return nil
		}
		var res MutationResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			return nil
		}
		path, found = res.Entry.Path, res.Entry.Path != ""
		return nil
	})
	return path, found, err
}
