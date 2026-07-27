package modelprofile

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var ErrSwarmProfileNotFound = errors.New("swarm profile not found")

type SwarmMemberInput struct {
	AgentID   string
	ModelMode string
	Single    *Selection
	Plan      *Selection
	Auto      *Selection
}

type SwarmInput struct {
	Name    string
	Members []SwarmMemberInput
}

type SwarmProfile = pebblestore.SwarmProfileRecord

type SwarmService struct {
	store *pebblestore.SwarmProfileStore
	now   func() time.Time
	newID func() string
}

func NewSwarmService(store *pebblestore.SwarmProfileStore) *SwarmService {
	return &SwarmService{store: store, now: time.Now, newID: func() string { return "sp_" + strings.ReplaceAll(uuid.NewString(), "-", "") }}
}

func (s *SwarmService) Create(ctx context.Context, input SwarmInput) (SwarmProfile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmProfile{}, err
	}
	if s == nil || s.store == nil {
		return SwarmProfile{}, ErrNotConfigured
	}
	input, err = validateSwarmInput(input)
	if err != nil {
		return SwarmProfile{}, err
	}
	now := s.now().UnixMilli()
	return s.store.PutForAccount(SwarmProfile{ProfileID: s.newID(), AccountScopeID: principal.AccountScopeID, Name: input.Name, Members: swarmMembers(input.Members), CreatedAt: now, UpdatedAt: now})
}

func (s *SwarmService) List(ctx context.Context) ([]SwarmProfile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	return s.store.ListForAccount(principal.AccountScopeID, 500)
}

func (s *SwarmService) Get(ctx context.Context, profileID string) (SwarmProfile, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return SwarmProfile{}, err
	}
	if s == nil || s.store == nil {
		return SwarmProfile{}, ErrNotConfigured
	}
	profile, ok, err := s.store.GetForAccount(principal.AccountScopeID, profileID)
	if err != nil {
		return SwarmProfile{}, err
	}
	if !ok {
		return SwarmProfile{}, ErrSwarmProfileNotFound
	}
	return profile, nil
}

func (s *SwarmService) Update(ctx context.Context, profileID string, input SwarmInput) (SwarmProfile, error) {
	current, err := s.Get(ctx, profileID)
	if err != nil {
		return SwarmProfile{}, err
	}
	input, err = validateSwarmInput(input)
	if err != nil {
		return SwarmProfile{}, err
	}
	current.Name, current.Members, current.UpdatedAt = input.Name, swarmMembers(input.Members), s.now().UnixMilli()
	return s.store.PutForAccount(current)
}

func (s *SwarmService) Delete(ctx context.Context, profileID string) (bool, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return false, err
	}
	if s == nil || s.store == nil {
		return false, ErrNotConfigured
	}
	return s.store.DeleteForAccount(principal.AccountScopeID, profileID)
}

func validateSwarmInput(input SwarmInput) (SwarmInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return SwarmInput{}, errors.New("swarm profile name is required")
	}
	if len(input.Members) == 0 {
		return SwarmInput{}, errors.New("swarm profile requires at least one member")
	}
	seen := make(map[string]struct{}, len(input.Members))
	for i := range input.Members {
		member := &input.Members[i]
		member.AgentID = strings.ToLower(strings.TrimSpace(member.AgentID))
		if member.AgentID == "" {
			return SwarmInput{}, errors.New("swarm profile member agent_id is required")
		}
		if _, ok := seen[member.AgentID]; ok {
			return SwarmInput{}, errors.New("swarm profile member agent_id must be unique")
		}
		seen[member.AgentID] = struct{}{}
		validated, err := validateInput(Input{ModelMode: member.ModelMode, Single: member.Single, Plan: member.Plan, Auto: member.Auto, Name: member.AgentID})
		if err != nil {
			return SwarmInput{}, err
		}
		member.ModelMode, member.Single, member.Plan, member.Auto = validated.ModelMode, validated.Single, validated.Plan, validated.Auto
	}
	return input, nil
}

func swarmMembers(inputs []SwarmMemberInput) []pebblestore.SwarmProfileMember {
	out := make([]pebblestore.SwarmProfileMember, len(inputs))
	for i, member := range inputs {
		out[i] = pebblestore.SwarmProfileMember{AgentID: member.AgentID, ModelMode: member.ModelMode, Single: cloneSelection(member.Single), Plan: cloneSelection(member.Plan), Auto: cloneSelection(member.Auto)}
	}
	return out
}
