package permission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
			Version:        1,
			BashProfile:    policy.BashProfile,
			Subagents:      policy.Subagents,
			SessionDeploy:  policy.SessionDeploy,
			PlanAcceptance: policy.PlanAcceptance,
			Rules:          append(append([]PolicyRule(nil), overlay.Rules...), policy.Rules...),
		})
	}
	if state.BypassPermissions {
		mode = policyModeWithBypass(strings.TrimSpace(mode), true)
	}
	return ExplainPolicy(mode, toolName, toolArguments, policy), nil
}

func (s *Service) UpdateBashApprovalProfileForAccount(accountScopeID string, profile BashApprovalProfile) (Policy, error) {
	if s == nil {
		return Policy{}, errors.New("permission service is not configured")
	}
	profile = BashApprovalProfile(strings.TrimSpace(strings.ToLower(string(profile))))
	if err := ValidateBashApprovalProfile(profile); err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	policy := state.Policy
	policy.BashProfile = profile
	now := time.Now().UnixMilli()
	policy.UpdatedAt = now
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return Policy{}, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return NormalizePolicy(policy), nil
}

func (s *Service) UpdateCapabilityPoliciesForAccount(accountScopeID string, sessionDeploy SessionDeployPolicy, planAcceptance PlanAcceptancePolicy) (Policy, error) {
	if s == nil {
		return Policy{}, errors.New("permission service is not configured")
	}
	if err := ValidateSessionDeployPolicy(sessionDeploy); err != nil {
		return Policy{}, err
	}
	if err := ValidatePlanAcceptancePolicy(planAcceptance); err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadPermissionStateLocked(accountScopeID)
	if err != nil {
		return Policy{}, err
	}
	policy := state.Policy
	policy.SessionDeploy = sessionDeploy
	policy.PlanAcceptance = planAcceptance
	now := time.Now().UnixMilli()
	policy.UpdatedAt = now
	if err := s.persistPolicyLocked(accountScopeID, policy); err != nil {
		return Policy{}, err
	}
	s.cachePermissionStateLocked(accountScopeID, policy, state.BypassPermissions, now, state.BypassUpdatedAt)
	return NormalizePolicy(policy), nil
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
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&subagents); err != nil {
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
	action, err := normalizeResolveAction(action)
	if err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	}

	var savedRule *PolicyRule
	if action == ActionAllowAlways {
		pending, lookupErr := s.lookupPermission(sessionID, permissionID)
		if lookupErr != nil {
			return pebblestore.PermissionRecord{}, nil, lookupErr
		}
		requirement := authorizationRequirement(pending.Mode, pending.ToolName, pending.ToolArguments)
		if requirement == "session_deploy" || requirement == "plan_acceptance" || IsPlanAcceptanceLifecycleRequirement(requirement) {
			accountScopeID, scopeErr := s.accountScopeIDForSession(sessionID)
			if scopeErr != nil {
				return pebblestore.PermissionRecord{}, nil, scopeErr
			}
			policy, policyErr := s.CurrentPolicyForAccount(accountScopeID)
			if policyErr != nil {
				return pebblestore.PermissionRecord{}, nil, policyErr
			}
			if requirement == "session_deploy" {
				policy.SessionDeploy.Mode = CapabilityModeAlwaysAllow
			} else {
				policy.PlanAcceptance.Mode = CapabilityModeAlwaysAllow
			}
			if _, policyErr = s.UpdateCapabilityPoliciesForAccount(accountScopeID, policy.SessionDeploy, policy.PlanAcceptance); policyErr != nil {
				return pebblestore.PermissionRecord{}, nil, policyErr
			}
			record, resolveErr := s.ResolveWithArguments(sessionID, permissionID, action, reason, approvedArguments)
			return record, nil, resolveErr
		}
	}
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
	case "exit_plan_mode", "plan_acceptance", "session_deploy", "ask_user":
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
