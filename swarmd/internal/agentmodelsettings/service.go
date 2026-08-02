package agentmodelsettings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrNotConfigured = errors.New("agent model settings service is not configured")
	ErrNotFound      = errors.New("agent model settings not found")
)

type Settings = pebblestore.AgentModelSettingsRecord
type Assignment = pebblestore.AgentModelAssignment

type SwarmInput struct {
	Action Assignment
	Plan   Assignment
}

type Service struct {
	store *pebblestore.AgentModelSettingsStore
	now   func() time.Time
}

func NewService(store *pebblestore.AgentModelSettingsStore) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Settings{}, err
	}
	if s == nil || s.store == nil {
		return Settings{}, ErrNotConfigured
	}
	settings, found, err := s.store.GetForAccount(strings.TrimSpace(principal.AccountScopeID))
	if err != nil {
		return Settings{}, err
	}
	if !found {
		return Settings{}, ErrNotFound
	}
	return settings, nil
}

// ReplaceSwarm atomically replaces the Action and Plan assignments for the authenticated account.
func (s *Service) ReplaceSwarm(ctx context.Context, input SwarmInput) (Settings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Settings{}, err
	}
	if s == nil || s.store == nil {
		return Settings{}, ErrNotConfigured
	}
	action, err := validateAssignment("swarm.action", input.Action)
	if err != nil {
		return Settings{}, err
	}
	plan, err := validateAssignment("swarm.plan", input.Plan)
	if err != nil {
		return Settings{}, err
	}
	return s.store.UpdateSwarmForAccount(principal.AccountScopeID, action, plan, s.now().UnixMilli())
}

// UpdateSystemAgent changes one named assignment without replacing its siblings.
func (s *Service) UpdateSystemAgent(ctx context.Context, name string, assignment Assignment) (Settings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Settings{}, err
	}
	if s == nil || s.store == nil {
		return Settings{}, ErrNotConfigured
	}
	canonicalName, err := pebblestore.NormalizeSystemAgentName(name)
	if err != nil {
		return Settings{}, err
	}
	assignment, err = validateAssignment("system_agents."+canonicalName, assignment)
	if err != nil {
		return Settings{}, err
	}
	return s.store.UpdateSystemAgentForAccount(principal.AccountScopeID, canonicalName, assignment, s.now().UnixMilli())
}

func validateAssignment(name string, assignment Assignment) (Assignment, error) {
	assignment = pebblestore.NormalizeAgentModelAssignment(assignment)
	if err := pebblestore.ValidateAgentModelAssignment(assignment); err != nil {
		return Assignment{}, fmt.Errorf("%s: %w", name, err)
	}
	return assignment, nil
}

func requirePrincipal(ctx context.Context) (identity.Principal, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return identity.Principal{}, identity.ErrPrincipalRequired
	}
	return principal, nil
}
