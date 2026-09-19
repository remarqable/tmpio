// Package db owns every database handle. Nothing else opens a connection.
//
// Two handles exist: the runtime handle connects as a NOBYPASSRLS role and is
// subject to row-level security; the owner handle bypasses it and is used only
// by tests, fixtures and platform tooling.
package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	gdb      *gorm.DB // runtime: non-owner role, RLS applies
	gdbOwner *gorm.DB // owner: bypasses RLS, sees every tenant
)

// ErrNoTenant is returned when tenant-scoped work is attempted with no tenant.
var ErrNoTenant = errors.New("no tenant in context")

// Connect opens a PostgreSQL handle.
func Connect(dsn string) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("empty database url")
	}
	cfg := &gorm.Config{
		// Callers open transactions explicitly. GORM's implicit per-statement
		// transactions would not carry a tenant scope.
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError:         false,
	}
	g, err := gorm.Open(postgres.Open(dsn), cfg)
	if err != nil {
		return nil, err
	}
	sqlDB, err := g.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	return g, nil
}

// SetDB installs the runtime handle.
func SetDB(g *gorm.DB) { gdb = g }

// SetOwnerDB installs the owner handle (tests and tooling only).
func SetOwnerDB(g *gorm.DB) { gdbOwner = g }

// Get returns the runtime handle. Correct for tables with no tenant_id.
// On a tenant-scoped table it yields zero rows, because no tenant is set.
func Get() *gorm.DB { return gdb }

// Unscoped returns the owner handle: it bypasses tenant isolation.
// Never call it from request-handling code. CI greps for it.
func Unscoped() *gorm.DB {
	if gdbOwner == nil {
		return gdb
	}
	return gdbOwner
}

// WithTx wraps non-tenant work in a transaction on the runtime handle.
func WithTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return gdb.WithContext(ctx).Transaction(fn)
}

// WithTenant runs fn inside a transaction scoped to tenantID. It is the ONLY
// way tenant-scoped queries reach the database. The tenant is set with
// set_config(..., true), which is SET LOCAL with a bind parameter.
func WithTenant(ctx context.Context, tenantID int64, fn func(tx *gorm.DB) error) error {
	if tenantID == 0 {
		return ErrNoTenant
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := setTenant(tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// WithTenantSerialized is WithTenant plus a per-tenant transaction-scoped
// advisory lock. Every content mutation takes it, so concurrent writers to one
// site are serialized and path reservations can be checked safely.
func WithTenantSerialized(ctx context.Context, tenantID int64, fn func(tx *gorm.DB) error) error {
	if tenantID == 0 {
		return ErrNoTenant
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := setTenant(tx, tenantID); err != nil {
			return err
		}
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, tenantID).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

// WithUser runs fn in a transaction that exposes the user ID to the membership
// policy, so a user can list the tenants they belong to before any tenant is chosen.
func WithUser(ctx context.Context, userID int64, fn func(tx *gorm.DB) error) error {
	if userID == 0 {
		return errors.New("no user")
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT set_config('app.user_id', ?, true)`, strconv.FormatInt(userID, 10)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func setTenant(tx *gorm.DB, tenantID int64) error {
	return tx.Exec(`SELECT set_config('app.tenant_id', ?, true)`, strconv.FormatInt(tenantID, 10)).Error
}

// EnterTenantScope sets the tenant scope on a transaction that has just
// created the tenant itself (provisioning). Everything else uses WithTenant;
// `make check` restricts callers of this function to the signup path.
func EnterTenantScope(tx *gorm.DB, tenantID int64) error { return setTenant(tx, tenantID) }

// SetUserScope exposes the user ID to the membership policy inside an open transaction.
func SetUserScope(tx *gorm.DB, userID int64) error {
	return tx.Exec(`SELECT set_config('app.user_id', ?, true)`, strconv.FormatInt(userID, 10)).Error
}

// WithTenantSnapshot is WithTenant at REPEATABLE READ isolation: every read in
// fn sees one consistent snapshot (export).
func WithTenantSnapshot(ctx context.Context, tenantID int64, fn func(tx *gorm.DB) error) error {
	if tenantID == 0 {
		return ErrNoTenant
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`).Error; err != nil {
			return err
		}
		if err := setTenant(tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Ping checks the runtime connection.
func Ping(ctx context.Context) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
