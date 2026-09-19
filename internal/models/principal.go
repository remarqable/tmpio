package models

import (
	"fmt"
	"strings"

	"github.com/remarqable/tmpio/internal/platform/i18n"
)

// Principal kinds.
const (
	PrincipalOwner     = "owner"        // browser session of an organization owner
	PrincipalAPIToken  = "api_token"    // owner-created development API token
	PrincipalOAuth     = "oauth_client" // AI client connected through tmp OAuth
	PrincipalShareLink = "share_link"   // holder of a secret document link
	PrincipalAnonymous = "anonymous"
)

// OAuth scopes.
const (
	ScopeRead   = "content:read"
	ScopeWrite  = "content:write"
	ScopeDelete = "content:delete"
)

// AllScopes lists the supported scopes in canonical order.
var AllScopes = []string{ScopeRead, ScopeWrite, ScopeDelete}

// Principal is the authenticated actor for one request. It binds tenant,
// user, client and scope set; a share principal is additionally bound to one entry.
type Principal struct {
	Kind         string
	TenantID     int64
	UserID       int64
	Scopes       []string
	SessionID    int64
	APITokenID   int64
	OAuthGrantID int64
	ClientID     string
	ClientName   string
	ShareGrantID int64
	ShareEntryID int64
	AuthorName   string // unverified display name supplied by an anonymous editor
}

// Can reports whether the principal holds a scope. Owners hold every scope.
func (p Principal) Can(scope string) bool {
	if p.Kind == PrincipalOwner {
		return true
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// IsOwnerSession reports whether this is an owner browser session (management rights).
func (p Principal) IsOwnerSession() bool { return p.Kind == PrincipalOwner }

// IsShare reports whether this principal is a secret-link holder.
func (p Principal) IsShare() bool { return p.Kind == PrincipalShareLink }

// Key identifies the principal for idempotency receipts.
func (p Principal) Key() string {
	switch p.Kind {
	case PrincipalOwner:
		return fmt.Sprintf("user:%d", p.UserID)
	case PrincipalAPIToken:
		return fmt.Sprintf("api_token:%d", p.APITokenID)
	case PrincipalOAuth:
		return fmt.Sprintf("oauth_grant:%d", p.OAuthGrantID)
	case PrincipalShareLink:
		return fmt.Sprintf("share:%d", p.ShareGrantID)
	}
	return "anonymous"
}

// ActorLabel is the history label shown to owners.
func (p Principal) ActorLabel() string {
	switch p.Kind {
	case PrincipalOwner:
		return i18n.T("en", "actor.owner")
	case PrincipalAPIToken:
		return i18n.T("en", "actor.api_token")
	case PrincipalOAuth:
		if p.ClientName != "" {
			return p.ClientName
		}
		return i18n.T("en", "actor.client")
	case PrincipalShareLink:
		return i18n.T("en", "actor.share_link")
	}
	return i18n.T("en", "actor.anonymous")
}

// NormalizeScopes validates and orders a requested scope set. Write and delete imply read.
func NormalizeScopes(requested []string) ([]string, error) {
	set := map[string]bool{}
	for _, s := range requested {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		switch s {
		case ScopeRead, ScopeWrite, ScopeDelete:
			set[s] = true
		default:
			return nil, fmt.Errorf("unsupported scope %q", s)
		}
	}
	if set[ScopeWrite] || set[ScopeDelete] {
		set[ScopeRead] = true
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("at least one scope is required")
	}
	var out []string
	for _, s := range AllScopes {
		if set[s] {
			out = append(out, s)
		}
	}
	return out, nil
}
