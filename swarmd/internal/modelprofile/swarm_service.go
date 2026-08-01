package modelprofile

import (
	"context"
	"errors"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrSwarmModeSettingsNotFound           = errors.New("swarm mode settings not found")
	ErrSwarmActionFavoriteNotFound         = errors.New("swarm action favorite not found")
	ErrSwarmPlanFavoriteNotFound           = errors.New("swarm plan favorite not found")
	ErrSwarmPlanConfigurationContradictory = errors.New("swarm plan configuration is contradictory")
)

// SwarmSettings is the model assignment for the one compiled Swarm identity.
type SwarmSettings = pebblestore.SwarmModeSettingsRecord

type SwarmSettingsInput struct {
	ActionFavoriteID string
	PlanEnabled      bool
	PlanFavoriteID   string
}

type SwarmService struct {
	settings  *pebblestore.SwarmModeSettingsStore
	favorites *pebblestore.ModelProfileStore
	now       func() time.Time
}

func NewSwarmService(settings *pebblestore.SwarmModeSettingsStore, favorites *pebblestore.ModelProfileStore) *SwarmService {
	return &SwarmService{settings: settings, favorites: favorites, now: time.Now}
}

// Get returns the account's configured Action and optional Plan favorite assignments.
// Referenced favorites are checked on every read so persisted dangling settings fail
// explicitly instead of silently falling back to another model.
func (s *SwarmService) Get(ctx context.Context) (SwarmSettings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmSettings{}, err
	}
	if s == nil || s.settings == nil || s.favorites == nil {
		return SwarmSettings{}, ErrNotConfigured
	}

	settings, found, err := s.settings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return SwarmSettings{}, mapSwarmModeStoreError(err)
	}
	if !found {
		return SwarmSettings{}, ErrSwarmModeSettingsNotFound
	}
	if err := s.verifyFavorites(principal.AccountScopeID, settings); err != nil {
		return SwarmSettings{}, err
	}
	return settings, nil
}

// Put replaces the account's model assignments after validating that every
// referenced favorite belongs to that same account.
func (s *SwarmService) Put(ctx context.Context, input SwarmSettingsInput) (SwarmSettings, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmSettings{}, err
	}
	if s == nil || s.settings == nil || s.favorites == nil {
		return SwarmSettings{}, ErrNotConfigured
	}

	settings := SwarmSettings{
		AccountScopeID:   principal.AccountScopeID,
		ActionFavoriteID: strings.TrimSpace(input.ActionFavoriteID),
		PlanEnabled:      input.PlanEnabled,
		PlanFavoriteID:   strings.TrimSpace(input.PlanFavoriteID),
		UpdatedAt:        s.now().UnixMilli(),
	}
	if err := validateSwarmSettingsShape(settings); err != nil {
		return SwarmSettings{}, err
	}
	if err := s.verifyFavorites(principal.AccountScopeID, settings); err != nil {
		return SwarmSettings{}, err
	}
	stored, err := s.settings.PutForAccount(settings)
	if err != nil {
		return SwarmSettings{}, mapSwarmModeStoreError(err)
	}
	return stored, nil
}

// Update is an explicit replacement alias for callers that describe settings
// mutation as an update rather than a put.
func (s *SwarmService) Update(ctx context.Context, input SwarmSettingsInput) (SwarmSettings, error) {
	return s.Put(ctx, input)
}

func (s *SwarmService) verifyFavorites(accountScopeID string, settings SwarmSettings) error {
	if _, found, err := s.favorites.GetForAccount(accountScopeID, settings.ActionFavoriteID); err != nil {
		return err
	} else if !found {
		return ErrSwarmActionFavoriteNotFound
	}
	if !settings.PlanEnabled {
		return nil
	}
	if _, found, err := s.favorites.GetForAccount(accountScopeID, settings.PlanFavoriteID); err != nil {
		return err
	} else if !found {
		return ErrSwarmPlanFavoriteNotFound
	}
	return nil
}

func validateSwarmSettingsShape(settings SwarmSettings) error {
	switch {
	case strings.TrimSpace(settings.ActionFavoriteID) == "":
		return pebblestore.ErrSwarmModeActionFavoriteIDRequired
	case settings.PlanEnabled && strings.TrimSpace(settings.PlanFavoriteID) == "":
		return errors.Join(ErrSwarmPlanConfigurationContradictory, pebblestore.ErrSwarmModePlanFavoriteIDRequired)
	case !settings.PlanEnabled && strings.TrimSpace(settings.PlanFavoriteID) != "":
		return errors.Join(ErrSwarmPlanConfigurationContradictory, pebblestore.ErrSwarmModePlanFavoriteIDUnexpected)
	default:
		return nil
	}
}

func mapSwarmModeStoreError(err error) error {
	switch {
	case errors.Is(err, pebblestore.ErrSwarmModePlanFavoriteIDRequired),
		errors.Is(err, pebblestore.ErrSwarmModePlanFavoriteIDUnexpected):
		return errors.Join(ErrSwarmPlanConfigurationContradictory, err)
	default:
		return err
	}
}
