package models

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// ExportManifest describes an export archive.
type ExportManifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Org        string    `json:"org"`
	Entries    int       `json:"entries"`
}

// Export builds a consistent ZIP snapshot of current, live content. The
// snapshot is read inside one REPEATABLE READ transaction and buffered before
// the archive is returned, so a slow client never holds a transaction open.
func (o *Ops) Export(ctx context.Context, p Principal, orgCode string) ([]byte, error) {
	if !p.IsOwnerSession() {
		return nil, errors.New(errors.CodeForbidden, "export requires the owner's browser session")
	}
	type file struct {
		path string
		data []byte
		dir  bool
	}
	var files []file
	err := db.WithTenantSnapshot(ctx, p.TenantID, func(tx *gorm.DB) error {
		var entries []Entry
		if err := tx.Where("deleted_at IS NULL").Order("path").Find(&entries).Error; err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			switch e.Kind {
			case KindDirectory:
				if e.Path != "/" {
					files = append(files, file{path: strings.TrimPrefix(e.Path, "/") + "/", dir: true})
				}
			case KindPage, KindConfig, KindFile:
				src, _, err := currentSource(tx, e)
				if err != nil {
					return err
				}
				files = append(files, file{path: strings.TrimPrefix(e.Path, "/"), data: []byte(src)})
			case KindAsset:
				if e.BlobID == nil {
					continue
				}
				var b AssetBlob
				if err := tx.First(&b, *e.BlobID).Error; err != nil {
					return err
				}
				files = append(files, file{path: strings.TrimPrefix(e.Path, "/"), data: b.Data})
			}
		}
		return audit(tx, p, "site.export", nil, nil, "")
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifest := ExportManifest{Format: "tmp-export", Version: 1, ExportedAt: time.Now().UTC(), Org: orgCode, Entries: len(files)}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	w, err := zw.Create("tmp-export.json")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(mb); err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.dir {
			if _, err := zw.Create(f.path); err != nil {
				return nil, err
			}
			continue
		}
		hdr := &zip.FileHeader{Name: f.path, Method: zip.Deflate}
		hdr.Modified = manifest.ExportedAt
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
