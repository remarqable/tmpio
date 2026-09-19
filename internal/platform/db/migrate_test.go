package db

import (
	"os"
	"testing"

	"github.com/remarqable/tmpio/migrations"
)

func TestUserFromDSN(t *testing.T) {
	cases := []struct {
		dsn, user, password string
		wantErr             bool
	}{
		{dsn: "postgres://app_user:secret@db:5432/tmp?sslmode=disable", user: "app_user", password: "secret"},
		{dsn: "postgres://app_user:p%40ss%3Aword@db:5432/tmp", user: "app_user", password: "p@ss:word"},
		{dsn: "postgres://app_user@db:5432/tmp", wantErr: true}, // no password
		{dsn: "postgres://:secret@db:5432/tmp", wantErr: true},  // no role
		{dsn: "postgres://db:5432/tmp", wantErr: true},          // neither
		{dsn: "://not a url", wantErr: true},
	}
	for _, c := range cases {
		user, password, err := userFromDSN(c.dsn)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error", c.dsn)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.dsn, err)
			continue
		}
		if user != c.user || password != c.password {
			t.Errorf("%q: got %q/%q, want %q/%q", c.dsn, user, password, c.user, c.password)
		}
	}
}

// TestMigrateUpIsIdempotent proves the embedded migrations parse and apply. The
// test database is already migrated, so this is a no-op that still exercises
// the embedded filesystem, the dialect and the advisory lock.
func TestMigrateUpIsIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_OWNER_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_OWNER_URL is not set")
	}
	version, err := MigrateUp(t.Context(), dsn, migrations.FS)
	if err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if version < 10 {
		t.Fatalf("expected the schema to be at migration 10 or later, got %d", version)
	}
	again, err := MigrateUp(t.Context(), dsn, migrations.FS)
	if err != nil {
		t.Fatalf("migrating twice: %v", err)
	}
	if again != version {
		t.Fatalf("a second run changed the version from %d to %d", version, again)
	}
}
