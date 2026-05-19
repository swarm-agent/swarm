package identity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const defaultBackendTeamName = "Default backend team"

var (
	ErrServiceNotConfigured = errors.New("identity service is not configured")
	ErrBootstrapExists      = errors.New("identity bootstrap already exists")
)

type IDGenerator func(prefix string) (string, error)

type Service struct {
	store      *pebblestore.IdentityStore
	generateID IDGenerator
}

type Option func(*Service)

func WithIDGenerator(generate IDGenerator) Option {
	return func(s *Service) {
		s.generateID = generate
	}
}

func NewService(store *pebblestore.IdentityStore, opts ...Option) *Service {
	svc := &Service{store: store, generateID: randomIdentityID}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

type BootstrapResult struct {
	User             pebblestore.UserRecord             `json:"user"`
	Team             pebblestore.TeamRecord             `json:"team"`
	Membership       pebblestore.TeamMembershipRecord   `json:"membership"`
	CurrentSelection pebblestore.CurrentSelectionRecord `json:"current_selection"`
	Counts           pebblestore.IdentityCounts         `json:"counts"`
}

type StateSummary struct {
	Counts            pebblestore.IdentityCounts          `json:"counts"`
	CurrentUser       *pebblestore.UserRecord             `json:"current_user,omitempty"`
	CurrentTeam       *pebblestore.TeamRecord             `json:"current_team,omitempty"`
	CurrentMembership *pebblestore.TeamMembershipRecord   `json:"current_membership,omitempty"`
	CurrentSelection  *pebblestore.CurrentSelectionRecord `json:"current_selection,omitempty"`
}

func (s *Service) BootstrapFirstIdentity(username string) (BootstrapResult, error) {
	if err := s.configured(); err != nil {
		return BootstrapResult{}, err
	}
	username = pebblestore.NormalizeIdentityUsername(username)
	if username == "" {
		return BootstrapResult{}, errors.New("username is required")
	}
	if empty, err := s.store.IsIdentityNamespaceEmpty(); err != nil {
		return BootstrapResult{}, err
	} else if !empty {
		return BootstrapResult{}, fmt.Errorf("identity store is not empty: %w", ErrBootstrapExists)
	}

	userID, err := s.generateID("user")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("generate user id: %w", err)
	}
	teamID, err := s.generateID("team")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("generate team id: %w", err)
	}

	created, err := s.store.CreateBootstrapIdentityRecords(pebblestore.BootstrapIdentityRecords{
		User: pebblestore.UserRecord{
			ID:       userID,
			Username: username,
		},
		Team: pebblestore.TeamRecord{
			ID:      teamID,
			Name:    defaultBackendTeamName,
			Default: true,
		},
		Membership: pebblestore.TeamMembershipRecord{
			TeamID: teamID,
			UserID: userID,
			Role:   pebblestore.TeamRoleOwner,
		},
		CurrentSelection: pebblestore.CurrentSelectionRecord{
			UserID: userID,
			TeamID: teamID,
		},
	})
	if err != nil {
		if errors.Is(err, pebblestore.ErrIdentityRecordExists) {
			return BootstrapResult{}, fmt.Errorf("identity bootstrap exists: %w", ErrBootstrapExists)
		}
		return BootstrapResult{}, err
	}
	counts, err := s.store.IdentityCounts()
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{
		User:             created.User,
		Team:             created.Team,
		Membership:       created.Membership,
		CurrentSelection: created.CurrentSelection,
		Counts:           counts,
	}, nil
}

func (s *Service) StateSummary() (StateSummary, error) {
	if err := s.configured(); err != nil {
		return StateSummary{}, err
	}
	counts, err := s.store.IdentityCounts()
	if err != nil {
		return StateSummary{}, err
	}
	selection, ok, err := s.store.GetCurrentSelection()
	if err != nil {
		return StateSummary{}, err
	}
	summary := StateSummary{Counts: counts}
	if !ok {
		return summary, nil
	}
	summary.CurrentSelection = &selection
	user, ok, err := s.store.GetUser(selection.UserID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return StateSummary{}, fmt.Errorf("current selection user %q is missing", selection.UserID)
	}
	team, ok, err := s.store.GetTeam(selection.TeamID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return StateSummary{}, fmt.Errorf("current selection team %q is missing", selection.TeamID)
	}
	membership, ok, err := s.store.GetTeamMembership(selection.TeamID, selection.UserID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return StateSummary{}, fmt.Errorf("current selection membership team=%q user=%q is missing", selection.TeamID, selection.UserID)
	}
	summary.CurrentUser = &user
	summary.CurrentTeam = &team
	summary.CurrentMembership = &membership
	return summary, nil
}

func (s *Service) configured() error {
	if s == nil || s.store == nil || s.generateID == nil {
		return ErrServiceNotConfigured
	}
	return nil
}

func randomIdentityID(prefix string) (string, error) {
	if prefix == "" {
		return "", errors.New("identity id prefix is required")
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf[:]), nil
}
