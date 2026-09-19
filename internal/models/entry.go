package models

import (
	"database/sql/driver"
	"encoding/json"
	goerrors "errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/errors"
)

// StringArray maps a PostgreSQL text[] column.
type StringArray []string

// Value encodes the array literal.
func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return "{}", nil
	}
	parts := make([]string, len(a))
	for i, s := range a {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts[i] = `"` + s + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// Scan decodes the array literal.
func (a *StringArray) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("cannot scan %T into StringArray", src)
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return fmt.Errorf("malformed array literal")
	}
	s = s[1 : len(s)-1]
	out := []string{}
	if s == "" {
		*a = out
		return nil
	}
	var cur strings.Builder
	inQuote, escaped := false, false
	flush := func() {
		v := cur.String()
		if v == "NULL" && !inQuote {
			v = ""
		}
		out = append(out, v)
		cur.Reset()
	}
	quoted := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
			quoted = true
		case r == ',' && !inQuote:
			if !quoted && cur.String() == "NULL" {
				cur.Reset()
			}
			flush()
			quoted = false
		default:
			cur.WriteRune(r)
		}
	}
	if !quoted && cur.String() == "NULL" {
		cur.Reset()
	}
	flush()
	*a = out
	return nil
}

// Entry is one node of the logical filesystem. The path is mutable; the ID is not.
type Entry struct {
	ID              int64 `gorm:"primaryKey"`
	TenantID        int64
	Kind            string
	Path            string
	RenderedPath    string
	ParentID        *int64
	CurrentRevision int64
	Title           string
	Description     string
	Tags            StringArray `gorm:"type:text[]"`
	OrderIndex      *int
	PlainText       string
	BlobID          *int64
	SizeBytes       int64
	DeletedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TableName follows the singular naming convention.
func (Entry) TableName() string { return "entry" }

// Deleted reports whether the entry is a tombstone.
func (e *Entry) Deleted() bool { return e.DeletedAt != nil }

// Name is the last path segment.
func (e *Entry) Name() string {
	if e.Path == "/" {
		return ""
	}
	i := strings.LastIndexByte(e.Path, '/')
	return e.Path[i+1:]
}

// IsIndexPage reports whether this is a directory overview page.
func (e *Entry) IsIndexPage() bool {
	return e.Kind == KindPage && strings.HasSuffix(e.Path, "/index.md")
}

// HTMLPath is the rendered URL path (without address prefix).
func (e *Entry) HTMLPath() string {
	switch e.Kind {
	case KindPage:
		if e.IsIndexPage() {
			d := strings.TrimSuffix(e.Path, "index.md")
			return d
		}
		return strings.TrimSuffix(e.Path, ".md")
	case KindDirectory:
		if e.Path == "/" {
			return "/"
		}
		return e.Path + "/"
	default:
		return e.Path
	}
}

// JSONPath is the JSON representation URL path ("" for kinds without one).
func (e *Entry) JSONPath() string {
	switch e.Kind {
	case KindPage:
		return strings.TrimSuffix(e.Path, ".md") + ".json"
	case KindDirectory:
		if e.Path == "/" {
			return "/index.json"
		}
		return e.Path + "/index.json"
	case KindFile, KindAsset:
		return ""
	default:
		return e.Path + ".json"
	}
}

// HasSource reports whether the entry's revisions carry text source.
func (e *Entry) HasSource() bool {
	return e.Kind == KindPage || e.Kind == KindConfig || e.Kind == KindFile
}

// Revision is an immutable record of one mutation. Source is retained for pages
// and config; assets reference an immutable blob.
type Revision struct {
	ID           int64 `gorm:"primaryKey"`
	TenantID     int64
	EntryID      int64
	Seq          int64
	Operation    string
	Path         string
	PriorPath    *string
	Source       *string
	BlobID       *int64
	Meta         json.RawMessage `gorm:"type:jsonb"`
	ActorKind    string
	UserID       *int64
	OAuthGrantID *int64 `gorm:"column:oauth_grant_id"`
	APITokenID   *int64 `gorm:"column:api_token_id"`
	ShareGrantID *int64
	AuthorName   string
	Summary      string
	RequestID    string
	SizeBytes    int64
	CreatedAt    time.Time
}

// TableName follows the singular naming convention.
func (Revision) TableName() string { return "revision" }

// PathAlias preserves an old address after a move.
type PathAlias struct {
	ID           int64 `gorm:"primaryKey"`
	TenantID     int64
	Path         string
	RenderedPath string
	EntryID      int64
	CreatedAt    time.Time
}

// TableName follows the singular naming convention.
func (PathAlias) TableName() string { return "path_alias" }

// AuditEvent records who did what, never content or secrets.
type AuditEvent struct {
	ID           int64 `gorm:"primaryKey"`
	TenantID     int64
	ActorKind    string
	UserID       *int64
	OAuthGrantID *int64 `gorm:"column:oauth_grant_id"`
	APITokenID   *int64 `gorm:"column:api_token_id"`
	ShareGrantID *int64
	Action       string
	EntryID      *int64
	RevisionSeq  *int64
	RequestID    string
	CreatedAt    time.Time
}

// TableName follows the singular naming convention.
func (AuditEvent) TableName() string { return "audit_event" }

// MutationReceipt caches the result of an idempotent mutation for 24 hours.
type MutationReceipt struct {
	ID            int64 `gorm:"primaryKey"`
	TenantID      int64
	PrincipalKey  string
	RequestID     string
	Operation     string
	RequestDigest []byte
	Result        json.RawMessage `gorm:"type:jsonb"`
	ShareGrantID  *int64
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// TableName follows the singular naming convention.
func (MutationReceipt) TableName() string { return "mutation_receipt" }

// AssetBlob holds immutable image bytes.
type AssetBlob struct {
	ID        int64 `gorm:"primaryKey"`
	TenantID  int64
	SHA256    []byte `gorm:"column:sha256"`
	MIME      string `gorm:"column:mime"`
	SizeBytes int64
	Width     int
	Height    int
	Data      []byte
	CreatedAt time.Time
}

// TableName follows the singular naming convention.
func (AssetBlob) TableName() string { return "asset_blob" }

// PageMeta is the metadata persisted with a revision.
type PageMeta struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Order       *int           `json:"order,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// ---- lookups inside a tenant transaction ----

func entryByPath(tx *gorm.DB, p string, lock bool) (*Entry, error) {
	var e Entry
	q := tx.Where("path = ?", p)
	if lock {
		q = q.Clauses(forUpdate)
	}
	if err := q.First(&e).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

func entryByID(tx *gorm.DB, id int64, lock bool) (*Entry, error) {
	var e Entry
	q := tx.Where("id = ?", id)
	if lock {
		q = q.Clauses(forUpdate)
	}
	if err := q.First(&e).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// liveEntryByPath returns a non-deleted entry or nil.
func liveEntryByPath(tx *gorm.DB, p string) (*Entry, error) {
	e, err := entryByPath(tx, p, false)
	if err != nil || e == nil || e.Deleted() {
		return nil, err
	}
	return e, nil
}

func aliasByPath(tx *gorm.DB, p string) (*PathAlias, error) {
	var a PathAlias
	if err := tx.Where("path = ?", p).First(&a).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func currentSource(tx *gorm.DB, e *Entry) (string, *Revision, error) {
	var r Revision
	if err := tx.Where("entry_id = ? AND seq = ?", e.ID, e.CurrentRevision).First(&r).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, errors.New(errors.CodeNotFound, "revision not found")
		}
		return "", nil, err
	}
	if r.Source == nil {
		return "", &r, nil
	}
	return *r.Source, &r, nil
}

func revisionBySeq(tx *gorm.DB, entryID, seq int64) (*Revision, error) {
	var r Revision
	if err := tx.Where("entry_id = ? AND seq = ?", entryID, seq).First(&r).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeNotFound, "revision not found")
		}
		return nil, err
	}
	return &r, nil
}

func notFound() error { return errors.New(errors.CodeNotFound, "not found") }
