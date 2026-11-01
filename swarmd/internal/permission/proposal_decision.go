package permission

import (
	"errors"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// RequirementProposalDecision marks a pending request in the proposing
// conversation that asks the person to accept or discard a workflow or
// worker proposal (here or on a linked machine). It is a review resource,
// not a suspended tool call: it carries no run, so no run waits on it.
//
// Only the person's own answer resolves it (ResolveProposalDecision, after
// the decision was applied through the proposals service). Every other
// resolve path refuses it: resolve_permission and resolve_task_permission,
// resolve-all, persistent rules and any AI key.
const RequirementProposalDecision = "proposal_decision"

// ErrProposalDecisionPersonOnly is the refusal of a proposal decision through
// any path but the person's own answer.
var ErrProposalDecisionPersonOnly = errors.New("this pending request is a workflow or worker proposal: only the person decides it, by answering it in their Swarm app (Accept or Discard) or with the app's accept/discard; an AI cannot decide a proposal, and a lasting rule cannot either")

// IsProposalDecision reports whether record asks the person to decide a
// proposal.
func IsProposalDecision(record pebblestore.PermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), RequirementProposalDecision)
}

// RequestProposalDecision creates (or refreshes, for a revised proposal) the
// pending request in sessionID. key names the proposal (its kind, machine
// and id), so a re-proposal updates the same request rather than adding one.
// arguments is the request's JSON payload: what the person sees.
func (s *Service) RequestProposalDecision(sessionID, key, toolName, arguments string) (pebblestore.PermissionRecord, error) {
	if s == nil {
		return pebblestore.PermissionRecord{}, errors.New("permission service is not configured")
	}
	if strings.TrimSpace(key) == "" {
		return pebblestore.PermissionRecord{}, errors.New("proposal key is required")
	}
	return s.CreatePending(CreateInput{SessionID: sessionID, CallID: key, ToolName: toolName, ToolArguments: arguments, Requirement: RequirementProposalDecision, Mode: "auto"})
}

// ResolveProposalDecision records the person's decision on a pending
// proposal request. The caller has already applied the decision (or found
// the proposal stale): action is allow_once (accepted), deny_once
// (discarded) or cancel (stale or decided elsewhere), with reason.
func (s *Service) ResolveProposalDecision(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, error) {
	if s == nil {
		return pebblestore.PermissionRecord{}, errors.New("permission service is not configured")
	}
	action, err := normalizeResolveAction(action)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if actionIsPersistent(action) {
		return pebblestore.PermissionRecord{}, ErrProposalDecisionPersonOnly
	}
	record, ok, err := s.store.GetPermission(strings.TrimSpace(sessionID), strings.TrimSpace(permissionID))
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if !ok || !IsProposalDecision(record) {
		return pebblestore.PermissionRecord{}, errors.New("not a pending proposal request")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposalDecisionUnlocked = true
	defer func() { s.proposalDecisionUnlocked = false }()
	updated, _, err := s.resolveLocked(record.SessionID, record.ID, action, reason, "", time.Now().UnixMilli())
	return updated, err
}
