package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/auth"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func (s *Server) verifyAuthCredentialConnection(ctx context.Context, provider, credentialID string) (*auth.ConnectionStatus, error) {
	return s.verifyAuthCredentialConnectionForAccount(ctx, "", provider, credentialID)
}

func (s *Server) verifyAuthCredentialConnectionForAccount(ctx context.Context, accountScopeID, provider, credentialID string) (*auth.ConnectionStatus, error) {
	if s == nil || s.auth == nil {
		return nil, nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	if provider == "" || credentialID == "" {
		return nil, nil
	}

	record, found, err := s.auth.GetCredentialRecordForAccount(accountScopeID, provider, credentialID)
	if err != nil {
		return &auth.ConnectionStatus{
			Connected:  false,
			Method:     strings.TrimSpace(record.Type),
			Message:    err.Error(),
			VerifiedAt: time.Now().UnixMilli(),
		}, nil
	}
	if !found {
		return &auth.ConnectionStatus{
			Connected:  false,
			Method:     "unknown",
			Message:    "credential not found",
			VerifiedAt: time.Now().UnixMilli(),
		}, nil
	}

	credential := provideriface.AuthCredential{
		ID:           record.ID,
		Provider:     record.Provider,
		Type:         record.Type,
		Label:        record.Label,
		Tags:         append([]string(nil), record.Tags...),
		APIKey:       record.APIKey,
		AccessToken:  record.AccessToken,
		RefreshToken: record.RefreshToken,
		AccountID:    record.AccountID,
		ExpiresAt:    record.ExpiresAt,
	}
	status, err := s.verifyCredentialMaterialForAccount(ctx, accountScopeID, credential)
	if err != nil {
		return nil, err
	}
	if status == nil {
		return nil, nil
	}
	if _, event, persistErr := s.auth.UpdateCredentialConnectionForAccount(accountScopeID, provider, credentialID, status); persistErr != nil {
		return nil, persistErr
	} else if event != nil {
		s.hub.Publish(*event)
	}
	return status, nil
}

func (s *Server) verifyCredentialMaterialForAccount(ctx context.Context, accountScopeID string, credential provideriface.AuthCredential) (*auth.ConnectionStatus, error) {
	if s == nil || s.providers == nil {
		return nil, nil
	}
	provider := strings.ToLower(strings.TrimSpace(credential.Provider))
	if provider == "" {
		return nil, nil
	}
	adapter, ok := s.providers.Get(provider)
	if !ok || adapter == nil {
		return nil, nil
	}
	verifier, ok := adapter.(provideriface.AuthVerifier)
	if !ok {
		return nil, nil
	}

	credential.Provider = provider
	credential.ID = strings.ToLower(strings.TrimSpace(credential.ID))
	if s.auth != nil && strings.TrimSpace(accountScopeID) != "" && credential.ID != "" {
		if record, found, err := s.auth.GetCredentialRecordForAccount(accountScopeID, provider, credential.ID); err != nil {
			return &auth.ConnectionStatus{
				Connected:  false,
				Method:     strings.TrimSpace(credential.Type),
				Message:    err.Error(),
				VerifiedAt: time.Now().UnixMilli(),
			}, nil
		} else if found {
			if strings.TrimSpace(credential.Type) == "" {
				credential.Type = record.Type
			}
			if strings.TrimSpace(credential.Label) == "" {
				credential.Label = record.Label
			}
			if credential.Tags == nil {
				credential.Tags = append([]string(nil), record.Tags...)
			}
			if strings.TrimSpace(credential.APIKey) == "" {
				credential.APIKey = record.APIKey
			}
			if strings.TrimSpace(credential.AccessToken) == "" {
				credential.AccessToken = record.AccessToken
			}
			if strings.TrimSpace(credential.RefreshToken) == "" {
				credential.RefreshToken = record.RefreshToken
			}
			if strings.TrimSpace(credential.AccountID) == "" {
				credential.AccountID = record.AccountID
			}
			if credential.ExpiresAt <= 0 {
				credential.ExpiresAt = record.ExpiresAt
			}
		}
	}

	verifyCtx := ctx
	if verifyCtx == nil {
		verifyCtx = context.Background()
	}
	if _, hasDeadline := verifyCtx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		verifyCtx, cancel = context.WithTimeout(verifyCtx, 8*time.Second)
		defer cancel()
	}

	result, verifyErr := verifier.VerifyCredential(verifyCtx, credential)
	status := &auth.ConnectionStatus{
		Connected:  result.Connected,
		Method:     strings.TrimSpace(result.Method),
		Message:    strings.TrimSpace(result.Message),
		VerifiedAt: time.Now().UnixMilli(),
	}
	if status.Method == "" {
		status.Method = strings.TrimSpace(credential.Type)
	}
	if verifyErr != nil {
		status.Connected = false
		if status.Message == "" {
			status.Message = verifyErr.Error()
		}
	}
	return status, nil
}

func authCredentialVerificationAccepted(connection *auth.ConnectionStatus) bool {
	return connection == nil || connection.Connected
}

func authCredentialVerificationError(connection *auth.ConnectionStatus) error {
	message := "credential verification failed"
	if connection != nil && strings.TrimSpace(connection.Message) != "" {
		message = strings.TrimSpace(connection.Message)
	}
	return errors.New(message)
}
