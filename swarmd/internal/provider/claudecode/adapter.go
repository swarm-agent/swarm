package claudecode

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Adapter reports Claude Code availability for the calling account. The
// machine-level CLI sign-in belongs to one person, so each Swarm account must
// opt in with an active `cli` credential record before the provider is ready.
// That record carries no secret material.
type Adapter struct {
	authStore *pebblestore.AuthStore
	cli       *CLI
}

func NewAdapter(authStore *pebblestore.AuthStore, cli *CLI) *Adapter {
	if cli == nil {
		cli = NewCLI()
	}
	return &Adapter{authStore: authStore, cli: cli}
}

func (a *Adapter) ID() string {
	return ProviderID
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup(ProviderID)
	status := provideriface.Status{
		ID:              ProviderID,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     authMethods(),
	}
	if a == nil || a.authStore == nil {
		status.Reason = "claude code auth store is not configured"
		return status, nil
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		status.Reason = "product identity is required"
		return status, nil
	}
	_, enabled, err := activeCLICredential(a.authStore, principal.AccountScopeID)
	if err != nil {
		return provideriface.Status{}, err
	}
	cliStatus := a.cli.Status(ctx)
	if !enabled {
		reason := "Claude Code is not enabled for this account yet (needs an active Claude Code CLI login credential)"
		if !cliStatus.Ready {
			reason = cliStatus.Reason + "; " + reason
		}
		status.Reason = reason
		return status, nil
	}
	status.Ready = cliStatus.Ready
	status.Reason = cliStatus.Reason
	return status, nil
}

// VerifyCredential confirms the local CLI is installed and signed in. It never
// receives or returns credential material.
func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	if !strings.EqualFold(strings.TrimSpace(credential.Type), pebblestore.AuthTypeCLI) {
		return provideriface.AuthVerification{Connected: false, Method: credential.Type}, errors.New("claude code supports only the local CLI login credential type")
	}
	if a == nil || a.cli == nil {
		return provideriface.AuthVerification{Connected: false, Method: pebblestore.AuthTypeCLI}, errors.New("claude code CLI is not configured")
	}
	cliStatus := a.cli.Refresh(ctx)
	if !cliStatus.Ready {
		return provideriface.AuthVerification{Connected: false, Method: pebblestore.AuthTypeCLI, Message: cliStatus.Reason}, errors.New(cliStatus.Reason)
	}
	return provideriface.AuthVerification{Connected: true, Method: pebblestore.AuthTypeCLI, Message: cliStatus.Reason}, nil
}

func activeCLICredential(authStore *pebblestore.AuthStore, accountScopeID string) (pebblestore.AuthCredentialRecord, bool, error) {
	record, ok, err := authStore.GetActiveCredentialForAccount(accountScopeID, ProviderID)
	if err != nil || !ok {
		return pebblestore.AuthCredentialRecord{}, false, err
	}
	if !strings.EqualFold(strings.TrimSpace(record.Type), pebblestore.AuthTypeCLI) {
		return pebblestore.AuthCredentialRecord{}, false, nil
	}
	return record, true, nil
}

func authMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{{
		ID:             "cli",
		Label:          "Claude Code CLI login",
		CredentialType: pebblestore.AuthTypeCLI,
		Description:    "Use the Claude Code CLI installed on this machine. Sign in yourself with `claude auth login` as the user that runs the Swarm daemon; Swarm never sees or stores Claude credentials.",
	}}
}
