package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	AuthTypeAPI   = "api"
	AuthTypeOAuth = "oauth"
	AuthTypeCLI   = "cli"
	AuthTypeGH    = "gh"

	CodexAuthTypeAPI   = AuthTypeAPI
	CodexAuthTypeOAuth = AuthTypeOAuth

	DefaultAuthCredentialID = "default"
)

type AuthCredentialRecord struct {
	ID           string                          `json:"id"`
	Provider     string                          `json:"provider"`
	Type         string                          `json:"type"`
	Label        string                          `json:"label,omitempty"`
	Tags         []string                        `json:"tags,omitempty"`
	APIKey       string                          `json:"api_key,omitempty"`
	AccessToken  string                          `json:"access_token,omitempty"`
	RefreshToken string                          `json:"refresh_token,omitempty"`
	ExpiresAt    int64                           `json:"expires_at,omitempty"`
	AccountID    string                          `json:"account_id,omitempty"`
	Managed      bool                            `json:"managed,omitempty"`
	OwnerSwarmID string                          `json:"owner_swarm_id,omitempty"`
	Connection   *AuthCredentialConnectionRecord `json:"connection,omitempty"`
	CreatedAt    int64                           `json:"created_at"`
	UpdatedAt    int64                           `json:"updated_at"`
}

type AuthCredentialConnectionRecord struct {
	Connected  bool   `json:"connected"`
	Method     string `json:"method,omitempty"`
	Message    string `json:"message,omitempty"`
	VerifiedAt int64  `json:"verified_at,omitempty"`
}

// Kept as an alias for existing codex-provider call sites.
type CodexAuthRecord = AuthCredentialRecord

type AuthCredentialInput struct {
	ID           string
	Provider     string
	Type         string
	Label        string
	Tags         []string
	APIKey       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	AccountID    string
	SetActive    bool
}

type authCredentialActiveRecord struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updated_at"`
}

type AuthStore struct {
	store        *Store
	secretStore  *Store
	localKeyPath string
	mu           sync.RWMutex
	vaultMeta    *VaultMetadata
	vaultDEK     []byte
	localRootKey []byte
}

func NewAuthStore(store *Store) *AuthStore {
	return NewAuthStoreWithSecretStore(store, store)
}

func NewAuthStoreWithSecretStore(store, secretStore *Store) *AuthStore {
	if store == nil {
		store = secretStore
	}
	if secretStore == nil {
		secretStore = store
	}
	localKeyPath := ""
	if secretStore != nil {
		if path := strings.TrimSpace(secretStore.Path()); path != "" {
			localKeyPath = path + ".key"
		}
	}
	return &AuthStore{
		store:        store,
		secretStore:  secretStore,
		localKeyPath: localKeyPath,
	}
}

func (s *AuthStore) SetCodexAPIKey(apiKey string) (CodexAuthRecord, error) {
	return s.UpsertCredential(AuthCredentialInput{
		ID:        DefaultAuthCredentialID,
		Provider:  "codex",
		Type:      CodexAuthTypeAPI,
		Label:     "default",
		APIKey:    strings.TrimSpace(apiKey),
		SetActive: true,
	})
}

func (s *AuthStore) SetCodexOAuth(accessToken, refreshToken string, expiresAt int64, accountID string) (CodexAuthRecord, error) {
	return s.UpsertCredential(AuthCredentialInput{
		ID:           DefaultAuthCredentialID,
		Provider:     "codex",
		Type:         CodexAuthTypeOAuth,
		Label:        "default",
		AccessToken:  strings.TrimSpace(accessToken),
		RefreshToken: strings.TrimSpace(refreshToken),
		ExpiresAt:    expiresAt,
		AccountID:    strings.TrimSpace(accountID),
		SetActive:    true,
	})
}

func (s *AuthStore) UpdateOAuthCredential(provider, credentialID, accessToken, refreshToken string, expiresAt int64, accountID string) (AuthCredentialRecord, error) {
	record, ok, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if !ok {
		return AuthCredentialRecord{}, fmt.Errorf("credential not found: provider=%s id=%s", provider, credentialID)
	}

	provider = normalizeProvider(provider)
	credentialID = normalizeCredentialID(credentialID)
	if record.Managed {
		return AuthCredentialRecord{}, fmt.Errorf("credential %s/%s is managed by swarm %s and cannot be refreshed locally", record.Provider, record.ID, record.OwnerSwarmID)
	}
	now := time.Now().UnixMilli()
	record.Provider = provider
	record.ID = credentialID
	record.Type = CodexAuthTypeOAuth
	record.APIKey = ""
	record.AccessToken = strings.TrimSpace(accessToken)
	record.RefreshToken = strings.TrimSpace(refreshToken)
	record.AccountID = strings.TrimSpace(accountID)
	if expiresAt > 0 {
		record.ExpiresAt = expiresAt
	}
	record.UpdatedAt = now
	record = normalizeCredentialRecord(record)

	return s.saveCredential(record, false)
}

func (s *AuthStore) GetCodexAuthRecord() (CodexAuthRecord, bool, error) {
	record, ok, err := s.GetActiveCredential("codex")
	if err != nil {
		return CodexAuthRecord{}, false, err
	}
	if ok {
		return record, true, nil
	}
	legacy, migrated, err := s.migrateLegacyCodexRecord()
	if err != nil {
		return CodexAuthRecord{}, false, err
	}
	if migrated {
		return legacy, true, nil
	}
	return CodexAuthRecord{}, false, nil
}

func (s *AuthStore) UpsertCredential(input AuthCredentialInput) (AuthCredentialRecord, error) {
	provider := normalizeProvider(input.Provider)
	if provider == "" {
		return AuthCredentialRecord{}, fmt.Errorf("provider must not be empty")
	}
	credentialID := normalizeCredentialID(input.ID)
	if credentialID == "" {
		generated, err := generateCredentialID(10)
		if err != nil {
			return AuthCredentialRecord{}, err
		}
		credentialID = generated
	}

	current, exists, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if exists && current.Managed {
		return AuthCredentialRecord{}, fmt.Errorf("credential %s/%s is managed by swarm %s and cannot be modified locally", current.Provider, current.ID, current.OwnerSwarmID)
	}

	now := time.Now().UnixMilli()
	authType := normalizeAuthType(input.Type, input.APIKey, input.AccessToken, input.RefreshToken)
	if authType == "" && exists {
		authType = normalizeAuthType(current.Type, current.APIKey, current.AccessToken, current.RefreshToken)
	}
	if authType == "" {
		authType = CodexAuthTypeAPI
	}

	label := strings.TrimSpace(input.Label)
	if label == "" && exists {
		label = current.Label
	}
	if label == "" {
		label = credentialID
	}

	tags := current.Tags
	if input.Tags != nil {
		tags = normalizeTags(input.Tags)
	}

	next := AuthCredentialRecord{
		ID:        credentialID,
		Provider:  provider,
		Type:      authType,
		Label:     label,
		Tags:      tags,
		UpdatedAt: now,
	}
	if exists {
		next.CreatedAt = current.CreatedAt
	} else {
		next.CreatedAt = now
	}

	switch authType {
	case AuthTypeOAuth:
		next.AccessToken = strings.TrimSpace(input.AccessToken)
		next.RefreshToken = strings.TrimSpace(input.RefreshToken)
		next.AccountID = strings.TrimSpace(input.AccountID)
		if next.AccessToken == "" && exists {
			next.AccessToken = current.AccessToken
		}
		if next.RefreshToken == "" && exists {
			next.RefreshToken = current.RefreshToken
		}
		if next.AccessToken == "" {
			return AuthCredentialRecord{}, fmt.Errorf("oauth credentials require access_token")
		}
		if provider == "codex" && next.RefreshToken == "" {
			return AuthCredentialRecord{}, fmt.Errorf("codex oauth credentials require refresh_token")
		}
		if input.ExpiresAt > 0 {
			next.ExpiresAt = input.ExpiresAt
		} else if exists {
			next.ExpiresAt = current.ExpiresAt
		}
		if next.AccountID == "" && exists {
			next.AccountID = current.AccountID
		}
	case AuthTypeAPI:
		next.APIKey = strings.TrimSpace(input.APIKey)
		if next.APIKey == "" && exists {
			next.APIKey = current.APIKey
		}
		if next.APIKey == "" {
			return AuthCredentialRecord{}, fmt.Errorf("api credentials require api_key")
		}
	case AuthTypeCLI, AuthTypeGH:
		// CLI-backed Copilot auth sources intentionally persist only the selected
		// source metadata; runtime credential material is resolved at use time.
	default:
		return AuthCredentialRecord{}, fmt.Errorf("unsupported auth type %q", authType)
	}

	if exists && sameCredentialBinding(current, next) {
		next.Connection = cloneCredentialConnection(current.Connection)
	}

	return s.saveCredential(next, input.SetActive)
}

func (s *AuthStore) GetCredential(provider, credentialID string) (AuthCredentialRecord, bool, error) {
	provider = normalizeProvider(provider)
	credentialID = normalizeCredentialID(credentialID)
	if provider == "" || credentialID == "" {
		return AuthCredentialRecord{}, false, nil
	}
	payload, ok, err := s.secretStore.GetBytes(KeyAuthCredential(provider, credentialID))
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	if !ok {
		return AuthCredentialRecord{}, false, nil
	}
	record, err := s.decodeStoredCredential(payload)
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	record.Provider = provider
	record.ID = credentialID
	return normalizeCredentialRecord(record), true, nil
}

func (s *AuthStore) GetActiveCredential(provider string) (AuthCredentialRecord, bool, error) {
	provider = normalizeProvider(provider)
	if provider == "" {
		return AuthCredentialRecord{}, false, nil
	}
	credentialID, ok, err := s.getActiveCredentialID(provider)
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	if !ok {
		records, listErr := s.ListCredentials(provider, 200)
		if listErr != nil {
			return AuthCredentialRecord{}, false, listErr
		}
		if len(records) == 0 {
			return AuthCredentialRecord{}, false, nil
		}
		record := records[0]
		if _, setErr := s.SetActiveCredential(provider, record.ID); setErr != nil {
			return AuthCredentialRecord{}, false, setErr
		}
		return record, true, nil
	}
	record, found, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	if !found {
		_ = s.store.Delete(KeyAuthCredentialActive(provider))
		return AuthCredentialRecord{}, false, nil
	}
	return record, true, nil
}

func (s *AuthStore) SetActiveCredential(provider, credentialID string) (AuthCredentialRecord, error) {
	provider = normalizeProvider(provider)
	credentialID = normalizeCredentialID(credentialID)
	if provider == "" || credentialID == "" {
		return AuthCredentialRecord{}, fmt.Errorf("provider and id are required")
	}
	record, ok, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if !ok {
		return AuthCredentialRecord{}, fmt.Errorf("credential not found: provider=%s id=%s", provider, credentialID)
	}
	if err := s.setActiveCredentialID(provider, credentialID, time.Now().UnixMilli()); err != nil {
		return AuthCredentialRecord{}, err
	}
	return record, nil
}

func (s *AuthStore) UpdateCredentialConnection(provider, credentialID string, connection *AuthCredentialConnectionRecord) (AuthCredentialRecord, error) {
	record, ok, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if !ok {
		return AuthCredentialRecord{}, fmt.Errorf("credential not found: provider=%s id=%s", normalizeProvider(provider), normalizeCredentialID(credentialID))
	}
	record.Connection = cloneCredentialConnection(connection)
	return s.saveCredential(record, false)
}

func (s *AuthStore) DeleteCredential(provider, credentialID string) (bool, error) {
	provider = normalizeProvider(provider)
	credentialID = normalizeCredentialID(credentialID)
	if provider == "" || credentialID == "" {
		return false, nil
	}
	record, ok, err := s.GetCredential(provider, credentialID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if record.Managed {
		return false, fmt.Errorf("credential %s/%s is managed by swarm %s and cannot be deleted locally", record.Provider, record.ID, record.OwnerSwarmID)
	}
	return s.deleteCredentialRecord(record)
}

func (s *AuthStore) DeleteCredentialsByOwnerSwarmID(ownerSwarmID string) (int, error) {
	ownerSwarmID = strings.TrimSpace(ownerSwarmID)
	if ownerSwarmID == "" {
		return 0, nil
	}
	records, err := s.ListCredentials("", 10_000)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, record := range records {
		if strings.TrimSpace(record.OwnerSwarmID) != ownerSwarmID {
			continue
		}
		removed, err := s.deleteCredentialRecord(record)
		if err != nil {
			return deleted, err
		}
		if removed {
			deleted++
		}
	}
	return deleted, nil
}

func (s *AuthStore) deleteCredentialRecord(record AuthCredentialRecord) (bool, error) {
	if err := s.deleteTagIndexes(record); err != nil {
		return false, err
	}
	if err := s.secretStore.Delete(KeyAuthCredential(record.Provider, record.ID)); err != nil {
		return false, err
	}

	activeID, activeSet, err := s.getActiveCredentialID(record.Provider)
	if err != nil {
		return false, err
	}
	if activeSet && activeID == record.ID {
		records, err := s.ListCredentials(record.Provider, 200)
		if err != nil {
			return false, err
		}
		if len(records) == 0 {
			if err := s.store.Delete(KeyAuthCredentialActive(record.Provider)); err != nil {
				return false, err
			}
		} else {
			if err := s.setActiveCredentialID(record.Provider, records[0].ID, time.Now().UnixMilli()); err != nil {
				return false, err
			}
		}
	}

	return true, nil
}

func (s *AuthStore) ListCredentials(provider string, limit int) ([]AuthCredentialRecord, error) {
	provider = normalizeProvider(provider)
	if limit <= 0 {
		limit = 200
	}
	scanLimit := limit
	if scanLimit < 1000 {
		scanLimit = 1000
	}
	if provider != "" {
		scanLimit = maxInt(scanLimit, limit*4)
	}

	prefix := AuthCredentialPrefix()
	if provider != "" {
		prefix = AuthCredentialProviderPrefix(provider)
	}

	records := make([]AuthCredentialRecord, 0, minInt(scanLimit, 256))
	err := s.secretStore.IteratePrefix(prefix, scanLimit, func(_ string, value []byte) error {
		record, err := s.decodeStoredCredential(value)
		if err != nil {
			return err
		}
		record = normalizeCredentialRecord(record)
		if provider != "" && record.Provider != provider {
			return nil
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortCredentials(records)
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *AuthStore) ListCredentialProviders(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	records, err := s.ListCredentials("", maxInt(limit*20, 1000))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(records))
	out := make([]string, 0, len(records))
	for _, record := range records {
		if _, ok := seen[record.Provider]; ok {
			continue
		}
		seen[record.Provider] = struct{}{}
		out = append(out, record.Provider)
		if len(out) >= limit {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *AuthStore) saveCredential(next AuthCredentialRecord, setActive bool) (AuthCredentialRecord, error) {
	next = normalizeCredentialRecord(next)
	current, exists, err := s.GetCredential(next.Provider, next.ID)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if exists {
		next.CreatedAt = current.CreatedAt
		if err := s.deleteTagIndexes(current); err != nil {
			return AuthCredentialRecord{}, err
		}
	}
	if next.CreatedAt <= 0 {
		next.CreatedAt = next.UpdatedAt
	}
	payload, err := s.encodeStoredCredential(next)
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if err := s.secretStore.PutBytes(KeyAuthCredential(next.Provider, next.ID), payload); err != nil {
		return AuthCredentialRecord{}, err
	}
	if err := s.writeTagIndexes(next); err != nil {
		return AuthCredentialRecord{}, err
	}
	if setActive {
		if err := s.setActiveCredentialID(next.Provider, next.ID, next.UpdatedAt); err != nil {
			return AuthCredentialRecord{}, err
		}
	} else {
		_, ok, err := s.getActiveCredentialID(next.Provider)
		if err != nil {
			return AuthCredentialRecord{}, err
		}
		if !ok {
			if err := s.setActiveCredentialID(next.Provider, next.ID, next.UpdatedAt); err != nil {
				return AuthCredentialRecord{}, err
			}
		}
	}
	return next, nil
}

func (s *AuthStore) getActiveCredentialID(provider string) (string, bool, error) {
	var record authCredentialActiveRecord
	ok, err := s.store.GetJSON(KeyAuthCredentialActive(provider), &record)
	if err != nil {
		return "", false, err
	}
	if !ok || strings.TrimSpace(record.ID) == "" {
		return "", false, nil
	}
	return normalizeCredentialID(record.ID), true, nil
}

func (s *AuthStore) setActiveCredentialID(provider, credentialID string, updatedAt int64) error {
	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}
	return s.store.PutJSON(KeyAuthCredentialActive(provider), authCredentialActiveRecord{
		ID:        normalizeCredentialID(credentialID),
		UpdatedAt: updatedAt,
	})
}

func (s *AuthStore) writeTagIndexes(record AuthCredentialRecord) error {
	for _, tag := range normalizeTags(record.Tags) {
		if err := s.store.PutBytes(KeyAuthCredentialTag(tag, record.Provider, record.ID), []byte(record.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *AuthStore) deleteTagIndexes(record AuthCredentialRecord) error {
	for _, tag := range normalizeTags(record.Tags) {
		if err := s.store.Delete(KeyAuthCredentialTag(tag, record.Provider, record.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *AuthStore) migrateLegacyCodexRecord() (AuthCredentialRecord, bool, error) {
	var legacy AuthCredentialRecord
	ok, err := s.store.GetJSON(KeyAuthCodexDefault, &legacy)
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	if !ok {
		return AuthCredentialRecord{}, false, nil
	}

	now := time.Now().UnixMilli()
	legacy.Provider = "codex"
	legacy.ID = DefaultAuthCredentialID
	if legacy.UpdatedAt <= 0 {
		legacy.UpdatedAt = now
	}
	if legacy.CreatedAt <= 0 {
		legacy.CreatedAt = legacy.UpdatedAt
	}
	legacy.Type = normalizeAuthType(legacy.Type, legacy.APIKey, legacy.AccessToken, legacy.RefreshToken)
	if legacy.Label == "" {
		legacy.Label = DefaultAuthCredentialID
	}
	legacy = normalizeCredentialRecord(legacy)

	record, err := s.saveCredential(legacy, true)
	if err != nil {
		return AuthCredentialRecord{}, false, err
	}
	_ = s.store.Delete(KeyAuthCodexDefault)
	return record, true, nil
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func normalizeCredentialID(credentialID string) string {
	return strings.ToLower(strings.TrimSpace(credentialID))
}

func normalizeCredentialRecord(record AuthCredentialRecord) AuthCredentialRecord {
	record.Provider = normalizeProvider(record.Provider)
	record.ID = normalizeCredentialID(record.ID)
	record.Type = normalizeAuthType(record.Type, record.APIKey, record.AccessToken, record.RefreshToken)
	record.Label = strings.TrimSpace(record.Label)
	if record.Label == "" {
		record.Label = record.ID
	}
	record.Tags = normalizeTags(record.Tags)
	record.APIKey = strings.TrimSpace(record.APIKey)
	record.AccessToken = strings.TrimSpace(record.AccessToken)
	record.RefreshToken = strings.TrimSpace(record.RefreshToken)
	record.AccountID = strings.TrimSpace(record.AccountID)
	record.Managed = record.Managed
	record.OwnerSwarmID = strings.TrimSpace(record.OwnerSwarmID)
	record.Connection = normalizeCredentialConnection(record.Connection)
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = time.Now().UnixMilli()
	}
	if record.CreatedAt <= 0 {
		record.CreatedAt = record.UpdatedAt
	}
	return record
}

func normalizeAuthType(authType, apiKey, accessToken, refreshToken string) string {
	normalized := strings.ToLower(strings.TrimSpace(authType))
	switch normalized {
	case AuthTypeAPI, AuthTypeOAuth, AuthTypeCLI, AuthTypeGH:
		return normalized
	}
	if strings.TrimSpace(accessToken) != "" || strings.TrimSpace(refreshToken) != "" {
		return AuthTypeOAuth
	}
	if strings.TrimSpace(apiKey) != "" {
		return AuthTypeAPI
	}
	return ""
}

func normalizeCredentialConnection(connection *AuthCredentialConnectionRecord) *AuthCredentialConnectionRecord {
	if connection == nil {
		return nil
	}
	method := strings.ToLower(strings.TrimSpace(connection.Method))
	message := strings.TrimSpace(connection.Message)
	verifiedAt := connection.VerifiedAt
	if !connection.Connected && method == "" && message == "" && verifiedAt <= 0 {
		return nil
	}
	out := &AuthCredentialConnectionRecord{
		Connected:  connection.Connected,
		Method:     method,
		Message:    message,
		VerifiedAt: verifiedAt,
	}
	if out.VerifiedAt <= 0 {
		out.VerifiedAt = time.Now().UnixMilli()
	}
	return out
}

func cloneCredentialConnection(connection *AuthCredentialConnectionRecord) *AuthCredentialConnectionRecord {
	if connection == nil {
		return nil
	}
	cloned := *connection
	return normalizeCredentialConnection(&cloned)
}

func sameCredentialBinding(current, next AuthCredentialRecord) bool {
	if current.Type != next.Type {
		return false
	}
	switch next.Type {
	case AuthTypeOAuth:
		return current.AccessToken == next.AccessToken &&
			current.RefreshToken == next.RefreshToken &&
			current.AccountID == next.AccountID
	case AuthTypeCLI, AuthTypeGH:
		return true
	default:
		return current.APIKey == next.APIKey
	}
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortCredentials(records []AuthCredentialRecord) {
	sort.Slice(records, func(i, j int) bool {
		a := records[i]
		b := records[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.UpdatedAt != b.UpdatedAt {
			return a.UpdatedAt > b.UpdatedAt
		}
		return a.ID < b.ID
	})
}

func generateCredentialID(size int) (string, error) {
	if size <= 0 {
		size = 10
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate credential id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
