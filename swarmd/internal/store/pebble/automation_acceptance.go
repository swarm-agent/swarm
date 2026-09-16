package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
)

// CreateAutomationApprovalWithSession publishes the grant and permanent binding
// in one V3 transaction. Lock ordering remains automation -> session. No grant
// or session identity becomes visible when validation or the durable commit fails.
func (s *Store) CreateAutomationApprovalWithSession(g AutomationApproval, in V3SessionMutationInput) (AutomationApproval, error) {
	if in.SessionID == "" || in.AccountScopeID != g.Scope.AccountID || in.UserID != g.SubjectID || in.Kind != V3SessionMutationUpdateMetadata || in.Session != nil || in.PlanSave != nil || in.RunIntent != nil || in.AutomationDefinitionRevision != g.DefinitionRevision {
		return AutomationApproval{}, ErrAutomationInvalid
	}
	key, err := approvalKey(g.Scope, g.ID)
	if err != nil || !automationValidID(g.AutomationID) || !automationValidID(g.SubjectID) || g.DefinitionRevision == 0 || g.WrittenAt <= 0 || g.ExpiresAt <= g.WrittenAt || g.Revision != 0 || g.RevokedAt != 0 {
		return AutomationApproval{}, ErrAutomationInvalid
	}
	m := &automationRealtimeMutation{scope: g.Scope, writes: map[string]json.RawMessage{}}
	defer s.publishAutomationRealtime(m)
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	d, found, err := s.GetAutomationRecord(g.Scope, g.AutomationID, "definition", g.AutomationID, 0)
	if err != nil {
		return AutomationApproval{}, err
	}
	if !found || d.Revision != g.DefinitionRevision || d.Definition == nil || d.Definition.SessionID != in.SessionID || d.Definition.Authorization.ExpiresAt != g.ExpiresAt {
		return AutomationApproval{}, ErrAutomationConflict
	}
	policy := *d.Definition
	policy.Enabled = false
	policy.Authorization.Mode = "approval_required"
	policy.Authorization.ApprovalReference = ""
	raw, err := json.Marshal(policy)
	if err != nil {
		return AutomationApproval{}, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != g.PolicySHA256 {
		return AutomationApproval{}, ErrAutomationConflict
	}
	// A deterministic receipt makes retries return the original grant, not a new
	// nonce or a grant which was never persisted by a replayed session mutation.
	receiptID := fmt.Sprintf("accept:%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", g.AutomationID, g.DefinitionRevision, g.SubjectID))))
	receiptKey, _ := approvalKey(g.Scope, receiptID)
	var receipt struct {
		Grant    AutomationApproval       `json:"grant"`
		Proposal *AutomationPlanReference `json:"proposal,omitempty"`
	}
	if ok, err := s.GetJSON(receiptKey+":receipt", &receipt); err != nil {
		return AutomationApproval{}, err
	} else if ok {
		if in.AutomationPermission != nil {
			return AutomationApproval{}, ErrAutomationConflict
		}
		current, exists, err := s.GetAutomationApproval(g.Scope, receipt.Grant.ID)
		if err != nil {
			return AutomationApproval{}, err
		}
		if !reflect.DeepEqual(receipt.Proposal, in.AutomationProposal) || !exists || current.RevokedAt != 0 || current.ExpiresAt <= g.WrittenAt || current.PolicySHA256 != g.PolicySHA256 {
			return AutomationApproval{}, ErrAutomationConflict
		}
		return current, nil
	}
	if _, exists, err := s.GetAutomationApproval(g.Scope, g.ID); err != nil {
		return AutomationApproval{}, err
	} else if exists {
		return AutomationApproval{}, ErrAutomationConflict
	}
	g.Revision = 1
	for k, v := range map[string]AutomationApproval{key + ":head": g, automationRevisionKey(key, 1): g} {
		if err := m.put(k, v); err != nil {
			return AutomationApproval{}, err
		}
	}
	receipt.Grant, receipt.Proposal = g, in.AutomationProposal
	if err := m.put(receiptKey+":receipt", receipt); err != nil {
		return AutomationApproval{}, err
	}
	in.automationRealtime = m
	in.automationAcceptance = &g
	in.ClientRequestID = receiptKey
	in.IdempotencyKey = receiptKey
	in.PayloadHash = g.PolicySHA256
	in.RequestHash = g.PolicySHA256
	in.NowUnixMs = g.WrittenAt
	result, err := NewSessionStore(s).ApplyV3SessionMutation(in)
	if err != nil {
		return AutomationApproval{}, err
	}
	m.outbox = result.RealtimeOutbox
	return g, nil
}
