package db

import (
	"os"
	"testing"

	"gorm.io/gorm"
)

// ConnectTest wires the runtime and owner handles to the test database and
// truncates every table. Tests are skipped when TEST_DATABASE_URL is unset.
// The suite connects as the runtime role; the owner handle is used only here.
func ConnectTest(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	owner := os.Getenv("TEST_DATABASE_OWNER_URL")
	if dsn == "" || owner == "" {
		t.Skip("TEST_DATABASE_URL and TEST_DATABASE_OWNER_URL are required for PostgreSQL tests")
	}
	if gdb == nil {
		g, err := Connect(dsn)
		if err != nil {
			t.Fatalf("connect runtime: %v", err)
		}
		SetDB(g)
	}
	if gdbOwner == nil {
		o, err := Connect(owner)
		if err != nil {
			t.Fatalf("connect owner: %v", err)
		}
		SetOwnerDB(o)
	}
	ResetTestData(t)
}

// ResetTestData truncates every application table (owner connection).
func ResetTestData(t *testing.T) {
	t.Helper()
	tables := []string{"mutation_receipt", "audit_event", "share_grant_asset", "share_grant", "oauth_token", "oauth_code", "oauth_grant", "oauth_client", "api_token", "path_alias", "revision", "entry", "asset_blob", "membership", "login_state", "session", "identity", "local_credential", "launch_signup", "tenant", `"user"`}
	for _, tbl := range tables {
		if err := gdbOwner.Exec("TRUNCATE TABLE " + tbl + " RESTART IDENTITY CASCADE").Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
}

// OwnerForTest exposes the owner handle to tests for assertions that must see every tenant.
func OwnerForTest(t *testing.T) *gorm.DB {
	t.Helper()
	if gdbOwner == nil {
		t.Fatal("owner handle not connected")
	}
	return gdbOwner
}
