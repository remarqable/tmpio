package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"time"

	"github.com/pressly/goose/v3"
)

// migrateLockID is an arbitrary constant that identifies the schema lock. Two
// instances starting at the same moment must not run migrations concurrently;
// the second waits and then finds nothing to do.
const migrateLockID int64 = 8_274_119_003

// MigrateUp applies pending migrations as the owner role and returns the
// version it arrived at. The owner connection is opened for this and closed
// again, so the running server keeps only its NOBYPASSRLS handle.
func MigrateUp(ctx context.Context, ownerDSN string, fsys fs.FS) (int64, error) {
	if ownerDSN == "" {
		return 0, fmt.Errorf("DATABASE_OWNER_URL is required to run migrations")
	}
	g, err := Connect(ownerDSN)
	if err != nil {
		return 0, err
	}
	sqlDB, err := g.DB()
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// The lock lives on one connection for as long as it is held, so take a
	// connection out of the pool rather than using the pool itself.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return 0, fmt.Errorf("waiting for the schema lock: %w", err)
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlock, `SELECT pg_advisory_unlock($1)`, migrateLockID)
	}()

	goose.SetBaseFS(fsys)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, err
	}
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return 0, err
	}
	return goose.GetDBVersionContext(ctx, sqlDB)
}

// EnsureRuntimeRole creates the role named in the runtime DSN if it is missing
// and gives it the privileges the server needs, including the default
// privileges that cover tables migrations have not created yet. It runs as the
// owner, before migrations, and is idempotent.
//
// This is what a self-hosted instance needs to start against an empty database
// with nothing but a compose file. Where an operator provisions roles by hand
// (a managed database, or any deployment with AUTO_MIGRATE off) it never runs.
func EnsureRuntimeRole(ctx context.Context, ownerDSN, runtimeDSN string) error {
	name, password, err := userFromDSN(runtimeDSN)
	if err != nil {
		return err
	}
	g, err := Connect(ownerDSN)
	if err != nil {
		return err
	}
	sqlDB, err := g.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// The identifier and the literal are quoted by the server, which is the
		// only safe way to put a name and a password into DDL.
		if err := execFormatted(ctx, sqlDB,
			`SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOBYPASSRLS', $1::text, $2::text)`, name, password); err != nil {
			return fmt.Errorf("creating the runtime role %q: %w (create it by hand, or turn AUTO_MIGRATE off)", name, err)
		}
	}
	stmts := []string{
		`SELECT format('GRANT USAGE ON SCHEMA public TO %I', $1::text)`,
		`SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', $1::text)`,
		`SELECT format('GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO %I', $1::text)`,
		`SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', current_user, $1::text)`,
		`SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT USAGE ON SEQUENCES TO %I', current_user, $1::text)`,
		`SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO %I', current_user, $1::text)`,
	}
	for _, q := range stmts {
		if err := execFormatted(ctx, sqlDB, q, name); err != nil {
			return fmt.Errorf("granting to %q: %w", name, err)
		}
	}
	return nil
}

// execFormatted asks the server to build a statement with its own quoting, then
// runs what it built.
func execFormatted(ctx context.Context, sqlDB *sql.DB, query string, args ...any) error {
	var stmt string
	if err := sqlDB.QueryRowContext(ctx, query, args...).Scan(&stmt); err != nil {
		return err
	}
	_, err := sqlDB.ExecContext(ctx, stmt)
	return err
}

// userFromDSN reads the role name and password the server will connect with.
func userFromDSN(dsn string) (string, string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("DATABASE_URL: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return "", "", fmt.Errorf("DATABASE_URL has no role name")
	}
	password, _ := u.User.Password()
	if password == "" {
		return "", "", fmt.Errorf("DATABASE_URL has no password for %q", u.User.Username())
	}
	return u.User.Username(), password, nil
}
