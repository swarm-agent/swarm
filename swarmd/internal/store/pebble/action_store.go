package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	WorkspaceActionInputKindText   = "text"
	WorkspaceActionInputKindSecret = "secret"
)

type WorkspaceActionInput struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Kind        string   `json:"kind"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Arguments   []string `json:"arguments,omitempty"`
}

type WorkspaceAction struct {
	ID             string                 `json:"id"`
	AccountScopeID string                 `json:"account_scope_id"`
	WorkspaceID    string                 `json:"workspace_id"`
	WorkspacePath  string                 `json:"workspace_path"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description,omitempty"`
	Icon           string                 `json:"icon,omitempty"`
	Entrypoint     string                 `json:"entrypoint"`
	Arguments      []string               `json:"arguments,omitempty"`
	Inputs         []WorkspaceActionInput `json:"inputs,omitempty"`
	SortIndex      int                    `json:"sort_index"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

type WorkspaceActionStore struct {
	store *Store
	mu    sync.Mutex
}

func NewWorkspaceActionStore(store *Store) *WorkspaceActionStore {
	return &WorkspaceActionStore{store: store}
}

func (s *WorkspaceActionStore) Get(accountScopeID, workspaceID, actionID string) (WorkspaceAction, bool, error) {
	accountScopeID, workspaceID, actionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID), strings.TrimSpace(actionID)
	if accountScopeID == "" || workspaceID == "" || actionID == "" {
		return WorkspaceAction{}, false, errors.New("account scope id, workspace id, and action id are required")
	}
	var action WorkspaceAction
	ok, err := s.store.GetJSON(KeyWorkspaceActionForAccount(accountScopeID, workspaceID, actionID), &action)
	if err != nil || !ok {
		return WorkspaceAction{}, ok, err
	}
	action, err = NormalizeWorkspaceAction(action)
	if err != nil {
		return WorkspaceAction{}, false, err
	}
	if action.AccountScopeID != accountScopeID || action.WorkspaceID != workspaceID {
		return WorkspaceAction{}, false, nil
	}
	return action, true, nil
}

func (s *WorkspaceActionStore) List(accountScopeID, workspaceID string, limit int) ([]WorkspaceAction, error) {
	accountScopeID, workspaceID = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	actions := make([]WorkspaceAction, 0, minWorkspaceActionInt(limit, 100))
	err := s.store.IteratePrefix(WorkspaceActionPrefixForAccount(accountScopeID, workspaceID), limit, func(_ string, value []byte) error {
		var action WorkspaceAction
		if err := json.Unmarshal(value, &action); err != nil {
			return err
		}
		var err error
		action, err = NormalizeWorkspaceAction(action)
		if err != nil {
			return err
		}
		if action.AccountScopeID == accountScopeID && action.WorkspaceID == workspaceID {
			actions = append(actions, action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortWorkspaceActions(actions)
	return actions, nil
}

func (s *WorkspaceActionStore) Save(action WorkspaceAction) (WorkspaceAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(action)
}

func (s *WorkspaceActionStore) saveLocked(action WorkspaceAction) (WorkspaceAction, error) {
	normalized, err := NormalizeWorkspaceAction(action)
	if err != nil {
		return WorkspaceAction{}, err
	}
	if normalized.AccountScopeID == "" || normalized.WorkspaceID == "" || normalized.WorkspacePath == "" {
		return WorkspaceAction{}, errors.New("action account scope, workspace id, and workspace path are required")
	}
	if current, found, err := s.Get(normalized.AccountScopeID, normalized.WorkspaceID, normalized.ID); err != nil {
		return WorkspaceAction{}, err
	} else if found {
		normalized.CreatedAt = current.CreatedAt
	}
	if err := s.store.PutJSON(KeyWorkspaceActionForAccount(normalized.AccountScopeID, normalized.WorkspaceID, normalized.ID), normalized); err != nil {
		return WorkspaceAction{}, err
	}
	return normalized, nil
}

func (s *WorkspaceActionStore) Delete(accountScopeID, workspaceID, actionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found, err := s.Get(accountScopeID, workspaceID, actionID)
	if err != nil || !found {
		return false, err
	}
	if err := s.store.Delete(KeyWorkspaceActionForAccount(accountScopeID, workspaceID, actionID)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *WorkspaceActionStore) Reorder(accountScopeID, workspaceID string, orderedIDs []string) ([]WorkspaceAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions, err := s.List(accountScopeID, workspaceID, 10000)
	if err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return []WorkspaceAction{}, nil
	}
	if len(orderedIDs) != len(actions) {
		return nil, fmt.Errorf("ordered_ids must include every workspace action exactly once")
	}
	byID := make(map[string]WorkspaceAction, len(actions))
	for _, action := range actions {
		byID[action.ID] = action
	}
	seen := make(map[string]struct{}, len(orderedIDs))
	now := time.Now().UnixMilli()
	batch := s.store.NewBatch()
	defer batch.Close()
	ordered := make([]WorkspaceAction, 0, len(actions))
	for index, rawID := range orderedIDs {
		id := strings.TrimSpace(rawID)
		action, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("action %q does not belong to the workspace", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("ordered_ids contains duplicate action %q", id)
		}
		seen[id] = struct{}{}
		action.SortIndex, action.UpdatedAt = index, now
		raw, err := json.Marshal(action)
		if err != nil {
			return nil, err
		}
		if err := batch.Set([]byte(KeyWorkspaceActionForAccount(accountScopeID, workspaceID, id)), raw, nil); err != nil {
			return nil, err
		}
		ordered = append(ordered, action)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return ordered, nil
}

func NormalizeWorkspaceAction(action WorkspaceAction) (WorkspaceAction, error) {
	action.ID = strings.TrimSpace(action.ID)
	action.AccountScopeID = strings.TrimSpace(action.AccountScopeID)
	action.WorkspaceID = strings.TrimSpace(action.WorkspaceID)
	action.WorkspacePath = strings.TrimSpace(action.WorkspacePath)
	action.Name = strings.TrimSpace(action.Name)
	action.Description = strings.TrimSpace(action.Description)
	action.Icon = strings.TrimSpace(action.Icon)
	action.Entrypoint = filepath.Clean(strings.TrimSpace(action.Entrypoint))
	if action.ID == "" || action.Name == "" || strings.TrimSpace(action.Entrypoint) == "" {
		return WorkspaceAction{}, errors.New("action id, name, and entrypoint are required")
	}
	if filepath.IsAbs(action.Entrypoint) || action.Entrypoint == "." || action.Entrypoint == ".." || strings.HasPrefix(action.Entrypoint, ".."+string(filepath.Separator)) {
		return WorkspaceAction{}, errors.New("action entrypoint must be a workspace-relative path")
	}
	var err error
	if action.Arguments, err = normalizeWorkspaceActionStrings(action.Arguments, "argument"); err != nil {
		return WorkspaceAction{}, err
	}
	if action.Inputs, err = normalizeWorkspaceActionInputs(action.Inputs); err != nil {
		return WorkspaceAction{}, err
	}
	if action.SortIndex < 0 {
		action.SortIndex = 0
	}
	now := time.Now().UnixMilli()
	if action.CreatedAt <= 0 {
		action.CreatedAt = now
	}
	if action.UpdatedAt < action.CreatedAt {
		action.UpdatedAt = action.CreatedAt
	}
	return action, nil
}

func normalizeWorkspaceActionInputs(inputs []WorkspaceActionInput) ([]WorkspaceActionInput, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(inputs))
	out := make([]WorkspaceActionInput, 0, len(inputs))
	for _, input := range inputs {
		input.ID = strings.TrimSpace(input.ID)
		input.Label = strings.TrimSpace(input.Label)
		input.Description = strings.TrimSpace(input.Description)
		input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
		input.Placeholder = strings.TrimSpace(input.Placeholder)
		if input.ID == "" || input.Label == "" {
			return nil, errors.New("action input id and label are required")
		}
		if _, duplicate := seen[input.ID]; duplicate {
			return nil, fmt.Errorf("duplicate action input id %q", input.ID)
		}
		seen[input.ID] = struct{}{}
		if input.Kind == "" {
			input.Kind = WorkspaceActionInputKindText
		}
		if input.Kind != WorkspaceActionInputKindText && input.Kind != WorkspaceActionInputKindSecret {
			return nil, fmt.Errorf("action input %q kind must be text or secret", input.ID)
		}
		var err error
		if input.Arguments, err = normalizeWorkspaceActionStrings(input.Arguments, "input argument"); err != nil {
			return nil, err
		}
		out = append(out, input)
	}
	return out, nil
}

func normalizeWorkspaceActionStrings(values []string, label string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, len(values))
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("action %s %d contains a null byte", label, index)
		}
		out[index] = value
	}
	return out, nil
}

func sortWorkspaceActions(actions []WorkspaceAction) {
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].SortIndex != actions[j].SortIndex {
			return actions[i].SortIndex < actions[j].SortIndex
		}
		if actions[i].UpdatedAt != actions[j].UpdatedAt {
			return actions[i].UpdatedAt > actions[j].UpdatedAt
		}
		return actions[i].ID < actions[j].ID
	})
	for index := range actions {
		actions[index].SortIndex = index
	}
}

func minWorkspaceActionInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
