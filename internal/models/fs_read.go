package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// Document is a readable entry with its current (or requested) source.
type Document struct {
	Entry    *Entry
	Source   string
	Revision *Revision
	Blob     *AssetBlob // populated for assets on demand
}

// Read returns a live entry and its source. revision=0 means current.
// Tombstones are returned to authorized account readers with Deleted=true.
func (o *Ops) Read(ctx context.Context, p Principal, path string, revision int64) (*Document, error) {
	if !p.Can(ScopeRead) {
		return nil, errors.New(errors.CodeInsufficientScope, "content:read scope is required")
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if p.IsShare() && revision != 0 {
		return nil, notFound()
	}
	var doc *Document
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		e, err := entryByPath(tx, info.Path, false)
		if err != nil {
			return err
		}
		if e == nil {
			return notFound()
		}
		if p.IsShare() && (e.ID != p.ShareEntryID || e.Deleted()) {
			return notFound()
		}
		if e.Deleted() && p.IsShare() {
			return notFound()
		}
		if e.Kind == KindConfig && p.IsShare() {
			return notFound()
		}
		doc = &Document{Entry: e}
		if e.Kind == KindDirectory {
			return nil
		}
		seq := e.CurrentRevision
		if revision != 0 {
			seq = revision
		}
		rev, err := revisionBySeq(tx, e.ID, seq)
		if err != nil {
			return err
		}
		doc.Revision = rev
		if rev.Source != nil {
			doc.Source = *rev.Source
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// ReadEntryByID loads an entry by its stable ID (share-link routes).
func (o *Ops) ReadEntryByID(ctx context.Context, tenantID, entryID int64) (*Document, error) {
	var doc *Document
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		e, err := entryByID(tx, entryID, false)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() {
			return notFound()
		}
		src, rev, err := currentSource(tx, e)
		if err != nil {
			return err
		}
		doc = &Document{Entry: e, Source: src, Revision: rev}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// ReadBlob loads an asset's bytes.
func (o *Ops) ReadBlob(ctx context.Context, tenantID int64, blobID int64) (*AssetBlob, error) {
	var b AssetBlob
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		return tx.First(&b, blobID).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, notFound()
		}
		return nil, err
	}
	return &b, nil
}

// Resolution is the outcome of resolving a rendered request path.
type Resolution struct {
	Entry      *Entry
	Redirect   string // rendered path to redirect to (alias or trailing slash), or ""
	IsDirIndex bool   // directory URL served by its index page
	Directory  *Entry // the directory for listings (when no index page)
}

// Resolve maps a rendered (URL) path to the entry that serves it, following
// aliases and directory rules. The returned Redirect is a site-relative
// rendered path without address prefix.
func (o *Ops) Resolve(ctx context.Context, tenantID int64, rendered string) (*Resolution, error) {
	var res *Resolution
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		pagePath, dirPath, isDirURL := RenderedToCandidates(rendered)
		if isDirURL {
			if e, err := liveEntryByPath(tx, pagePath); err != nil {
				return err
			} else if e != nil {
				res = &Resolution{Entry: e, IsDirIndex: true}
				return nil
			}
			d, err := liveEntryByPath(tx, dirPath)
			if err != nil {
				return err
			}
			if d != nil && d.Kind == KindDirectory {
				res = &Resolution{Directory: d}
				return nil
			}
			// alias for the index page or directory
			if a, err := aliasByPath(tx, pagePath); err != nil {
				return err
			} else if a != nil {
				return o.aliasTarget(tx, a, &res)
			}
			return notFound()
		}
		if e, err := liveEntryByPath(tx, pagePath); err != nil {
			return err
		} else if e != nil {
			res = &Resolution{Entry: e}
			return nil
		}
		// A directory without trailing slash redirects to the slash form.
		if d, err := liveEntryByPath(tx, dirPath); err != nil {
			return err
		} else if d != nil {
			if d.Kind == KindDirectory {
				res = &Resolution{Redirect: rendered + "/"}
				return nil
			}
			if d.Kind == KindAsset || d.Kind == KindConfig || d.Kind == KindFile {
				res = &Resolution{Entry: d}
				return nil
			}
		}
		if a, err := aliasByPath(tx, pagePath); err != nil {
			return err
		} else if a != nil {
			return o.aliasTarget(tx, a, &res)
		}
		if a, err := aliasByPath(tx, dirPath); err != nil {
			return err
		} else if a != nil {
			return o.aliasTarget(tx, a, &res)
		}
		return notFound()
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (o *Ops) aliasTarget(tx *gorm.DB, a *PathAlias, res **Resolution) error {
	e, err := entryByID(tx, a.EntryID, false)
	if err != nil {
		return err
	}
	if e == nil || e.Deleted() {
		return notFound()
	}
	*res = &Resolution{Entry: e, Redirect: e.HTMLPath()}
	return nil
}

// ResolveRaw maps a raw source path request (e.g. /research/circle.md) to an entry or alias redirect.
func (o *Ops) ResolveRaw(ctx context.Context, tenantID int64, sourcePath string) (*Resolution, error) {
	var res *Resolution
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		e, err := liveEntryByPath(tx, sourcePath)
		if err != nil {
			return err
		}
		if e != nil {
			res = &Resolution{Entry: e}
			return nil
		}
		a, err := aliasByPath(tx, sourcePath)
		if err != nil {
			return err
		}
		if a == nil {
			return notFound()
		}
		t, err := entryByID(tx, a.EntryID, false)
		if err != nil {
			return err
		}
		if t == nil || t.Deleted() {
			return notFound()
		}
		res = &Resolution{Entry: t, Redirect: t.Path}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ListPage is one page of directory children.
type ListPage struct {
	Directory  EntryInfo   `json:"directory"`
	Entries    []EntryInfo `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// List returns the immediate live children of a directory, directories first,
// then by name. Cursor pagination is keyed on (kind-group, name).
func (o *Ops) List(ctx context.Context, p Principal, path string, cursorStr string, limit int) (*ListPage, error) {
	if !p.Can(ScopeRead) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:read scope is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if info.Kind != KindDirectory {
		return nil, errors.New(errors.CodeInvalidPath, "list expects a directory path")
	}
	c, ok := o.decodeCursor(cursorStr, "list:"+info.Path)
	if !ok {
		return nil, errors.New(errors.CodeValidationFailed, "invalid cursor")
	}
	var page ListPage
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		d, err := liveEntryByPath(tx, info.Path)
		if err != nil {
			return err
		}
		if d == nil || d.Kind != KindDirectory {
			return notFound()
		}
		page.Directory = Info(d)
		q := tx.Where("parent_id = ? AND deleted_at IS NULL", d.ID)
		if c != nil && c.After != "" {
			q = q.Where("(CASE WHEN kind = 'directory' THEN '0' ELSE '1' END) || path > ?", c.After)
		}
		var rows []Entry
		if err := q.Order("(CASE WHEN kind = 'directory' THEN '0' ELSE '1' END) || path").Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > limit {
			last := rows[limit-1]
			key := "1" + last.Path
			if last.Kind == KindDirectory {
				key = "0" + last.Path
			}
			page.NextCursor = o.encodeCursor(cursor{Scope: "list:" + info.Path, After: key})
			rows = rows[:limit]
		}
		page.Entries = make([]EntryInfo, 0, len(rows))
		for i := range rows {
			page.Entries = append(page.Entries, Info(&rows[i]))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// Children returns all live children of a directory (for sidebars/listings).
func (o *Ops) Children(ctx context.Context, tenantID int64, dirID int64) ([]Entry, error) {
	var rows []Entry
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		return tx.Where("parent_id = ? AND deleted_at IS NULL", dirID).Order("path").Find(&rows).Error
	})
	return rows, err
}

// Tree returns every live entry of the site ordered by path (bounded by quotas).
func (o *Ops) Tree(ctx context.Context, tenantID int64, includeDeleted bool) ([]Entry, error) {
	var rows []Entry
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		q := tx.Order("path")
		if !includeDeleted {
			q = q.Where("deleted_at IS NULL")
		}
		return q.Find(&rows).Error
	})
	return rows, err
}

// Trash returns tombstoned entries.
func (o *Ops) Trash(ctx context.Context, tenantID int64) ([]Entry, error) {
	var rows []Entry
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		return tx.Where("deleted_at IS NOT NULL").Order("deleted_at DESC").Limit(200).Find(&rows).Error
	})
	return rows, err
}

// HistoryItem is one revision without content.
type HistoryItem struct {
	Revision   int64     `json:"revision"`
	Operation  string    `json:"operation"`
	Summary    string    `json:"summary,omitempty"`
	Actor      string    `json:"actor"`
	ActorKind  string    `json:"actor_kind"`
	AuthorName string    `json:"author_name,omitempty"`
	Path       string    `json:"path"`
	PriorPath  string    `json:"prior_path,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	ShareLabel string    `json:"share_label,omitempty"`
	UserID     int64     `json:"-"`
	GrantID    int64     `json:"-"`
	Title      string    `json:"title,omitempty"`
}

// HistoryPage is one page of revisions.
type HistoryPage struct {
	Entry      EntryInfo     `json:"entry"`
	Items      []HistoryItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// History lists revisions of an entry, newest first, including tombstones.
func (o *Ops) History(ctx context.Context, p Principal, path string, cursorStr string, limit int) (*HistoryPage, error) {
	if !p.Can(ScopeRead) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:read scope is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	c, ok := o.decodeCursor(cursorStr, "history:"+info.Path)
	if !ok {
		return nil, errors.New(errors.CodeValidationFailed, "invalid cursor")
	}
	var page HistoryPage
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		e, err := entryByPath(tx, info.Path, false)
		if err != nil {
			return err
		}
		if e == nil {
			return notFound()
		}
		page.Entry = Info(e)
		q := tx.Where("entry_id = ?", e.ID)
		if c != nil && c.ID > 0 {
			q = q.Where("seq < ?", c.ID)
		}
		var revs []Revision
		if err := q.Order("seq DESC").Limit(limit + 1).Find(&revs).Error; err != nil {
			return err
		}
		if len(revs) > limit {
			page.NextCursor = o.encodeCursor(cursor{Scope: "history:" + info.Path, ID: revs[limit-1].Seq})
			revs = revs[:limit]
		}
		page.Items = historyItems(tx, revs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &page, nil
}

func historyItems(tx *gorm.DB, revs []Revision) []HistoryItem {
	items := make([]HistoryItem, 0, len(revs))
	userNames := map[int64]string{}
	grantLabels := map[int64]string{}
	clientNames := map[int64]string{}
	for _, r := range revs {
		it := HistoryItem{Revision: r.Seq, Operation: r.Operation, Summary: r.Summary, ActorKind: r.ActorKind, AuthorName: r.AuthorName, Path: r.Path, SizeBytes: r.SizeBytes, CreatedAt: r.CreatedAt}
		if r.PriorPath != nil {
			it.PriorPath = *r.PriorPath
		}
		var meta PageMeta
		_ = jsonUnmarshal(r.Meta, &meta)
		it.Title = meta.Title
		switch r.ActorKind {
		case PrincipalOwner:
			it.Actor = i18n.T("en", "actor.owner")
			if r.UserID != nil {
				if n, ok := userNames[*r.UserID]; ok {
					it.Actor = n
				} else {
					var u User
					if tx.Session(&gorm.Session{NewDB: true}).First(&u, *r.UserID).Error == nil && u.DisplayName != "" {
						it.Actor = u.DisplayName
					}
					userNames[*r.UserID] = it.Actor
				}
				it.UserID = *r.UserID
			}
		case PrincipalOAuth:
			it.Actor = i18n.T("en", "actor.client")
			if r.OAuthGrantID != nil {
				if n, ok := clientNames[*r.OAuthGrantID]; ok {
					it.Actor = n
				} else {
					var name string
					_ = tx.Raw(`SELECT c.name FROM oauth_grant g JOIN oauth_client c ON c.id = g.client_id WHERE g.id = ?`, *r.OAuthGrantID).Scan(&name).Error
					if name != "" {
						it.Actor = i18n.T("en", "actor.client_suffix", "name", name)
					}
					clientNames[*r.OAuthGrantID] = it.Actor
				}
			}
		case PrincipalAPIToken:
			it.Actor = i18n.T("en", "actor.api_token")
		case PrincipalShareLink:
			it.Actor = i18n.T("en", "actor.share_link")
			if r.ShareGrantID != nil {
				it.GrantID = *r.ShareGrantID
				if l, ok := grantLabels[*r.ShareGrantID]; ok {
					it.ShareLabel = l
				} else {
					var sg ShareGrant
					if tx.Session(&gorm.Session{NewDB: true}).First(&sg, *r.ShareGrantID).Error == nil {
						it.ShareLabel = sg.Label
					}
					grantLabels[*r.ShareGrantID] = it.ShareLabel
				}
			}
		default:
			it.Actor = i18n.T("en", "actor.system")
		}
		items = append(items, it)
	}
	return items
}

// SearchHit is one search result.
type SearchHit struct {
	Path     string  `json:"path"`
	Title    string  `json:"title"`
	Snippet  string  `json:"snippet"`
	Revision int64   `json:"revision"`
	HTMLPath string  `json:"html_path"`
	RawPath  string  `json:"raw_path"`
	JSONPath string  `json:"json_path"`
	Rank     float64 `json:"-"`
	ID       int64   `json:"-"`
}

// SearchPage is one page of results.
type SearchPage struct {
	Query      string      `json:"query"`
	Hits       []SearchHit `json:"hits"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// Search runs PostgreSQL full-text search over current, live pages.
func (o *Ops) Search(ctx context.Context, p Principal, query, pathPrefix, cursorStr string, limit int) (*SearchPage, error) {
	if !p.Can(ScopeRead) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:read scope is required")
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 200 {
		return nil, errors.New(errors.CodeValidationFailed, "query must be 1–200 characters")
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if pathPrefix != "" {
		if _, err := RequestPath(pathPrefix); err != nil || !strings.HasPrefix(pathPrefix, "/") {
			return nil, errors.New(errors.CodeInvalidPath, "path_prefix is invalid")
		}
	}
	scope := fmt.Sprintf("search:%s:%s", query, pathPrefix)
	c, ok := o.decodeCursor(cursorStr, scope)
	if !ok {
		return nil, errors.New(errors.CodeValidationFailed, "invalid cursor")
	}
	page := &SearchPage{Query: query, Hits: []SearchHit{}}
	err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		type row struct {
			ID              int64
			Path            string
			Title           string
			CurrentRevision int64
			Rank            float64
			Snippet         string
		}
		sql := `
SELECT e.id, e.path, e.title, e.current_revision,
       (ts_rank(e.tsv, q) + CASE WHEN e.path ILIKE '%' || ? || '%' THEN 0.5 ELSE 0 END)::float8 AS rank,
       ts_headline('simple', e.plain_text, q, 'MaxWords=30, MinWords=10, StartSel=**, StopSel=**') AS snippet
  FROM entry e, websearch_to_tsquery('simple', ?) q
 WHERE e.kind IN ('page', 'file') AND e.deleted_at IS NULL
   AND (e.tsv @@ q OR e.path ILIKE '%' || ? || '%')`
		args := []any{query, query, query}
		if pathPrefix != "" {
			sql += ` AND e.path LIKE ? || '%'`
			args = append(args, strings.TrimSuffix(pathPrefix, "/")+"/")
		}
		if c != nil && c.ID != 0 {
			sql += ` AND ((ts_rank(e.tsv, q) + CASE WHEN e.path ILIKE '%' || ? || '%' THEN 0.5 ELSE 0 END)::float8 < ? OR ((ts_rank(e.tsv, q) + CASE WHEN e.path ILIKE '%' || ? || '%' THEN 0.5 ELSE 0 END)::float8 = ? AND e.id > ?))`
			args = append(args, query, c.Rank, query, c.Rank, c.ID)
		}
		sql += ` ORDER BY rank DESC, e.id ASC LIMIT ?`
		args = append(args, limit+1)
		var rows []row
		if err := tx.Raw(sql, args...).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > limit {
			last := rows[limit-1]
			page.NextCursor = o.encodeCursor(cursor{Scope: scope, Rank: last.Rank, ID: last.ID})
			rows = rows[:limit]
		}
		for _, r := range rows {
			e := Entry{Path: r.Path, Kind: KindPage}
			if FileMIME(r.Path) != "" {
				e.Kind = KindFile
			}
			page.Hits = append(page.Hits, SearchHit{Path: r.Path, Title: r.Title, Snippet: r.Snippet, Revision: r.CurrentRevision, HTMLPath: e.HTMLPath(), RawPath: r.Path, JSONPath: e.JSONPath(), Rank: r.Rank, ID: r.ID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// RecentChanges lists the newest revisions across the site (owner dashboard).
func (o *Ops) RecentChanges(ctx context.Context, tenantID int64, limit int) ([]HistoryItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var items []HistoryItem
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		var revs []Revision
		if err := tx.Order("created_at DESC, id DESC").Limit(limit).Find(&revs).Error; err != nil {
			return err
		}
		items = historyItems(tx, revs)
		return nil
	})
	return items, err
}

// SiteConfigFor loads and parses the tenant's /tmp.yaml, falling back to defaults.
func (o *Ops) SiteConfigFor(ctx context.Context, tenantID int64) (*SiteConfig, string, error) {
	var src string
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		e, err := liveEntryByPath(tx, ConfigPath)
		if err != nil || e == nil {
			return err
		}
		src, _, err = currentSource(tx, e)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if src == "" {
		return DefaultSiteConfig(), DefaultSiteConfigYAML, nil
	}
	cfg, err := ParseSiteConfig(src)
	if err != nil {
		return DefaultSiteConfig(), src, nil
	}
	return cfg, src, nil
}

// RenderPage renders Markdown for one address form, resolving local images
// against the tenant's live assets. It is the single publishing renderer used
// by the site, the dashboard preview and the shared view.
func (o *Ops) RenderPage(ctx context.Context, tenantID int64, prefix, sourcePath, source string) (*render.Result, error) {
	var res *render.Result
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		var rerr error
		res, rerr = render.Render(source, render.Options{
			SourcePath:            sourcePath,
			FilenameFallbackTitle: strings.TrimSuffix(sourcePath[strings.LastIndexByte(sourcePath, '/')+1:], ".md"),
			Prefix:                prefix,
			ResolveAsset:          assetResolver(tx, prefix),
			MaxBytes:              int(o.Quotas.MaxPageBytes),
		})
		return rerr
	})
	return res, err
}

// PageExists reports whether a live page or file already occupies the path.
// It is the question "is this a new page or an existing one", asked without
// loading the document, and a malformed path is simply not an existing page.
func (o *Ops) PageExists(ctx context.Context, p Principal, path string) (bool, error) {
	if !p.Can(ScopeRead) {
		return false, errors.New(errors.CodeInsufficientScope, "content:read scope is required")
	}
	info, err := ValidatePath(path)
	if err != nil {
		return false, nil
	}
	var found bool
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		e, err := entryByPath(tx, info.Path, false)
		if err != nil {
			return err
		}
		found = e != nil && !e.Deleted()
		return nil
	})
	return found, err
}
