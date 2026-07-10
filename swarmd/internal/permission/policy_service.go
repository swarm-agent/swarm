package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type ManagedPolicyState struct {
	Policy            Policy `json:"policy"`
	BypassPermissions bool   `json:"bypass_permissions"`
	ExportedAt        int64  `json:"exported_at,omitempty"`
}

func (s *Service) CurrentPolicy() (Policy, error) {
	return s.CurrentPolicyForAccount("")
}

func (s *Service) CurrentPolicyForAccount(accountScopeID string) (Policy, error) {
	if s == nil {
		return DefaultPolicy(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.refreshPermissionStatePolicyLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	return state.Policy, nil
}

func (s *Service) ExportPolicyState() (ManagedPolicyState, error) {
	return s.ExportPolicyStateForAccount("")
}

func (s *Service) ExportPolicyStateForAccount(accountScopeID string) (ManagedPolicyState, error) {
	if s == nil {
		return ManagedPolicyState{Policy: DefaultPolicy(), ExportedAt: time.Now().UnixMilli()}, nil
	}
	s.mu.Lock()
	state, err := s.loadPermissionStateLocked(accountScopeID)
	s.mu.Unlock()
	if err != nil {
		return ManagedPolicyState{}, err
	}
	return ManagedPolicyState{
		Policy:            NormalizePolicy(state.Policy),
		BypassPermissions: state.BypassPermissions,
		ExportedAt:        time.Now().UnixMilli(),
	}, nil
}

func (s *Service) ApplyManagedPolicyState(state ManagedPolicyState) (ManagedPolicyState, error) {
	return s.ApplyManagedPolicyStateForAccount("", state)
}

func (s *Service) ApplyManagedPolicyStateForAccount(accountScopeID string, state ManagedPolicyState) (ManagedPolicyState, error) {
	if s == nil {
		return ManagedPolicyState{}, errors.New("permission service is not configured")
	}
	if strings.TrimSpace(accountScopeID) == "" {
		return ManagedPolicyState{}, errors.New("account scope ID is required")
	}
	if err := ValidateSubagentPolicy(state.Policy.Subagents); err != nil {
		return ManagedPolicyState{}, err
	}
	policy := NormalizePolicy(state.Policy)
	now := time.Now().UnixMilli()
	if policy.UpdatedAt <= 0 {
		policy.UpdatedAt = now
	}
	s.mu.Lock()
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		s.mu.Unlock()
		return ManagedPolicyState{}, err
	}
	s.bypassPermissions = state.BypassPermissions
	s.permissionStateCache[permissionStateCacheKey(accountScopeID)] = permissionStateCacheEntry{
		AccountScopeID:    strings.TrimSpace(accountScopeID),
		Policy:            NormalizePolicy(policy),
		BypassPermissions: state.BypassPermissions,
		LoadedAt:          now,
		PolicyUpdatedAt:   policy.UpdatedAt,
		BypassUpdatedAt:   now,
	}
	for cachedAccountScopeID, cached := range s.permissionStateCache {
		if cachedAccountScopeID == permissionStateCacheKey(accountScopeID) {
			continue
		}
		cached.BypassPermissions = state.BypassPermissions
		cached.BypassUpdatedAt = now
		cached.LoadedAt = now
		s.permissionStateCache[cachedAccountScopeID] = cached
	}
	s.mu.Unlock()
	return ManagedPolicyState{
		Policy:            policy,
		BypassPermissions: state.BypassPermissions,
		ExportedAt:        time.Now().UnixMilli(),
	}, nil
}

func (s *Service) ExplainTool(mode, toolName, toolArguments string, overlay *Policy) (PolicyExplain, error) {
	return s.ExplainToolForAccount("", mode, toolName, toolArguments, overlay)
}

func (s *Service) ExplainToolForAccount(accountScopeID, mode, toolName, toolArguments string, overlay *Policy) (PolicyExplain, error) {
	state, err := s.CurrentPermissionStateForAccount(accountScopeID)
	if err != nil {
		return PolicyExplain{}, err
	}
	policy := state.Policy
	if overlay != nil {
		policy = NormalizePolicy(Policy{
			Version: 1,
			Rules:   append(append([]PolicyRule(nil), overlay.Rules...), policy.Rules...),
		})
	}
	if state.BypassPermissions {
		mode = policyModeWithBypass(strings.TrimSpace(mode), true)
	}
	return ExplainPolicy(mode, toolName, toolArguments, policy), nil
}

func (s *Service) CurrentSubagentPolicyForAccount(accountScopeID string) (map[string]any, error) {
	policy, err := s.CurrentPolicyForAccount(accountScopeID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(policy.Subagents)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) UpdateSubagentPolicyMapForAccount(accountScopeID string, input map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var subagents SubagentPolicy
	if err := json.Unmarshal(encoded, &subagents); err != nil {
		return nil, err
	}
	_, err = s.UpdateSubagentPolicyForAccount(accountScopeID, subagents)
	if err != nil {
		return nil, err
	}
	return s.CurrentSubagentPolicyForAccount(accountScopeID)
}

func (s *Service) UpdateSubagentPolicyForAccount(accountScopeID string, subagents SubagentPolicy) (Policy, error) {
	if s == nil {
		return Policy{}, errors.New("permission service is not configured")
	}
	if err := ValidateSubagentPolicy(subagents); err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	policy := state.Policy
	policy.Subagents = subagents
	now := time.Now().UnixMilli()
	policy.UpdatedAt = now
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return Policy{}, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return NormalizePolicy(policy), nil
}

func (s *Service) UpsertRule(rule PolicyRule) (PolicyRule, error) {
	return s.UpsertRuleForAccount("", rule)
}

func (s *Service) UpsertRuleForAccount(accountScopeID string, rule PolicyRule) (PolicyRule, error) {
	if s == nil {
		return PolicyRule{}, errors.New("permission service is not configured")
	}
	now := time.Now().UnixMilli()
	normalized, ok := normalizePolicyRule(rule, now)
	if !ok {
		return PolicyRule{}, errors.New("invalid permission rule")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return PolicyRule{}, err
	}
	policy := state.Policy

	signature := policyRuleSignature(normalized)
	matched := -1
	for i, existing := range policy.Rules {
		if normalized.ID != "" && strings.TrimSpace(existing.ID) == normalized.ID {
			matched = i
			normalized.CreatedAt = existing.CreatedAt
			break
		}
		if signature != "" && signature == policyRuleSignature(existing) {
			matched = i
			normalized.ID = existing.ID
			normalized.CreatedAt = existing.CreatedAt
			break
		}
	}

	if normalized.ID == "" {
		normalized.ID = s.newPolicyRuleID(now)
	}
	if normalized.CreatedAt <= 0 {
		normalized.CreatedAt = now
	}
	normalized.UpdatedAt = now

	if matched >= 0 {
		policy.Rules[matched] = normalized
	} else {
		policy.Rules = append(policy.Rules, normalized)
	}
	policy.UpdatedAt = now
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return PolicyRule{}, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return normalized, nil
}

func (s *Service) RemoveRule(ruleID string) (bool, error) {
	return s.RemoveRuleForAccount("", ruleID)
}

func (s *Service) RemoveRuleForAccount(accountScopeID, ruleID string) (bool, error) {
	if s == nil {
		return false, errors.New("permission service is not configured")
	}
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return false, errors.New("rule id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return false, err
	}
	policy := state.Policy

	next := make([]PolicyRule, 0, len(policy.Rules))
	removed := false
	for _, rule := range policy.Rules {
		if strings.TrimSpace(rule.ID) == ruleID {
			removed = true
			continue
		}
		next = append(next, rule)
	}
	if !removed {
		return false, nil
	}
	policy.Rules = next
	now := time.Now().UnixMilli()
	policy.UpdatedAt = now
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return false, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return true, nil
}

func (s *Service) ResetPolicy() (Policy, error) {
	return s.ResetPolicyForAccount("")
}

func (s *Service) ResetPolicyForAccount(accountScopeID string) (Policy, error) {
	if s == nil {
		return Policy{}, errors.New("permission service is not configured")
	}
	policy := DefaultPolicy()
	now := time.Now().UnixMilli()
	policy.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}

	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return Policy{}, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return policy, nil
}

func (s *Service) PreviewAllowRule(sessionID, permissionID string) (PolicyRule, string, error) {
	return s.previewRule(sessionID, permissionID, PolicyDecisionAllow)
}

func (s *Service) ResolveWithPolicy(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, *PolicyRule, error) {
	return s.ResolveWithPolicyAndArguments(sessionID, permissionID, action, reason, "")
}

func (s *Service) ResolveWithPolicyAndArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, *PolicyRule, error) {
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	} else if hosted {
		result, err := s.hosted.Resolve(context.Background(), descriptor, ResolveInput{
			SessionID:         sessionID,
			PermissionID:      permissionID,
			Action:            action,
			Reason:            reason,
			ApprovedArguments: approvedArguments,
		})
		if err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		if err := s.storeMirroredPermission(result.Record); err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		return result.Record, result.SavedRule, nil
	}

	action, err := normalizeResolveAction(action)
	if err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	}

	var savedRule *PolicyRule
	if actionIsPersistent(action) {
		decision := PolicyDecisionAllow
		if actionIsDeny(action) {
			decision = PolicyDecisionDeny
		}
		rule, _, err := s.previewRule(sessionID, permissionID, decision)
		if err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		accountScopeID, err := s.accountScopeIDForSession(sessionID)
		if err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		persisted, err := s.UpsertRuleForAccount(accountScopeID, rule)
		if err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		savedRule = &persisted
	}

	record, err := s.ResolveWithArguments(sessionID, permissionID, action, reason, approvedArguments)
	if err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	}
	return record, savedRule, nil
}

func (s *Service) previewRule(sessionID, permissionID string, decision PolicyDecision) (PolicyRule, string, error) {
	record, err := s.lookupPermission(sessionID, permissionID)
	if err != nil {
		return PolicyRule{}, "", err
	}
	if !allowRuleSupported(record.ToolName) {
		return PolicyRule{}, "", fmt.Errorf("persistent permission rules are unavailable for %s", strings.TrimSpace(record.ToolName))
	}
	rule, ok := policyRuleFromToolCall(record.ToolName, record.ToolArguments, decision)
	if !ok {
		return PolicyRule{}, "", errors.New("unable to preview permission rule")
	}
	rule, ok = normalizePolicyRule(rule, time.Now().UnixMilli())
	if !ok {
		return PolicyRule{}, "", errors.New("unable to normalize permission rule")
	}
	return rule, previewPolicyRule(rule), nil
}

func (s *Service) lookupPermission(sessionID, permissionID string) (pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	permissionID = strings.TrimSpace(permissionID)
	if sessionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("session id is required")
	}
	if permissionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("permission id is required")
	}
	record, ok, err := s.store.GetPermission(sessionID, permissionID)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if !ok {
		return pebblestore.PermissionRecord{}, fmt.Errorf("permission %q not found", permissionID)
	}
	return record, nil
}

func (s *Service) accountScopeIDForSession(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	if s == nil || s.sessions == nil {
		return "", errors.New("session account scope is required for persistent permission rules")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	accountScopeID := strings.TrimSpace(session.AccountScopeID)
	if accountScopeID == "" {
		return "", errors.New("session account scope is required for persistent permission rules")
	}
	return accountScopeID, nil
}

func allowRuleSupported(toolName string) bool {
	switch normalizePolicyToolName(toolName) {
	case "exit_plan_mode", "ask_user":
		return false
	default:
		return true
	}
}

func (s *Service) newPolicyRuleID(now int64) string {
	seq := s.counter.Add(1)
	return fmt.Sprintf("rule_%d_%d", now, seq)
}

func (s *Service) persistPolicyLocked(accountScopeID string, policy Policy) error {
	if s.store == nil {
		return errors.New("permission store is not configured")
	}
	policy = NormalizePolicy(policy)
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return s.store.PutPolicyForAccount(accountScopeID, raw)
}
