package permission

import (
	"encoding/json"
	"strings"
	"time"
)

// PermissionState is the cached account-scoped authorization state used by
// policy explanation and tool authorization paths. Pending permission records
// are intentionally excluded from this cache.
type PermissionState struct {
	AccountScopeID    string `json:"account_scope_id,omitempty"`
	Policy            Policy `json:"policy"`
	BypassPermissions bool   `json:"bypass_permissions"`
	LoadedAt          int64  `json:"loaded_at,omitempty"`
	PolicyUpdatedAt   int64  `json:"policy_updated_at,omitempty"`
	BypassUpdatedAt   int64  `json:"bypass_updated_at,omitempty"`
}

func (s *Service) CurrentPermissionStateForAccount(accountScopeID string) (PermissionState, error) {
	if s == nil {
		return PermissionState{Policy: DefaultPolicy()}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return PermissionState{}, err
	}
	return permissionStateFromCacheEntry(entry), nil
}

func (s *Service) loadPermissionStateLocked(accountScopeID string) (permissionStateCacheEntry, error) {
	key := permissionStateCacheKey(accountScopeID)
	if s.permissionStateCache == nil {
		s.permissionStateCache = make(map[string]permissionStateCacheEntry)
	}
	if entry, ok := s.permissionStateCache[key]; ok {
		return clonePermissionStateCacheEntry(entry), nil
	}
	policy, err := s.loadPolicyFromStoreLocked(accountScopeID)
	if err != nil {
		return permissionStateCacheEntry{}, err
	}
	now := time.Now().UnixMilli()
	entry := permissionStateCacheEntry{
		AccountScopeID:    strings.TrimSpace(accountScopeID),
		Policy:            NormalizePolicy(policy),
		BypassPermissions: s.bypassPermissions,
		LoadedAt:          now,
		PolicyUpdatedAt:   policy.UpdatedAt,
		BypassUpdatedAt:   now,
	}
	s.permissionStateCache[key] = entry
	return clonePermissionStateCacheEntry(entry), nil
}

func (s *Service) cachePermissionStateLocked(accountScopeID string, policy Policy, bypass bool, policyUpdatedAt, bypassUpdatedAt int64) {
	if s.permissionStateCache == nil {
		s.permissionStateCache = make(map[string]permissionStateCacheEntry)
	}
	now := time.Now().UnixMilli()
	policy = NormalizePolicy(policy)
	if policyUpdatedAt <= 0 {
		policyUpdatedAt = policy.UpdatedAt
	}
	if bypassUpdatedAt <= 0 {
		bypassUpdatedAt = now
	}
	s.permissionStateCache[permissionStateCacheKey(accountScopeID)] = permissionStateCacheEntry{
		AccountScopeID:    strings.TrimSpace(accountScopeID),
		Policy:            policy,
		BypassPermissions: bypass,
		LoadedAt:          now,
		PolicyUpdatedAt:   policyUpdatedAt,
		BypassUpdatedAt:   bypassUpdatedAt,
	}
}

func (s *Service) invalidatePermissionStateCacheLocked(accountScopeID string) {
	if len(s.permissionStateCache) == 0 {
		return
	}
	if strings.TrimSpace(accountScopeID) == "" {
		s.permissionStateCache = make(map[string]permissionStateCacheEntry)
		return
	}
	delete(s.permissionStateCache, permissionStateCacheKey(accountScopeID))
}

func (s *Service) loadPolicyLocked(accountScopeID string) (Policy, error) {
	entry, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	return entry.Policy, nil
}

func (s *Service) loadPolicyFromStoreLocked(accountScopeID string) (Policy, error) {
	if s.store == nil {
		return DefaultPolicy(), nil
	}
	raw, ok, err := s.store.GetPolicyForAccount(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	if !ok || strings.TrimSpace(string(raw)) == "" {
		return DefaultPolicy(), nil
	}
	var policy Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return Policy{}, err
	}
	return NormalizePolicy(policy), nil
}

func permissionStateCacheKey(accountScopeID string) string {
	return strings.TrimSpace(accountScopeID)
}

func permissionStateFromCacheEntry(entry permissionStateCacheEntry) PermissionState {
	return PermissionState{
		AccountScopeID:    strings.TrimSpace(entry.AccountScopeID),
		Policy:            NormalizePolicy(entry.Policy),
		BypassPermissions: entry.BypassPermissions,
		LoadedAt:          entry.LoadedAt,
		PolicyUpdatedAt:   entry.PolicyUpdatedAt,
		BypassUpdatedAt:   entry.BypassUpdatedAt,
	}
}

func clonePermissionStateCacheEntry(entry permissionStateCacheEntry) permissionStateCacheEntry {
	entry.Policy = NormalizePolicy(entry.Policy)
	return entry
}
