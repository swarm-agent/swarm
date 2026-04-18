package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Service struct {
	authStore *pebblestore.AuthStore
	events    *pebblestore.EventLog
}

type CodexStatus struct {
	Provider     string              `json:"provider"`
	Configured   bool                `json:"configured"`
	AuthType     string              `json:"auth_type,omitempty"`
	UpdatedAt    int64               `json:"updated_at"`
	ExpiresAt    int64               `json:"expires_at,omitempty"`
	Last4        string              `json:"last4,omitempty"`
	HasRefresh   bool                `json:"has_refresh_token,omitempty"`
	HasAccountID bool                `json:"has_account_id,omitempty"`
	StorageMode  string              `json:"storage_mode"`
	AutoDefaults *AutoDefaultsStatus `json:"auto_defaults,omitempty"`
}

type CredentialStatus struct {
	ID           string              `json:"id"`
	Provider     string              `json:"provider"`
	Active       bool                `json:"active"`
	AuthType     string              `json:"auth_type"`
	Label        string              `json:"label,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
	UpdatedAt    int64               `json:"updated_at"`
	CreatedAt    int64               `json:"created_at"`
	ExpiresAt    int64               `json:"expires_at,omitempty"`
	Last4        string              `json:"last4,omitempty"`
	HasRefresh   bool                `json:"has_refresh_token,omitempty"`
	HasAccountID bool                `json:"has_account_id,omitempty"`
	StorageMode  string              `json:"storage_mode"`
	AutoDefaults *AutoDefaultsStatus `json:"auto_defaults,omitempty"`
	Connection   *ConnectionStatus   `json:"connection,omitempty"`
}

type ConnectionStatus struct {
	Connected  bool   `json:"connected"`
	Method     string `json:"method,omitempty"`
	Message    string `json:"message,omitempty"`
	VerifiedAt int64  `json:"verified_at,omitempty"`
}

type AutoDefaultsStatus struct {
	Applied         bool     `json:"applied"`
	Error           string   `json:"error,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Model           string   `json:"model,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	GlobalModel     bool     `json:"global_model,omitempty"`
	Agents          []string `json:"agents,omitempty"`
	Subagents       []string `json:"subagents,omitempty"`
	UtilityProvider string   `json:"utility_provider,omitempty"`
	UtilityModel    string   `json:"utility_model,omitempty"`
	UtilityThinking string   `json:"utility_thinking,omitempty"`
}

type CredentialList struct {
	Provider  string             `json:"provider,omitempty"`
	Query     string             `json:"query,omitempty"`
	Total     int                `json:"total"`
	Records   []CredentialStatus `json:"records"`
	Providers []string           `json:"providers,omitempty"`
}

type CredentialUpsertInput struct {
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
	Active       bool
}

type VaultStatus = pebblestore.VaultStatus

type CredentialImportResult = pebblestore.CredentialImportResult

type CredentialBundleMetadata = pebblestore.CredentialBundleMetadata

func (s *Service) ImportManagedCredentials(ownerSwarmID, bundlePassword, vaultPassword string, payload []byte) (CredentialImportResult, error) {
	if s == nil || s.authStore == nil {
		return CredentialImportResult{}, errors.New("auth store is not configured")
	}
	return s.authStore.ImportManagedCredentials(ownerSwarmID, bundlePassword, vaultPassword, payload)
}

func (s *Service) DeleteManagedCredentialsByOwnerSwarmID(ownerSwarmID string) (int, error) {
	if s == nil || s.authStore == nil {
		return 0, errors.New("auth store is not configured")
	}
	return s.authStore.DeleteCredentialsByOwnerSwarmID(ownerSwarmID)
}

func (s *Service) NewManagedCredentialBundle(ownerSwarmID string) ([]byte, string, int, error) {
	if s == nil || s.authStore == nil {
		return nil, "", 0, errors.New("auth store is not configured")
	}
	ownerSwarmID = strings.TrimSpace(ownerSwarmID)
	if ownerSwarmID == "" {
		return nil, "", 0, errors.New("owner swarm id is required")
	}
	status, err := s.authStore.VaultStatus()
	if err != nil {
		return nil, "", 0, err
	}
	if status.Enabled && !status.Unlocked {
		return nil, "", 0, pebblestore.ErrVaultLocked
	}
	password, err := randomSecretString(32)
	if err != nil {
		return nil, "", 0, err
	}
	payload, exported, err := s.authStore.ExportCredentials(password, "")
	if err != nil {
		return nil, "", 0, err
	}
	return payload, password, exported, nil
}

func NewService(authStore *pebblestore.AuthStore, events *pebblestore.EventLog) *Service {
	return &Service{authStore: authStore, events: events}
}

func (s *Service) VaultStatus() (VaultStatus, error) {
	if s == nil || s.authStore == nil {
		return VaultStatus{Enabled: false, Unlocked: true, StorageMode: "pebble/plain"}, nil
	}
	return s.authStore.VaultStatus()
}

func (s *Service) EnableVault(password string) (VaultStatus, error) {
	if s == nil || s.authStore == nil {
		return VaultStatus{}, errors.New("auth store is not configured")
	}
	return s.authStore.EnableVault(password)
}

func (s *Service) UnlockVault(password string) (VaultStatus, error) {
	if s == nil || s.authStore == nil {
		return VaultStatus{}, errors.New("auth store is not configured")
	}
	return s.authStore.UnlockVault(password)
}

func (s *Service) LockVault() (VaultStatus, error) {
	if s == nil || s.authStore == nil {
		return VaultStatus{}, errors.New("auth store is not configured")
	}
	return s.authStore.LockVault()
}

func (s *Service) DisableVault(password string) (VaultStatus, error) {
	if s == nil || s.authStore == nil {
		return VaultStatus{}, errors.New("auth store is not configured")
	}
	return s.authStore.DisableVault(password)
}

func (s *Service) ExportCredentials(bundlePassword, vaultPassword string) ([]byte, int, error) {
	if s == nil || s.authStore == nil {
		return nil, 0, errors.New("auth store is not configured")
	}
	return s.authStore.ExportCredentials(bundlePassword, vaultPassword)
}

func (s *Service) CredentialBundleMetadata(bundlePassword string, payload []byte) (CredentialBundleMetadata, error) {
	if s == nil || s.authStore == nil {
		return CredentialBundleMetadata{}, errors.New("auth store is not configured")
	}
	return s.authStore.CredentialBundleMetadata(bundlePassword, payload)
}

func (s *Service) ImportCredentials(bundlePassword, vaultPassword string, payload []byte) (CredentialImportResult, error) {
	if s == nil || s.authStore == nil {
		return CredentialImportResult{}, errors.New("auth store is not configured")
	}
	return s.authStore.ImportCredentials(bundlePassword, vaultPassword, payload)
}

func (s *Service) SetCodexKey(rawKey string) (CodexStatus, *pebblestore.EventEnvelope, error) {
	apiKey := strings.TrimSpace(rawKey)
	if apiKey == "" {
		return CodexStatus{}, nil, errors.New("codex api key must not be empty")
	}

	record, err := s.authStore.SetCodexAPIKey(apiKey)
	if err != nil {
		return CodexStatus{}, nil, fmt.Errorf("persist codex auth: %w", err)
	}

	status := s.statusFromRecord(record)
	env, err := s.appendCodexUpdatedEvent(status)
	if err != nil {
		return CodexStatus{}, nil, err
	}
	return status, env, nil
}

func (s *Service) SetCodexOAuth(accessToken, refreshToken string, expiresAt int64, accountID string) (CodexStatus, *pebblestore.EventEnvelope, error) {
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	accountID = strings.TrimSpace(accountID)
	if accessToken == "" {
		return CodexStatus{}, nil, errors.New("codex access token must not be empty")
	}
	if refreshToken == "" {
		return CodexStatus{}, nil, errors.New("codex refresh token must not be empty")
	}

	record, err := s.authStore.SetCodexOAuth(accessToken, refreshToken, expiresAt, accountID)
	if err != nil {
		return CodexStatus{}, nil, fmt.Errorf("persist codex oauth: %w", err)
	}

	status := s.statusFromRecord(record)
	env, err := s.appendCodexUpdatedEvent(status)
	if err != nil {
		return CodexStatus{}, nil, err
	}
	return status, env, nil
}

func (s *Service) CodexStatus() (CodexStatus, error) {
	record, ok, err := s.authStore.GetCodexAuthRecord()
	if err != nil {
		return CodexStatus{}, fmt.Errorf("read codex auth: %w", err)
	}
	if !ok {
		storageMode := "pebble/plain"
		if s.authStore != nil {
			storageMode = s.authStore.StorageMode()
		}
		return CodexStatus{
			Provider:    "codex",
			Configured:  false,
			StorageMode: storageMode,
		}, nil
	}
	return s.statusFromRecord(record), nil
}

func (s *Service) ListCredentials(provider, query string, limit int) (CredentialList, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 200
	}

	records, err := s.authStore.ListCredentials(provider, maxInt(limit*4, 400))
	if err != nil {
		return CredentialList{}, fmt.Errorf("list credentials: %w", err)
	}
	providers, err := s.authStore.ListCredentialProviders(200)
	if err != nil {
		return CredentialList{}, fmt.Errorf("list providers: %w", err)
	}

	activeByProvider := make(map[string]string, len(providers))
	for _, p := range providers {
		active, ok, err := s.authStore.GetActiveCredential(p)
		if err != nil {
			return CredentialList{}, fmt.Errorf("read active credential for provider %s: %w", p, err)
		}
		if ok {
			activeByProvider[p] = active.ID
		}
	}

	out := make([]CredentialStatus, 0, len(records))
	for _, record := range records {
		active := activeByProvider[record.Provider] == record.ID
		status := s.credentialStatusFromRecord(record, active)
		if !matchesCredentialQuery(status, query) {
			continue
		}
		out = append(out, status)
	}
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return CredentialList{
		Provider:  provider,
		Query:     query,
		Total:     total,
		Records:   out,
		Providers: providers,
	}, nil
}

func (s *Service) UpsertCredential(input CredentialUpsertInput) (CredentialStatus, *pebblestore.EventEnvelope, error) {
	input.Tags = normalizeCredentialTags(input.Tags)

	record, err := s.authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:           input.ID,
		Provider:     input.Provider,
		Type:         input.Type,
		Label:        input.Label,
		Tags:         input.Tags,
		APIKey:       input.APIKey,
		AccessToken:  input.AccessToken,
		RefreshToken: input.RefreshToken,
		ExpiresAt:    input.ExpiresAt,
		AccountID:    input.AccountID,
		SetActive:    input.Active,
	})
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	active, ok, err := s.authStore.GetActiveCredential(record.Provider)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	isActive := ok && active.ID == record.ID
	status := s.credentialStatusFromRecord(record, isActive)

	env, err := s.appendCredentialEvent("auth.credential.upserted", status)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	s.enqueueCapabilityTagEnrichment(record, input.APIKey)
	return status, env, nil
}

func (s *Service) SetActiveCredential(provider, credentialID string) (CredentialStatus, *pebblestore.EventEnvelope, error) {
	record, err := s.authStore.SetActiveCredential(provider, credentialID)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	status := s.credentialStatusFromRecord(record, true)
	env, err := s.appendCredentialEvent("auth.credential.activated", status)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	return status, env, nil
}

func (s *Service) UpdateCredentialConnection(provider, credentialID string, connection *ConnectionStatus) (CredentialStatus, *pebblestore.EventEnvelope, error) {
	record, err := s.authStore.UpdateCredentialConnection(provider, credentialID, connectionRecordFromStatus(connection))
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	active, ok, err := s.authStore.GetActiveCredential(record.Provider)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	status := s.credentialStatusFromRecord(record, ok && active.ID == record.ID)
	env, err := s.appendCredentialEvent("auth.credential.connection.updated", status)
	if err != nil {
		return CredentialStatus{}, nil, err
	}
	return status, env, nil
}

func (s *Service) DeleteCredential(provider, credentialID string) (bool, *pebblestore.EventEnvelope, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	deleted, err := s.authStore.DeleteCredential(provider, credentialID)
	if err != nil {
		return false, nil, err
	}
	if !deleted {
		return false, nil, nil
	}
	payload := map[string]string{
		"provider": provider,
		"id":       credentialID,
	}
	env, err := s.appendAuthEvent("auth.credential.deleted", provider, payload)
	if err != nil {
		return false, nil, err
	}
	return true, env, nil
}

func (s *Service) GetCredentialRecord(provider, credentialID string) (pebblestore.AuthCredentialRecord, bool, error) {
	return s.authStore.GetCredential(provider, credentialID)
}

func (s *Service) statusFromRecord(record pebblestore.CodexAuthRecord) CodexStatus {
	storageMode := "pebble/plain"
	if s != nil && s.authStore != nil {
		storageMode = s.authStore.StorageMode()
	}
	status := CodexStatus{
		Provider:    "codex",
		Configured:  true,
		AuthType:    record.Type,
		UpdatedAt:   record.UpdatedAt,
		StorageMode: storageMode,
	}
	switch record.Type {
	case pebblestore.AuthTypeOAuth:
		status.ExpiresAt = record.ExpiresAt
		status.Last4 = last4(record.AccessToken)
		status.HasRefresh = strings.TrimSpace(record.RefreshToken) != ""
		status.HasAccountID = strings.TrimSpace(record.AccountID) != ""
	case pebblestore.AuthTypeAPI:
		status.Last4 = last4(record.APIKey)
	}
	return status
}

func (s *Service) credentialStatusFromRecord(record pebblestore.AuthCredentialRecord, active bool) CredentialStatus {
	storageMode := "pebble/plain"
	if s != nil && s.authStore != nil {
		storageMode = s.authStore.StorageMode()
	}
	status := CredentialStatus{
		ID:          record.ID,
		Provider:    record.Provider,
		Active:      active,
		AuthType:    record.Type,
		Label:       strings.TrimSpace(record.Label),
		Tags:        append([]string(nil), record.Tags...),
		UpdatedAt:   record.UpdatedAt,
		CreatedAt:   record.CreatedAt,
		StorageMode: storageMode,
		Connection:  connectionStatusFromRecord(record.Connection),
	}
	switch record.Type {
	case pebblestore.AuthTypeOAuth:
		status.ExpiresAt = record.ExpiresAt
		status.Last4 = last4(record.AccessToken)
		status.HasRefresh = strings.TrimSpace(record.RefreshToken) != ""
		status.HasAccountID = strings.TrimSpace(record.AccountID) != ""
	case pebblestore.AuthTypeAPI:
		status.Last4 = last4(record.APIKey)
	}
	return status
}

func randomSecretString(byteLen int) (string, error) {
	if byteLen <= 0 {
		return "", errors.New("secret length must be positive")
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func connectionStatusFromRecord(record *pebblestore.AuthCredentialConnectionRecord) *ConnectionStatus {
	if record == nil {
		return nil
	}
	return &ConnectionStatus{
		Connected:  record.Connected,
		Method:     strings.TrimSpace(record.Method),
		Message:    strings.TrimSpace(record.Message),
		VerifiedAt: record.VerifiedAt,
	}
}

func connectionRecordFromStatus(status *ConnectionStatus) *pebblestore.AuthCredentialConnectionRecord {
	if status == nil {
		return nil
	}
	return &pebblestore.AuthCredentialConnectionRecord{
		Connected:  status.Connected,
		Method:     strings.ToLower(strings.TrimSpace(status.Method)),
		Message:    strings.TrimSpace(status.Message),
		VerifiedAt: status.VerifiedAt,
	}
}

func (s *Service) appendCodexUpdatedEvent(status CodexStatus) (*pebblestore.EventEnvelope, error) {
	return s.appendAuthEvent("auth.codex.updated", "codex", status)
}

func (s *Service) appendCredentialEvent(eventType string, status CredentialStatus) (*pebblestore.EventEnvelope, error) {
	return s.appendAuthEvent(eventType, status.Provider, status)
}

func (s *Service) appendAuthEvent(eventType, entity string, payloadObj any) (*pebblestore.EventEnvelope, error) {
	payload, err := json.Marshal(payloadObj)
	if err != nil {
		return nil, fmt.Errorf("marshal auth event payload: %w", err)
	}
	env, err := s.events.Append("system:auth", eventType, entity, payload, "", "")
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func matchesCredentialQuery(status CredentialStatus, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	terms := strings.Fields(query)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "#") {
			tagQuery := strings.TrimPrefix(term, "#")
			if !hasCredentialTag(status, tagQuery) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "tag:") {
			tagQuery := strings.TrimSpace(strings.TrimPrefix(term, "tag:"))
			if !hasCredentialTag(status, tagQuery) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "provider:") {
			providerQuery := strings.TrimSpace(strings.TrimPrefix(term, "provider:"))
			if providerQuery == "" || !strings.Contains(status.Provider, providerQuery) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "type:") {
			typeQuery := strings.TrimSpace(strings.TrimPrefix(term, "type:"))
			if typeQuery == "" || !strings.Contains(status.AuthType, typeQuery) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "active:") {
			activeQuery := strings.TrimSpace(strings.TrimPrefix(term, "active:"))
			switch activeQuery {
			case "true", "yes", "1":
				if !status.Active {
					return false
				}
			case "false", "no", "0":
				if status.Active {
					return false
				}
			default:
				return false
			}
			continue
		}
		if matches := strings.Contains(status.Provider, term) ||
			strings.Contains(status.ID, term) ||
			strings.Contains(strings.ToLower(status.Label), term) ||
			strings.Contains(strings.ToLower(status.Last4), term) ||
			strings.Contains(status.AuthType, term) ||
			hasCredentialTag(status, term); !matches {
			return false
		}
	}
	return true
}

func hasCredentialTag(status CredentialStatus, tagQuery string) bool {
	tagQuery = strings.ToLower(strings.TrimSpace(tagQuery))
	if tagQuery == "" {
		return false
	}
	for _, tag := range status.Tags {
		if strings.Contains(strings.ToLower(tag), tagQuery) {
			return true
		}
	}
	return false
}

func last4(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}

func normalizeCredentialTags(tags []string) []string {
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
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeCredentialTags(base, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make([]string, 0, len(base)+len(extra))
	merged = append(merged, base...)
	merged = append(merged, extra...)
	return normalizeCredentialTags(merged)
}

func (s *Service) enqueueCapabilityTagEnrichment(record pebblestore.AuthCredentialRecord, apiKey string) {
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	authType := strings.ToLower(strings.TrimSpace(record.Type))
	credentialID := strings.ToLower(strings.TrimSpace(record.ID))
	apiKey = strings.TrimSpace(apiKey)
	if provider == "" || credentialID == "" || authType != "api" || apiKey == "" {
		return
	}
	if provider != "google" {
		return
	}

	go s.applyInferredCapabilityTags(provider, credentialID, authType, apiKey)
}

func (s *Service) applyInferredCapabilityTags(provider, credentialID, authType, apiKey string) {
	inferred := inferCredentialCapabilityTags(provider, authType, apiKey)
	if len(inferred) == 0 {
		return
	}

	current, ok, err := s.authStore.GetCredential(provider, credentialID)
	if err != nil || !ok {
		return
	}

	merged := mergeCredentialTags(current.Tags, inferred)
	if stringSliceEqual(current.Tags, merged) {
		return
	}

	updated, err := s.authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        credentialID,
		Provider:  provider,
		Type:      current.Type,
		Label:     current.Label,
		Tags:      merged,
		SetActive: false,
	})
	if err != nil {
		return
	}
	active, activeSet, err := s.authStore.GetActiveCredential(provider)
	if err != nil {
		return
	}
	status := s.credentialStatusFromRecord(updated, activeSet && active.ID == updated.ID)
	if s.events != nil {
		_, _ = s.appendCredentialEvent("auth.credential.capabilities.updated", status)
	}
}

func stringSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
