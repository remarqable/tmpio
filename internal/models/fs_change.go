package models

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// MoveInput moves one page.
type MoveInput struct {
	From             string
	To               string
	ExpectedRevision int64
	RequestID        string
}

// Move relocates one page atomically, leaves an alias at the old path and
// rewrites relative links inside the page so they keep their meaning.
func (o *Ops) Move(ctx context.Context, p Principal, in MoveInput) (*MutationResult, error) {
	if !p.Can(ScopeWrite) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:write scope is required")
	}
	from, err := ValidatePath(in.From)
	if err != nil {
		return nil, err
	}
	to, err := ValidatePath(in.To)
	if err != nil {
		return nil, err
	}
	if from.Kind != KindPage && from.Kind != KindAsset {
		return nil, errors.New(errors.CodeInvalidPath, "only pages and assets can be moved in this version")
	}
	if to.Kind != from.Kind {
		return nil, errors.New(errors.CodeInvalidPath, "source and destination must be the same kind of file")
	}
	if from.Path == to.Path {
		return nil, errors.New(errors.CodeValidationFailed, "source and destination are the same")
	}
	if in.ExpectedRevision == 0 {
		return nil, errors.New(errors.CodeValidationFailed, "expected_revision is required")
	}
	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "move", in.RequestID, digest(from.Path, to.Path, in.ExpectedRevision)); err != nil || cached != nil {
			out = cached
			return err
		}
		e, err := entryByPath(tx, from.Path, true)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() {
			return notFound()
		}
		if e.CurrentRevision != in.ExpectedRevision {
			return conflict(e.CurrentRevision, in.ExpectedRevision)
		}
		if dest, err := entryByPath(tx, to.Path, true); err != nil {
			return err
		} else if dest != nil {
			return errors.Newf(errors.CodePathConflict, "%s already exists", to.Path)
		}
		if err := checkRenderedFree(tx, to); err != nil {
			return err
		}
		parentID, err := o.ensureParents(tx, p, to.Parent, in.RequestID)
		if err != nil {
			return err
		}
		src, cur, err := currentSource(tx, e)
		if err != nil {
			return err
		}
		var rewrites []string
		newSrc := src
		if e.Kind == KindPage {
			newSrc, rewrites, err = render.RewriteRelativeLinks(src, from.Path, to.Path)
			if err != nil {
				return renderError(err)
			}
			if _, err := render.Render(newSrc, render.Options{SourcePath: to.Path, MaxBytes: int(o.Quotas.MaxPageBytes)}); err != nil {
				return renderError(err)
			}
		}
		alias := &PathAlias{TenantID: p.TenantID, Path: from.Path, RenderedPath: from.Rendered, EntryID: e.ID}
		if err := tx.Create(alias).Error; err != nil {
			return err
		}
		prior := e.Path
		e.Path, e.RenderedPath, e.ParentID = to.Path, to.Rendered, parentID
		var meta PageMeta
		_ = jsonUnmarshal(cur.Meta, &meta)
		var srcPtr *string
		if e.Kind == KindPage {
			srcPtr = &newSrc
		}
		rev, err := o.appendRevision(tx, p, e, "move", srcPtr, cur.BlobID, meta, i18n.T("en", "summary.moved_from", "path", prior), in.RequestID, &prior)
		if err != nil {
			return err
		}
		if err := tx.Model(e).Updates(map[string]any{"path": e.Path, "rendered_path": e.RenderedPath, "parent_id": e.ParentID, "current_revision": rev.Seq, "size_bytes": int64(len(newSrc))}).Error; err != nil {
			return err
		}
		if err := bumpGeneration(tx, p.TenantID); err != nil {
			return err
		}
		if err := audit(tx, p, "entry.move", &e.ID, &rev.Seq, in.RequestID); err != nil {
			return err
		}
		fresh, err := entryByID(tx, e.ID, false)
		if err != nil {
			return err
		}
		out = &MutationResult{Entry: Info(fresh), Rewrites: rewrites, Aliases: []string{from.Path}}
		return saveReceipt(tx, p, "move", in.RequestID, digest(from.Path, to.Path, in.ExpectedRevision), out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete soft-deletes one page, asset or empty directory. Sharing links to a
// deleted document are revoked permanently.
func (o *Ops) Delete(ctx context.Context, p Principal, path string, expectedRevision int64, requestID string) (*MutationResult, error) {
	if !p.Can(ScopeDelete) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:delete scope is required")
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if info.IsRoot || info.Kind == KindConfig {
		return nil, errors.New(errors.CodeValidationFailed, "the root directory and tmp.yaml cannot be deleted")
	}
	if expectedRevision == 0 {
		return nil, errors.New(errors.CodeValidationFailed, "expected_revision is required")
	}
	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "delete", requestID, digest(info.Path, expectedRevision)); err != nil || cached != nil {
			out = cached
			return err
		}
		e, err := entryByPath(tx, info.Path, true)
		if err != nil {
			return err
		}
		if e == nil || e.Deleted() {
			return notFound()
		}
		if e.CurrentRevision != expectedRevision {
			return conflict(e.CurrentRevision, expectedRevision)
		}
		if e.Kind == KindDirectory {
			var n int64
			if err := tx.Model(&Entry{}).Where("parent_id = ? AND deleted_at IS NULL", e.ID).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return errors.New(errors.CodeValidationFailed, "directory is not empty; delete its contents first")
			}
		}
		var meta PageMeta
		var srcPtr *string
		var blob *int64
		if e.Kind != KindDirectory {
			src, cur, err := currentSource(tx, e)
			if err != nil {
				return err
			}
			_ = jsonUnmarshal(cur.Meta, &meta)
			if cur.Source != nil {
				srcPtr = &src
			}
			blob = cur.BlobID
		}
		rev, err := o.appendRevision(tx, p, e, "delete", srcPtr, blob, meta, "", requestID, nil)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(e).Updates(map[string]any{"deleted_at": now, "current_revision": rev.Seq}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ShareGrant{}).Where("entry_id = ? AND revoked_at IS NULL", e.ID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := bumpGeneration(tx, p.TenantID); err != nil {
			return err
		}
		if err := audit(tx, p, "entry.delete", &e.ID, &rev.Seq, requestID); err != nil {
			return err
		}
		fresh, err := entryByID(tx, e.ID, false)
		if err != nil {
			return err
		}
		out = &MutationResult{Entry: Info(fresh)}
		return saveReceipt(tx, p, "delete", requestID, digest(info.Path, expectedRevision), out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Restore re-publishes a historical revision as a new revision at the current
// path, reviving a deleted entry if necessary. Sharing grants are untouched.
func (o *Ops) Restore(ctx context.Context, p Principal, path string, revision, expectedRevision int64, requestID string) (*MutationResult, error) {
	if !p.Can(ScopeWrite) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:write scope is required")
	}
	info, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if revision <= 0 || expectedRevision <= 0 {
		return nil, errors.New(errors.CodeValidationFailed, "revision and expected_revision are required")
	}
	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "restore", requestID, digest(info.Path, revision, expectedRevision)); err != nil || cached != nil {
			out = cached
			return err
		}
		e, err := entryByPath(tx, info.Path, true)
		if err != nil {
			return err
		}
		if e == nil {
			return notFound()
		}
		if e.Kind == KindDirectory {
			return errors.New(errors.CodeValidationFailed, "directory history is not restorable; recreate the directory instead")
		}
		if e.CurrentRevision != expectedRevision {
			return conflict(e.CurrentRevision, expectedRevision)
		}
		old, err := revisionBySeq(tx, e.ID, revision)
		if err != nil {
			return err
		}
		if old.Source == nil && old.BlobID == nil {
			return errors.New(errors.CodeValidationFailed, "that revision has no restorable content")
		}
		var meta PageMeta
		var plain string
		var size int64
		switch e.Kind {
		case KindPage:
			res, err := render.Render(*old.Source, render.Options{SourcePath: e.Path, FilenameFallbackTitle: strings.TrimSuffix(e.Name(), ".md"), ResolveAsset: assetResolver(tx, ""), ResolveLink: linkResolver(tx), MaxBytes: int(o.Quotas.MaxPageBytes)})
			if err != nil {
				return errors.Wrap(errors.CodeValidationFailed, "that revision is not valid under the current parser: "+err.Error(), err)
			}
			meta = PageMeta{Title: res.Title, Description: res.Meta.Description, Tags: res.Meta.Tags, Order: res.Meta.Order, Extra: res.Meta.Extra}
			plain = res.PlainText
			size = int64(len(*old.Source))
		case KindFile:
			info2, _ := ValidatePath(e.Path)
			if err := ValidateFileContent(info2, *old.Source); err != nil {
				return err
			}
			meta = PageMeta{Title: e.Name()}
			plain = filePlainText(*old.Source)
			size = int64(len(*old.Source))
		case KindConfig:
			cfg, err := ParseSiteConfig(*old.Source)
			if err != nil {
				return err
			}
			meta = PageMeta{Title: "tmp.yaml", Description: cfg.Description}
			size = int64(len(*old.Source))
		case KindAsset:
			_ = jsonUnmarshal(old.Meta, &meta)
			var b AssetBlob
			if err := tx.Select("id", "size_bytes").First(&b, *old.BlobID).Error; err != nil {
				return err
			}
			size = b.SizeBytes
		}
		if e.Deleted() {
			if err := o.checkPageQuota(tx, e.Kind); err != nil {
				return err
			}
			parentID, err := o.ensureParents(tx, p, info.Parent, requestID)
			if err != nil {
				return err
			}
			e.ParentID = parentID
		}
		if old.Source != nil {
			if err := o.checkRetainedQuota(tx, size); err != nil {
				return err
			}
		}
		rev, err := o.appendRevision(tx, p, e, "restore", old.Source, old.BlobID, meta, i18n.T("en", "summary.restored", "rev", itoa(revision)), requestID, nil)
		if err != nil {
			return err
		}
		updates := map[string]any{"deleted_at": nil, "parent_id": e.ParentID, "current_revision": rev.Seq, "title": meta.Title, "description": meta.Description, "tags": StringArray(nonNil(meta.Tags)), "order_index": meta.Order, "size_bytes": size}
		if e.Kind == KindPage || e.Kind == KindFile {
			updates["plain_text"] = plain
		}
		if e.Kind == KindAsset {
			updates["blob_id"] = old.BlobID
		}
		if err := tx.Model(e).Updates(updates).Error; err != nil {
			return err
		}
		if err := bumpGeneration(tx, p.TenantID); err != nil {
			return err
		}
		if err := audit(tx, p, "entry.restore", &e.ID, &rev.Seq, requestID); err != nil {
			return err
		}
		fresh, err := entryByID(tx, e.ID, false)
		if err != nil {
			return err
		}
		out = &MutationResult{Entry: Info(fresh)}
		return saveReceipt(tx, p, "restore", requestID, digest(info.Path, revision, expectedRevision), out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func itoa(n int64) string {
	return strings.TrimSpace(strings.Replace(fmtInt(n), " ", "", -1))
}
