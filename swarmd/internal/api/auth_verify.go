package api

import (
	"context"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/auth"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func (s *Server) verifyAuthCredentialConnection(ctx context.Context, provider, credentialID string) (*auth.ConnectionStatus, error) {
	if s == nil || s.auth == nil || s.providers == nil {
		return nil, nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	if provider == "" || credentialID == "" {
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

	record, found, err := s.auth.GetCredentialRecord(provider, credentialID)
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

	verifyCtx := ctx
	if verifyCtx == nil {
		verifyCtx = context.Background()
	}
	if _, hasDeadline := verifyCtx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		verifyCtx, cancel = context.WithTimeout(verifyCtx, 8*time.Second)
		defer cancel()
	}

	result, verifyErr := verifier.VerifyCredential(verifyCtx, provideriface.AuthCredential{
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
	})

	status := &auth.ConnectionStatus{
		Connected:  result.Connected,
		Method:     strings.TrimSpace(result.Method),
		Message:    strings.TrimSpace(result.Message),
		VerifiedAt: time.Now().UnixMilli(),
	}
	if status.Method == "" {
		status.Method = strings.TrimSpace(record.Type)
	}
	if verifyErr != nil {
		status.Connected = false
		if status.Message == "" {
			status.Message = verifyErr.Error()
		}
	}
	if _, event, persistErr := s.auth.UpdateCredentialConnection(provider, credentialID, status); persistErr != nil {
		return nil, persistErr
	} else if event != nil {
		s.hub.Publish(*event)
	}
	return status, nil
}
