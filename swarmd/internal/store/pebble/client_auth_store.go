package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AttachAuthRecord struct {
	Token     string `json:"token"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ClientAuthStore struct {
	store       *Store
	secretStore *Store
}

func NewClientAuthStore(store *Store) *ClientAuthStore {
	return NewClientAuthStoreWithSecretStore(store, store)
}

func NewClientAuthStoreWithSecretStore(store, secretStore *Store) *ClientAuthStore {
	return &ClientAuthStore{store: store, secretStore: secretStore}
}

func (s *ClientAuthStore) EnsureAttachToken() (AttachAuthRecord, error) {
	record, ok, err := s.GetAttachAuth()
	if err != nil {
		return AttachAuthRecord{}, err
	}
	if ok && record.Token != "" {
		return record, nil
	}
	return s.RotateAttachToken(0)
}

func (s *ClientAuthStore) RotateAttachToken(createdAt int64) (AttachAuthRecord, error) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	token, err := generateToken(32)
	if err != nil {
		return AttachAuthRecord{}, err
	}
	record := AttachAuthRecord{
		Token:     token,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	if err := s.secretStore.PutJSON(KeyAuthAttachDefault, record); err != nil {
		return AttachAuthRecord{}, err
	}
	return record, nil
}

func (s *ClientAuthStore) GetAttachAuth() (AttachAuthRecord, bool, error) {
	var record AttachAuthRecord
	ok, err := s.secretStore.GetJSON(KeyAuthAttachDefault, &record)
	if err != nil {
		return AttachAuthRecord{}, false, err
	}
	if ok {
		if s.store != nil && s.store != s.secretStore {
			if err := s.store.Delete(KeyAuthAttachDefault); err != nil {
				return AttachAuthRecord{}, false, fmt.Errorf("remove migrated attach token: %w", err)
			}
		}
		return record, true, nil
	}
	if s.store == nil || s.store == s.secretStore {
		return AttachAuthRecord{}, false, nil
	}
	ok, err = s.store.GetJSON(KeyAuthAttachDefault, &record)
	if err != nil || !ok {
		return AttachAuthRecord{}, false, err
	}
	if err := s.secretStore.PutJSON(KeyAuthAttachDefault, record); err != nil {
		return AttachAuthRecord{}, false, fmt.Errorf("migrate attach token to secret store: %w", err)
	}
	if err := s.store.Delete(KeyAuthAttachDefault); err != nil {
		return AttachAuthRecord{}, false, fmt.Errorf("remove migrated attach token: %w", err)
	}
	return record, true, nil
}

func GenerateToken(size int) (string, error) {
	return generateToken(size)
}

func generateToken(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("token size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

type ScopedTokenRecord struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	TokenHash      string   `json:"token_hash"`
	TokenHint      string   `json:"token_hint"`
	Scopes         []string `json:"scopes"`
	AccountScopeID string   `json:"account_scope_id"`
	UserID         string   `json:"user_id"`
	CreatedAt      int64    `json:"created_at"`
	ExpiresAt      int64    `json:"expires_at"` // 0 = never
	Revoked        bool     `json:"revoked"`
	LastUsedAt     int64    `json:"last_used_at,omitempty"`
}

func (r *ScopedTokenRecord) HasScope(required string) bool {
	if r == nil || r.Revoked {
		return false
	}
	required = strings.ToLower(strings.TrimSpace(required))
	for _, s := range r.Scopes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "*" || s == "admin" || s == required {
			return true
		}
		if (s == "workers:trigger" && required == "automations:trigger") ||
			(s == "automations:trigger" && required == "workers:trigger") ||
			(s == "workers:read" && required == "automations:read") ||
			(s == "automations:read" && required == "workers:read") ||
			(s == "workers:write" && required == "automations:write") ||
			(s == "automations:write" && required == "workers:write") {
			return true
		}
		if strings.HasSuffix(s, ":*") {
			prefix := strings.TrimSuffix(s, "*")
			if strings.HasPrefix(required, prefix) {
				return true
			}
			if prefix == "workers:" && strings.HasPrefix(required, "automations:") {
				return true
			}
			if prefix == "automations:" && strings.HasPrefix(required, "workers:") {
				return true
			}
		}
	}
	return false
}

func (s *ClientAuthStore) PutScopedToken(record ScopedTokenRecord) error {
	if s == nil || s.secretStore == nil {
		return fmt.Errorf("secret store is not configured")
	}
	if record.ID == "" || record.TokenHash == "" || record.AccountScopeID == "" {
		return fmt.Errorf("id, token_hash, and account_scope_id are required")
	}
	keyAccount := KeyAuthScopedToken(record.AccountScopeID, record.ID)
	keyHash := KeyAuthScopedTokenByHash(record.TokenHash)
	batch := s.secretStore.NewBatch()
	defer batch.Close()
	bytes, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal scoped token: %w", err)
	}
	if err := batch.Set([]byte(keyAccount), bytes, nil); err != nil {
		return fmt.Errorf("set scoped token by account: %w", err)
	}
	if err := batch.Set([]byte(keyHash), bytes, nil); err != nil {
		return fmt.Errorf("set scoped token by hash: %w", err)
	}
	return batch.Commit(nil)
}

func (s *ClientAuthStore) GetScopedToken(accountScopeID, tokenID string) (ScopedTokenRecord, bool, error) {
	if s == nil || s.secretStore == nil {
		return ScopedTokenRecord{}, false, fmt.Errorf("secret store is not configured")
	}
	var record ScopedTokenRecord
	ok, err := s.secretStore.GetJSON(KeyAuthScopedToken(accountScopeID, tokenID), &record)
	return record, ok, err
}

func (s *ClientAuthStore) GetScopedTokenByHash(tokenHash string) (ScopedTokenRecord, bool, error) {
	if s == nil || s.secretStore == nil {
		return ScopedTokenRecord{}, false, fmt.Errorf("secret store is not configured")
	}
	var record ScopedTokenRecord
	ok, err := s.secretStore.GetJSON(KeyAuthScopedTokenByHash(tokenHash), &record)
	return record, ok, err
}

func (s *ClientAuthStore) ListScopedTokens(accountScopeID string) ([]ScopedTokenRecord, error) {
	if s == nil || s.secretStore == nil {
		return nil, fmt.Errorf("secret store is not configured")
	}
	prefix := KeyAuthScopedTokenPrefixForAccount(accountScopeID)
	var records []ScopedTokenRecord
	err := iteratePrefixFromReader(s.secretStore.db, prefix, 0, func(key string, value []byte) error {
		var rec ScopedTokenRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return err
		}
		records = append(records, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (s *ClientAuthStore) RevokeScopedToken(accountScopeID, tokenID string) (ScopedTokenRecord, error) {
	record, ok, err := s.GetScopedToken(accountScopeID, tokenID)
	if err != nil {
		return ScopedTokenRecord{}, err
	}
	if !ok {
		return ScopedTokenRecord{}, fmt.Errorf("scoped token not found")
	}
	record.Revoked = true
	if err := s.PutScopedToken(record); err != nil {
		return ScopedTokenRecord{}, err
	}
	return record, nil
}

func (s *ClientAuthStore) DeleteScopedToken(accountScopeID, tokenID string) error {
	record, ok, err := s.GetScopedToken(accountScopeID, tokenID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	batch := s.secretStore.NewBatch()
	defer batch.Close()
	_ = batch.Delete([]byte(KeyAuthScopedToken(accountScopeID, tokenID)), nil)
	_ = batch.Delete([]byte(KeyAuthScopedTokenByHash(record.TokenHash)), nil)
	return batch.Commit(nil)
}

func (s *ClientAuthStore) UpdateScopedTokenLastUsed(accountScopeID, tokenID string, usedAt int64) error {
	record, ok, err := s.GetScopedToken(accountScopeID, tokenID)
	if err != nil || !ok || record.Revoked {
		return err
	}
	record.LastUsedAt = usedAt
	return s.PutScopedToken(record)
}
