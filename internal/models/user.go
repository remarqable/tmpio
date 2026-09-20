package models

import (
	"context"
	goerrors "errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// User is a person who signs in. Identity is by provider issuer+subject, never email alone.
type User struct {
	ID          int64  `gorm:"primaryKey"`
	DisplayName string `gorm:"column:display_name"`
	Email       string `gorm:"column:email"`
	AvatarURL   string `gorm:"column:avatar_url"`
	// IsInstanceAdmin marks the account that administers the installation, as
	// opposed to owning one organization within it. Set by the setup wizard.
	IsInstanceAdmin bool      `gorm:"column:is_instance_admin"`
	CreatedAt       time.Time `gorm:"autoCreateTime"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime"`
}

// TableName follows the singular naming convention.
func (User) TableName() string { return "user" }

// Identity links a user to an external identity provider subject.
type Identity struct {
	ID            int64 `gorm:"primaryKey"`
	UserID        int64
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	CreatedAt     time.Time `gorm:"autoCreateTime"`
}

// TableName follows the singular naming convention.
func (Identity) TableName() string { return "identity" }

// Tenant is an organization: the owner of all content. Its public code is
// immutable and is an identifier, never a secret.
type Tenant struct {
	ID         int64 `gorm:"primaryKey"`
	Code       string
	Name       *string
	Generation int64
	// AIFilingEnabled is the owner's opt-in to send document excerpts to the
	// configured model when filing content. Off by default.
	AIFilingEnabled bool
	CreatedAt       time.Time `gorm:"autoCreateTime"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime"`
}

// TableName follows the singular naming convention.
func (Tenant) TableName() string { return "tenant" }

// DisplayName returns the organization name or an empty string.
func (t *Tenant) DisplayName() string {
	if t.Name == nil {
		return ""
	}
	return *t.Name
}

// Membership joins a user to a tenant with a role.
type Membership struct {
	ID        int64 `gorm:"primaryKey"`
	TenantID  int64
	UserID    int64
	Role      string
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// TableName follows the singular naming convention.
func (Membership) TableName() string { return "membership" }

// ExternalIdentity is what a verified sign-in supplies.
type ExternalIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

// SignIn finds or creates the user for a verified external identity. A brand
// new user gets exactly one organization, an owner membership, and the initial
// site files in the same transaction. Repeat sign-ins create nothing. When
// allowCreate is false an unknown identity is refused with CodeSignupsClosed
// and nothing is written.
func SignIn(ctx context.Context, ext ExternalIdentity, initialConfig string, initialIndex string, allowCreate bool) (*User, *Tenant, bool, error) {
	if ext.Issuer == "" || ext.Subject == "" {
		return nil, nil, false, errors.New(errors.CodeUnauthorized, "identity is incomplete")
	}
	// The verified-address rule exists to stop an identity provider asserting
	// someone else's account. A local credential is set by the operator on
	// their own server, so there is no provider to distrust and no address to
	// verify: "admin" is a perfectly good identity.
	if ext.Issuer != LocalIssuer && !ext.EmailVerified {
		return nil, nil, false, errors.New(errors.CodeUnauthorized, "a verified email address is required")
	}
	var (
		user    User
		tenant  Tenant
		created bool
	)
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		var ident Identity
		err := tx.Where("issuer = ? AND subject = ?", ext.Issuer, ext.Subject).First(&ident).Error
		switch {
		case err == nil:
			if err := tx.First(&user, ident.UserID).Error; err != nil {
				return err
			}
			changed := false
			if ext.Name != "" && ext.Name != user.DisplayName {
				user.DisplayName = ext.Name
				changed = true
			}
			if ext.AvatarURL != user.AvatarURL {
				user.AvatarURL = ext.AvatarURL
				changed = true
			}
			if changed {
				if err := tx.Save(&user).Error; err != nil {
					return err
				}
			}
			t, err := currentTenantForUser(tx, user.ID)
			if err != nil {
				return err
			}
			tenant = *t
			return nil
		case goerrors.Is(err, gorm.ErrRecordNotFound):
			if !allowCreate {
				return errors.New(errors.CodeSignupsClosed, "new accounts are not being created right now")
			}
		default:
			return err
		}
		created = true
		user = User{DisplayName: strings.TrimSpace(ext.Name), Email: strings.ToLower(strings.TrimSpace(ext.Email)), AvatarURL: ext.AvatarURL}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		ident = Identity{UserID: user.ID, Issuer: ext.Issuer, Subject: ext.Subject, Email: user.Email, EmailVerified: true}
		if err := tx.Create(&ident).Error; err != nil {
			return err
		}
		t, err := createTenantWithOwner(tx, user.ID, initialConfig, initialIndex)
		if err != nil {
			return err
		}
		tenant = *t
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return &user, &tenant, created, nil
}

// createTenantWithOwner creates the organization, owner membership and initial
// filesystem inside the caller's transaction. It enters the tenant scope on
// the transaction so RLS WITH CHECK accepts the new rows.
func createTenantWithOwner(tx *gorm.DB, userID int64, initialConfig, initialIndex string) (*Tenant, error) {
	var tenant Tenant
	for attempt := 0; attempt < 5; attempt++ {
		tenant = Tenant{Code: NewOrgCode(), Generation: 1}
		var exists int64
		if err := tx.Model(&Tenant{}).Where("code = ?", tenant.Code).Count(&exists).Error; err != nil {
			return nil, err
		}
		if exists == 0 {
			break
		}
		if attempt == 4 {
			return nil, goerrors.New("could not allocate an organization code")
		}
	}
	if err := tx.Create(&tenant).Error; err != nil {
		return nil, err
	}
	if err := db.EnterTenantScope(tx, tenant.ID); err != nil {
		return nil, err
	}
	if err := tx.Create(&Membership{TenantID: tenant.ID, UserID: userID, Role: "owner"}).Error; err != nil {
		return nil, err
	}
	if err := initialFilesystem(tx, tenant.ID, userID, initialConfig, initialIndex); err != nil {
		return nil, err
	}
	return &tenant, nil
}

func currentTenantForUser(tx *gorm.DB, userID int64) (*Tenant, error) {
	if err := db.SetUserScope(tx, userID); err != nil {
		return nil, err
	}
	var m Membership
	if err := tx.Where("user_id = ?", userID).Order("id").First(&m).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeUnauthorized, "no organization membership")
		}
		return nil, err
	}
	var t Tenant
	if err := tx.First(&t, m.TenantID).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// TenantForUser returns the user's current (sole) organization.
func TenantForUser(ctx context.Context, userID int64) (*Tenant, error) {
	var t *Tenant
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		var err error
		t, err = currentTenantForUser(tx, userID)
		return err
	})
	return t, err
}

// GetUser loads a user by ID.
func GetUser(ctx context.Context, id int64) (*User, error) {
	var u User
	if err := db.Get().WithContext(ctx).First(&u, id).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeNotFound, "user not found")
		}
		return nil, err
	}
	return &u, nil
}

// GetTenantByCode resolves an organization by its public code (case-insensitive).
func GetTenantByCode(ctx context.Context, code string) (*Tenant, error) {
	code = strings.ToUpper(code)
	if !ValidOrgCode(code) {
		return nil, errors.New(errors.CodeNotFound, "organization not found")
	}
	var t Tenant
	if err := db.Get().WithContext(ctx).Where("code = ?", code).First(&t).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeNotFound, "organization not found")
		}
		return nil, err
	}
	return &t, nil
}

// GetTenant loads a tenant by ID.
func GetTenant(ctx context.Context, id int64) (*Tenant, error) {
	var t Tenant
	if err := db.Get().WithContext(ctx).First(&t, id).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeNotFound, "organization not found")
		}
		return nil, err
	}
	return &t, nil
}

// IsOwner reports whether the user holds the owner role in the tenant.
func IsOwner(ctx context.Context, tenantID, userID int64) (bool, error) {
	var n int64
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		return tx.Model(&Membership{}).Where("user_id = ? AND role = 'owner'", userID).Count(&n).Error
	})
	return n > 0, err
}

// SetTenantAIFiling records the owner's choice about AI-assisted filing.
func SetTenantAIFiling(ctx context.Context, tenantID int64, enabled bool) error {
	return db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&Tenant{}).Where("id = ?", tenantID).Update("ai_filing_enabled", enabled).Error
	})
}

// DeleteAccount removes a user and the organization they own: every entry,
// revision, asset, share link, token, connection, audit row and session goes
// with it, through ON DELETE CASCADE from tenant and user. Referential actions
// bypass row security, so the cascade reaches every tenant table. The caller
// must have verified an owner session and an explicit confirmation.
func DeleteAccount(ctx context.Context, userID, tenantID int64) error {
	ok, err := IsOwner(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(errors.CodeForbidden, "only the owner can delete this organization")
	}
	return db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, tenantID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM tenant WHERE id = ?`, tenantID).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM "user" WHERE id = ?`, userID).Error
	})
}

// RenameTenant sets the optional organization name. It never changes the code.
func RenameTenant(ctx context.Context, tenantID int64, name string) error {
	name = strings.TrimSpace(name)
	if len(name) > 120 {
		return errors.New(errors.CodeValidationFailed, "name must be 120 characters or fewer")
	}
	var v *string
	if name != "" {
		v = &name
	}
	return db.Get().WithContext(ctx).Model(&Tenant{}).Where("id = ?", tenantID).Update("name", v).Error
}
