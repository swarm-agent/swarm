package identity

import (
	"errors"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PrincipalTypeUser             = "user"
	AccountScopeSourceSession     = "session"
	AccountScopeSourceServerState = "server-state"
	AccountScopeSourceSelection   = "account-selection"
)

var ErrPrincipalRequired = errors.New("trusted principal is required")

type Principal struct {
	Type               string                         `json:"type"`
	UserID             string                         `json:"user_id"`
	AccountScopeID     string                         `json:"account_scope_id"`
	SessionID          string                         `json:"session_id,omitempty"`
	AuthProvider       string                         `json:"auth_provider,omitempty"`
	AuthSubject        string                         `json:"auth_subject,omitempty"`
	AccountScopeSource string                         `json:"account_scope_source"`
	User               pebblestore.UserRecord         `json:"user,omitempty"`
	AccountScope       pebblestore.AccountScopeRecord `json:"account_scope,omitempty"`
	AccountUser        pebblestore.AccountUserRecord  `json:"account_user,omitempty"`
	TokenExpires       time.Time                      `json:"token_expires_at,omitempty"`
}

func (p Principal) Valid() bool {
	return strings.TrimSpace(p.Type) == PrincipalTypeUser &&
		strings.TrimSpace(p.UserID) != "" &&
		strings.TrimSpace(p.AccountScopeID) != ""
}

func (p Principal) ActorContext() ActorContext {
	return ActorContext{
		Principal:      p,
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		User:           p.User,
		AccountScope:   p.AccountScope,
		AccountUser:    p.AccountUser,
		TokenExpires:   p.TokenExpires,
	}
}

func PrincipalFromActor(actor ActorContext) (Principal, error) {
	principal := actor.Principal
	if !principal.Valid() {
		principal = Principal{
			Type:               PrincipalTypeUser,
			UserID:             actor.UserID,
			AccountScopeID:     actor.AccountScopeID,
			AuthProvider:       actor.User.AuthProvider,
			AuthSubject:        actor.User.AuthSubject,
			AccountScopeSource: AccountScopeSourceServerState,
			User:               actor.User,
			AccountScope:       actor.AccountScope,
			AccountUser:        actor.AccountUser,
			TokenExpires:       actor.TokenExpires,
		}
	}
	if !principal.Valid() {
		return Principal{}, ErrPrincipalRequired
	}
	return principal, nil
}
