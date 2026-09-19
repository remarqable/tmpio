package models

import (
	"context"
	goerrors "errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// Token lifetimes (section 7).
const (
	APITokenMaxLifetime  = 30 * 24 * time.Hour
	AccessTokenLifetime  = 15 * time.Minute
	RefreshTokenLifetime = 30 * 24 * time.Hour
	AuthCodeLifetime     = 5 * time.Minute
)

// OAuthClient is a preregistered AI client.
type OAuthClient struct {
	ID           string `gorm:"primaryKey"`
	Name         string
	RedirectURIs StringArray `gorm:"column:redirect_uris;type:text[]"`
	IsPublic     bool
	SecretHash   []byte
	IsDynamic    bool
	ClientURI    string `gorm:"column:client_uri"`
	RegisteredIP string `gorm:"column:registered_ip"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MaxDynamicClientsPerDay bounds registrations from one address.
const MaxDynamicClientsPerDay = 50

// DynamicClientInput is the accepted subset of RFC 7591 client metadata.
type DynamicClientInput struct {
	Name         string
	ClientURI    string
	RedirectURIs []string
	Confidential bool // token_endpoint_auth_method client_secret_post or client_secret_basic
	Native       bool // application_type native: loopback http and private-use schemes allowed
	IP           string
}

// RegisterDynamicClient stores a client registered through RFC 7591 and
// returns it with the plaintext secret (confidential clients only), shown once.
// Redirect URIs must be https, or for native clients http on a loopback host
// or a private-use scheme (RFC 8252). PKCE remains mandatory for every client.
func RegisterDynamicClient(ctx context.Context, in DynamicClientInput) (*OAuthClient, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Registered client"
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		return nil, "", errors.New(errors.CodeValidationFailed, "redirect_uris must list between 1 and 10 URIs")
	}
	for _, u := range in.RedirectURIs {
		if err := validateRedirectURI(u, in.Native); err != nil {
			return nil, "", err
		}
	}
	clientURI := in.ClientURI
	if len(clientURI) > 200 || (clientURI != "" && !strings.HasPrefix(clientURI, "https://")) {
		clientURI = ""
	}
	c := &OAuthClient{ID: "dyn_" + NewToken()[:22], Name: name, RedirectURIs: in.RedirectURIs, IsPublic: !in.Confidential, IsDynamic: true, ClientURI: clientURI, RegisteredIP: in.IP}
	secret := ""
	if in.Confidential {
		secret = "tmps_" + NewToken()
		c.SecretHash = HashToken(secret)
	}
	err := db.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&OAuthClient{}).Where("is_dynamic AND registered_ip = ? AND created_at > ?", in.IP, time.Now().Add(-24*time.Hour)).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxDynamicClientsPerDay {
			return errors.New(errors.CodeRateLimited, "too many client registrations from this address today")
		}
		return tx.Create(c).Error
	})
	if err != nil {
		return nil, "", err
	}
	return c, secret, nil
}

var privateSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

func validateRedirectURI(raw string, native bool) error {
	if len(raw) > 512 {
		return errors.New(errors.CodeValidationFailed, "redirect_uri is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Fragment != "" || u.User != nil {
		return errors.Newf(errors.CodeValidationFailed, "invalid redirect_uri %q", raw)
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return errors.Newf(errors.CodeValidationFailed, "invalid redirect_uri %q", raw)
		}
		return nil
	case "http":
		if native && loopback {
			return nil
		}
		return errors.Newf(errors.CodeValidationFailed, "http redirect URIs are only allowed on loopback hosts for native clients: %q", raw)
	case "javascript", "data", "file", "vbscript", "blob", "about":
		return errors.Newf(errors.CodeValidationFailed, "unsafe redirect_uri scheme in %q", raw)
	default:
		if native && privateSchemeRe.MatchString(strings.ToLower(u.Scheme)) {
			return nil // private-use scheme for a native app (RFC 8252 section 7.1)
		}
		return errors.Newf(errors.CodeValidationFailed, "redirect_uri scheme must be https (native clients may use loopback http or a private-use scheme): %q", raw)
	}
}

// TableName follows the singular naming convention.
func (OAuthClient) TableName() string { return "oauth_client" }

// SyncOAuthClients upserts the configured clients at startup.
func SyncOAuthClients(ctx context.Context, clients []config.OAuthClient) error {
	return db.WithTx(ctx, func(tx *gorm.DB) error {
		for _, c := range clients {
			row := OAuthClient{ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs, IsPublic: c.Public}
			if c.Secret != "" {
				row.SecretHash = HashToken(c.Secret)
				row.IsPublic = false
			}
			// Atomic upsert: several instances may boot at once.
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "redirect_uris", "is_public", "secret_hash", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetOAuthClient loads a client.
func GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	var c OAuthClient
	if err := db.Get().WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		if goerrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(errors.CodeNotFound, "unknown client")
		}
		return nil, err
	}
	return &c, nil
}

// HasRedirect reports whether uri exactly matches a registered redirect URI.
func (c *OAuthClient) HasRedirect(uri string) bool {
	for _, r := range c.RedirectURIs {
		if r == uri {
			return true
		}
	}
	return false
}

// APIToken is an owner-created development credential for REST.
type APIToken struct {
	ID         int64 `gorm:"primaryKey"`
	TenantID   int64
	UserID     int64
	Name       string
	Scopes     StringArray `gorm:"type:text[]"`
	TokenHash  []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// TableName follows the singular naming convention.
func (APIToken) TableName() string { return "api_token" }

// CreateAPIToken mints a token shown once. Prefix "tmpk_" identifies REST tokens.
func CreateAPIToken(ctx context.Context, p Principal, name string, scopes []string, lifetime time.Duration) (string, *APIToken, error) {
	if !p.IsOwnerSession() {
		return "", nil, errors.New(errors.CodeForbidden, "only the owner's browser session can create API tokens")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", nil, errors.New(errors.CodeValidationFailed, "name is required (max 80 characters)")
	}
	sc, err := NormalizeScopes(scopes)
	if err != nil {
		return "", nil, errors.New(errors.CodeValidationFailed, err.Error())
	}
	if lifetime <= 0 || lifetime > APITokenMaxLifetime {
		lifetime = APITokenMaxLifetime
	}
	plain := "tmpk_" + NewToken()
	t := &APIToken{TenantID: p.TenantID, UserID: p.UserID, Name: name, Scopes: sc, TokenHash: HashToken(plain), ExpiresAt: time.Now().Add(lifetime)}
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		return audit(tx, p, "api_token.create", nil, nil, "")
	})
	if err != nil {
		return "", nil, err
	}
	return plain, t, nil
}

// ListAPITokens lists the tenant's tokens (metadata only).
func ListAPITokens(ctx context.Context, tenantID int64) ([]APIToken, error) {
	var rows []APIToken
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		return tx.Order("created_at DESC").Find(&rows).Error
	})
	return rows, err
}

// RevokeAPIToken revokes one token.
func RevokeAPIToken(ctx context.Context, p Principal, id int64) error {
	if !p.IsOwnerSession() {
		return errors.New(errors.CodeForbidden, "only the owner's browser session can revoke API tokens")
	}
	return db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		res := tx.Model(&APIToken{}).Where("id = ? AND revoked_at IS NULL", id).Update("revoked_at", time.Now())
		if res.Error != nil {
			return res.Error
		}
		return audit(tx, p, "api_token.revoke", nil, nil, "")
	})
}

// AuthenticateAPIToken resolves a bearer token to a principal.
func AuthenticateAPIToken(ctx context.Context, token string) (*Principal, error) {
	type row struct {
		TokenID   int64
		TenantID  int64
		UserID    int64
		Scopes    StringArray
		ExpiresAt time.Time
		Revoked   bool
	}
	var r row
	if err := db.Get().WithContext(ctx).Raw(`SELECT token_id, tenant_id, user_id, scopes, expires_at, revoked FROM lookup_api_token(decode(?, 'hex'))`, HashHex(token)).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.TokenID == 0 || r.Revoked || time.Now().After(r.ExpiresAt) {
		return nil, errors.New(errors.CodeUnauthorized, "invalid or expired token")
	}
	_ = db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
		return tx.Model(&APIToken{}).Where("id = ?", r.TokenID).Update("last_used_at", time.Now()).Error
	})
	return &Principal{Kind: PrincipalAPIToken, TenantID: r.TenantID, UserID: r.UserID, Scopes: r.Scopes, APITokenID: r.TokenID}, nil
}

// OAuthGrant is one authorized client connection.
type OAuthGrant struct {
	ID         int64 `gorm:"primaryKey"`
	TenantID   int64
	UserID     int64
	ClientID   string
	Scopes     StringArray `gorm:"type:text[]"`
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// TableName follows the singular naming convention.
func (OAuthGrant) TableName() string { return "oauth_grant" }

// OAuthCode is a single-use authorization code.
type OAuthCode struct {
	ID                  int64 `gorm:"primaryKey"`
	TenantID            int64
	GrantID             int64
	CodeHash            []byte
	ClientID            string
	RedirectURI         string `gorm:"column:redirect_uri"`
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
	CreatedAt           time.Time
	ExpiresAt           time.Time
	UsedAt              *time.Time
}

// TableName follows the singular naming convention.
func (OAuthCode) TableName() string { return "oauth_code" }

// OAuthToken is an opaque access or refresh token (hash only).
type OAuthToken struct {
	ID        int64 `gorm:"primaryKey"`
	TenantID  int64
	GrantID   int64
	Kind      string
	TokenHash []byte
	FamilyID  string
	Audience  string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
	RotatedAt *time.Time
}

// TableName follows the singular naming convention.
func (OAuthToken) TableName() string { return "oauth_token" }

// AuthorizeClient records consent and issues an authorization code bound to
// client, redirect URI, resource and PKCE challenge.
func AuthorizeClient(ctx context.Context, p Principal, clientID, redirectURI, resource, challenge, method string, scopes []string) (string, error) {
	if !p.IsOwnerSession() {
		return "", errors.New(errors.CodeForbidden, "consent requires the owner's browser session")
	}
	sc, err := NormalizeScopes(scopes)
	if err != nil {
		return "", errors.New(errors.CodeValidationFailed, err.Error())
	}
	code := NewToken()
	err = db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		g := &OAuthGrant{TenantID: p.TenantID, UserID: p.UserID, ClientID: clientID, Scopes: sc}
		if err := tx.Create(g).Error; err != nil {
			return err
		}
		c := &OAuthCode{TenantID: p.TenantID, GrantID: g.ID, CodeHash: HashToken(code), ClientID: clientID, RedirectURI: redirectURI, Resource: resource, CodeChallenge: challenge, CodeChallengeMethod: method, ExpiresAt: time.Now().Add(AuthCodeLifetime)}
		if err := tx.Create(c).Error; err != nil {
			return err
		}
		return audit(tx, p, "oauth.grant", nil, nil, "")
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// CodeRecord is the consumed authorization code.
type CodeRecord struct {
	TenantID            int64
	GrantID             int64
	ClientID            string
	RedirectURI         string
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
}

// ConsumeAuthCode single-uses a code. A replayed code revokes the grant it belongs to.
func ConsumeAuthCode(ctx context.Context, code string) (*CodeRecord, error) {
	type row struct {
		CodeID              int64
		TenantID            int64
		GrantID             int64
		ClientID            string
		RedirectURI         string
		Resource            string
		CodeChallenge       string
		CodeChallengeMethod string
		ExpiresAt           time.Time
		Used                bool
	}
	var r row
	if err := db.Get().WithContext(ctx).Raw(`SELECT code_id, tenant_id, grant_id, client_id, redirect_uri, resource, code_challenge, code_challenge_method, expires_at, used FROM lookup_oauth_code(decode(?, 'hex'))`, HashHex(code)).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.CodeID == 0 {
		return nil, errors.New(errors.CodeUnauthorized, "invalid_grant")
	}
	var rec *CodeRecord
	replayed := false
	err := db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
		var c OAuthCode
		if err := tx.Clauses(forUpdate).First(&c, r.CodeID).Error; err != nil {
			return errors.New(errors.CodeUnauthorized, "invalid_grant")
		}
		if c.UsedAt != nil {
			// Replay of a consumed code: revoke the whole grant (RFC 6749 §4.1.2).
			// The revocation must commit, so it happens here and the error is returned after.
			now := time.Now()
			if err := tx.Model(&OAuthGrant{}).Where("id = ?", c.GrantID).Update("revoked_at", now).Error; err != nil {
				return err
			}
			if err := tx.Model(&OAuthToken{}).Where("grant_id = ? AND revoked_at IS NULL", c.GrantID).Update("revoked_at", now).Error; err != nil {
				return err
			}
			replayed = true
			return nil
		}
		if time.Now().After(c.ExpiresAt) {
			return errors.New(errors.CodeUnauthorized, "invalid_grant")
		}
		if err := tx.Model(&c).Update("used_at", time.Now()).Error; err != nil {
			return err
		}
		rec = &CodeRecord{TenantID: c.TenantID, GrantID: c.GrantID, ClientID: c.ClientID, RedirectURI: c.RedirectURI, Resource: c.Resource, CodeChallenge: c.CodeChallenge, CodeChallengeMethod: c.CodeChallengeMethod}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if replayed {
		return nil, errors.New(errors.CodeUnauthorized, "invalid_grant")
	}
	return rec, nil
}

// TokenPair is an issued access/refresh pair (plaintext, returned once).
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scopes       []string
}

// IssueTokens creates a new access+refresh pair for a grant in a new family
// (authorization code) or an existing family (refresh rotation).
func IssueTokens(ctx context.Context, tenantID, grantID int64, audience, familyID string) (*TokenPair, error) {
	if familyID == "" {
		familyID = NewToken()
	}
	access := "tmpa_" + NewToken()
	refresh := "tmpr_" + NewToken()
	var scopes StringArray
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		var g OAuthGrant
		if err := tx.First(&g, grantID).Error; err != nil {
			return err
		}
		if g.RevokedAt != nil {
			return errors.New(errors.CodeUnauthorized, "invalid_grant")
		}
		scopes = g.Scopes
		now := time.Now()
		rows := []OAuthToken{
			{TenantID: tenantID, GrantID: grantID, Kind: "access", TokenHash: HashToken(access), FamilyID: familyID, Audience: audience, ExpiresAt: now.Add(AccessTokenLifetime)},
			{TenantID: tenantID, GrantID: grantID, Kind: "refresh", TokenHash: HashToken(refresh), FamilyID: familyID, Audience: audience, ExpiresAt: now.Add(RefreshTokenLifetime)},
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		return tx.Model(&g).Update("last_used_at", now).Error
	})
	if err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(AccessTokenLifetime.Seconds()), Scopes: scopes}, nil
}

type bearerRow struct {
	TokenID      int64
	TenantID     int64
	GrantID      int64
	Kind         string
	FamilyID     string
	Audience     string
	ExpiresAt    time.Time
	Revoked      bool
	Rotated      bool
	UserID       int64
	ClientID     string
	Scopes       StringArray
	GrantRevoked bool
}

func lookupBearer(ctx context.Context, token string) (*bearerRow, error) {
	var r bearerRow
	err := db.Get().WithContext(ctx).Raw(`SELECT token_id, tenant_id, grant_id, kind, family_id, audience, expires_at, revoked, rotated, user_id, client_id, scopes, grant_revoked FROM lookup_oauth_token(decode(?, 'hex'))`, HashHex(token)).Scan(&r).Error
	if err != nil {
		return nil, err
	}
	if r.TokenID == 0 {
		return nil, errors.New(errors.CodeUnauthorized, "invalid_token")
	}
	return &r, nil
}

// AuthenticateAccessToken validates an OAuth access token for the given audience.
func AuthenticateAccessToken(ctx context.Context, token, audience string) (*Principal, time.Time, error) {
	r, err := lookupBearer(ctx, token)
	if err != nil {
		return nil, time.Time{}, err
	}
	if r.Kind != "access" || r.Revoked || r.GrantRevoked || time.Now().After(r.ExpiresAt) {
		return nil, time.Time{}, errors.New(errors.CodeUnauthorized, "invalid_token")
	}
	if r.Audience != audience {
		return nil, time.Time{}, errors.New(errors.CodeUnauthorized, "invalid_token: wrong audience")
	}
	name := ""
	if c, err := GetOAuthClient(ctx, r.ClientID); err == nil {
		name = c.Name
	}
	return &Principal{Kind: PrincipalOAuth, TenantID: r.TenantID, UserID: r.UserID, Scopes: r.Scopes, OAuthGrantID: r.GrantID, ClientID: r.ClientID, ClientName: name}, r.ExpiresAt, nil
}

// RefreshTokens rotates a refresh token. Reuse of a rotated token revokes the family.
func RefreshTokens(ctx context.Context, refresh, clientID, audience string) (*TokenPair, error) {
	r, err := lookupBearer(ctx, refresh)
	if err != nil {
		return nil, err
	}
	if r.Kind != "refresh" || r.ClientID != clientID || r.Audience != audience {
		return nil, errors.New(errors.CodeUnauthorized, "invalid_grant")
	}
	if r.Revoked || r.GrantRevoked || time.Now().After(r.ExpiresAt) {
		return nil, errors.New(errors.CodeUnauthorized, "invalid_grant")
	}
	if r.Rotated {
		_ = db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
			now := time.Now()
			return tx.Model(&OAuthToken{}).Where("family_id = ? AND revoked_at IS NULL", r.FamilyID).Update("revoked_at", now).Error
		})
		return nil, errors.New(errors.CodeUnauthorized, "invalid_grant: refresh token reuse detected; the token family was revoked")
	}
	err = db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&OAuthToken{}).Where("id = ?", r.TokenID).Update("rotated_at", now).Error; err != nil {
			return err
		}
		// Expire the family's outstanding access tokens; the client gets a fresh one.
		return tx.Model(&OAuthToken{}).Where("family_id = ? AND kind = 'access' AND revoked_at IS NULL", r.FamilyID).Update("revoked_at", now).Error
	})
	if err != nil {
		return nil, err
	}
	return IssueTokens(ctx, r.TenantID, r.GrantID, audience, r.FamilyID)
}

// RevokeOAuthToken implements RFC 7009 revocation for either token kind.
func RevokeOAuthToken(ctx context.Context, token, clientID string) error {
	r, err := lookupBearer(ctx, token)
	if err != nil {
		return nil // RFC 7009: invalid tokens return 200
	}
	if r.ClientID != clientID {
		return nil
	}
	return db.WithTenant(ctx, r.TenantID, func(tx *gorm.DB) error {
		return tx.Model(&OAuthToken{}).Where("family_id = ? AND revoked_at IS NULL", r.FamilyID).Update("revoked_at", time.Now()).Error
	})
}

// ConnectionInfo is an owner-visible connection.
type ConnectionInfo struct {
	ID         int64      `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ListConnections lists a tenant's client grants.
func ListConnections(ctx context.Context, tenantID int64) ([]ConnectionInfo, error) {
	var out []ConnectionInfo
	err := db.WithTenant(ctx, tenantID, func(tx *gorm.DB) error {
		var grants []OAuthGrant
		if err := tx.Order("created_at DESC").Limit(200).Find(&grants).Error; err != nil {
			return err
		}
		for _, g := range grants {
			name := g.ClientID
			var c OAuthClient
			if tx.Session(&gorm.Session{NewDB: true}).First(&c, "id = ?", g.ClientID).Error == nil {
				name = c.Name
			}
			out = append(out, ConnectionInfo{ID: g.ID, ClientID: g.ClientID, ClientName: name, Scopes: g.Scopes, CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt, RevokedAt: g.RevokedAt})
		}
		return nil
	})
	return out, err
}

// RevokeConnection revokes a grant and every token issued under it, immediately.
func RevokeConnection(ctx context.Context, p Principal, grantID int64) error {
	if !p.IsOwnerSession() {
		return errors.New(errors.CodeForbidden, "only the owner's browser session can revoke connections")
	}
	return db.WithTenant(ctx, p.TenantID, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&OAuthGrant{}).Where("id = ? AND revoked_at IS NULL", grantID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&OAuthToken{}).Where("grant_id = ? AND revoked_at IS NULL", grantID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		return audit(tx, p, "oauth.revoke", nil, nil, "")
	})
}
