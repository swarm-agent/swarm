package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Service struct {
	authStore *pebblestore.ClientAuthStore
	events    *pebblestore.EventLog
}

type AttachStatus struct {
	Configured bool   `json:"configured"`
	TokenHint  string `json:"token_hint,omitempty"`
	CreatedAt  int64  `json:"created_at,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

func NewService(authStore *pebblestore.ClientAuthStore, events *pebblestore.EventLog) *Service {
	return &Service{
		authStore: authStore,
		events:    events,
	}
}

func (s *Service) EnsureAttachAuth() (AttachStatus, error) {
	record, err := s.authStore.EnsureAttachToken()
	if err != nil {
		return AttachStatus{}, err
	}
	return statusFromRecord(record), nil
}

func (s *Service) AttachStatus() (AttachStatus, error) {
	record, ok, err := s.authStore.GetAttachAuth()
	if err != nil {
		return AttachStatus{}, err
	}
	if !ok || strings.TrimSpace(record.Token) == "" {
		return AttachStatus{Configured: false}, nil
	}
	return statusFromRecord(record), nil
}

func (s *Service) RevealAttachToken() (string, error) {
	record, ok, err := s.authStore.GetAttachAuth()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("attach token is not configured")
	}
	return record.Token, nil
}

func (s *Service) RotateAttachToken() (AttachStatus, *pebblestore.EventEnvelope, error) {
	current, ok, err := s.authStore.GetAttachAuth()
	if err != nil {
		return AttachStatus{}, nil, err
	}
	createdAt := int64(0)
	if ok {
		createdAt = current.CreatedAt
	}
	record, err := s.authStore.RotateAttachToken(createdAt)
	if err != nil {
		return AttachStatus{}, nil, err
	}
	status := statusFromRecord(record)
	payload, err := json.Marshal(map[string]any{
		"configured": true,
		"token_hint": status.TokenHint,
		"updated_at": status.UpdatedAt,
	})
	if err != nil {
		return AttachStatus{}, nil, err
	}
	env, err := s.events.Append("system:security", "security.attach.rotated", "attach", payload, "", "")
	if err != nil {
		return AttachStatus{}, nil, err
	}
	return status, &env, nil
}

func (s *Service) ValidateAttachToken(rawToken string) (bool, error) {
	provided := strings.TrimSpace(rawToken)
	if provided == "" {
		return false, nil
	}
	record, ok, err := s.authStore.GetAttachAuth()
	if err != nil {
		return false, err
	}
	if !ok || strings.TrimSpace(record.Token) == "" {
		return false, nil
	}
	expected := record.Token
	if len(provided) != len(expected) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1, nil
}

func (s *Service) AuditDenied(method, path, remoteAddr, reason, suppliedToken string) {
	if s.events == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"method":      sanitizeString(method),
		"path":        sanitizeString(path),
		"remote_addr": sanitizeString(remoteAddr),
		"reason":      sanitizeString(reason),
		"token_hint":  maskSecret(suppliedToken),
	})
	if err != nil {
		return
	}
	_, _ = s.events.Append("system:security", "security.attach.denied", "attach", payload, "", "")
}

func (s *Service) CreateScopedToken(name string, scopes []string, accountScopeID, userID string, expiresIn time.Duration) (string, pebblestore.ScopedTokenRecord, error) {
	if s == nil || s.authStore == nil {
		return "", pebblestore.ScopedTokenRecord{}, errors.New("auth store not configured")
	}
	rawSecret, err := pebblestore.GenerateToken(32)
	if err != nil {
		return "", pebblestore.ScopedTokenRecord{}, fmt.Errorf("generate scoped token: %w", err)
	}
	token := "swk_" + rawSecret
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	tokenIDBytes, err := pebblestore.GenerateToken(8)
	if err != nil {
		return "", pebblestore.ScopedTokenRecord{}, fmt.Errorf("generate token id: %w", err)
	}
	tokenID := "tok_" + tokenIDBytes

	tokenHint := token[:8] + "..." + token[len(token)-4:]

	now := time.Now().UnixMilli()
	var expiresAt int64
	if expiresIn != 0 {
		expiresAt = now + expiresIn.Milliseconds()
	}

	cleanScopes := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		sc = strings.TrimSpace(sc)
		if sc != "" {
			cleanScopes = append(cleanScopes, sc)
		}
	}
	if len(cleanScopes) == 0 {
		cleanScopes = []string{"*"}
	}

	record := pebblestore.ScopedTokenRecord{
		ID:             tokenID,
		Name:           strings.TrimSpace(name),
		TokenHash:      tokenHash,
		TokenHint:      tokenHint,
		Scopes:         cleanScopes,
		AccountScopeID: accountScopeID,
		UserID:         userID,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
		Revoked:        false,
	}

	if err := s.authStore.PutScopedToken(record); err != nil {
		return "", pebblestore.ScopedTokenRecord{}, err
	}

	return token, record, nil
}

func (s *Service) ValidateScopedToken(rawToken string) (*pebblestore.ScopedTokenRecord, error) {
	if s == nil || s.authStore == nil {
		return nil, nil
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, nil
	}
	hashBytes := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hashBytes[:])

	record, ok, err := s.authStore.GetScopedTokenByHash(tokenHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if record.Revoked {
		return nil, errors.New("token has been revoked")
	}
	if record.ExpiresAt > 0 && time.Now().UnixMilli() >= record.ExpiresAt {
		return nil, errors.New("token has expired")
	}

	_ = s.authStore.UpdateScopedTokenLastUsed(record.AccountScopeID, record.ID, time.Now().UnixMilli())

	return &record, nil
}

func (s *Service) ListScopedTokens(accountScopeID string) ([]pebblestore.ScopedTokenRecord, error) {
	if s == nil || s.authStore == nil {
		return nil, errors.New("auth store not configured")
	}
	return s.authStore.ListScopedTokens(accountScopeID)
}

func (s *Service) RevokeScopedToken(accountScopeID, tokenID string) (pebblestore.ScopedTokenRecord, error) {
	if s == nil || s.authStore == nil {
		return pebblestore.ScopedTokenRecord{}, errors.New("auth store not configured")
	}
	return s.authStore.RevokeScopedToken(accountScopeID, tokenID)
}

func (s *Service) DeleteScopedToken(accountScopeID, tokenID string) error {
	if s == nil || s.authStore == nil {
		return errors.New("auth store not configured")
	}
	return s.authStore.DeleteScopedToken(accountScopeID, tokenID)
}

func statusFromRecord(record pebblestore.AttachAuthRecord) AttachStatus {
	return AttachStatus{
		Configured: true,
		TokenHint:  maskSecret(record.Token),
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
}

func sanitizeString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "********"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}
