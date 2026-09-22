package models

import (
	"context"
	"database/sql"
	goerrors "errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// Invite is a pending invitation to an organization. The token is never
// stored, only its hash, the same way a sharing link works.
type Invite struct {
	ID              int64 `gorm:"primaryKey"`
	TenantID        int64
	Email           string
	Role            string
	TokenHash       []byte
	CreatedByUserID *int64
	ExpiresAt       time.Time
	AcceptedAt      *time.Time
	AcceptedUserID  *int64
	RevokedAt       *time.Time
	CreatedAt       time.Time `gorm:"autoCreateTime"`
}

// TableName follows the singular naming convention.
func (Invite) TableName() string { return "invite" }

// MemberInfo is one row of the members list.
type MemberInfo struct {
	UserID      int64
	Email       string
	DisplayName string
	Role        string
	JoinedAt    time.Time
	IsYou       bool
}

// PendingInvite is one row of the outstanding invitations.
type PendingInvite struct {
	ID        int64
	Email     string
	Role      string
	ExpiresAt time.Time
	Expired   bool
}

const inviteLifetime = 14 * 24 * time.Hour

// Members lists who belongs to this organization.
func (o *Ops) Members(ctx context.Context, p Principal) ([]MemberInfo, error) {
	if !p.IsOwnerSession() {
		return nil, errors.New(errors.CodeForbidden, "only a signed-in member can list members")
	}
	var out []MemberInfo
	err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		var rows []struct {
			UserID      int64
			Role        string
			CreatedAt   time.Time
			Email       string
			DisplayName string
		}
		if err := tx.Table("membership m").
			Select(`m.user_id, m.role, m.created_at, u.email, u.display_name`).
			Joins(`JOIN "user" u ON u.id = m.user_id`).
			Where("m.tenant_id = ?", p.TenantID).
			Order("m.id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, MemberInfo{
				UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName,
				Role: r.Role, JoinedAt: r.CreatedAt, IsYou: r.UserID == p.UserID,
			})
		}
		return nil
	})
	return out, err
}

// PendingInvites lists invitations that have not been accepted or revoked.
func (o *Ops) PendingInvites(ctx context.Context, p Principal) ([]PendingInvite, error) {
	if !RoleAdmin(p.Role) {
		return nil, errors.New(errors.CodeForbidden, "only an owner can see invitations")
	}
	var out []PendingInvite
	err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		var rows []Invite
		if err := tx.Where("tenant_id = ? AND accepted_at IS NULL AND revoked_at IS NULL", p.TenantID).
			Order("id DESC").Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, PendingInvite{
				ID: r.ID, Email: r.Email, Role: r.Role,
				ExpiresAt: r.ExpiresAt, Expired: r.ExpiresAt.Before(time.Now()),
			})
		}
		return nil
	})
	return out, err
}

// CreateInvite records an invitation and returns the secret token once. The
// token is shown to the inviter to pass on; this server sends no mail.
func (o *Ops) CreateInvite(ctx context.Context, p Principal, email, role string) (string, error) {
	if !RoleAdmin(p.Role) {
		return "", errors.New(errors.CodeForbidden, "only an owner can invite people")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", errors.New(errors.CodeValidationFailed, "an email address is required")
	}
	if len(RoleScopes(role)) == 0 {
		return "", errors.New(errors.CodeValidationFailed, "role must be owner, editor or viewer")
	}
	token := NewToken()
	uid := p.UserID
	inv := &Invite{
		TenantID: p.TenantID, Email: email, Role: role,
		TokenHash: HashToken(token), CreatedByUserID: &uid,
		ExpiresAt: time.Now().Add(inviteLifetime),
	}
	if err := db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		return tx.Create(inv).Error
	}); err != nil {
		return "", err
	}
	return token, nil
}

// RevokeInvite withdraws an unaccepted invitation.
func (o *Ops) RevokeInvite(ctx context.Context, p Principal, id int64) error {
	if !RoleAdmin(p.Role) {
		return errors.New(errors.CodeForbidden, "only an owner can revoke invitations")
	}
	return db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		return tx.Model(&Invite{}).
			Where("id = ? AND tenant_id = ? AND accepted_at IS NULL", id, p.TenantID).
			Update("revoked_at", time.Now()).Error
	})
}

// AcceptInvite turns a token into a membership for this user. The lookup and
// the write both run in a SECURITY DEFINER function, because the accepter is
// not yet a member and so cannot see the row under the tenant policy.
func AcceptInvite(ctx context.Context, token string, userID int64) (int64, error) {
	var tenantID int64
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		var got sql.NullInt64
		if err := tx.Raw(`SELECT accept_invite(decode(?, 'hex'), ?)`, HashHex(token), userID).Scan(&got).Error; err != nil {
			return err
		}
		if !got.Valid {
			return errors.New(errors.CodeNotFound, "that invitation is not valid any more")
		}
		tenantID = got.Int64
		return nil
	})
	return tenantID, err
}

// InviteInfo describes an invitation to whoever is holding the link, without
// requiring them to be a member yet.
type InviteInfo struct {
	TenantID int64
	Email    string
	Role     string
	Valid    bool
}

// LookupInvite reads an invitation by token for the acceptance page.
func LookupInvite(ctx context.Context, token string) (*InviteInfo, error) {
	var row struct {
		ID         int64
		TenantID   int64
		Email      string
		Role       string
		ExpiresAt  time.Time
		AcceptedAt *time.Time
		RevokedAt  *time.Time
	}
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id, tenant_id, email, role, expires_at, accepted_at, revoked_at FROM lookup_invite(decode(?, 'hex'))`, HashHex(token)).Scan(&row).Error
	})
	if err != nil {
		return nil, err
	}
	if row.ID == 0 {
		return nil, errors.New(errors.CodeNotFound, "no such invitation")
	}
	info := &InviteInfo{TenantID: row.TenantID, Email: row.Email, Role: row.Role}
	info.Valid = row.AcceptedAt == nil && row.RevokedAt == nil && row.ExpiresAt.After(time.Now())
	return info, nil
}

// SetMemberRole changes what a member may do. The last owner cannot be
// demoted: an organization with no owner has nobody who can invite, change
// settings or delete it.
func (o *Ops) SetMemberRole(ctx context.Context, p Principal, userID int64, role string) error {
	if !RoleAdmin(p.Role) {
		return errors.New(errors.CodeForbidden, "only an owner can change roles")
	}
	if len(RoleScopes(role)) == 0 {
		return errors.New(errors.CodeValidationFailed, "role must be owner, editor or viewer")
	}
	return db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		if role != RoleOwner {
			if err := lastOwnerGuard(tx, p.TenantID, userID); err != nil {
				return err
			}
		}
		res := tx.Model(&Membership{}).
			Where("tenant_id = ? AND user_id = ?", p.TenantID, userID).
			Update("role", role)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New(errors.CodeNotFound, "not a member of this organization")
		}
		return nil
	})
}

// RemoveMember takes someone out of the organization and ends every way they
// were reaching it: their sessions here, their API tokens, their OAuth grants.
func (o *Ops) RemoveMember(ctx context.Context, p Principal, userID int64) error {
	if !RoleAdmin(p.Role) {
		return errors.New(errors.CodeForbidden, "only an owner can remove members")
	}
	return db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		if err := lastOwnerGuard(tx, p.TenantID, userID); err != nil {
			return err
		}
		res := tx.Where("tenant_id = ? AND user_id = ?", p.TenantID, userID).Delete(&Membership{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New(errors.CodeNotFound, "not a member of this organization")
		}
		// Credentials that acted for this organization stop working now rather
		// than whenever they happen to expire.
		now := time.Now()
		if err := tx.Table("api_token").
			Where("tenant_id = ? AND user_id = ? AND revoked_at IS NULL", p.TenantID, userID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Table("oauth_token").
			Where("tenant_id = ? AND user_id = ? AND revoked_at IS NULL", p.TenantID, userID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return nil
	})
}

// lastOwnerGuard refuses a change that would leave the organization ownerless.
func lastOwnerGuard(tx *gorm.DB, tenantID, userID int64) error {
	var target Membership
	if err := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&target).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New(errors.CodeNotFound, "not a member of this organization")
		}
		return err
	}
	if target.Role != RoleOwner {
		return nil
	}
	var owners int64
	if err := tx.Model(&Membership{}).
		Where("tenant_id = ? AND role = ?", tenantID, RoleOwner).Count(&owners).Error; err != nil {
		return err
	}
	if owners <= 1 {
		return errors.New(errors.CodeValidationFailed,
			"this is the only owner; make someone else an owner first")
	}
	return nil
}

// OrgSummary is one organization this account belongs to.
type OrgSummary struct {
	TenantID int64
	Code     string
	Name     string
	Role     string
	Current  bool
}

// OrganizationsFor lists the organizations this account is a member of. It
// runs in the user's own scope, not a tenant's: the whole point is to see
// across them.
func OrganizationsFor(ctx context.Context, userID, currentTenantID int64) ([]OrgSummary, error) {
	var out []OrgSummary
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		if err := db.SetUserScope(tx, userID); err != nil {
			return err
		}
		var rows []struct {
			TenantID int64
			Role     string
			Code     string
			Name     *string
		}
		if err := tx.Table("membership m").
			Select("m.tenant_id, m.role, t.code, t.name").
			Joins("JOIN tenant t ON t.id = m.tenant_id").
			Where("m.user_id = ?", userID).Order("m.id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			name := ""
			if r.Name != nil {
				name = *r.Name
			}
			out = append(out, OrgSummary{
				TenantID: r.TenantID, Code: r.Code, Name: name,
				Role: r.Role, Current: r.TenantID == currentTenantID,
			})
		}
		return nil
	})
	return out, err
}
