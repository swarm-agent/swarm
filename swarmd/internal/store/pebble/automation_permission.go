package pebblestore

import (
	"encoding/json"
	"reflect"

	"github.com/cockroachdb/pebble"
)

// AutomationPermissionResolution is prepared only while the permission service
// owns its mutex; the store commits it with the conversion, never independently.
type AutomationPermissionResolution struct {
	Previous PermissionRecord
	Record   PermissionRecord
	Summary  PermissionSummary
}

func (s *SessionStore) setAutomationPermissionInBatch(batch *pebble.Batch, in V3SessionMutationInput) error {
	p := in.AutomationPermission
	if p == nil {
		return nil
	}
	if in.automationAcceptance == nil || in.AutomationProposal == nil || p.Previous.SessionID != in.SessionID || p.Record.SessionID != in.SessionID || p.Record.ID != p.Previous.ID || p.Previous.Status != PermissionStatusPending || p.Record.Status != PermissionStatusCancelled || p.Summary.SessionID != in.SessionID || p.Summary.AccountScopeID != in.AccountScopeID || p.Summary.PrincipalID != in.UserID {
		return ErrAutomationConflict
	}
	permissions := NewPermissionStore(s.store)
	current, found, err := permissions.GetPermission(in.SessionID, p.Previous.ID)
	if err != nil {
		return err
	}
	if !found || !reflect.DeepEqual(current, p.Previous) {
		return ErrAutomationConflict
	}
	raw, err := json.Marshal(sanitizePermissionRecord(p.Record))
	if err != nil {
		return err
	}
	if err := putPermissionRecordInBatch(batch, p.Record, &p.Previous, raw); err != nil {
		return err
	}
	var previous PermissionSummary
	found, err = s.store.GetJSON(KeyPermissionSummary(p.Summary.PrincipalID, in.SessionID), &previous)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(p.Summary)
	if err != nil {
		return err
	}
	if err := putPermissionSummaryInBatch(batch, p.Summary, raw, previous, found); err != nil {
		return err
	}
	if p.Record.RunID == "" {
		return nil
	}
	wait, found, err := permissions.GetRunWait(in.SessionID, p.Record.RunID)
	if err != nil || !found {
		return err
	}
	next := make([]string, 0, len(wait.PendingPermissionIDs))
	for _, id := range wait.PendingPermissionIDs {
		if id != p.Record.ID {
			next = append(next, id)
		}
	}
	key := []byte(KeyRunWait(in.SessionID, p.Record.RunID))
	if len(next) == 0 {
		return batch.Delete(key, nil)
	}
	wait.PendingPermissionIDs, wait.UpdatedAt = next, p.Record.UpdatedAt
	raw, err = json.Marshal(wait)
	if err != nil {
		return err
	}
	return batch.Set(key, raw, nil)
}
