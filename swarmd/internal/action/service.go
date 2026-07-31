package action

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Service struct {
	store *pebblestore.WorkspaceActionStore
}

type Scope struct {
	AccountScopeID string
	WorkspaceID    string
	WorkspacePath  string
}

type CreateInput struct {
	Scope
	Name        string
	Description string
	Icon        string
	Entrypoint  string
	Arguments   []string
	Inputs      []pebblestore.WorkspaceActionInput
}

type UpdateInput struct {
	Scope
	ID          string
	Name        *string
	Description *string
	Icon        *string
	Entrypoint  *string
	Arguments   *[]string
	Inputs      *[]pebblestore.WorkspaceActionInput
}

func NewService(store *pebblestore.WorkspaceActionStore) *Service {
	return &Service{store: store}
}

func (s *Service) List(scope Scope) ([]pebblestore.WorkspaceAction, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	actions, err := s.store.List(scope.AccountScopeID, scope.WorkspaceID, 1000)
	if err != nil {
		return nil, err
	}
	for _, action := range actions {
		if action.WorkspacePath != scope.WorkspacePath {
			return nil, errors.New("action canonical workspace path no longer matches")
		}
	}
	return actions, nil
}

func (s *Service) Get(scope Scope, id string) (pebblestore.WorkspaceAction, bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return pebblestore.WorkspaceAction{}, false, err
	}
	action, found, err := s.store.Get(scope.AccountScopeID, scope.WorkspaceID, strings.TrimSpace(id))
	if err != nil || !found {
		return pebblestore.WorkspaceAction{}, found, err
	}
	if action.WorkspacePath != scope.WorkspacePath {
		return pebblestore.WorkspaceAction{}, false, errors.New("action canonical workspace path no longer matches")
	}
	return action, true, nil
}

func (s *Service) Create(input CreateInput) (pebblestore.WorkspaceAction, error) {
	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return pebblestore.WorkspaceAction{}, err
	}
	actions, err := s.store.List(scope.AccountScopeID, scope.WorkspaceID, 1000)
	if err != nil {
		return pebblestore.WorkspaceAction{}, err
	}
	now := time.Now().UnixMilli()
	action := pebblestore.WorkspaceAction{
		ID:             newActionID(),
		AccountScopeID: scope.AccountScopeID,
		WorkspaceID:    scope.WorkspaceID,
		WorkspacePath:  scope.WorkspacePath,
		Name:           input.Name,
		Description:    input.Description,
		Icon:           input.Icon,
		Entrypoint:     input.Entrypoint,
		Arguments:      append([]string(nil), input.Arguments...),
		Inputs:         append([]pebblestore.WorkspaceActionInput(nil), input.Inputs...),
		SortIndex:      len(actions),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return s.store.Save(action)
}

func (s *Service) Update(input UpdateInput) (pebblestore.WorkspaceAction, error) {
	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return pebblestore.WorkspaceAction{}, err
	}
	action, found, err := s.store.Get(scope.AccountScopeID, scope.WorkspaceID, input.ID)
	if err != nil {
		return pebblestore.WorkspaceAction{}, err
	}
	if !found {
		return pebblestore.WorkspaceAction{}, fmt.Errorf("action %q not found", strings.TrimSpace(input.ID))
	}
	if action.WorkspacePath != scope.WorkspacePath {
		return pebblestore.WorkspaceAction{}, errors.New("action canonical workspace path no longer matches")
	}
	if input.Name != nil {
		action.Name = *input.Name
	}
	if input.Description != nil {
		action.Description = *input.Description
	}
	if input.Icon != nil {
		action.Icon = *input.Icon
	}
	if input.Entrypoint != nil {
		action.Entrypoint = *input.Entrypoint
	}
	if input.Arguments != nil {
		action.Arguments = append([]string(nil), (*input.Arguments)...)
	}
	if input.Inputs != nil {
		action.Inputs = append([]pebblestore.WorkspaceActionInput(nil), (*input.Inputs)...)
	}
	action.UpdatedAt = time.Now().UnixMilli()
	return s.store.Save(action)
}

func (s *Service) Delete(scope Scope, id string) (bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return false, err
	}
	action, found, err := s.store.Get(scope.AccountScopeID, scope.WorkspaceID, strings.TrimSpace(id))
	if err != nil || !found {
		return false, err
	}
	if action.WorkspacePath != scope.WorkspacePath {
		return false, errors.New("action canonical workspace path no longer matches")
	}
	return s.store.Delete(scope.AccountScopeID, scope.WorkspaceID, action.ID)
}

func (s *Service) Reorder(scope Scope, orderedIDs []string) ([]pebblestore.WorkspaceAction, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	if _, err := s.List(scope); err != nil {
		return nil, err
	}
	return s.store.Reorder(scope.AccountScopeID, scope.WorkspaceID, orderedIDs)
}

func normalizeScope(scope Scope) (Scope, error) {
	scope.AccountScopeID = strings.TrimSpace(scope.AccountScopeID)
	scope.WorkspaceID = strings.TrimSpace(scope.WorkspaceID)
	scope.WorkspacePath = strings.TrimSpace(scope.WorkspacePath)
	if scope.AccountScopeID == "" || scope.WorkspaceID == "" || scope.WorkspacePath == "" {
		return Scope{}, errors.New("canonical account-owned workspace scope is required")
	}
	return scope, nil
}

func newActionID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "action_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("action_%d", time.Now().UnixNano())
}
