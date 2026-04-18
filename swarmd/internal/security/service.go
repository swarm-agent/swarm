package security

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"

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
