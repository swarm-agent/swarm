package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// acceptFirstOnboardingProviderCredential is the only first-run API-key path.
// It synchronously verifies the submitted credential, activates it only after
// verification succeeds, hydrates onboarding agent/model defaults, and only
// then returns to the desktop client.
func (s *Server) acceptFirstOnboardingProviderCredential(ctx context.Context, principal identity.Principal, req onboardingProviderCredentialRequest) (auth.CredentialStatus, error) {
	if s == nil || s.auth == nil {
		return auth.CredentialStatus{}, errors.New("auth service not configured")
	}
	if !principal.Valid() {
		return auth.CredentialStatus{}, identity.ErrProductIdentityRequired
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		return auth.CredentialStatus{}, errors.New("provider is required")
	}

	if err := s.requireFirstOnboardingProviderHydrationState(accountScopeID); err != nil {
		return auth.CredentialStatus{}, err
	}

	input := auth.CredentialUpsertInput{
		Provider:       provider,
		AccountScopeID: accountScopeID,
		Type:           req.Type,
		Label:          req.Label,
		Tags:           append([]string(nil), req.Tags...),
		APIKey:         req.APIKey,
		AccessToken:    req.AccessToken,
		RefreshToken:   req.RefreshToken,
		ExpiresAt:      req.ExpiresAt,
		AccountID:      req.AccountID,
		Active:         false,
	}

	connection, verifyErr := s.verifyCredentialMaterialForAccount(ctx, accountScopeID, provideriface.AuthCredential{
		Provider:     provider,
		Type:         input.Type,
		Label:        input.Label,
		Tags:         append([]string(nil), input.Tags...),
		APIKey:       input.APIKey,
		AccessToken:  input.AccessToken,
		RefreshToken: input.RefreshToken,
		ExpiresAt:    input.ExpiresAt,
		AccountID:    input.AccountID,
	})
	if verifyErr != nil {
		return auth.CredentialStatus{}, verifyErr
	}
	if !authCredentialVerificationAccepted(connection) {
		return auth.CredentialStatus{}, authCredentialVerificationError(connection)
	}

	status, event, err := s.auth.UpsertCredential(input)
	if err != nil {
		return auth.CredentialStatus{}, err
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	if connection != nil {
		updated, updateEvent, updateErr := s.auth.UpdateCredentialConnectionForAccount(accountScopeID, provider, status.ID, connection)
		if updateErr != nil {
			_, _, _ = s.auth.DeleteCredentialForAccount(accountScopeID, provider, status.ID)
			return auth.CredentialStatus{}, updateErr
		}
		status = updated
		if updateEvent != nil && s.hub != nil {
			s.hub.Publish(*updateEvent)
		}
	}

	status, event, err = s.auth.SetActiveCredentialForAccount(accountScopeID, provider, status.ID)
	if err != nil {
		_, _, _ = s.auth.DeleteCredentialForAccount(accountScopeID, provider, status.ID)
		return auth.CredentialStatus{}, err
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	status.Connection = connection

	autoDefaults, defaultsErr := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(accountScopeID, principal.UserID, provider)
	if defaultsErr != nil {
		_, _, _ = s.auth.DeleteCredentialForAccount(accountScopeID, provider, status.ID)
		rollbackOnboardingAgentHydration(s, accountScopeID)
		return auth.CredentialStatus{}, fmt.Errorf("hydrate onboarding provider defaults: %w", defaultsErr)
	}
	if autoDefaults == nil || !autoDefaults.Applied {
		_, _, _ = s.auth.DeleteCredentialForAccount(accountScopeID, provider, status.ID)
		rollbackOnboardingAgentHydration(s, accountScopeID)
		return auth.CredentialStatus{}, errors.New("onboarding provider defaults were not hydrated")
	}
	status.AutoDefaults = autoDefaults
	return status, nil
}

func rollbackOnboardingAgentHydration(s *Server, accountScopeID string) {
	if s == nil || s.agents == nil {
		return
	}
	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return
	}
	for _, profile := range state.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), "memory") {
			continue
		}
		_, _, _, _ = s.agents.DeleteForAccount(accountScopeID, profile.Name)
	}
}

func (s *Server) requireFirstOnboardingProviderHydrationState(accountScopeID string) error {
	firstRun, credentialCount, agentCount, err := s.firstOnboardingProviderHydrationState(accountScopeID)
	if err != nil {
		return err
	}
	if !firstRun {
		if credentialCount != 0 {
			return fmt.Errorf("onboarding provider credential requires zero existing credentials; found %d", credentialCount)
		}
		return fmt.Errorf("onboarding provider credential requires zero existing agents; found %d", agentCount)
	}
	return nil
}

func (s *Server) firstOnboardingProviderHydrationState(accountScopeID string) (bool, int, int, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return false, 0, 0, identity.ErrProductIdentityRequired
	}
	if s == nil || s.auth == nil {
		return false, 0, 0, errors.New("auth service not configured")
	}
	credentials, err := s.auth.ListCredentialsForAccount(accountScopeID, "", "", 200)
	if err != nil {
		return false, 0, 0, fmt.Errorf("read onboarding credentials: %w", err)
	}
	if s.agents == nil {
		return false, credentials.Total, 0, errors.New("agent service not configured")
	}
	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return false, credentials.Total, 0, fmt.Errorf("read onboarding agents: %w", err)
	}
	agentCount := len(state.Profiles)
	return credentials.Total == 0 && agentCount == 0, credentials.Total, agentCount, nil
}
