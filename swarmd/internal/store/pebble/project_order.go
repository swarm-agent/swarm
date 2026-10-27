package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func projectOrderKey(account string) string { return "project_order/account/" + keyPart(account) }

// SetProjectOrder stores account-owned presentation order, independently of project updates.
func (s *SessionStore) SetProjectOrder(account string, ids []string) error {
	if s == nil || s.store == nil {
		return errors.New("database not available")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return errors.New("account scope id is required")
	}
	s.store.projectsMu.Lock()
	mutation, err := s.setProjectOrderLocked(account, ids)
	s.store.projectsMu.Unlock()
	if err != nil {
		return err
	}
	s.store.publishProjectRealtime(mutation)
	return nil
}

func (s *SessionStore) setProjectOrderLocked(account string, ids []string) (*projectRealtimeMutation, error) {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("invalid project id")
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate project id: %s", id)
		}
		seen[id] = true
		_, found, err := s.GetProject(account, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("project not found in account")
		}
	}
	payload, _ := json.Marshal(map[string]any{"account_id": account, "action": "project_order_updated"})
	mutation := &projectRealtimeMutation{accountScopeID: account, projectID: "order", eventPayload: payload}
	if err := mutation.put(projectOrderKey(account), ids); err != nil {
		return nil, err
	}
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return nil, err
	}
	return mutation, nil
}
