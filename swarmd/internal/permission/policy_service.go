package permission

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) CurrentPolicy() (Policy, error) {
	if s == nil {
		return DefaultPolicy(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadPolicyLocked()
}

func (s *Service) ExplainTool(mode, toolName, toolArguments string, overlay *Policy) (PolicyExplain, error) {
	policy, err := s.CurrentPolicy()
	if err != nil {
		return PolicyExplain{}, err
	}
	if overlay != nil {
		policy = NormalizePolicy(Policy{
			Version: 1,
			Rules:   append(append([]PolicyRule(nil), overlay.Rules...), policy.Rules...),
		})
	}
	return ExplainPolicy(mode, toolName, toolArguments, policy), nil
}

func (s *Service) UpsertRule(rule PolicyRule) (PolicyRule, error) {
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

	policy, err := s.loadPolicyLocked()
	if err != nil {
		return PolicyRule{}, err
	}

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
	if err := s.persistPolicyLocked(policy); err != nil {
		return PolicyRule{}, err
	}
	return normalized, nil
}

func (s *Service) RemoveRule(ruleID string) (bool, error) {
	if s == nil {
		return false, errors.New("permission service is not configured")
	}
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return false, errors.New("rule id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	policy, err := s.loadPolicyLocked()
	if err != nil {
		return false, err
	}

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
	policy.UpdatedAt = time.Now().UnixMilli()
	if err := s.persistPolicyLocked(policy); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ResetPolicy() (Policy, error) {
	if s == nil {
		return Policy{}, errors.New("permission service is not configured")
	}
	policy := DefaultPolicy()
	policy.UpdatedAt = time.Now().UnixMilli()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.persistPolicyLocked(policy); err != nil {
		return Policy{}, err
	}
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
	if actionIsPersistent(action) {
		decision := PolicyDecisionAllow
		if actionIsDeny(action) {
			decision = PolicyDecisionDeny
		}
		rule, _, err := s.previewRule(sessionID, permissionID, decision)
		if err != nil {
			return pebblestore.PermissionRecord{}, nil, err
		}
		persisted, err := s.UpsertRule(rule)
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

func (s *Service) loadPolicyLocked() (Policy, error) {
	if s.store == nil {
		return DefaultPolicy(), nil
	}
	raw, ok, err := s.store.GetPolicy()
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

func (s *Service) persistPolicyLocked(policy Policy) error {
	if s.store == nil {
		return errors.New("permission store is not configured")
	}
	policy = NormalizePolicy(policy)
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return s.store.PutPolicy(raw)
}
