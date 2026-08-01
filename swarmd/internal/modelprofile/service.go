package modelprofile

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrNotConfigured = errors.New("model profile service is not configured")
	ErrNotFound      = errors.New("model profile not found")
)

type Profile = pebblestore.ModelProfileRecord
type BulkDeleteResult = pebblestore.ModelProfileBulkDeleteResult

type ListState struct {
	Profiles         []Profile
	DefaultProfileID string
}

// Input is one flat favorite model selection.
type Input struct {
	Name        string
	Provider    string
	Model       string
	Thinking    string
	ServiceTier string
	ContextMode string
}

type Service struct {
	store *pebblestore.ModelProfileStore
	now   func() time.Time
	newID func() string
}

func NewService(store *pebblestore.ModelProfileStore) *Service {
	return &Service{
		store: store,
		now:   time.Now,
		newID: func() string { return "mp_" + strings.ReplaceAll(uuid.NewString(), "-", "") },
	}
}

func (s *Service) Create(ctx context.Context, input Input) (Profile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Profile{}, err
	}
	return s.createForAccount(principal.AccountScopeID, input)
}

// CreateFirstForAccount atomically creates the favorite only if the account still has none.
func (s *Service) CreateFirstForAccount(accountScopeID string, input Input) (Profile, bool, error) {
	if s == nil || s.store == nil {
		return Profile{}, false, ErrNotConfigured
	}
	input, err := validateInput(input)
	if err != nil {
		return Profile{}, false, err
	}
	now := s.now().UnixMilli()
	return s.store.PutForAccountIfEmpty(Profile{
		ProfileID:      s.newID(),
		AccountScopeID: strings.TrimSpace(accountScopeID),
		Name:           input.Name,
		Provider:       input.Provider,
		Model:          input.Model,
		Thinking:       input.Thinking,
		ServiceTier:    input.ServiceTier,
		ContextMode:    input.ContextMode,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
}

func (s *Service) createForAccount(accountScopeID string, input Input) (Profile, error) {
	if s == nil || s.store == nil {
		return Profile{}, ErrNotConfigured
	}
	input, err := validateInput(input)
	if err != nil {
		return Profile{}, err
	}
	now := s.now().UnixMilli()
	return s.store.PutForAccount(Profile{
		ProfileID:      s.newID(),
		AccountScopeID: strings.TrimSpace(accountScopeID),
		Name:           input.Name,
		Provider:       input.Provider,
		Model:          input.Model,
		Thinking:       input.Thinking,
		ServiceTier:    input.ServiceTier,
		ContextMode:    input.ContextMode,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
}

func (s *Service) List(ctx context.Context) ([]Profile, error) {
	state, err := s.ListState(ctx)
	return state.Profiles, err
}

func (s *Service) ListState(ctx context.Context) (ListState, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return ListState{}, err
	}
	if s == nil || s.store == nil {
		return ListState{}, ErrNotConfigured
	}
	state, err := s.store.ListStateForAccount(principal.AccountScopeID, 500)
	if err != nil {
		return ListState{}, mapStoreError(err)
	}
	return ListState{Profiles: state.Profiles, DefaultProfileID: state.DefaultProfileID}, nil
}

func (s *Service) Reorder(ctx context.Context, profileIDs []string) ([]Profile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	profiles, err := s.store.ReorderForAccount(principal.AccountScopeID, profileIDs)
	return profiles, mapStoreError(err)
}

func (s *Service) GetDefault(ctx context.Context) (Profile, bool, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Profile{}, false, err
	}
	return s.GetDefaultForAccount(principal.AccountScopeID)
}

func (s *Service) GetDefaultForAccount(accountScopeID string) (Profile, bool, error) {
	if s == nil || s.store == nil {
		return Profile{}, false, ErrNotConfigured
	}
	state, err := s.store.ListStateForAccount(strings.TrimSpace(accountScopeID), 500)
	if err != nil {
		return Profile{}, false, mapStoreError(err)
	}
	for _, profile := range state.Profiles {
		if profile.ProfileID == state.DefaultProfileID {
			return profile, true, nil
		}
	}
	return Profile{}, false, nil
}

func (s *Service) SetDefault(ctx context.Context, profileID string) (Profile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Profile{}, err
	}
	if s == nil || s.store == nil {
		return Profile{}, ErrNotConfigured
	}
	profile, err := s.store.SetDefaultForAccount(principal.AccountScopeID, profileID)
	return profile, mapStoreError(err)
}

func (s *Service) Get(ctx context.Context, profileID string) (Profile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return Profile{}, err
	}
	if s == nil || s.store == nil {
		return Profile{}, ErrNotConfigured
	}
	profile, ok, err := s.store.GetForAccount(principal.AccountScopeID, profileID)
	if err != nil {
		return Profile{}, mapStoreError(err)
	}
	if !ok {
		return Profile{}, ErrNotFound
	}
	return profile, nil
}

func (s *Service) Update(ctx context.Context, profileID string, input Input) (Profile, error) {
	current, err := s.Get(ctx, profileID)
	if err != nil {
		return Profile{}, err
	}
	input, err = validateInput(input)
	if err != nil {
		return Profile{}, err
	}
	current.Name = input.Name
	current.Provider = input.Provider
	current.Model = input.Model
	current.Thinking = input.Thinking
	current.ServiceTier = input.ServiceTier
	current.ContextMode = input.ContextMode
	current.UpdatedAt = s.now().UnixMilli()
	profile, err := s.store.PutForAccount(current)
	return profile, mapStoreError(err)
}

func (s *Service) Delete(ctx context.Context, profileID string) (bool, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return false, err
	}
	if s == nil || s.store == nil {
		return false, ErrNotConfigured
	}
	deleted, err := s.store.DeleteForAccount(principal.AccountScopeID, profileID)
	return deleted, mapStoreError(err)
}

func (s *Service) BulkDelete(ctx context.Context, profileIDs []string) (BulkDeleteResult, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return BulkDeleteResult{}, err
	}
	if s == nil || s.store == nil {
		return BulkDeleteResult{}, ErrNotConfigured
	}
	result, err := s.store.BulkDeleteForAccount(principal.AccountScopeID, profileIDs)
	return result, mapStoreError(err)
}

func requirePrincipal(ctx context.Context) (identity.Principal, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return identity.Principal{}, identity.ErrPrincipalRequired
	}
	return principal, nil
}

// ValidateInput normalizes and validates a flat favorite without saving it.
func ValidateInput(input Input) (Input, error) {
	return validateInput(input)
}

func validateInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Provider = strings.TrimSpace(input.Provider)
	input.Model = strings.TrimSpace(input.Model)
	input.Thinking = strings.TrimSpace(input.Thinking)
	input.ServiceTier = strings.TrimSpace(input.ServiceTier)
	input.ContextMode = strings.TrimSpace(input.ContextMode)
	if input.Name == "" {
		return Input{}, errors.New("model favorite name is required")
	}
	if input.Provider == "" || input.Model == "" || input.Thinking == "" {
		return Input{}, errors.New("model favorite provider, model, and thinking are required")
	}
	return input, nil
}

func mapStoreError(err error) error {
	if errors.Is(err, pebblestore.ErrModelProfileNotFound) {
		return ErrNotFound
	}
	return err
}
