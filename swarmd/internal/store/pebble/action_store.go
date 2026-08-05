package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

const (
	WorkspaceActionInputKindText   = "text"
	WorkspaceActionInputKindSecret = "secret"

	maxWorkspaceActionNameBytes        = 120
	maxWorkspaceActionDescriptionBytes = 2000
	maxWorkspaceActionIconBytes        = 120
	maxWorkspaceActionEntrypointBytes  = 1024
	maxWorkspaceActionArguments        = 128
	maxWorkspaceActionArgumentBytes    = 4096
	maxWorkspaceActionInputs           = 32
	maxWorkspaceActionInputIDBytes     = 64
	maxWorkspaceActionInputLabelBytes  = 120
	maxWorkspaceActionInputTextBytes   = 2000
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
	Pinned         bool                   `json:"pinned"`
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

// Append assigns the next durable workspace order while holding the store lock.
func (s *WorkspaceActionStore) Append(action WorkspaceAction) (WorkspaceAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions, err := s.List(action.AccountScopeID, action.WorkspaceID, 10000)
	if err != nil {
		return WorkspaceAction{}, err
	}
	action.SortIndex = len(actions)
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
	actions, err := s.List(accountScopeID, workspaceID, 10000)
	if err != nil {
		return false, err
	}
	actionID = strings.TrimSpace(actionID)
	found := false
	remaining := make([]WorkspaceAction, 0, len(actions))
	for _, action := range actions {
		if action.ID == actionID {
			found = true
			continue
		}
		remaining = append(remaining, action)
	}
	if !found {
		return false, nil
	}
	now := time.Now().UnixMilli()
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeyWorkspaceActionForAccount(accountScopeID, workspaceID, actionID)), nil); err != nil {
		return false, err
	}
	for index, action := range remaining {
		if action.SortIndex == index {
			continue
		}
		action.SortIndex, action.UpdatedAt = index, now
		raw, err := json.Marshal(action)
		if err != nil {
			return false, err
		}
		if err := batch.Set([]byte(KeyWorkspaceActionForAccount(accountScopeID, workspaceID, action.ID)), raw, nil); err != nil {
			return false, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
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
	rawEntrypoint := strings.TrimSpace(action.Entrypoint)
	if action.ID == "" || action.Name == "" || rawEntrypoint == "" {
		return WorkspaceAction{}, errors.New("action id, name, and entrypoint are required")
	}
	if err := validateWorkspaceActionText(action.Name, "name", maxWorkspaceActionNameBytes, false); err != nil {
		return WorkspaceAction{}, err
	}
	if err := validateWorkspaceActionText(action.Description, "description", maxWorkspaceActionDescriptionBytes, true); err != nil {
		return WorkspaceAction{}, err
	}
	if err := validateWorkspaceActionText(action.Icon, "icon", maxWorkspaceActionIconBytes, false); err != nil {
		return WorkspaceAction{}, err
	}
	if !utf8.ValidString(rawEntrypoint) || len(rawEntrypoint) > maxWorkspaceActionEntrypointBytes || strings.IndexByte(rawEntrypoint, 0) >= 0 || strings.Contains(rawEntrypoint, "\\") || hasWorkspaceActionControl(rawEntrypoint, false) {
		return WorkspaceAction{}, errors.New("action entrypoint must be a valid workspace-relative path")
	}
	action.Entrypoint = path.Clean(rawEntrypoint)
	segments := strings.Split(rawEntrypoint, "/")
	traverses := false
	for _, segment := range segments {
		if segment == ".." {
			traverses = true
			break
		}
	}
	windowsDrive := len(rawEntrypoint) >= 2 && ((rawEntrypoint[0] >= 'a' && rawEntrypoint[0] <= 'z') || (rawEntrypoint[0] >= 'A' && rawEntrypoint[0] <= 'Z')) && rawEntrypoint[1] == ':'
	if filepath.IsAbs(rawEntrypoint) || path.IsAbs(rawEntrypoint) || windowsDrive || traverses || action.Entrypoint == "." {
		return WorkspaceAction{}, errors.New("action entrypoint must be a non-traversing workspace-relative path")
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
	if len(inputs) > maxWorkspaceActionInputs {
		return nil, fmt.Errorf("action inputs exceed limit of %d", maxWorkspaceActionInputs)
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
		if !validWorkspaceActionInputID(input.ID) {
			return nil, fmt.Errorf("action input id %q must start with a letter and contain only letters, numbers, underscores, or hyphens", input.ID)
		}
		if err := validateWorkspaceActionText(input.Label, "input label", maxWorkspaceActionInputLabelBytes, false); err != nil {
			return nil, err
		}
		if err := validateWorkspaceActionText(input.Description, "input description", maxWorkspaceActionInputTextBytes, true); err != nil {
			return nil, err
		}
		if err := validateWorkspaceActionText(input.Placeholder, "input placeholder", maxWorkspaceActionInputTextBytes, false); err != nil {
			return nil, err
		}
		if err := validateWorkspaceActionText(input.Default, "input default", maxWorkspaceActionArgumentBytes, true); err != nil {
			return nil, err
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
		if input.Kind == WorkspaceActionInputKindSecret && input.Default != "" {
			return nil, fmt.Errorf("action secret input %q cannot persist a default value", input.ID)
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
	if len(values) > maxWorkspaceActionArguments {
		return nil, fmt.Errorf("action %ss exceed limit of %d", label, maxWorkspaceActionArguments)
	}
	out := make([]string, len(values))
	for index, value := range values {
		if !utf8.ValidString(value) || len(value) > maxWorkspaceActionArgumentBytes {
			return nil, fmt.Errorf("action %s %d is invalid or exceeds %d bytes", label, index, maxWorkspaceActionArgumentBytes)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("action %s %d contains a null byte", label, index)
		}
		out[index] = value
	}
	return out, nil
}

func validateWorkspaceActionText(value, label string, maxBytes int, allowNewlines bool) error {
	if !utf8.ValidString(value) || len(value) > maxBytes || hasWorkspaceActionControl(value, allowNewlines) {
		return fmt.Errorf("action %s is invalid or exceeds %d bytes", label, maxBytes)
	}
	if !allowNewlines && strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("action %s must be a single line", label)
	}
	return nil
}

func hasWorkspaceActionControl(value string, allowNewlines bool) bool {
	for _, char := range value {
		if !unicode.IsControl(char) {
			continue
		}
		if allowNewlines && (char == '\n' || char == '\r' || char == '\t') {
			continue
		}
		return true
	}
	return false
}

func validWorkspaceActionInputID(value string) bool {
	if len(value) == 0 || len(value) > maxWorkspaceActionInputIDBytes || !utf8.ValidString(value) {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (index > 0 && char >= '0' && char <= '9') || (index > 0 && (char == '_' || char == '-')) {
			continue
		}
		return false
	}
	return true
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
