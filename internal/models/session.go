package models

import (
	"context"
	goerrors "errors"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// SessionLifetime is the proposed 30-day browser session.
const SessionLifetime = 30 * 24 * time.Hour

// Session is a browser session. Only the token hash is stored.
type Session struct {
	ID         int64 `gorm:"primaryKey"`
	TokenHash  []byte
	UserID     int64
	CSRFToken  string `gorm:"column:csrf_token"`
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	// ActingTenantID is the organization this session is working in. Nil means
	// "the oldest membership", which is everyone who belongs to exactly one.
	ActingTenantID *int64 `gorm:"column:acting_tenant_id"`
}

// ActInTenant points a session at an organization, after checking the user is
// a member of it. The check is here rather than at the caller so no handler
// can set it from a form value.
func ActInTenant(ctx context.Context, sessionID, userID, tenantID int64) error {
	return db.WithTx(ctx, func(tx *gorm.DB) error {
		if err := db.SetUserScope(tx, userID); err != nil {
			return err
		}
		var m Membership
		if err := tx.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&m).Error; err != nil {
			return errors.New(errors.CodeForbidden, "not a member of that organization")
		}
		return tx.Model(&Session{}).Where("id = ?", sessionID).Update("acting_tenant_id", tenantID).Error
	})
}

// TableName follows the singular naming convention.
func (Session) TableName() string { return "session" }

// CreateSession issues a new session and returns the plaintext token exactly once.
func CreateSession(ctx context.Context, userID int64) (string, *Session, error) {
	token := NewToken()
	now := time.Now()
	s := &Session{TokenHash: HashToken(token), UserID: userID, CSRFToken: NewToken(), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(SessionLifetime)}
	if err := db.Get().WithContext(ctx).Create(s).Error; err != nil {
		return "", nil, err
	}
	return token, s, nil
}

// GetSession validates a session token.
func GetSession(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, errors.New(errors.CodeUnauthorized, "no session")
	}
	var s Session
	err := db.Get().WithContext(ctx).Where("token_hash = ?", HashToken(token)).First(&s).Error
	if err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeUnauthorized, "invalid session")
		}
		return nil, err
	}
	if s.RevokedAt != nil || time.Now().After(s.ExpiresAt) {
		return nil, errors.New(errors.CodeUnauthorized, "session expired")
	}
	if time.Since(s.LastSeenAt) > time.Hour {
		_ = db.Get().WithContext(ctx).Model(&Session{}).Where("id = ?", s.ID).Update("last_seen_at", time.Now()).Error
	}
	return &s, nil
}

// RevokeSession ends a session.
func RevokeSession(ctx context.Context, token string) error {
	now := time.Now()
	return db.Get().WithContext(ctx).Model(&Session{}).Where("token_hash = ?", HashToken(token)).Update("revoked_at", now).Error
}

// LoginState stores pending sign-in state server-side.
type LoginState struct {
	ID           int64 `gorm:"primaryKey"`
	StateHash    []byte
	Nonce        string
	PKCEVerifier string `gorm:"column:pkce_verifier"`
	ReturnPath   string
	OAuthRequest []byte `gorm:"column:oauth_request;type:jsonb"`
	CreatedAt    time.Time
	ExpiresAt    time.Time
	UsedAt       *time.Time
}

// TableName follows the singular naming convention.
func (LoginState) TableName() string { return "login_state" }

// CreateLoginState persists pending sign-in state and returns the plaintext state value.
func CreateLoginState(ctx context.Context, nonce, verifier, returnPath string, oauthRequest []byte) (string, error) {
	state := NewToken()
	ls := &LoginState{StateHash: HashToken(state), Nonce: nonce, PKCEVerifier: verifier, ReturnPath: returnPath, OAuthRequest: oauthRequest, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Minute)}
	if len(ls.OAuthRequest) == 0 {
		ls.OAuthRequest = nil
	}
	if err := db.Get().WithContext(ctx).Create(ls).Error; err != nil {
		return "", err
	}
	return state, nil
}

// ConsumeLoginState validates and single-uses a state value.
func ConsumeLoginState(ctx context.Context, state string) (*LoginState, error) {
	if state == "" {
		return nil, errors.New(errors.CodeUnauthorized, "missing state")
	}
	var ls LoginState
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("state_hash = ?", HashToken(state)).First(&ls).Error; err != nil {
			if goerrors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New(errors.CodeUnauthorized, "unknown or expired sign-in state")
			}
			return err
		}
		if ls.UsedAt != nil || time.Now().After(ls.ExpiresAt) {
			return errors.New(errors.CodeUnauthorized, "sign-in state expired or already used")
		}
		now := time.Now()
		return tx.Model(&LoginState{}).Where("id = ?", ls.ID).Update("used_at", now).Error
	})
	if err != nil {
		return nil, err
	}
	return &ls, nil
}
