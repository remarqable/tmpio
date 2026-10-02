package models

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// SignupAllow is one address allowed to create an account while sign-ups are
// closed. It belongs to the installation, not to an organization.
type SignupAllow struct {
	Email         string     `gorm:"primaryKey" json:"email"`
	Note          string     `json:"note"`
	AddedByUserID *int64     `json:"-"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (SignupAllow) TableName() string { return "signup_allow" }

// normalizeEmail lowercases and trims an address and checks it is one.
func normalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if a, err := mail.ParseAddress(e); err != nil || a.Address != e || len(e) > 254 {
		return "", errors.New(errors.CodeValidationFailed, "that is not an email address")
	}
	return e, nil
}

// AllowSignup adds an address to the allowlist. Adding one that is already
// there updates its note and is not an error.
func AllowSignup(ctx context.Context, email, note string, byUserID int64) (string, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	row := SignupAllow{Email: e, Note: strings.TrimSpace(note), AddedByUserID: &byUserID}
	err = db.Get().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{"note"}),
	}).Create(&row).Error
	return e, err
}

// DisallowSignup removes an address. An account already created stays.
func DisallowSignup(ctx context.Context, email string) error {
	e := strings.ToLower(strings.TrimSpace(email))
	return db.Get().WithContext(ctx).Where("email = ?", e).Delete(&SignupAllow{}).Error
}

// ListSignupAllow returns the allowlist, newest first.
func ListSignupAllow(ctx context.Context) ([]SignupAllow, error) {
	var rows []SignupAllow
	err := db.Get().WithContext(ctx).Order("created_at DESC, email").Find(&rows).Error
	return rows, err
}

// signupAllowed reports whether an address is on the allowlist, and marks it
// used when it is, inside the sign-in transaction.
func signupAllowed(tx *gorm.DB, email string) (bool, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return false, nil
	}
	res := tx.Model(&SignupAllow{}).Where("email = ?", e).Update("used_at", time.Now())
	return res.RowsAffected > 0, res.Error
}
