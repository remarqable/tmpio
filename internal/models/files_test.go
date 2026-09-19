package models

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

func TestTextFilesAndPDF(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "files@x.test")

	// CSV and JSON files are stored as source, versioned and searchable.
	r, err := o.Write(ctx, p, WriteInput{Path: "/data/report.csv", Content: "name,score\nCircle,9\nDiscord,7\n", RequestID: rid()})
	require.NoError(t, err)
	assert.Equal(t, KindFile, r.Entry.Kind)
	assert.Equal(t, "report.csv", r.Entry.Title)
	assert.Equal(t, "", r.Entry.JSONPath, "files have no JSON representation")
	_, err = o.Write(ctx, p, WriteInput{Path: "/data/bad.json", Content: "{not json", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "invalid JSON is rejected: %v", err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/data/ok.json", Content: "{\"a\": 1}\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/data/bad.yaml", Content: "a: [unclosed\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))
	_, err = o.Write(ctx, p, WriteInput{Path: "/data/bin.txt", Content: "a\x00b", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "NUL bytes are not text")

	doc, err := o.Read(ctx, p, "/data/report.csv", 0)
	require.NoError(t, err)
	assert.Contains(t, doc.Source, "Discord,7")
	res, err := o.Search(ctx, p, "Discord", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, res.Hits, 1)

	// Update with CAS and restore, like pages.
	r2, err := o.Write(ctx, p, WriteInput{Path: "/data/report.csv", Content: "name,score\nCircle,10\n", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, r2.Entry.Revision)
	rs, err := o.Restore(ctx, p, "/data/report.csv", 1, 2, rid())
	require.NoError(t, err)
	assert.EqualValues(t, 3, rs.Entry.Revision)

	// Files cannot be shared; only pages can.
	_, _, err = o.CreateShareGrant(ctx, p, r.Entry.ID, "x")
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))

	// PDF assets by signature; size limit is the document limit.
	pdf := []byte("%PDF-1.4\n%fake but signed\n")
	a, err := o.PutAsset(ctx, p, AssetInput{Path: "/docs/spec.pdf", Data: pdf, RequestID: rid()})
	require.NoError(t, err)
	b, err := o.ReadBlob(ctx, tn.ID, mustBlobID(t, tn.ID, "/docs/spec.pdf"))
	require.NoError(t, err)
	assert.Equal(t, "application/pdf", b.MIME)
	_, err = o.PutAsset(ctx, p, AssetInput{Path: "/docs/notpdf.pdf", Data: pngBytes(t, 2, 2), RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "content must match the extension")
	assert.Equal(t, KindAsset, a.Entry.Kind)

	// Export carries the file source.
	zipData, err := o.Export(ctx, p, tn.Code)
	require.NoError(t, err)
	assert.Contains(t, string(zipData), "data/report.csv")
}
