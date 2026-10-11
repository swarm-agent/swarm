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

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// maxScopedTokenChainDepth bounds the walk from a token up through the keys
// that issued it, so a corrupt or cyclic parent link cannot loop forever.
const maxScopedTokenChainDepth = 16

type Service struct {
	authStore *pebblestore.ClientAuthStore
	events    *pebblestore.EventLog
	signals   *signals.Emitter
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

func (s *Service) CreateScopedToken(name string, scopes []string, accountScopeID, userID string, expiresIn time.Duration, workerID, workerName string) (string, pebblestore.ScopedTokenRecord, error) {
	return s.CreateScopedTokenUnder(nil, name, scopes, accountScopeID, userID, expiresIn, workerID, workerName)
}

// CreateScopedTokenUnder mints a token on behalf of the scoped token parent
// (nil: the owner). A token never outlives its parent: its lifetime is capped
// at the parent's, and it stops validating once the parent is revoked, expired
// or deleted.
func (s *Service) CreateScopedTokenUnder(parent *pebblestore.ScopedTokenRecord, name string, scopes []string, accountScopeID, userID string, expiresIn time.Duration, workerID, workerName string) (string, pebblestore.ScopedTokenRecord, error) {
	return s.createScopedTokenRecord(parent, name, scopes, accountScopeID, userID, expiresIn, func(record *pebblestore.ScopedTokenRecord) {
		record.WorkerID = strings.TrimSpace(workerID)
		record.WorkerName = strings.TrimSpace(workerName)
	})
}

// CreateAgentBoundToken mints a session token limited to one sealed agent's
// sessions. The caller must have checked the agent is sealed.
func (s *Service) CreateAgentBoundToken(name, accountScopeID, userID string, expiresIn time.Duration, agentName string, messagesPerMinute, sessionsPerHour int) (string, pebblestore.ScopedTokenRecord, error) {
	return s.CreateAgentBoundTokenUnder(nil, name, accountScopeID, userID, expiresIn, agentName, messagesPerMinute, sessionsPerHour)
}

// CreateAgentBoundTokenUnder is CreateAgentBoundToken minted on behalf of the
// scoped token parent (see CreateScopedTokenUnder).
func (s *Service) CreateAgentBoundTokenUnder(parent *pebblestore.ScopedTokenRecord, name, accountScopeID, userID string, expiresIn time.Duration, agentName string, messagesPerMinute, sessionsPerHour int) (string, pebblestore.ScopedTokenRecord, error) {
	if strings.TrimSpace(agentName) == "" {
		return "", pebblestore.ScopedTokenRecord{}, errors.New("agent name is required")
	}
	if messagesPerMinute < 0 || messagesPerMinute > 6000 || sessionsPerHour < 0 || sessionsPerHour > 100000 {
		return "", pebblestore.ScopedTokenRecord{}, errors.New("rate limits out of range")
	}
	return s.createScopedTokenRecord(parent, name, []string{"sessions:read", "sessions:write"}, accountScopeID, userID, expiresIn, func(record *pebblestore.ScopedTokenRecord) {
		record.AgentName = strings.TrimSpace(agentName)
		record.MessagesPerMinute = messagesPerMinute
		record.SessionsPerHour = sessionsPerHour
	})
}

func (s *Service) createScopedTokenRecord(parent *pebblestore.ScopedTokenRecord, name string, scopes []string, accountScopeID, userID string, expiresIn time.Duration, bind func(*pebblestore.ScopedTokenRecord)) (string, pebblestore.ScopedTokenRecord, error) {
	if s == nil || s.authStore == nil {
		return "", pebblestore.ScopedTokenRecord{}, errors.New("auth store not configured")
	}
	if parent != nil && parent.ExpiresAt > 0 {
		remaining := time.Until(time.UnixMilli(parent.ExpiresAt))
		if remaining <= 0 {
			return "", pebblestore.ScopedTokenRecord{}, errors.New("the issuing key has expired")
		}
		if expiresIn <= 0 || expiresIn > remaining {
			expiresIn = remaining
		}
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

	bind(&record)
	if parent != nil {
		record.ParentTokenID = parent.ID
	}
	if err := s.authStore.PutScopedToken(record); err != nil {
		return "", pebblestore.ScopedTokenRecord{}, err
	}
	s.emitTokenLifecycle("token.minted", record)

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
		s.emitTokenDenied(record, "revoked")
		return nil, errors.New("token has been revoked")
	}
	if record.ExpiresAt > 0 && time.Now().UnixMilli() >= record.ExpiresAt {
		s.emitTokenDenied(record, "expired")
		return nil, errors.New("token has expired")
	}
	// Every ancestor must still be valid: revoking, deleting or expiring a key
	// cuts off every key minted beneath it, however deep.
	parentID := record.ParentTokenID
	for depth := 0; parentID != ""; depth++ {
		if depth >= maxScopedTokenChainDepth {
			s.emitTokenDenied(record, "issuer chain too deep")
			return nil, errors.New("the chain of keys that issued this token is too deep")
		}
		parent, found, err := s.authStore.GetScopedToken(record.AccountScopeID, parentID)
		if err != nil {
			return nil, err
		}
		if !found || parent.Revoked || (parent.ExpiresAt > 0 && time.Now().UnixMilli() >= parent.ExpiresAt) {
			s.emitTokenDenied(record, "issuing key no longer valid")
			return nil, errors.New("the key that issued this token is no longer valid")
		}
		parentID = parent.ParentTokenID
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
	record, err := s.authStore.RevokeScopedToken(accountScopeID, tokenID)
	if err == nil {
		s.emitTokenLifecycle("token.revoked", record)
	}
	return record, err
}

func (s *Service) DeleteScopedToken(accountScopeID, tokenID string) error {
	if s == nil || s.authStore == nil {
		return errors.New("auth store not configured")
	}
	record, found, _ := s.authStore.GetScopedToken(accountScopeID, tokenID)
	if err := s.authStore.DeleteScopedToken(accountScopeID, tokenID); err != nil {
		return err
	}
	if found {
		s.emitTokenLifecycle("token.deleted", record)
	}
	return nil
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
