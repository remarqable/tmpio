package models

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	goerrors "errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// DevIssuer is the identity issuer for the development sign-in bypass. Like
// LocalIssuer it is this server vouching for a name it was handed, not a
// provider asserting one.
const DevIssuer = "dev"

// SelfIssued reports whether an identity came from this server rather than
// from an identity provider. Those identities are names, not verified
// addresses, so the verified-address rule does not apply to them.
func SelfIssued(issuer string) bool { return issuer == LocalIssuer || issuer == DevIssuer }

// ValidUsername accepts a plain name or an email address and refuses anything
// awkward to type or ambiguous between accounts.
func ValidUsername(u string) error {
	if n := len([]rune(u)); n < 2 || n > 200 {
		return errors.New(errors.CodeValidationFailed, "a username must be between 2 and 200 characters")
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+' || r == '@':
		default:
			return errors.New(errors.CodeValidationFailed, "a username may contain letters, digits, and . - _ + @ only")
		}
	}
	return nil
}

// LocalIssuer is the identity issuer for the self-hosted owner account. It is
// not a provider: the subject is the email address and this server is the only
// thing that vouches for it.
const LocalIssuer = "local"

// Argon2id parameters. Tuned for roughly a tenth of a second on a small server,
// which is the right order for a sign-in form that is rate limited to a handful
// of attempts a minute. They are written into every hash, so raising them here
// leaves existing credentials verifiable and upgrades them on the next sign-in.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// LocalCredential is the password for one local account. Username is whatever
// the owner signs in with: "admin" by default, an email address if they set
// one.
type LocalCredential struct {
	UserID       int64 `gorm:"primaryKey"`
	Username     string
	PasswordHash string
	CreatedAt    time.Time `gorm:"autoCreateTime"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime"`
}

// TableName follows the singular naming convention.
func (LocalCredential) TableName() string { return "local_credential" }

// HashPassword returns an argon2id hash in the standard encoded form, which
// carries the parameters and the salt alongside the digest.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether plain produces the encoded hash. It compares
// in constant time and treats any malformed hash as a mismatch.
func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when no credential matches, so that a wrong
// address and a wrong password cost the same and neither can be told apart by
// how long the answer took.
var dummyHash, _ = HashPassword("this password is never correct")

// EnsureLocalOwner provisions the single local owner account from the
// environment at startup, and marks it as the installation's administrator:
// it is named by the server's own configuration, so it is the operator's. A missing account is created with its organization
// and initial site files, exactly as a first sign-in would. An existing account
// keeps its content, and its password is rotated to the one supplied. Passing
// an empty password leaves an existing credential untouched, so an operator can
// drop OWNER_PASSWORD from the environment once the account exists.
//
// It returns whether the account was created.
func EnsureLocalOwner(ctx context.Context, username, password, initialConfig, initialIndex string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return false, errors.New(errors.CodeValidationFailed, "a username is required for the local owner account")
	}
	if err := ValidUsername(username); err != nil {
		return false, err
	}
	var existing LocalCredential
	err := db.Get().WithContext(ctx).Where("username = ?", username).First(&existing).Error
	switch {
	case err == nil:
		// The account named by the server's own environment administers the
		// installation. Set it on every boot, not just at creation, so an
		// instance provisioned before this existed is repaired rather than
		// left with settings nobody can reach.
		if err := SetInstanceAdmin(ctx, existing.UserID); err != nil {
			return false, err
		}
		if password == "" {
			return false, nil
		}
		if VerifyPassword(existing.PasswordHash, password) {
			return false, nil
		}
		hash, err := HashPassword(password)
		if err != nil {
			return false, err
		}
		return false, db.Get().WithContext(ctx).Model(&LocalCredential{}).
			Where("user_id = ?", existing.UserID).Update("password_hash", hash).Error
	case !goerrors.Is(err, gorm.ErrRecordNotFound):
		return false, err
	}
	if password == "" {
		return false, errors.New(errors.CodeValidationFailed, "OWNER_PASSWORD is required to create the owner account "+username)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	// allowCreate is true even when sign-ups are closed: this is the operator
	// provisioning their own instance, not a stranger signing up.
	ident := ExternalIdentity{Issuer: LocalIssuer, Subject: username, Name: username}
	// Only claim an address when one was actually given.
	if strings.Contains(username, "@") {
		ident.Email, ident.EmailVerified = username, true
	}
	user, _, _, err := SignIn(ctx, ident, initialConfig, initialIndex, true)
	if err != nil {
		return false, err
	}
	cred := &LocalCredential{UserID: user.ID, Username: username, PasswordHash: hash}
	if err := db.Get().WithContext(ctx).Create(cred).Error; err != nil {
		return false, err
	}
	if err := SetInstanceAdmin(ctx, user.ID); err != nil {
		return false, err
	}
	return true, nil
}

// AuthenticateLocal verifies a username and password against the local
// credential. Every failure is the same error, and an unknown username costs
// the same work as a wrong password.
func AuthenticateLocal(ctx context.Context, username, password string) (*User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	bad := errors.New(errors.CodeUnauthorized, "that username and password do not match")
	var cred LocalCredential
	err := db.Get().WithContext(ctx).Where("username = ?", username).First(&cred).Error
	if err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			VerifyPassword(dummyHash, password)
			return nil, bad
		}
		return nil, err
	}
	if !VerifyPassword(cred.PasswordHash, password) {
		return nil, bad
	}
	var user User
	if err := db.Get().WithContext(ctx).First(&user, cred.UserID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}
