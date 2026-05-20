package identity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrServiceNotConfigured  = errors.New("identity service is not configured")
	ErrBootstrapExists       = errors.New("identity bootstrap already exists")
	ErrTeamAlreadyExists     = errors.New("account scope already has a team")
	ErrTeamOptInUnauthorized = errors.New("team opt-in requires account owner/admin capability")
)

const defaultBackendTeamName = "Personal"

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
	AccountScope     pebblestore.AccountScopeRecord     `json:"account_scope"`
	AccountUser      pebblestore.AccountUserRecord      `json:"account_user"`
	CurrentSelection pebblestore.CurrentSelectionRecord `json:"current_selection"`
	Counts           pebblestore.IdentityCounts         `json:"counts"`
	Team             pebblestore.TeamRecord             `json:"team,omitempty"`       // legacy response compatibility only; no team is persisted.
	Membership       pebblestore.TeamMembershipRecord   `json:"membership,omitempty"` // legacy response compatibility only; no membership is persisted.
}

type TeamOptInResult struct {
	Team             pebblestore.TeamRecord             `json:"team"`
	Membership       pebblestore.TeamMembershipRecord   `json:"membership"`
	CurrentSelection pebblestore.CurrentSelectionRecord `json:"current_selection"`
	Counts           pebblestore.IdentityCounts         `json:"counts"`
}

type StateSummary struct {
	Counts            pebblestore.IdentityCounts          `json:"counts"`
	CurrentUser       *pebblestore.UserRecord             `json:"current_user,omitempty"`
	AccountScope      *pebblestore.AccountScopeRecord     `json:"account_scope,omitempty"`
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
	accountScopeID, err := s.generateID("acct")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("generate account scope id: %w", err)
	}

	created, err := s.store.CreateBootstrapIdentityRecords(pebblestore.BootstrapIdentityRecords{
		User: pebblestore.UserRecord{
			ID:             userID,
			AuthProvider:   LocalProductSessionIssuer,
			AuthSubject:    userID,
			DisplayName:    username,
			AccountScopeID: accountScopeID,
			Username:       username,
		},
		AccountScope: pebblestore.AccountScopeRecord{
			ID:              accountScopeID,
			Type:            pebblestore.AccountScopeTypePersonal,
			CreatedByUserID: userID,
			UserID:          userID,
			Role:            pebblestore.AccountRoleOwner,
		},
		AccountUser: pebblestore.AccountUserRecord{
			ID:             accountScopeID + ":" + userID,
			AccountScopeID: accountScopeID,
			UserID:         userID,
			Status:         pebblestore.AccountUserStatusActive,
		},
		CurrentSelection: pebblestore.CurrentSelectionRecord{
			UserID: userID,
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
		AccountScope:     created.AccountScope,
		AccountUser:      created.AccountUser,
		CurrentSelection: created.CurrentSelection,
		Counts:           counts,
		Team: pebblestore.TeamRecord{
			ID:             "",
			AccountScopeID: created.AccountScope.ID,
			Name:           defaultBackendTeamName,
		},
		Membership: pebblestore.TeamMembershipRecord{
			UserID: created.User.ID,
			Role:   pebblestore.TeamRoleOwner,
		},
	}, nil
}

func (s *Service) UpgradeAccountToTeam(actor ActorContext, teamDisplayName string) (TeamOptInResult, error) {
	if err := s.configured(); err != nil {
		return TeamOptInResult{}, err
	}
	teamDisplayName = strings.TrimSpace(teamDisplayName)
	if teamDisplayName == "" {
		return TeamOptInResult{}, errors.New("team name is required")
	}
	if strings.TrimSpace(actor.UserID) == "" || strings.TrimSpace(actor.User.ID) == "" {
		return TeamOptInResult{}, ErrTeamOptInUnauthorized
	}
	if actor.UserID != actor.User.ID {
		return TeamOptInResult{}, ErrTeamOptInUnauthorized
	}
	if strings.TrimSpace(actor.User.AccountScopeID) == "" || actor.User.AccountScopeID != actor.AccountScopeID || actor.AccountScope.UserID != actor.UserID {
		return TeamOptInResult{}, ErrTeamOptInUnauthorized
	}
	if actor.AccountScope.Role != "" && actor.AccountScope.Role != pebblestore.AccountRoleOwner {
		return TeamOptInResult{}, ErrTeamOptInUnauthorized
	}
	if strings.TrimSpace(actor.TeamID) != "" || strings.TrimSpace(actor.Membership.TeamID) != "" {
		return TeamOptInResult{}, ErrTeamAlreadyExists
	}
	if _, ok, err := s.store.GetTeamByAccountScope(actor.User.AccountScopeID); err != nil {
		return TeamOptInResult{}, err
	} else if ok {
		return TeamOptInResult{}, ErrTeamAlreadyExists
	}

	teamID, err := s.generateID("team")
	if err != nil {
		return TeamOptInResult{}, fmt.Errorf("generate team id: %w", err)
	}
	created, err := s.store.CreateTeamOptInRecords(pebblestore.TeamOptInRecords{
		Team: pebblestore.TeamRecord{
			ID:             teamID,
			AccountScopeID: actor.User.AccountScopeID,
			Name:           teamDisplayName,
		},
		Membership: pebblestore.TeamMembershipRecord{
			TeamID: teamID,
			UserID: actor.UserID,
			Role:   pebblestore.TeamRoleOwner,
		},
		CurrentSelection: pebblestore.CurrentSelectionRecord{
			UserID: actor.UserID,
			TeamID: teamID,
		},
	})
	if err != nil {
		if errors.Is(err, pebblestore.ErrIdentityRecordExists) {
			return TeamOptInResult{}, ErrTeamAlreadyExists
		}
		return TeamOptInResult{}, err
	}
	counts, err := s.store.IdentityCounts()
	if err != nil {
		return TeamOptInResult{}, err
	}
	return TeamOptInResult{
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
	summary.CurrentUser = &user
	accountScope, ok, err := s.store.GetAccountScope(user.AccountScopeID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return StateSummary{}, fmt.Errorf("current user account scope %q is missing", user.AccountScopeID)
	}
	summary.AccountScope = &accountScope
	team, ok, err := s.store.GetTeamByAccountScope(user.AccountScopeID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return summary, nil
	}
	summary.CurrentTeam = &team
	membership, ok, err := s.store.GetTeamMembership(team.ID, user.ID)
	if err != nil {
		return StateSummary{}, err
	}
	if !ok {
		return StateSummary{}, fmt.Errorf("current team membership team=%q user=%q is missing", team.ID, user.ID)
	}
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
