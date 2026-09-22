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

// Roles, and what each may do. They map onto the scopes every handler already
// checks, so a role is enforced by the same code that enforces an API token's
// scopes rather than by a second set of rules that could disagree with it.
const (
	RoleOwner  = "owner"  // content, plus the organization itself
	RoleEditor = "editor" // content
	RoleViewer = "viewer" // reading
)

// RoleScopes is what a member of this role may do with content. An unknown
// role gets nothing: a row that does not match a role we know about should
// fail closed, not open.
func RoleScopes(role string) []string {
	switch role {
	case RoleOwner, RoleEditor:
		return AllScopes
	case RoleViewer:
		return []string{ScopeRead}
	default:
		return nil
	}
}

// RoleAdmin reports whether a role may change the organization itself: its
// members, its settings, its existence. Editors and viewers may not.
func RoleAdmin(role string) bool { return role == RoleOwner }

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
	Role         string // the member's role, for owner-session principals
	AuthorName   string // unverified display name supplied by an anonymous editor
}

// Can reports whether the principal holds a scope.
// A browser session used to short-circuit this: a session meant the sole owner
// of the organization, so it could do anything. With roles it cannot, because
// a viewer holds a session too and must not inherit write and delete from the
// kind of credential they signed in with.
func (p Principal) Can(scope string) bool {
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
