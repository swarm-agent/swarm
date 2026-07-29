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

type Selection = pebblestore.ModelProfileSelection
type Profile = pebblestore.ModelProfileRecord
type BulkDeleteResult = pebblestore.ModelProfileBulkDeleteResult

type ListState struct {
	Profiles         []Profile
	DefaultProfileID string
}

type Input struct {
	Name      string
	ModelMode string
	Single    *Selection
	Plan      *Selection
	Auto      *Selection
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

// CreateFirstForAccount atomically creates the profile only if the account still has none.
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
		ProfileID: s.newID(), AccountScopeID: strings.TrimSpace(accountScopeID), Name: input.Name,
		ModelMode: input.ModelMode, Single: cloneSelection(input.Single), Plan: cloneSelection(input.Plan),
		Auto: cloneSelection(input.Auto), CreatedAt: now, UpdatedAt: now,
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
	record := Profile{ProfileID: s.newID(), AccountScopeID: strings.TrimSpace(accountScopeID), Name: input.Name, ModelMode: input.ModelMode, Single: cloneSelection(input.Single), Plan: cloneSelection(input.Plan), Auto: cloneSelection(input.Auto), CreatedAt: now, UpdatedAt: now}
	return s.store.PutForAccount(record)
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
		return ListState{}, err
	}
	return ListState{Profiles: state.Profiles, DefaultProfileID: state.DefaultProfileID}, nil
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
		return Profile{}, false, err
	}
	if strings.TrimSpace(state.DefaultProfileID) == "" {
		return Profile{}, false, nil
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
	if errors.Is(err, pebblestore.ErrModelProfileNotFound) {
		return Profile{}, ErrNotFound
	}
	return profile, err
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
		return Profile{}, err
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
	current.ModelMode = input.ModelMode
	current.Single = cloneSelection(input.Single)
	current.Plan = cloneSelection(input.Plan)
	current.Auto = cloneSelection(input.Auto)
	current.UpdatedAt = s.now().UnixMilli()
	return s.store.PutForAccount(current)
}

func (s *Service) Delete(ctx context.Context, profileID string) (bool, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return false, err
	}
	if s == nil || s.store == nil {
		return false, ErrNotConfigured
	}
	return s.store.DeleteForAccount(principal.AccountScopeID, profileID)
}

func (s *Service) BulkDelete(ctx context.Context, profileIDs []string) (BulkDeleteResult, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return BulkDeleteResult{}, err
	}
	if s == nil || s.store == nil {
		return BulkDeleteResult{}, ErrNotConfigured
	}
	return s.store.BulkDeleteForAccount(principal.AccountScopeID, profileIDs)
}

func requirePrincipal(ctx context.Context) (identity.Principal, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return identity.Principal{}, identity.ErrPrincipalRequired
	}
	return principal, nil
}

// ValidateInput normalizes and validates an inline model profile without saving it.
func ValidateInput(input Input) (Input, error) {
	return validateInput(input)
}

func validateInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ModelMode = strings.ToLower(strings.TrimSpace(input.ModelMode))
	if input.Name == "" {
		return Input{}, errors.New("model profile name is required")
	}
	switch input.ModelMode {
	case pebblestore.ModelProfileModeSingle:
		if input.Single == nil || input.Plan != nil || input.Auto != nil {
			return Input{}, errors.New("single model profile requires only a single selection")
		}
		selection, err := normalizeSelection(*input.Single)
		if err != nil {
			return Input{}, err
		}
		input.Single = &selection
	case pebblestore.ModelProfileModeSplit:
		if input.Single != nil || input.Plan == nil || input.Auto == nil {
			return Input{}, errors.New("split model profile requires only plan and auto selections")
		}
		plan, err := normalizeSelection(*input.Plan)
		if err != nil {
			return Input{}, err
		}
		auto, err := normalizeSelection(*input.Auto)
		if err != nil {
			return Input{}, err
		}
		input.Plan, input.Auto = &plan, &auto
	default:
		return Input{}, errors.New("model mode must be single or split")
	}
	return input, nil
}

func normalizeSelection(selection Selection) (Selection, error) {
	selection.Provider = strings.TrimSpace(selection.Provider)
	selection.Model = strings.TrimSpace(selection.Model)
	selection.Thinking = strings.TrimSpace(selection.Thinking)
	selection.ServiceTier = strings.TrimSpace(selection.ServiceTier)
	selection.ContextMode = strings.TrimSpace(selection.ContextMode)
	if selection.Provider == "" || selection.Model == "" || selection.Thinking == "" {
		return Selection{}, errors.New("model selection provider, model, and thinking are required")
	}
	return selection, nil
}

func cloneSelection(selection *Selection) *Selection {
	if selection == nil {
		return nil
	}
	copy := *selection
	return &copy
}
