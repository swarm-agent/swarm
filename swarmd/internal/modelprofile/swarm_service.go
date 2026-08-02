package modelprofile

import (
	"context"
	"errors"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrSwarmModeSettingsNotFound = errors.New("swarm model settings not found")
	ErrSwarmSelectionInvalid     = errors.New("swarm model selection is invalid")
)

// SwarmSettings is the direct model assignment for the one compiled Swarm identity.
type SwarmSettings = pebblestore.SwarmModeSettingsRecord

// SwarmSettingsInput replaces both required Swarm mode selections atomically.
type SwarmSettingsInput struct {
	Action pebblestore.ModelProfileSelection
	Plan   pebblestore.ModelProfileSelection
}

type SwarmService struct {
	settings *pebblestore.SwarmModeSettingsStore
	now      func() time.Time
}

func NewSwarmService(settings *pebblestore.SwarmModeSettingsStore) *SwarmService {
	return &SwarmService{settings: settings, now: time.Now}
}

func (s *SwarmService) Get(ctx context.Context) (SwarmSettings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmSettings{}, err
	}
	if s == nil || s.settings == nil {
		return SwarmSettings{}, ErrNotConfigured
	}
	settings, found, err := s.settings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return SwarmSettings{}, err
	}
	if !found {
		return SwarmSettings{}, ErrSwarmModeSettingsNotFound
	}
	return settings, nil
}

func (s *SwarmService) Put(ctx context.Context, input SwarmSettingsInput) (SwarmSettings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmSettings{}, err
	}
	if s == nil || s.settings == nil {
		return SwarmSettings{}, ErrNotConfigured
	}
	action, err := validateSwarmSelection("Action", input.Action)
	if err != nil {
		return SwarmSettings{}, err
	}
	plan, err := validateSwarmSelection("Plan", input.Plan)
	if err != nil {
		return SwarmSettings{}, err
	}
	stored, err := s.settings.PutForAccount(SwarmSettings{
		AccountScopeID: principal.AccountScopeID,
		Action:         action,
		Plan:           plan,
		UpdatedAt:      s.now().UnixMilli(),
	})
	if err != nil {
		return SwarmSettings{}, err
	}
	return stored, nil
}

func (s *SwarmService) Update(ctx context.Context, input SwarmSettingsInput) (SwarmSettings, error) {
	return s.Put(ctx, input)
}

func validateSwarmSelection(mode string, selection pebblestore.ModelProfileSelection) (pebblestore.ModelProfileSelection, error) {
	validated, err := ValidateInput(Input{
		Name:        "Swarm " + mode,
		Provider:    selection.Provider,
		Model:       selection.Model,
		Thinking:    selection.Thinking,
		ServiceTier: selection.ServiceTier,
		ContextMode: selection.ContextMode,
	})
	if err != nil {
		return pebblestore.ModelProfileSelection{}, errors.Join(ErrSwarmSelectionInvalid, err)
	}
	return pebblestore.ModelProfileSelection{
		Provider:    strings.ToLower(strings.TrimSpace(validated.Provider)),
		Model:       validated.Model,
		Thinking:    validated.Thinking,
		ServiceTier: validated.ServiceTier,
		ContextMode: validated.ContextMode,
	}, nil
}
