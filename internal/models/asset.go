package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	_ "image/jpeg" // decoders
	_ "image/png"

	_ "golang.org/x/image/webp"
	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// AssetInput is a bounded image upload.
type AssetInput struct {
	Path             string
	Data             []byte
	ExpectedRevision int64 // 0 = create only
	RequestID        string
}

// sniffMIME verifies the file signature; it never trusts filenames or headers.
func sniffMIME(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 5 && string(b[:5]) == "%PDF-":
		return "application/pdf"
	}
	return ""
}

// ValidateImage checks signature, extension consistency, and bounded decoding.
// PDFs are accepted by signature only, with their own size limit.
func (o *Ops) ValidateImage(info *PathInfo, data []byte) (mime string, w, h int, err error) {
	if len(data) == 0 {
		return "", 0, 0, errors.New(errors.CodeValidationFailed, "empty upload")
	}
	mime = sniffMIME(data)
	if mime == "" {
		return "", 0, 0, errors.New(errors.CodeValidationFailed, "file is not a PNG, JPEG, WebP image or a PDF")
	}
	if mime != info.MIME {
		return "", 0, 0, errors.Newf(errors.CodeValidationFailed, "file content is %s but the extension %s expects %s", mime, info.Ext, info.MIME)
	}
	if mime == "application/pdf" {
		if int64(len(data)) > o.Quotas.MaxDocumentBytes {
			return "", 0, 0, errors.Newf(errors.CodeTooLarge, "PDF exceeds %d bytes", o.Quotas.MaxDocumentBytes)
		}
		return mime, 0, 0, nil
	}
	if int64(len(data)) > o.Quotas.MaxAssetBytes {
		return "", 0, 0, errors.Newf(errors.CodeTooLarge, "image exceeds %d bytes", o.Quotas.MaxAssetBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, errors.New(errors.CodeValidationFailed, "image could not be decoded")
	}
	_ = format
	if int64(cfg.Width)*int64(cfg.Height) > o.Quotas.MaxAssetPixels || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", 0, 0, errors.Newf(errors.CodeValidationFailed, "image exceeds %d decoded pixels", o.Quotas.MaxAssetPixels)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", 0, 0, errors.New(errors.CodeValidationFailed, "image is corrupt")
	}
	return mime, cfg.Width, cfg.Height, nil
}

// PutAsset creates or replaces an image asset.
func (o *Ops) PutAsset(ctx context.Context, p Principal, in AssetInput) (*MutationResult, error) {
	if !p.Can(ScopeWrite) || p.IsShare() {
		return nil, errors.New(errors.CodeInsufficientScope, "content:write scope is required")
	}
	info, err := ValidatePath(in.Path)
	if err != nil {
		return nil, err
	}
	if info.Kind != KindAsset {
		return nil, errors.New(errors.CodeInvalidPath, "asset paths must end in .png, .jpg, .jpeg, .webp or .pdf")
	}
	mime, w, h, err := o.ValidateImage(info, in.Data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(in.Data)
	var out *MutationResult
	err = db.WithTenantSerialized(ctx, p.TenantID, func(tx *gorm.DB) error {
		if cached, err := loadReceipt(tx, p, "asset", in.RequestID, digest(info.Path, sum[:], in.ExpectedRevision)); err != nil || cached != nil {
			out = cached
			return err
		}
		e, err := entryByPath(tx, info.Path, true)
		if err != nil {
			return err
		}
		created := false
		switch {
		case e == nil:
			if in.ExpectedRevision != 0 {
				return &errors.AppError{Code: errors.CodeNotFound, Message: "asset does not exist; use expected_revision 0 to create it"}
			}
			created = true
		case e.Deleted():
			if in.ExpectedRevision != 0 && in.ExpectedRevision != e.CurrentRevision {
				return conflict(e.CurrentRevision, in.ExpectedRevision)
			}
			if e.Kind != KindAsset {
				return errors.New(errors.CodePathConflict, "a deleted entry of a different kind occupies this path")
			}
			created = true
		default:
			if e.Kind != KindAsset {
				return errors.Newf(errors.CodePathConflict, "%s exists and is a %s", info.Path, e.Kind)
			}
			if in.ExpectedRevision == 0 {
				return &errors.AppError{Code: errors.CodeRevisionConflict, Message: "asset already exists; pass its current revision to replace it", CurrentRevision: e.CurrentRevision}
			}
			if in.ExpectedRevision != e.CurrentRevision {
				return conflict(e.CurrentRevision, in.ExpectedRevision)
			}
		}
		var current int64
		if err := tx.Raw(`SELECT COALESCE(SUM(size_bytes),0) FROM entry WHERE kind = 'asset' AND deleted_at IS NULL`).Scan(&current).Error; err != nil {
			return err
		}
		if current+int64(len(in.Data)) > o.Quotas.MaxCurrentAssetBytes {
			return errors.Newf(errors.CodeQuotaExceeded, "current asset quota of %d bytes would be exceeded", o.Quotas.MaxCurrentAssetBytes)
		}
		if err := o.checkRetainedQuota(tx, int64(len(in.Data))); err != nil {
			return err
		}
		blob := &AssetBlob{TenantID: p.TenantID, SHA256: sum[:], MIME: mime, SizeBytes: int64(len(in.Data)), Width: w, Height: h, Data: in.Data}
		if err := tx.Create(blob).Error; err != nil {
			return err
		}
		if e == nil {
			parentID, err := o.ensureParents(tx, p, info.Parent, in.RequestID)
			if err != nil {
				return err
			}
			if err := checkRenderedFree(tx, info); err != nil {
				return err
			}
			e = &Entry{TenantID: p.TenantID, Kind: KindAsset, Path: info.Path, RenderedPath: info.Rendered, ParentID: parentID, Title: info.Name}
			if err := tx.Create(e).Error; err != nil {
				return err
			}
		} else if e.Deleted() {
			parentID, err := o.ensureParents(tx, p, info.Parent, in.RequestID)
			if err != nil {
				return err
			}
			e.ParentID = parentID
		}
		op := "update"
		if created {
			op = "create"
		}
		meta := PageMeta{Title: info.Name}
		rev, err := o.appendRevision(tx, p, e, op, nil, &blob.ID, meta, "", in.RequestID, nil)
		if err != nil {
			return err
		}
		rev.SizeBytes = blob.SizeBytes
		if err := tx.Model(rev).Update("size_bytes", blob.SizeBytes).Error; err != nil {
			return err
		}
		if err := tx.Model(e).Updates(map[string]any{"current_revision": rev.Seq, "blob_id": blob.ID, "size_bytes": blob.SizeBytes, "deleted_at": nil, "parent_id": e.ParentID, "title": info.Name}).Error; err != nil {
			return err
		}
		if err := bumpGeneration(tx, p.TenantID); err != nil {
			return err
		}
		if err := audit(tx, p, "asset."+op, &e.ID, &rev.Seq, in.RequestID); err != nil {
			return err
		}
		fresh, err := entryByID(tx, e.ID, false)
		if err != nil {
			return err
		}
		out = &MutationResult{Entry: Info(fresh), Created: created}
		return saveReceipt(tx, p, "asset", in.RequestID, digest(info.Path, sum[:], in.ExpectedRevision), out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
