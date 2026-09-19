package models

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

var testQuotas = config.Quotas{MaxPages: 1000, MaxDirectories: 100, MaxPageBytes: 256 * 1024, MaxConfigBytes: 32 * 1024, MaxAssetBytes: 5 << 20, MaxDocumentBytes: 20 << 20, MaxFileBytes: 1 << 20, MaxAssetPixels: 20_000_000, MaxCurrentAssetBytes: 100 << 20, MaxRetainedBytes: 500 << 20}

func testOps() *Ops {
	return &Ops{Quotas: testQuotas, CursorKey: []byte("0123456789abcdef0123456789abcdef")}
}

// newOwner signs up a fresh user (one org) and returns its owner principal.
func newOwner(t *testing.T, email string) (Principal, *Tenant, *User) {
	t.Helper()
	u, tn, _, err := SignIn(context.Background(), ExternalIdentity{Issuer: "dev", Subject: email, Email: email, EmailVerified: true, Name: email}, DefaultSiteConfigYAML, DefaultIndexMarkdown, true)
	require.NoError(t, err)
	return Principal{Kind: PrincipalOwner, TenantID: tn.ID, UserID: u.ID, Scopes: AllScopes}, tn, u
}

func rid() string { return uuidV4ForTest() }

func uuidV4ForTest() string {
	b := []byte(NewToken())[:16]
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

func pngBytes(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestCredentialLookupsWorkWithoutBypass guards the production shape: the lookup
// functions run as the table owner, which on a managed database has no BYPASSRLS.
// The credential tables must therefore grant the owner read access in their
// policy; everything else stays tenant-confined for the owner as well.
func TestCredentialLookupsWorkWithoutBypass(t *testing.T) {
	db.ConnectTest(t)
	for _, table := range []string{"share_grant", "api_token", "oauth_grant", "oauth_token", "oauth_code"} {
		var qual, check string
		require.NoError(t, db.Get().Raw(`SELECT qual, with_check FROM pg_policies WHERE tablename = ? AND policyname = 'tenant_isolation'`, table).Row().Scan(&qual, &check))
		assert.Contains(t, qual, "CURRENT_USER = 'app_owner'", table+" lets the owner role resolve hashes without a tenant scope")
		assert.NotContains(t, check, "CURRENT_USER", table+" never lets the owner write outside a tenant scope")
	}
	var qual string
	require.NoError(t, db.Get().Raw(`SELECT qual FROM pg_policies WHERE tablename = 'entry' AND policyname = 'tenant_isolation'`).Row().Scan(&qual))
	assert.NotContains(t, qual, "CURRENT_USER", "content tables stay tenant-confined for every role")
}

func TestEveryTenantTableHasRLS(t *testing.T) {
	db.ConnectTest(t)
	type row struct {
		Relname  string
		Rls      bool
		Forced   bool
		HasCheck bool
	}
	var rows []row
	require.NoError(t, db.OwnerForTest(t).Raw(`
    SELECT c.relname, c.relrowsecurity AS rls, c.relforcerowsecurity AS forced,
           bool_or(p.polwithcheck IS NOT NULL) AS has_check
      FROM pg_class c
      JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id'
      LEFT JOIN pg_policy p ON p.polrelid = c.oid
     WHERE c.relkind = 'r' AND c.relnamespace = 'public'::regnamespace
     GROUP BY c.relname, c.relrowsecurity, c.relforcerowsecurity`).Scan(&rows).Error)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.True(t, r.Rls, "%s has tenant_id but RLS is not enabled", r.Relname)
		assert.True(t, r.Forced, "%s has RLS but not FORCE", r.Relname)
		assert.True(t, r.HasCheck, "%s policy has no WITH CHECK", r.Relname)
	}
	var bypass bool
	require.NoError(t, db.Get().Raw(`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass).Error)
	assert.False(t, bypass, "runtime role must be NOBYPASSRLS")
}

func TestSignupCreatesSiteExactlyOnce(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	id := ExternalIdentity{Issuer: "https://accounts.google.com", Subject: "sub-1", Email: "a@example.com", EmailVerified: true, Name: "A"}
	u1, t1, created, err := SignIn(ctx, id, DefaultSiteConfigYAML, DefaultIndexMarkdown, true)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Nil(t, t1.Name, "signup does not invent an organization name")
	assert.True(t, ValidOrgCode(t1.Code))

	u2, t2, created, err := SignIn(ctx, id, DefaultSiteConfigYAML, DefaultIndexMarkdown, true)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, u1.ID, u2.ID)
	assert.Equal(t, t1.ID, t2.ID)

	// Same email, different subject: no merge.
	u3, t3, created, err := SignIn(ctx, ExternalIdentity{Issuer: "https://accounts.google.com", Subject: "sub-2", Email: "a@example.com", EmailVerified: true}, DefaultSiteConfigYAML, DefaultIndexMarkdown, true)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, u1.ID, u3.ID)
	assert.NotEqual(t, t1.ID, t3.ID)

	_, _, _, err = SignIn(ctx, ExternalIdentity{Issuer: "x", Subject: "y", Email: "z@example.com", EmailVerified: false}, DefaultSiteConfigYAML, DefaultIndexMarkdown, true)
	assert.Error(t, err, "unverified email rejected")

	o := testOps()
	p := Principal{Kind: PrincipalOwner, TenantID: t1.ID, UserID: u1.ID}
	for _, path := range []string{"/", "/tmp.yaml", "/index.md"} {
		doc, err := o.Read(ctx, p, path, 0)
		require.NoError(t, err, path)
		assert.False(t, doc.Entry.Deleted())
	}
	cfg, _, err := o.SiteConfigFor(ctx, t1.ID)
	require.NoError(t, err)
	assert.Equal(t, "docs", cfg.Theme)

	require.NoError(t, RenameTenant(ctx, t1.ID, "My Org"))
	t1b, err := GetTenant(ctx, t1.ID)
	require.NoError(t, err)
	assert.Equal(t, "My Org", t1b.DisplayName())
	assert.Equal(t, t1.Code, t1b.Code, "naming never changes the code")
}

func TestTenantIsolation(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	pa, ta, _ := newOwner(t, "a@iso.test")
	pb, tb, _ := newOwner(t, "b@iso.test")

	_, err := o.Write(ctx, pa, WriteInput{Path: "/secret.md", Content: "# Secret\n", RequestID: rid()})
	require.NoError(t, err)

	// 1. B cannot read A's rows through the scoped helper.
	var count int64
	require.NoError(t, db.WithTenant(ctx, tb.ID, func(tx *gorm.DB) error {
		return tx.Model(&Entry{}).Where("path = ?", "/secret.md").Count(&count).Error
	}))
	assert.Zero(t, count)
	_, err = o.Read(ctx, pb, "/secret.md", 0)
	assert.True(t, errors.Is(err, errors.CodeNotFound))

	// 2. B cannot INSERT into A (WITH CHECK).
	err = db.WithTenant(ctx, tb.ID, func(tx *gorm.DB) error {
		return tx.Create(&Entry{TenantID: ta.ID, Kind: KindDirectory, Path: "/forged", RenderedPath: "/forged"}).Error
	})
	assert.Error(t, err)

	// 3. No tenant set: helper refuses.
	assert.ErrorIs(t, db.WithTenant(ctx, 0, func(tx *gorm.DB) error { return nil }), db.ErrNoTenant)

	// 4. The shared handle fails closed.
	var leaked int64
	require.NoError(t, db.Get().Model(&Entry{}).Count(&leaked).Error)
	assert.Zero(t, leaked)

	// 5. B cannot update or delete across the boundary (by path or by ID).
	var aSecretID int64
	require.NoError(t, db.WithTenant(ctx, ta.ID, func(tx *gorm.DB) error {
		var e Entry
		if err := tx.Where("path = '/secret.md'").First(&e).Error; err != nil {
			return err
		}
		aSecretID = e.ID
		return nil
	}))
	require.NoError(t, db.WithTenant(ctx, tb.ID, func(tx *gorm.DB) error {
		res := tx.Model(&Entry{}).Where("path = ? OR id = ?", "/secret.md", aSecretID).Update("title", "hijacked")
		assert.Zero(t, res.RowsAffected)
		res = tx.Where("id = ?", aSecretID).Delete(&Entry{})
		assert.Zero(t, res.RowsAffected)
		return nil
	}))
	require.NoError(t, db.WithTenant(ctx, ta.ID, func(tx *gorm.DB) error {
		var e Entry
		if err := tx.First(&e, aSecretID).Error; err != nil {
			return err
		}
		assert.Equal(t, "Secret", e.Title)
		return nil
	}))

	// 6. Composite FKs reject foreign-tenant references even under A's scope.
	var bRoot Entry
	require.NoError(t, db.WithTenant(ctx, tb.ID, func(tx *gorm.DB) error { return tx.Where("path = '/'").First(&bRoot).Error }))
	err = db.WithTenant(ctx, ta.ID, func(tx *gorm.DB) error {
		return tx.Create(&Entry{TenantID: ta.ID, Kind: KindDirectory, Path: "/x", RenderedPath: "/x", ParentID: &bRoot.ID}).Error
	})
	assert.Error(t, err, "parent from another tenant must be rejected")

	// 7. A foreign-org principal reads nothing from A, even by ID.
	var aSecret Entry
	require.NoError(t, db.WithTenant(ctx, ta.ID, func(tx *gorm.DB) error { return tx.Where("path = '/secret.md'").First(&aSecret).Error }))
	_, err = o.ReadEntryByID(ctx, tb.ID, aSecret.ID)
	assert.True(t, errors.Is(err, errors.CodeNotFound))

	// 8. Search is tenant-bound.
	res, err := o.Search(ctx, pb, "secret", "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, res.Hits)
	res, err = o.Search(ctx, pa, "secret", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, res.Hits, 1)
}

func TestWriteCompareAndSwap(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "cas@x.test")

	_, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n", RequestID: ""})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "request_id required")

	k1 := rid()
	r1, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n\nv1\n", RequestID: k1, Summary: "first"})
	require.NoError(t, err)
	assert.True(t, r1.Created)
	assert.EqualValues(t, 1, r1.Entry.Revision)
	assert.Equal(t, "Circle", r1.Entry.Title)

	// Parent directory was created atomically.
	dir, err := o.Read(ctx, p, "/research", 0)
	require.NoError(t, err)
	assert.Equal(t, KindDirectory, dir.Entry.Kind)

	// Idempotent replay: same result, no new revision.
	r1b, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n\nv1\n", RequestID: k1, Summary: "first"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, r1b.Entry.Revision)
	h, err := o.History(ctx, p, "/research/circle.md", "", 50)
	require.NoError(t, err)
	assert.Len(t, h.Items, 1)

	// Same request id, different payload.
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Different\n", RequestID: k1})
	assert.True(t, errors.Is(err, errors.CodeIdempotencyMismatch))

	// Create-only on an existing page conflicts and reports the current revision.
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# X\n", ExpectedRevision: 0, RequestID: rid()})
	ae := errors.As(err)
	assert.Equal(t, errors.CodeRevisionConflict, ae.Code)
	assert.EqualValues(t, 1, ae.CurrentRevision)

	// Stale update.
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# X\n", ExpectedRevision: 5, RequestID: rid()})
	ae = errors.As(err)
	assert.Equal(t, errors.CodeRevisionConflict, ae.Code)
	assert.EqualValues(t, 1, ae.CurrentRevision)
	assert.EqualValues(t, 5, ae.ExpectedRevision)

	// Correct update.
	r2, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n\nv2\n", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, r2.Entry.Revision)

	// Unchanged write adds no history.
	r3, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n\nv2\n", ExpectedRevision: 2, RequestID: rid()})
	require.NoError(t, err)
	assert.True(t, r3.Unchanged)
	assert.EqualValues(t, 2, r3.Entry.Revision)

	// Historical read.
	doc, err := o.Read(ctx, p, "/research/circle.md", 1)
	require.NoError(t, err)
	assert.Contains(t, doc.Source, "v1")

	// Invalid content never changes the page.
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# X\n\n<script>alert(1)</script>\n", ExpectedRevision: 2, RequestID: rid()})
	ae = errors.As(err)
	assert.Equal(t, errors.CodeValidationFailed, ae.Code)
	assert.NotEmpty(t, ae.FieldErrors)
	doc, err = o.Read(ctx, p, "/research/circle.md", 0)
	require.NoError(t, err)
	assert.EqualValues(t, 2, doc.Entry.CurrentRevision)

	// Uppercase path rejected with suggestion; directory write rejected.
	_, err = o.Write(ctx, p, WriteInput{Path: "/Research/New.md", Content: "# X\n", RequestID: rid()})
	assert.Equal(t, "/research/new.md", errors.As(err).SuggestedPath)
	_, err = o.Write(ctx, p, WriteInput{Path: "/research", Content: "# X\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeInvalidPath))

	// Read scope required.
	ro := Principal{Kind: PrincipalOAuth, TenantID: p.TenantID, UserID: p.UserID, Scopes: []string{ScopeRead}, OAuthGrantID: 1}
	_, err = o.Write(ctx, ro, WriteInput{Path: "/research/circle.md", Content: "# X\n", ExpectedRevision: 2, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
}

func TestConcurrentWritersOneWinner(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "race@x.test")
	_, err := o.Write(ctx, p, WriteInput{Path: "/race.md", Content: "# Race\n", RequestID: rid()})
	require.NoError(t, err)

	const n = 6
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = o.Write(ctx, p, WriteInput{Path: "/race.md", Content: "# Race\n\nwriter " + string(rune('a'+i)) + "\n", ExpectedRevision: 1, RequestID: rid()})
		}(i)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, errors.CodeRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, wins)
	assert.Equal(t, n-1, conflicts)

	// Concurrent creation of one path yields one winner too.
	results = make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = o.Write(ctx, p, WriteInput{Path: "/created-once.md", Content: "# Once\n", RequestID: rid()})
		}(i)
	}
	wg.Wait()
	wins = 0
	for _, err := range results {
		if err == nil {
			wins++
		} else {
			assert.True(t, errors.Is(err, errors.CodeRevisionConflict) || errors.Is(err, errors.CodePathConflict), "%v", err)
		}
	}
	assert.Equal(t, 1, wins)
}

func TestDirectoriesAndCollisions(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "dirs@x.test")

	r, err := o.Mkdir(ctx, p, "/a/b/c", rid())
	require.NoError(t, err)
	assert.True(t, r.Created)
	r, err = o.Mkdir(ctx, p, "/a/b/c", rid())
	require.NoError(t, err)
	assert.False(t, r.Created, "mkdir on existing succeeds without duplicate")

	_, err = o.Write(ctx, p, WriteInput{Path: "/a.md", Content: "# a\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodePathConflict), "/a.md collides with directory /a in the rendered namespace: %v", err)

	_, err = o.Write(ctx, p, WriteInput{Path: "/pages.md", Content: "# p\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Mkdir(ctx, p, "/pages", rid())
	assert.True(t, errors.Is(err, errors.CodePathConflict), "directory /pages collides with /pages.md")
	_, err = o.Write(ctx, p, WriteInput{Path: "/pages/x.md", Content: "# x\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodePathConflict))

	// Directory listing order: directories first, then pages by path.
	_, err = o.Write(ctx, p, WriteInput{Path: "/a/z.md", Content: "# z\n", RequestID: rid()})
	require.NoError(t, err)
	page, err := o.List(ctx, p, "/a", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	assert.Equal(t, "/a/b", page.Entries[0].Path)
	assert.Equal(t, "/a/z.md", page.Entries[1].Path)

	// Pagination.
	page, err = o.List(ctx, p, "/a", "", 1)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.NotEmpty(t, page.NextCursor)
	page2, err := o.List(ctx, p, "/a", page.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, page2.Entries, 1)
	assert.Equal(t, "/a/z.md", page2.Entries[0].Path)

	// Non-empty directory cannot be deleted; root and config never.
	dirDoc, _ := o.Read(ctx, p, "/a", 0)
	_, err = o.Delete(ctx, p, "/a", dirDoc.Entry.CurrentRevision, rid())
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))
	_, err = o.Delete(ctx, p, "/", 1, rid())
	assert.Error(t, err)
	_, err = o.Delete(ctx, p, ConfigPath, 1, rid())
	assert.Error(t, err)

	// Empty directory deletion works and is a tombstone.
	cDoc, _ := o.Read(ctx, p, "/a/b/c", 0)
	_, err = o.Delete(ctx, p, "/a/b/c", cDoc.Entry.CurrentRevision, rid())
	require.NoError(t, err)
	res, err := o.Resolve(ctx, p.TenantID, "/a/b/c/")
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, errors.CodeNotFound))
}

func TestMoveAliasAndRewrite(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "move@x.test")

	_, err := o.Write(ctx, p, WriteInput{Path: "/research/other.md", Content: "# Other\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n\nSee [other](other.md) and [abs](/research/other.md).\n", RequestID: rid()})
	require.NoError(t, err)
	var circleID int64
	require.NoError(t, db.WithTenant(ctx, tn.ID, func(tx *gorm.DB) error {
		var e Entry
		if err := tx.Where("path = '/research/circle.md'").First(&e).Error; err != nil {
			return err
		}
		circleID = e.ID
		return nil
	}))
	tok, _, err := o.CreateShareGrant(ctx, p, circleID, "r")
	require.NoError(t, err)

	_, err = o.Move(ctx, p, MoveInput{From: "/research/circle.md", To: "/notes/deep/circle.md", ExpectedRevision: 99, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeRevisionConflict))

	mv, err := o.Move(ctx, p, MoveInput{From: "/research/circle.md", To: "/notes/deep/circle.md", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	assert.Equal(t, "/notes/deep/circle.md", mv.Entry.Path)
	assert.EqualValues(t, 2, mv.Entry.Revision)
	assert.NotEmpty(t, mv.Rewrites, "relative link rewritten")
	assert.Equal(t, []string{"/research/circle.md"}, mv.Aliases)

	doc, err := o.Read(ctx, p, "/notes/deep/circle.md", 0)
	require.NoError(t, err)
	assert.Contains(t, doc.Source, "../../research/other.md", "relative link keeps its meaning")
	assert.Contains(t, doc.Source, "(/research/other.md)", "absolute link untouched")

	// Old addresses redirect in every representation; identity and history preserved.
	res, err := o.Resolve(ctx, tn.ID, "/research/circle")
	require.NoError(t, err)
	assert.Equal(t, "/notes/deep/circle", res.Redirect)
	raw, err := o.ResolveRaw(ctx, tn.ID, "/research/circle.md")
	require.NoError(t, err)
	assert.Equal(t, "/notes/deep/circle.md", raw.Redirect)
	assert.Equal(t, circleID, raw.Entry.ID)
	h, err := o.History(ctx, p, "/notes/deep/circle.md", "", 10)
	require.NoError(t, err)
	assert.Len(t, h.Items, 2)
	assert.Equal(t, "/research/circle.md", h.Items[0].PriorPath)

	// The sharing link follows the entry.
	sc, err := o.ResolveShareToken(ctx, tok)
	require.NoError(t, err)
	assert.Equal(t, "/notes/deep/circle.md", sc.Entry.Path)

	// Destination occupied, alias reuse and cross-kind moves are refused.
	_, err = o.Move(ctx, p, MoveInput{From: "/notes/deep/circle.md", To: "/research/other.md", ExpectedRevision: 2, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodePathConflict))
	_, err = o.Move(ctx, p, MoveInput{From: "/notes/deep/circle.md", To: "/research/circle.md", ExpectedRevision: 2, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodePathConflict), "aliases cannot be reused as new paths")
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# new\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodePathConflict), "aliases cannot be reused by writes either")
}

func TestDeleteRestoreAndShareRevocation(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "del@x.test")
	w, err := o.Write(ctx, p, WriteInput{Path: "/doc.md", Content: "# Doc\n\nv1\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/doc.md", Content: "# Doc\n\nv2\n", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	tok, info, err := o.CreateShareGrant(ctx, p, w.Entry.ID, "reviewer")
	require.NoError(t, err)
	_, err = o.ResolveShareToken(ctx, tok)
	require.NoError(t, err)

	del, err := o.Delete(ctx, p, "/doc.md", 2, rid())
	require.NoError(t, err)
	assert.True(t, del.Entry.Deleted)
	assert.EqualValues(t, 3, del.Entry.Revision)

	_, err = o.ResolveShareToken(ctx, tok)
	assert.True(t, errors.Is(err, errors.CodeNotFound), "deletion revokes links")
	links, err := o.ListShareGrants(ctx, p, w.Entry.ID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.NotNil(t, links[0].RevokedAt)
	assert.Equal(t, info.ID, links[0].ID)

	// Owner sees a tombstone; anonymous resolution 404s.
	doc, err := o.Read(ctx, p, "/doc.md", 0)
	require.NoError(t, err)
	assert.True(t, doc.Entry.Deleted())
	_, err = o.Resolve(ctx, tn.ID, "/doc")
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	trash, err := o.Trash(ctx, tn.ID)
	require.NoError(t, err)
	assert.Len(t, trash, 1)

	// Restore v1 as a new revision; the link stays revoked.
	rs, err := o.Restore(ctx, p, "/doc.md", 1, 3, rid())
	require.NoError(t, err)
	assert.EqualValues(t, 4, rs.Entry.Revision)
	assert.False(t, rs.Entry.Deleted)
	doc, err = o.Read(ctx, p, "/doc.md", 0)
	require.NoError(t, err)
	assert.Contains(t, doc.Source, "v1")
	_, err = o.ResolveShareToken(ctx, tok)
	assert.True(t, errors.Is(err, errors.CodeNotFound), "restore does not reactivate links")

	h, err := o.History(ctx, p, "/doc.md", "", 10)
	require.NoError(t, err)
	ops := []string{}
	for _, it := range h.Items {
		ops = append(ops, it.Operation)
	}
	assert.Equal(t, []string{"restore", "delete", "update", "create"}, ops)

	// Re-creating at a deleted path revives the entry (identity preserved).
	_, err = o.Delete(ctx, p, "/doc.md", 4, rid())
	require.NoError(t, err)
	rec, err := o.Write(ctx, p, WriteInput{Path: "/doc.md", Content: "# Doc\n\nv3\n", RequestID: rid()})
	require.NoError(t, err)
	assert.Equal(t, w.Entry.ID, rec.Entry.ID)
	assert.EqualValues(t, 6, rec.Entry.Revision)
}

func TestShareTokenCapability(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "share@x.test")
	w, err := o.Write(ctx, p, WriteInput{Path: "/shared.md", Content: "# Shared\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/private.md", Content: "# Private\n", RequestID: rid()})
	require.NoError(t, err)

	// Only owner sessions manage links; only live pages can be shared.
	client := Principal{Kind: PrincipalOAuth, TenantID: p.TenantID, UserID: p.UserID, Scopes: AllScopes, OAuthGrantID: 1}
	_, _, err = o.CreateShareGrant(ctx, client, w.Entry.ID, "")
	assert.True(t, errors.Is(err, errors.CodeForbidden))
	dir, _ := o.Read(ctx, p, "/", 0)
	_, _, err = o.CreateShareGrant(ctx, p, dir.Entry.ID, "")
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))

	tok, info, err := o.CreateShareGrant(ctx, p, w.Entry.ID, "Reviewer A")
	require.NoError(t, err)
	assert.Equal(t, "Reviewer A", info.Label)

	_, err = o.ResolveShareToken(ctx, "not-a-real-token-at-all-really")
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	sc, err := o.ResolveShareToken(ctx, tok)
	require.NoError(t, err)
	sp := sc.Principal
	assert.Equal(t, PrincipalShareLink, sp.Kind)

	// The link edits exactly its document, with anonymous attribution.
	sp.AuthorName = "Anon"
	r, err := o.Write(ctx, sp, WriteInput{Path: "/shared.md", Content: "# Shared\n\nedited\n", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.Entry.Revision)
	h, err := o.History(ctx, p, "/shared.md", "", 10)
	require.NoError(t, err)
	assert.Equal(t, "Anonymous via sharing link", h.Items[0].Actor)
	assert.Equal(t, "Anon", h.Items[0].AuthorName)
	assert.Equal(t, "Reviewer A", h.Items[0].ShareLabel)
	assert.Zero(t, h.Items[0].UserID, "anonymous edits carry no verified identity")

	// Everything else is denied: other pages, config, history, list, search, restore, move, delete, historical reads.
	_, err = o.Write(ctx, sp, WriteInput{Path: "/private.md", Content: "# X\n", ExpectedRevision: 1, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	_, err = o.Write(ctx, sp, WriteInput{Path: "/another.md", Content: "# X\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeNotFound), "cannot create")
	_, err = o.Read(ctx, sp, "/private.md", 0)
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	_, err = o.Read(ctx, sp, ConfigPath, 0)
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	_, err = o.Read(ctx, sp, "/shared.md", 1)
	assert.True(t, errors.Is(err, errors.CodeNotFound), "no historical reads")
	_, err = o.Write(ctx, sp, WriteInput{Path: ConfigPath, Content: DefaultSiteConfigYAML, ExpectedRevision: 1, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.History(ctx, sp, "/shared.md", "", 10)
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.List(ctx, sp, "/", "", 10)
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.Search(ctx, sp, "shared", "", "", 10)
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.Restore(ctx, sp, "/shared.md", 1, 2, rid())
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.Move(ctx, sp, MoveInput{From: "/shared.md", To: "/moved.md", ExpectedRevision: 2, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.Delete(ctx, sp, "/shared.md", 2, rid())
	assert.True(t, errors.Is(err, errors.CodeInsufficientScope))
	_, err = o.Export(ctx, sp, "X")
	assert.True(t, errors.Is(err, errors.CodeForbidden))

	// Idempotent replay is per principal: the owner cannot retrieve the link's receipt.
	k := rid()
	_, err = o.Write(ctx, sp, WriteInput{Path: "/shared.md", Content: "# Shared\n\nagain\n", ExpectedRevision: 2, RequestID: k})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/shared.md", Content: "# Shared\n\nagain\n", ExpectedRevision: 2, RequestID: k})
	assert.True(t, errors.Is(err, errors.CodeRevisionConflict), "another principal gets no cached result")

	// Revocation stops everything, including replays.
	require.NoError(t, o.RevokeShareGrant(ctx, p, info.ID))
	_, err = o.ResolveShareToken(ctx, tok)
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	// A stale principal built before revocation cannot write: the grant is rechecked in the transaction.
	// (The HTTP layer resolves the token per request; this exercises the domain check.)
	_, err = o.Write(ctx, sp, WriteInput{Path: "/shared.md", Content: "# Shared\n\nafter revoke\n", ExpectedRevision: 3, RequestID: rid()})
	assert.Error(t, err)
	doc, _ := o.Read(ctx, p, "/shared.md", 0)
	assert.EqualValues(t, 3, doc.Entry.CurrentRevision, "nothing committed after revocation")

	// Owner access continues.
	_, err = o.Write(ctx, p, WriteInput{Path: "/shared.md", Content: "# Shared\n\nowner\n", ExpectedRevision: 3, RequestID: rid()})
	require.NoError(t, err)
}

func TestShareEmbeddedAssets(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "assets@x.test")
	img := pngBytes(t, 4, 4)
	a1, err := o.PutAsset(ctx, p, AssetInput{Path: "/assets/one.png", Data: img, RequestID: rid()})
	require.NoError(t, err)
	_, err = o.PutAsset(ctx, p, AssetInput{Path: "/assets/two.png", Data: img, RequestID: rid()})
	require.NoError(t, err)
	_, err = o.PutAsset(ctx, p, AssetInput{Path: "/assets/bad.jpg", Data: img, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "extension must match content")
	_, err = o.PutAsset(ctx, p, AssetInput{Path: "/assets/notimage.png", Data: []byte("<svg/>"), RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))

	w, err := o.Write(ctx, p, WriteInput{Path: "/doc.md", Content: "# Doc\n\n![one](/assets/one.png)\n", RequestID: rid()})
	require.NoError(t, err)
	tok, info, err := o.CreateShareGrant(ctx, p, w.Entry.ID, "")
	require.NoError(t, err)
	assert.Equal(t, 1, info.Assets)
	sc, err := o.ResolveShareToken(ctx, tok)
	require.NoError(t, err)

	blob, err := o.ShareAsset(ctx, sc, a1.Entry.ID)
	require.NoError(t, err)
	assert.Equal(t, "image/png", blob.MIME)
	var twoID int64
	require.NoError(t, db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		var e Entry
		err := tx.Where("path = '/assets/two.png'").First(&e).Error
		twoID = e.ID
		return err
	}))
	_, err = o.ShareAsset(ctx, sc, twoID)
	assert.True(t, errors.Is(err, errors.CodeNotFound), "unapproved asset is unavailable")

	// A recipient referencing a private asset does not enlarge the allowlist.
	_, err = o.Write(ctx, sc.Principal, WriteInput{Path: "/doc.md", Content: "# Doc\n\n![one](/assets/one.png)\n![two](/assets/two.png)\n", ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	sc, _ = o.ResolveShareToken(ctx, tok)
	_, err = o.ShareAsset(ctx, sc, twoID)
	assert.True(t, errors.Is(err, errors.CodeNotFound))
	resolver := o.ShareAssetResolver(ctx, sc, "/s/x")
	_, ok := resolver("/assets/two.png")
	assert.False(t, ok)
	u, ok := resolver("/assets/one.png")
	assert.True(t, ok)
	assert.Contains(t, u, "/s/x/assets/")

	// Owner refresh approves what is now embedded.
	info2, err := o.RefreshShareGrantAssets(ctx, p, info.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, info2.Assets)
	sc, _ = o.ResolveShareToken(ctx, tok)
	_, err = o.ShareAsset(ctx, sc, twoID)
	require.NoError(t, err)

	// Removing the image from the document removes access immediately, even though approved.
	_, err = o.Write(ctx, p, WriteInput{Path: "/doc.md", Content: "# Doc\n\nno images\n", ExpectedRevision: 2, RequestID: rid()})
	require.NoError(t, err)
	sc, _ = o.ResolveShareToken(ctx, tok)
	_, err = o.ShareAsset(ctx, sc, a1.Entry.ID)
	assert.True(t, errors.Is(err, errors.CodeNotFound))

	// Asset replacement and restore.
	a1b, err := o.PutAsset(ctx, p, AssetInput{Path: "/assets/one.png", Data: pngBytes(t, 8, 8), ExpectedRevision: 1, RequestID: rid()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, a1b.Entry.Revision)
	rs, err := o.Restore(ctx, p, "/assets/one.png", 1, 2, rid())
	require.NoError(t, err)
	b, err := o.ReadBlob(ctx, p.TenantID, mustBlobID(t, p.TenantID, "/assets/one.png"))
	require.NoError(t, err)
	assert.Equal(t, 4, b.Width, "restored asset restores its retained bytes")
	assert.EqualValues(t, 3, rs.Entry.Revision)
}

func mustBlobID(t *testing.T, tenantID int64, path string) int64 {
	var id int64
	require.NoError(t, db.WithTenant(context.Background(), tenantID, func(tx *gorm.DB) error {
		var e Entry
		if err := tx.Where("path = ?", path).First(&e).Error; err != nil {
			return err
		}
		id = *e.BlobID
		return nil
	}))
	return id
}

func TestSearchAndConfig(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "search@x.test")
	_, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "---\ntitle: Circle\n---\n\nCommunity platform research about Circle.\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/research/discord.md", Content: "# Discord\n\nChat platform.\n", RequestID: rid()})
	require.NoError(t, err)
	res, err := o.Search(ctx, p, "platform", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, res.Hits, 2)
	res, err = o.Search(ctx, p, "circle", "", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res.Hits)
	assert.Equal(t, "/research/circle.md", res.Hits[0].Path, "title match ranks first")
	assert.Contains(t, res.Hits[0].Snippet, "Circle")
	res, err = o.Search(ctx, p, "platform", "", "", 1)
	require.NoError(t, err)
	assert.Len(t, res.Hits, 1)
	assert.NotEmpty(t, res.NextCursor)
	res2, err := o.Search(ctx, p, "platform", "", res.NextCursor, 1)
	require.NoError(t, err)
	assert.Len(t, res2.Hits, 1)
	assert.NotEqual(t, res.Hits[0].Path, res2.Hits[0].Path)

	// Deleted pages drop out of search.
	d, _ := o.Read(ctx, p, "/research/discord.md", 0)
	_, err = o.Delete(ctx, p, "/research/discord.md", d.Entry.CurrentRevision, rid())
	require.NoError(t, err)
	res, err = o.Search(ctx, p, "discord", "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, res.Hits)

	// Config: invalid config is rejected and the previous one stays active.
	cfgDoc, _ := o.Read(ctx, p, ConfigPath, 0)
	_, err = o.Write(ctx, p, WriteInput{Path: ConfigPath, Content: "version: 1\ntheme: neon\n", ExpectedRevision: cfgDoc.Entry.CurrentRevision, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeValidationFailed))
	cfg, _, _ := o.SiteConfigFor(ctx, tn.ID)
	assert.Equal(t, "docs", cfg.Theme)
	_, err = o.Write(ctx, p, WriteInput{Path: ConfigPath, Content: "version: 1\ntheme: editorial\nappearance: dark\nnavigation:\n  - title: Research\n    path: /research/\n", ExpectedRevision: cfgDoc.Entry.CurrentRevision, RequestID: rid()})
	require.NoError(t, err)
	cfg, _, _ = o.SiteConfigFor(ctx, tn.ID)
	assert.Equal(t, "editorial", cfg.Theme)
	assert.Len(t, cfg.Navigation, 1)
}

func TestQuotasAndSizes(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	q := testQuotas
	q.MaxPages = 2 // /index.md already counts as one
	q.MaxPageBytes = 64
	o := &Ops{Quotas: q, CursorKey: testOps().CursorKey}
	p, _, _ := newOwner(t, "quota@x.test")
	_, err := o.Write(ctx, p, WriteInput{Path: "/a.md", Content: "# a\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/b.md", Content: "# b\n", RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeQuotaExceeded))
	_, err = o.Write(ctx, p, WriteInput{Path: "/a.md", Content: "# a\n\n" + string(bytes.Repeat([]byte("x"), 100)) + "\n", ExpectedRevision: 1, RequestID: rid()})
	assert.True(t, errors.Is(err, errors.CodeTooLarge))
}

func TestExportSnapshot(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "export@x.test")
	_, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "# Circle\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.PutAsset(ctx, p, AssetInput{Path: "/assets/one.png", Data: pngBytes(t, 2, 2), RequestID: rid()})
	require.NoError(t, err)
	del, err := o.Write(ctx, p, WriteInput{Path: "/gone.md", Content: "# Gone\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Delete(ctx, p, "/gone.md", del.Entry.Revision, rid())
	require.NoError(t, err)
	_, _, err = o.CreateShareGrant(ctx, p, mustEntryID(t, tn.ID, "/research/circle.md"), "x")
	require.NoError(t, err)

	data, err := o.Export(ctx, p, tn.Code)
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "research/circle.md" {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			assert.Equal(t, "# Circle\n", b.String())
		}
	}
	assert.True(t, names["tmp-export.json"])
	assert.True(t, names["tmp.yaml"])
	assert.True(t, names["index.md"])
	assert.True(t, names["research/"])
	assert.True(t, names["research/circle.md"])
	assert.True(t, names["assets/one.png"])
	assert.False(t, names["gone.md"], "deleted pages excluded")
	assert.NotContains(t, string(data), "/s/", "no sharing tokens in export")
}

func mustEntryID(t *testing.T, tenantID int64, path string) int64 {
	var id int64
	require.NoError(t, db.WithTenant(context.Background(), tenantID, func(tx *gorm.DB) error {
		var e Entry
		if err := tx.Where("path = ?", path).First(&e).Error; err != nil {
			return err
		}
		id = e.ID
		return nil
	}))
	return id
}

func TestOAuthAndAPITokens(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	p, tn, _ := newOwner(t, "oauth@x.test")
	require.NoError(t, SyncOAuthClients(ctx, []config.OAuthClient{{ID: "c1", Name: "Client One", RedirectURIs: []string{"http://127.0.0.1:1/cb"}, Public: true}}))
	aud := "http://localhost:8000/mcp"

	code, err := AuthorizeClient(ctx, p, "c1", "http://127.0.0.1:1/cb", aud, "chal", "S256", []string{ScopeWrite})
	require.NoError(t, err)
	rec, err := ConsumeAuthCode(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, "c1", rec.ClientID)
	pair, err := IssueTokens(ctx, rec.TenantID, rec.GrantID, aud, "")
	require.NoError(t, err)
	assert.Equal(t, []string{ScopeRead, ScopeWrite}, pair.Scopes)

	prin, _, err := AuthenticateAccessToken(ctx, pair.AccessToken, aud)
	require.NoError(t, err)
	assert.Equal(t, PrincipalOAuth, prin.Kind)
	assert.Equal(t, tn.ID, prin.TenantID)
	assert.Equal(t, "Client One", prin.ClientName)
	_, _, err = AuthenticateAccessToken(ctx, pair.AccessToken, "http://other/mcp")
	assert.Error(t, err, "wrong audience")
	_, _, err = AuthenticateAccessToken(ctx, pair.RefreshToken, aud)
	assert.Error(t, err, "refresh token is not an access token")

	// Code reuse revokes the grant.
	_, err = ConsumeAuthCode(ctx, code)
	assert.Error(t, err)
	_, _, err = AuthenticateAccessToken(ctx, pair.AccessToken, aud)
	assert.Error(t, err, "grant revoked after code replay")

	// Fresh grant: refresh rotation and reuse detection.
	code, err = AuthorizeClient(ctx, p, "c1", "http://127.0.0.1:1/cb", aud, "chal", "S256", []string{ScopeRead})
	require.NoError(t, err)
	rec, _ = ConsumeAuthCode(ctx, code)
	pair, _ = IssueTokens(ctx, rec.TenantID, rec.GrantID, aud, "")
	pair2, err := RefreshTokens(ctx, pair.RefreshToken, "c1", aud)
	require.NoError(t, err)
	_, _, err = AuthenticateAccessToken(ctx, pair.AccessToken, aud)
	assert.Error(t, err, "old access token revoked on rotation")
	_, _, err = AuthenticateAccessToken(ctx, pair2.AccessToken, aud)
	require.NoError(t, err)
	_, err = RefreshTokens(ctx, pair.RefreshToken, "c1", aud)
	assert.Error(t, err, "reuse detected")
	_, _, err = AuthenticateAccessToken(ctx, pair2.AccessToken, aud)
	assert.Error(t, err, "family revoked after reuse")
	_, err = RefreshTokens(ctx, pair2.RefreshToken, "other", aud)
	assert.Error(t, err, "wrong client")

	// Connection revocation kills live tokens.
	code, _ = AuthorizeClient(ctx, p, "c1", "http://127.0.0.1:1/cb", aud, "chal", "S256", []string{ScopeRead})
	rec, _ = ConsumeAuthCode(ctx, code)
	pair, _ = IssueTokens(ctx, rec.TenantID, rec.GrantID, aud, "")
	conns, err := ListConnections(ctx, tn.ID)
	require.NoError(t, err)
	assert.Len(t, conns, 3)
	require.NoError(t, RevokeConnection(ctx, p, rec.GrantID))
	_, _, err = AuthenticateAccessToken(ctx, pair.AccessToken, aud)
	assert.Error(t, err)
	_, err = RefreshTokens(ctx, pair.RefreshToken, "c1", aud)
	assert.Error(t, err)

	// API tokens.
	plain, tok, err := CreateAPIToken(ctx, p, "cli", []string{ScopeRead}, 0)
	require.NoError(t, err)
	assert.Contains(t, plain, "tmpk_")
	ap, err := AuthenticateAPIToken(ctx, plain)
	require.NoError(t, err)
	assert.Equal(t, PrincipalAPIToken, ap.Kind)
	assert.Equal(t, []string{ScopeRead}, ap.Scopes)
	require.NoError(t, RevokeAPIToken(ctx, p, tok.ID))
	_, err = AuthenticateAPIToken(ctx, plain)
	assert.Error(t, err)
	_, _, err = CreateAPIToken(ctx, *ap, "x", []string{ScopeRead}, 0)
	assert.True(t, errors.Is(err, errors.CodeForbidden), "only owner sessions mint tokens")
	// Expired token.
	plain2, _, err := CreateAPIToken(ctx, p, "short", []string{ScopeRead}, time.Nanosecond)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	_, err = AuthenticateAPIToken(ctx, plain2)
	assert.Error(t, err)
}

func TestRestoreRejectsContentInvalidUnderCurrentParser(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, tn, _ := newOwner(t, "restore@x.test")
	w, err := o.Write(ctx, p, WriteInput{Path: "/r.md", Content: "# R\n", RequestID: rid()})
	require.NoError(t, err)
	// Simulate an old revision written under a laxer parser (owner handle: platform tooling).
	bad := "# R\n\n<div onclick=x>raw</div>\n"
	require.NoError(t, db.OwnerForTest(t).Exec(`INSERT INTO revision (tenant_id, entry_id, seq, operation, path, source, actor_kind, size_bytes) VALUES (?, ?, 2, 'update', '/r.md', ?, 'owner', ?)`, tn.ID, w.Entry.ID, bad, len(bad)).Error)
	require.NoError(t, db.OwnerForTest(t).Exec(`UPDATE entry SET current_revision = 2 WHERE id = ?`, w.Entry.ID).Error)
	_, err = o.Restore(ctx, p, "/r.md", 2, 2, rid())
	assert.True(t, errors.Is(err, errors.CodeValidationFailed), "restore fails explicitly: %v", err)
	rs, err := o.Restore(ctx, p, "/r.md", 1, 2, rid())
	require.NoError(t, err)
	assert.EqualValues(t, 3, rs.Entry.Revision)
}
