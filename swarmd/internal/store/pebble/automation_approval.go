package pebblestore

import (
	"encoding/json"
)

// AutomationApproval is internal authorization state, never writable through the
// generic automation mutation API. Revocation is terminal; replacement needs a new ID.
type AutomationApproval struct {
	Scope              AutomationScope `json:"scope"`
	ID                 string          `json:"id"`
	AutomationID       string          `json:"automation_id"`
	DefinitionRevision uint64          `json:"definition_revision"`
	PolicySHA256       string          `json:"policy_sha256"`
	SubjectID          string          `json:"subject_id"`
	ExpiresAt          int64           `json:"expires_at"`
	WrittenAt          int64           `json:"written_at"`
	RevokedAt          int64           `json:"revoked_at,omitempty"`
	Revision           uint64          `json:"revision"`
}

func approvalKey(scope AutomationScope, id string) (string, error) {
	prefix, err := automationPrefix(scope)
	if err != nil || !automationValidID(id) {
		return "", ErrAutomationInvalid
	}
	return prefix + "approval:" + automationPart(id), nil
}

func (s *Store) GetAutomationApproval(scope AutomationScope, id string) (AutomationApproval, bool, error) {
	var grant AutomationApproval
	key, err := approvalKey(scope, id)
	if err != nil {
		return grant, false, err
	}
	found, err := s.GetJSON(key+":head", &grant)
	return grant, found, err
}

// CreateAutomationApproval is create-only CAS. The caller authenticates the user;
// storage serializes against definition edits and rejects stale reviewed revisions.
func (s *Store) CreateAutomationApproval(g AutomationApproval) (AutomationApproval, error) {
	key, err := approvalKey(g.Scope, g.ID)
	if err != nil || !automationValidID(g.AutomationID) || !automationValidID(g.SubjectID) || len(g.PolicySHA256) != 64 || g.DefinitionRevision == 0 || g.WrittenAt <= 0 || g.ExpiresAt <= g.WrittenAt || g.Revision != 0 || g.RevokedAt != 0 {
		return AutomationApproval{}, ErrAutomationInvalid
	}
	participant := &automationRealtimeMutation{scope: g.Scope, writes: map[string]json.RawMessage{}}
	defer s.publishAutomationRealtime(participant)
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	var old AutomationApproval
	found, err := s.GetJSON(key+":head", &old)
	if err != nil {
		return AutomationApproval{}, err
	}
	if found {
		return AutomationApproval{}, ErrAutomationConflict
	}
	d, found, err := s.GetAutomationRecord(g.Scope, g.AutomationID, "definition", g.AutomationID, 0)
	if err != nil {
		return AutomationApproval{}, err
	}
	if !found || d.Revision != g.DefinitionRevision {
		return AutomationApproval{}, ErrAutomationConflict
	}
	g.Revision = 1
	return s.writeAutomationApproval(key, g, participant)
}

func (s *Store) RevokeAutomationApproval(scope AutomationScope, id, subject string, expected uint64, now int64) (AutomationApproval, error) {
	key, err := approvalKey(scope, id)
	if err != nil || !automationValidID(subject) || expected == 0 || now <= 0 {
		return AutomationApproval{}, ErrAutomationInvalid
	}
	participant := &automationRealtimeMutation{scope: scope, writes: map[string]json.RawMessage{}}
	defer s.publishAutomationRealtime(participant)
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	g, found, err := s.GetAutomationApproval(scope, id)
	if err != nil {
		return AutomationApproval{}, err
	}
	if !found || g.SubjectID != subject || g.Revision != expected || g.RevokedAt != 0 || expected == ^uint64(0) || now < g.WrittenAt {
		return AutomationApproval{}, ErrAutomationConflict
	}
	g.Revision++
	g.RevokedAt = now
	return s.writeAutomationApproval(key, g, participant)
}

func (s *Store) writeAutomationApproval(key string, g AutomationApproval, participant *automationRealtimeMutation) (AutomationApproval, error) {
	if err := participant.put(key+":head", g); err != nil {
		return AutomationApproval{}, err
	}
	if err := participant.put(automationRevisionKey(key, g.Revision), g); err != nil {
		return AutomationApproval{}, err
	}
	if err := s.commitAutomationRealtime(participant); err != nil {
		return AutomationApproval{}, err
	}
	return g, nil
}
