package session

import (
	"encoding/json"
	"errors"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ProposeAutomationV2 authenticates managed references before publishing the
// canonical pending review. Unaccepted revisions never edit active policy.
func (s *Service) ProposeAutomationV2(account, user, workspace, sessionID string, document *pebblestore.SessionPlanDocument, expected pebblestore.AutomationV2Review) (pebblestore.AutomationV2Proposal, error) {
	if s == nil || s.store == nil {
		return pebblestore.AutomationV2Proposal{}, errors.New("session store required")
	}
	if document == nil {
		return pebblestore.AutomationV2Proposal{}, errors.New("complete document required")
	}
	b, err := json.Marshal(document)
	if err != nil || len(b) > 256*1024 {
		return pebblestore.AutomationV2Proposal{}, errors.New("invalid or oversized document")
	}
	var doc pebblestore.SessionPlanDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		return pebblestore.AutomationV2Proposal{}, err
	}
	if doc.AutomationV2 == nil && doc.WorkerV2 != nil {
		doc.AutomationV2 = doc.WorkerV2
	}
	if doc.AutomationV2 == nil || doc.Automation != nil {
		return pebblestore.AutomationV2Proposal{}, errors.New("exclusive automation_v2 required")
	}
	if doc.AutomationV2.Expiration == (pebblestore.AutomationV2Expiration{}) {
		doc.AutomationV2.Expiration.Kind = "indefinite"
	}
	if doc.AutomationV2.Schedule.Kind == "" {
		doc.AutomationV2.Schedule.Kind = "trigger"
	}
	if doc.AutomationV2.SchemaVersion == 0 {
		doc.AutomationV2.SchemaVersion = 2
	}
	if !doc.AutomationV2.ActivateOnAccept {
		doc.AutomationV2.ActivateOnAccept = true
	}
	if doc.AutomationV2.Missed == "" {
		doc.AutomationV2.Missed = "skip"
	}
	if doc.AutomationV2.Overlap == "" {
		doc.AutomationV2.Overlap = "serialize"
	}
	if err := validateAutomationV2Proposal(&doc); err != nil {
		return pebblestore.AutomationV2Proposal{}, err
	}
	if err := s.authenticatePlanDocumentArtifacts(account, sessionID, &doc); err != nil {
		return pebblestore.AutomationV2Proposal{}, err
	}
	return s.store.ProposeAutomationV2(account, user, workspace, sessionID, doc, expected, validateAutomationV2Proposal)
}
func validateAutomationV2Proposal(doc *pebblestore.SessionPlanDocument) error {
	if err := ValidateExecutablePlanDocument(doc); err != nil {
		return err
	}
	if err := pebblestore.ValidateAutomationV2Settings(doc.AutomationV2, time.Now().UnixMilli()); err != nil {
		return err
	}
	if doc.ID != "" || doc.RevisionID != "" || doc.ExecutionOrigin != "" || doc.ExecutionState != nil || len(doc.OriginalCheckpoints) != 0 || (doc.Status != "" && doc.Status != "pending") {
		return errors.New("automation proposal cannot contain execution state")
	}
	for _, c := range doc.Checkpoints {
		if c.Status != "pending" || c.AttemptID != "" || c.RunID != "" || c.SessionID != "" || c.StartedAt != 0 || c.CompletedAt != 0 || len(c.Attempts) != 0 || c.Review != nil || c.Handoff != nil || c.Recommendation != nil || c.Report != "" || c.Result != "" || c.ActiveSubtaskID != "" {
			return errors.New("automation checkpoints must be unexecuted")
		}
		for _, t := range c.Subtasks {
			if (t.Status != "" && t.Status != "pending") || t.StartedAt != 0 || t.CompletedAt != 0 || t.Result != "" {
				return errors.New("automation subtasks must be unexecuted")
			}
		}
	}
	return nil
}
func (s *Service) GetAutomationV2Proposal(account, user, workspace, id string) (pebblestore.AutomationV2Proposal, bool, error) {
	return s.store.GetAutomationV2Proposal(account, user, workspace, id)
}
func (s *Service) AcceptAutomationV2(account, user, workspace, id string, review pebblestore.AutomationV2Review) (pebblestore.AutomationV2Record, error) {
	p, ok, err := s.store.GetAutomationV2Proposal(account, user, workspace, id)
	if err != nil {
		return pebblestore.AutomationV2Record{}, err
	}
	if !ok || p.AutomationV2Review != review {
		return pebblestore.AutomationV2Record{}, pebblestore.ErrAutomationV2Conflict
	}
	// A receipt replay is not a new grant: expiry and artifact availability must
	// not regenerate or revoke the original result. Store reads recheck ownership.
	if r, found, err := s.store.GetAutomationV2Record(account, user, workspace, id); err != nil {
		return pebblestore.AutomationV2Record{}, err
	} else if found && r.AutomationV2Review == review {
		return r, nil
	}
	if err := validateAutomationV2Proposal(&p.Document); err != nil {
		return pebblestore.AutomationV2Record{}, err
	}
	if err := s.authenticatePlanDocumentArtifacts(account, id, &p.Document); err != nil {
		return pebblestore.AutomationV2Record{}, err
	}
	return s.store.AcceptAutomationV2(account, user, workspace, id, review, validateAutomationV2Proposal)
}

func (s *Service) DeclineAutomationV2(account, user, workspace, id string, review pebblestore.AutomationV2Review) error {
	p, ok, err := s.store.GetAutomationV2Proposal(account, user, workspace, id)
	if err != nil {
		return err
	}
	if !ok || p.AutomationV2Review != review {
		return pebblestore.ErrAutomationV2Conflict
	}
	return s.store.DeclineAutomationV2(account, user, workspace, id, review)
}
func (s *Service) GetAutomationV2Record(account, user, workspace, id string) (pebblestore.AutomationV2Record, bool, error) {
	return s.store.GetAutomationV2Record(account, user, workspace, id)
}

func (s *Service) ListAutomationV2Records(account, user, workspace, after string, limit int, archivedMode ...string) ([]pebblestore.AutomationV2Record, string, error) {
	return s.store.ListAutomationV2Records(account, user, workspace, after, limit, archivedMode...)
}
