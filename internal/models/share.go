package models

import (
	"context"
	goerrors "errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// ShareGrant is a revocable read+write capability bound to one page entry.
type ShareGrant struct {
	ID              int64 `gorm:"primaryKey"`
	TenantID        int64
	EntryID         int64
	TokenHash       []byte
	Label           string
	CreatedByUserID *int64
	AssetsVersion   int
	CreatedAt       time.Time
	RevokedAt       *time.Time
}

// TableName follows the singular naming convention.
func (ShareGrant) TableName() string { return "share_grant" }

// ShareGrantAsset is one owner-approved embedded asset for a grant.
type ShareGrantAsset struct {
	TenantID     int64
	GrantID      int64
	AssetEntryID int64
}

// TableName follows the singular naming convention.
func (ShareGrantAsset) TableName() string { return "share_grant_asset" }

// ShareGrantInfo is the owner-visible metadata of a grant (never the token).
type ShareGrantInfo struct {
	ID        int64      `json:"id"`
	EntryID   int64      `json:"entry_id"`
	Path      string     `json:"path"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Assets    int        `json:"allowed_assets"`
}

// CreateShareGrant mints a new secret link for a live page. The plaintext token
// is returned exactly once; only its hash is stored.
func (o *Ops) CreateShareGrant(ctx context.Context, p Principal, entryID int64, label string) (token string, info *ShareGrantInfo, err error) {
	if !p.IsOwnerSession() {
		return "", nil, errors.New(errors.CodeForbidden, "only the owner's browser session can manage sharing links")
	}
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		return "", nil, errors.New(errors.CodeValidationFailed, "label must be 80 characters or fewer")
	}
	token = NewToken()
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		e, err := entryByID(tx, entryID, false)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() || e.Kind != KindPage {
			return errors.New(errors.CodeValidationFailed, "only live Markdown pages can be shared")
		}
		uid := p.UserID
		g := &ShareGrant{TenantID: p.TenantID, EntryID: e.ID, TokenHash: HashToken(token), Label: label, CreatedByUserID: &uid, AssetsVersion: 1}
		if err := tx.Create(g).Error; err != nil {
			return err
		}
		n, err := refreshGrantAssets(tx, p.TenantID, g, e)
		if err != nil {
			return err
		}
		if err := audit(tx, p, "share.create", &e.ID, nil, ""); err != nil {
			return err
		}
		info = &ShareGrantInfo{ID: g.ID, EntryID: e.ID, Path: e.Path, Label: g.Label, CreatedAt: g.CreatedAt, Assets: n}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return token, info, nil
}

// refreshGrantAssets replaces the allowlist with the local images embedded in the current revision.
func refreshGrantAssets(tx *gorm.DB, tenantID int64, g *ShareGrant, e *Entry) (int, error) {
	src, _, err := currentSource(tx, e)
	if err != nil {
		return 0, err
	}
	paths, err := render.ExtractLocalImagePaths(src, e.Path)
	if err != nil {
		return 0, err
	}
	if err := tx.Where("grant_id = ?", g.ID).Delete(&ShareGrantAsset{}).Error; err != nil {
		return 0, err
	}
	n := 0
	seen := map[int64]bool{}
	for _, sp := range paths {
		a, err := liveEntryByPath(tx, sp)
		if err != nil {
			return 0, err
		}
		if a == nil || a.Kind != KindAsset || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		if err := tx.Create(&ShareGrantAsset{TenantID: tenantID, GrantID: g.ID, AssetEntryID: a.ID}).Error; err != nil {
			return 0, err
		}
		n++
	}
	return n, tx.Model(g).Update("assets_version", gorm.Expr("assets_version + 1")).Error
}

// RefreshShareGrantAssets is the explicit owner action to re-approve embedded assets.
func (o *Ops) RefreshShareGrantAssets(ctx context.Context, p Principal, grantID int64) (*ShareGrantInfo, error) {
	if !p.IsOwnerSession() {
		return nil, errors.New(errors.CodeForbidden, "only the owner's browser session can manage sharing links")
	}
	var info *ShareGrantInfo
	err := db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		var g ShareGrant
		if err := tx.Clauses(forUpdate).First(&g, grantID).Error; err != nil {
			if goerrors.Is(err, gorm.ErrRecordNotFound) {
				return notFound()
			}
			return err
		}
		if g.RevokedAt != nil {
			return errors.New(errors.CodeValidationFailed, "this link is revoked")
		}
		e, err := entryByID(tx, g.EntryID, false)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() {
			return notFound()
		}
		n, err := refreshGrantAssets(tx, p.TenantID, &g, e)
		if err != nil {
			return err
		}
		if err := audit(tx, p, "share.assets_refresh", &e.ID, nil, ""); err != nil {
			return err
		}
		info = &ShareGrantInfo{ID: g.ID, EntryID: e.ID, Path: e.Path, Label: g.Label, CreatedAt: g.CreatedAt, Assets: n}
		return nil
	})
	return info, err
}

// RevokeShareGrant ends a link. Subsequent reads and writes fail with 404.
func (o *Ops) RevokeShareGrant(ctx context.Context, p Principal, grantID int64) error {
	if !p.IsOwnerSession() {
		return errors.New(errors.CodeForbidden, "only the owner's browser session can manage sharing links")
	}
	return db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		var g ShareGrant
		if err := tx.Clauses(forUpdate).First(&g, grantID).Error; err != nil {
			if goerrors.Is(err, gorm.ErrRecordNotFound) {
				return notFound()
			}
			return err
		}
		if g.RevokedAt == nil {
			now := time.Now()
			if err := tx.Model(&g).Update("revoked_at", now).Error; err != nil {
				return err
			}
		}
		return audit(tx, p, "share.revoke", &g.EntryID, nil, "")
	})
}

// ListShareGrants lists grants (metadata only), optionally for one entry.
func (o *Ops) ListShareGrants(ctx context.Context, p Principal, entryID int64) ([]ShareGrantInfo, error) {
	if !p.IsOwnerSession() {
		return nil, errors.New(errors.CodeForbidden, "only the owner's browser session can manage sharing links")
	}
	var out []ShareGrantInfo
	err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		q := tx.Order("created_at DESC").Limit(500)
		if entryID != 0 {
			q = q.Where("entry_id = ?", entryID)
		}
		var grants []ShareGrant
		if err := q.Find(&grants).Error; err != nil {
			return err
		}
		out = make([]ShareGrantInfo, 0, len(grants))
		for _, g := range grants {
			e, err := entryByID(tx, g.EntryID, false)
			if err != nil {
				return err
			}
			var n int64
			_ = tx.Model(&ShareGrantAsset{}).Where("grant_id = ?", g.ID).Count(&n).Error
			info := ShareGrantInfo{ID: g.ID, EntryID: g.EntryID, Label: g.Label, CreatedAt: g.CreatedAt, RevokedAt: g.RevokedAt, Assets: int(n)}
			if e != nil {
				info.Path = e.Path
			}
			out = append(out, info)
		}
		return nil
	})
	return out, err
}

// ShareContext is the validated capability for one secret-link request.
type ShareContext struct {
	Principal Principal
	Grant     ShareGrant
	Entry     *Entry
	Source    string
	Revision  *Revision
}

// ResolveShareToken validates a token and loads the linked document. Invalid,
// revoked and unknown tokens are indistinguishable: all return not_found.
func (o *Ops) ResolveShareToken(ctx context.Context, token string) (*ShareContext, error) {
	if token == "" || len(token) > 128 {
		return nil, notFound()
	}
	type row struct {
		GrantID  int64
		TenantID int64
		EntryID  int64
		Revoked  bool
	}
	var r row
	if err := db.Get().WithContext(ctx).Raw(`SELECT grant_id, tenant_id, entry_id, revoked FROM lookup_share_grant(decode(?, 'hex'))`, HashHex(token)).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.GrantID == 0 || r.Revoked {
		return nil, notFound()
	}
	sc := &ShareContext{}
	err := db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
		var g ShareGrant
		if err := tx.First(&g, r.GrantID).Error; err != nil {
			return notFound()
		}
		if g.RevokedAt != nil {
			return notFound()
		}
		e, err := entryByID(tx, g.EntryID, false)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() || e.Kind != KindPage {
			return notFound()
		}
		src, rev, err := currentSource(tx, e)
		if err != nil {
			return err
		}
		sc.Grant, sc.Entry, sc.Source, sc.Revision = g, e, src, rev
		sc.Principal = Principal{Kind: PrincipalShareLink, TenantID: r.TenantID, Scopes: []string{ScopeRead, ScopeWrite}, ShareGrantID: g.ID, ShareEntryID: e.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sc, nil
}

// ShareAssetURLResolver returns a resolver that maps an embedded local image to
// its token-scoped URL only if the owner approved it and it is still embedded.
func (o *Ops) ShareAssetResolver(ctx context.Context, sc *ShareContext, tokenBase string) func(string) (string, bool) {
	allowed := map[int64]bool{}
	pathToID := map[string]int64{}
	_ = db.WithTenant(ctx, sc.Principal.TenantID, func(tx *gorm.DB) error {
		var rows []ShareGrantAsset
		if err := tx.Where("grant_id = ?", sc.Grant.ID).Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			allowed[r.AssetEntryID] = true
		}
		if len(rows) > 0 {
			var ids []int64
			for id := range allowed {
				ids = append(ids, id)
			}
			var assets []Entry
			if err := tx.Where("id IN ? AND deleted_at IS NULL AND kind = 'asset'", ids).Find(&assets).Error; err != nil {
				return err
			}
			for _, a := range assets {
				pathToID[a.Path] = a.ID
			}
		}
		return nil
	})
	return func(sitePath string) (string, bool) {
		id, ok := pathToID[sitePath]
		if !ok || !allowed[id] {
			return "", false
		}
		return tokenBase + "/assets/" + fmtInt(id), true
	}
}

// ShareAsset loads an approved asset for a grant, checking that the current
// revision still embeds it.
func (o *Ops) ShareAsset(ctx context.Context, sc *ShareContext, assetID int64) (*AssetBlob, error) {
	var blob *AssetBlob
	err := db.WithTenant(ctx, sc.Principal.TenantID, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&ShareGrantAsset{}).Where("grant_id = ? AND asset_entry_id = ?", sc.Grant.ID, assetID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return notFound()
		}
		a, err := entryByID(tx, assetID, false)
		if err != nil {
			return err
		}
		if a == nil || a.Deleted() || a.Kind != KindAsset || a.BlobID == nil {
			return notFound()
		}
		paths, err := render.ExtractLocalImagePaths(sc.Source, sc.Entry.Path)
		if err != nil {
			return notFound()
		}
		embedded := false
		for _, p := range paths {
			if p == a.Path {
				embedded = true
			}
		}
		if !embedded {
			return notFound()
		}
		var b AssetBlob
		if err := tx.First(&b, *a.BlobID).Error; err != nil {
			return notFound()
		}
		blob = &b
		return nil
	})
	return blob, err
}
