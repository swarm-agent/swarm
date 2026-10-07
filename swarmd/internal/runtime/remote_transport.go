package runtime

import (
	"errors"

	identityruntime "swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/security"
)

// remoteTokenIssuer mints the remote transport's device token for the
// machine owner (the current identity selection), never for another account.
type remoteTokenIssuer struct {
	security   *security.Service
	identities *identityruntime.SessionService
}

func (i remoteTokenIssuer) Mint(name string, scopes []string) (string, string, error) {
	if i.security == nil || i.identities == nil {
		return "", "", errors.New("token issuer is not configured")
	}
	actor, err := i.identities.ActorForCurrentSelection()
	if err != nil {
		return "", "", err
	}
	if actor.UserID == "" || actor.AccountScopeID == "" {
		return "", "", errors.New("complete Swarm setup before enabling remote access")
	}
	token, record, err := i.security.CreateScopedToken(name, scopes, actor.AccountScopeID, actor.UserID, 0, "", "")
	if err != nil {
		return "", "", err
	}
	return token, record.ID, nil
}

func (i remoteTokenIssuer) Revoke(id string) error {
	if i.security == nil || i.identities == nil {
		return errors.New("token issuer is not configured")
	}
	actor, err := i.identities.ActorForCurrentSelection()
	if err != nil {
		return err
	}
	_, err = i.security.RevokeScopedToken(actor.AccountScopeID, id)
	return err
}
