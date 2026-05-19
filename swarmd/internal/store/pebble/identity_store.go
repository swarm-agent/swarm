package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const TeamRoleOwner = "owner"

var (
	ErrIdentityStoreNotConfigured = errors.New("identity store is not configured")
	ErrIdentityRecordExists       = errors.New("identity record already exists")
	ErrIdentityRecordNotFound     = errors.New("identity record not found")
)

type UserRecord struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TeamRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Default   bool      `json:"default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TeamMembershipRecord struct {
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CurrentSelectionRecord struct {
	UserID      string    `json:"user_id"`
	TeamID      string    `json:"team_id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type IdentityCounts struct {
	Users             int `json:"users"`
	Teams             int `json:"teams"`
	TeamMemberships   int `json:"team_memberships"`
	CurrentSelections int `json:"current_selections"`
}

type BootstrapIdentityRecords struct {
	User             UserRecord             `json:"user"`
	Team             TeamRecord             `json:"team"`
	Membership       TeamMembershipRecord   `json:"membership"`
	CurrentSelection CurrentSelectionRecord `json:"current_selection"`
}

type IdentityStore struct {
	store *Store
}

func NewIdentityStore(store *Store) *IdentityStore {
	return &IdentityStore{store: store}
}

func (s *IdentityStore) PutUser(record UserRecord) (UserRecord, error) {
	if err := s.configured(); err != nil {
		return UserRecord{}, err
	}
	record = normalizeUserRecord(record)
	if record.ID == "" {
		return UserRecord{}, errors.New("user id is required")
	}
	if record.Username == "" {
		return UserRecord{}, errors.New("username is required")
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	priorUsername := ""
	if existing, ok, err := s.GetUser(record.ID); err != nil {
		return UserRecord{}, err
	} else if ok {
		priorUsername = existing.Username
		if record.CreatedAt.IsZero() {
			record.CreatedAt = existing.CreatedAt
		}
	}
	if existing, ok, err := s.GetUserByUsername(record.Username); err != nil {
		return UserRecord{}, err
	} else if ok && existing.ID != record.ID {
		return UserRecord{}, fmt.Errorf("username already exists: %w", ErrIdentityRecordExists)
	}
	return s.saveUser(record, priorUsername)
}

func (s *IdentityStore) CreateUserIfAbsent(record UserRecord) (UserRecord, error) {
	if err := s.configured(); err != nil {
		return UserRecord{}, err
	}
	record = normalizeUserRecord(record)
	if record.ID == "" {
		return UserRecord{}, errors.New("user id is required")
	}
	if record.Username == "" {
		return UserRecord{}, errors.New("username is required")
	}
	if _, ok, err := s.GetUser(record.ID); err != nil {
		return UserRecord{}, err
	} else if ok {
		return UserRecord{}, fmt.Errorf("user already exists: %w", ErrIdentityRecordExists)
	}
	if _, ok, err := s.GetUserByUsername(record.Username); err != nil {
		return UserRecord{}, err
	} else if ok {
		return UserRecord{}, fmt.Errorf("username already exists: %w", ErrIdentityRecordExists)
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	return s.saveUser(record, "")
}

func (s *IdentityStore) saveUser(record UserRecord, priorUsername string) (UserRecord, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return UserRecord{}, fmt.Errorf("marshal user: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyIdentityUser(record.ID)), payload, nil); err != nil {
		return UserRecord{}, err
	}
	if priorUsername != "" && priorUsername != record.Username {
		if err := batch.Delete([]byte(KeyIdentityUserByUsername(priorUsername)), nil); err != nil {
			return UserRecord{}, err
		}
	}
	if err := batch.Set([]byte(KeyIdentityUserByUsername(record.Username)), []byte(record.ID), nil); err != nil {
		return UserRecord{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return UserRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) GetUser(userID string) (UserRecord, bool, error) {
	if err := s.configured(); err != nil {
		return UserRecord{}, false, err
	}
	userID = normalizeIdentityID(userID)
	if userID == "" {
		return UserRecord{}, false, nil
	}
	var record UserRecord
	ok, err := s.store.GetJSON(KeyIdentityUser(userID), &record)
	if err != nil || !ok {
		return UserRecord{}, ok, err
	}
	return normalizeUserRecord(record), true, nil
}

func (s *IdentityStore) GetUserByUsername(username string) (UserRecord, bool, error) {
	if err := s.configured(); err != nil {
		return UserRecord{}, false, err
	}
	normalized := NormalizeIdentityUsername(username)
	if normalized == "" {
		return UserRecord{}, false, nil
	}
	payload, ok, err := s.store.GetBytes(KeyIdentityUserByUsername(normalized))
	if err != nil || !ok {
		return UserRecord{}, ok, err
	}
	return s.GetUser(string(payload))
}

func (s *IdentityStore) ListUsers(limit int) ([]UserRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return listIdentityRecords(s.store, IdentityUserPrefix(), limit, func(value []byte) (UserRecord, error) {
		var record UserRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return UserRecord{}, fmt.Errorf("decode user: %w", err)
		}
		return normalizeUserRecord(record), nil
	})
}

func (s *IdentityStore) PutTeam(record TeamRecord) (TeamRecord, error) {
	if err := s.configured(); err != nil {
		return TeamRecord{}, err
	}
	record = normalizeTeamRecord(record)
	if record.ID == "" {
		return TeamRecord{}, errors.New("team id is required")
	}
	if record.Name == "" {
		return TeamRecord{}, errors.New("team name is required")
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyIdentityTeam(record.ID), record); err != nil {
		return TeamRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) CreateTeamIfAbsent(record TeamRecord) (TeamRecord, error) {
	if err := s.configured(); err != nil {
		return TeamRecord{}, err
	}
	record = normalizeTeamRecord(record)
	if record.ID == "" {
		return TeamRecord{}, errors.New("team id is required")
	}
	if record.Name == "" {
		return TeamRecord{}, errors.New("team name is required")
	}
	if _, ok, err := s.GetTeam(record.ID); err != nil {
		return TeamRecord{}, err
	} else if ok {
		return TeamRecord{}, fmt.Errorf("team already exists: %w", ErrIdentityRecordExists)
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if err := s.store.PutJSON(KeyIdentityTeam(record.ID), record); err != nil {
		return TeamRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) GetTeam(teamID string) (TeamRecord, bool, error) {
	if err := s.configured(); err != nil {
		return TeamRecord{}, false, err
	}
	teamID = normalizeIdentityID(teamID)
	if teamID == "" {
		return TeamRecord{}, false, nil
	}
	var record TeamRecord
	ok, err := s.store.GetJSON(KeyIdentityTeam(teamID), &record)
	if err != nil || !ok {
		return TeamRecord{}, ok, err
	}
	return normalizeTeamRecord(record), true, nil
}

func (s *IdentityStore) ListTeams(limit int) ([]TeamRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return listIdentityRecords(s.store, IdentityTeamPrefix(), limit, func(value []byte) (TeamRecord, error) {
		var record TeamRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TeamRecord{}, fmt.Errorf("decode team: %w", err)
		}
		return normalizeTeamRecord(record), nil
	})
}

func (s *IdentityStore) PutTeamMembership(record TeamMembershipRecord) (TeamMembershipRecord, error) {
	if err := s.configured(); err != nil {
		return TeamMembershipRecord{}, err
	}
	record = normalizeTeamMembershipRecord(record)
	if err := s.validateMembershipReferences(record); err != nil {
		return TeamMembershipRecord{}, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyIdentityTeamMembership(record.TeamID, record.UserID), record); err != nil {
		return TeamMembershipRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) CreateTeamMembershipIfAbsent(record TeamMembershipRecord) (TeamMembershipRecord, error) {
	if err := s.configured(); err != nil {
		return TeamMembershipRecord{}, err
	}
	record = normalizeTeamMembershipRecord(record)
	if err := s.validateMembershipReferences(record); err != nil {
		return TeamMembershipRecord{}, err
	}
	if _, ok, err := s.GetTeamMembership(record.TeamID, record.UserID); err != nil {
		return TeamMembershipRecord{}, err
	} else if ok {
		return TeamMembershipRecord{}, fmt.Errorf("membership already exists: %w", ErrIdentityRecordExists)
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if err := s.store.PutJSON(KeyIdentityTeamMembership(record.TeamID, record.UserID), record); err != nil {
		return TeamMembershipRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) validateMembershipReferences(record TeamMembershipRecord) error {
	if record.TeamID == "" {
		return errors.New("team id is required")
	}
	if record.UserID == "" {
		return errors.New("user id is required")
	}
	if record.Role == "" {
		return errors.New("membership role is required")
	}
	if _, ok, err := s.GetTeam(record.TeamID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("membership team %q: %w", record.TeamID, ErrIdentityRecordNotFound)
	}
	if _, ok, err := s.GetUser(record.UserID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("membership user %q: %w", record.UserID, ErrIdentityRecordNotFound)
	}
	return nil
}

func (s *IdentityStore) GetTeamMembership(teamID, userID string) (TeamMembershipRecord, bool, error) {
	if err := s.configured(); err != nil {
		return TeamMembershipRecord{}, false, err
	}
	teamID = normalizeIdentityID(teamID)
	userID = normalizeIdentityID(userID)
	if teamID == "" || userID == "" {
		return TeamMembershipRecord{}, false, nil
	}
	var record TeamMembershipRecord
	ok, err := s.store.GetJSON(KeyIdentityTeamMembership(teamID, userID), &record)
	if err != nil || !ok {
		return TeamMembershipRecord{}, ok, err
	}
	return normalizeTeamMembershipRecord(record), true, nil
}

func (s *IdentityStore) ListTeamMemberships(limit int) ([]TeamMembershipRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return listIdentityRecords(s.store, IdentityTeamMembershipPrefix(""), limit, func(value []byte) (TeamMembershipRecord, error) {
		var record TeamMembershipRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TeamMembershipRecord{}, fmt.Errorf("decode team membership: %w", err)
		}
		return normalizeTeamMembershipRecord(record), nil
	})
}

func (s *IdentityStore) ListTeamMembershipsForTeam(teamID string, limit int) ([]TeamMembershipRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return listIdentityRecords(s.store, IdentityTeamMembershipPrefix(teamID), limit, func(value []byte) (TeamMembershipRecord, error) {
		var record TeamMembershipRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TeamMembershipRecord{}, fmt.Errorf("decode team membership: %w", err)
		}
		return normalizeTeamMembershipRecord(record), nil
	})
}

func (s *IdentityStore) PutCurrentSelection(record CurrentSelectionRecord) (CurrentSelectionRecord, error) {
	if err := s.configured(); err != nil {
		return CurrentSelectionRecord{}, err
	}
	record = normalizeCurrentSelectionRecord(record)
	if err := s.validateCurrentSelection(record); err != nil {
		return CurrentSelectionRecord{}, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyIdentityCurrentSelection(), record); err != nil {
		return CurrentSelectionRecord{}, err
	}
	return record, nil
}

func (s *IdentityStore) validateCurrentSelection(record CurrentSelectionRecord) error {
	if record.UserID == "" {
		return errors.New("current selection user id is required")
	}
	if record.TeamID == "" {
		return errors.New("current selection team id is required")
	}
	if _, ok, err := s.GetUser(record.UserID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("current selection user %q: %w", record.UserID, ErrIdentityRecordNotFound)
	}
	if _, ok, err := s.GetTeam(record.TeamID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("current selection team %q: %w", record.TeamID, ErrIdentityRecordNotFound)
	}
	if _, ok, err := s.GetTeamMembership(record.TeamID, record.UserID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("current selection membership team=%q user=%q: %w", record.TeamID, record.UserID, ErrIdentityRecordNotFound)
	}
	return nil
}

func (s *IdentityStore) GetCurrentSelection() (CurrentSelectionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return CurrentSelectionRecord{}, false, err
	}
	var record CurrentSelectionRecord
	ok, err := s.store.GetJSON(KeyIdentityCurrentSelection(), &record)
	if err != nil || !ok {
		return CurrentSelectionRecord{}, ok, err
	}
	return normalizeCurrentSelectionRecord(record), true, nil
}

func (s *IdentityStore) IdentityCounts() (IdentityCounts, error) {
	if err := s.configured(); err != nil {
		return IdentityCounts{}, err
	}
	users, err := countPrefix(s.store, IdentityUserPrefix(), func(key string) bool {
		return strings.HasPrefix(key, KeyIdentityUserByUsernamePrefix)
	})
	if err != nil {
		return IdentityCounts{}, err
	}
	teams, err := countPrefix(s.store, IdentityTeamPrefix(), nil)
	if err != nil {
		return IdentityCounts{}, err
	}
	memberships, err := countPrefix(s.store, IdentityTeamMembershipPrefix(""), nil)
	if err != nil {
		return IdentityCounts{}, err
	}
	selections, err := countPrefix(s.store, IdentityCurrentSelectionPrefix(), nil)
	if err != nil {
		return IdentityCounts{}, err
	}
	return IdentityCounts{Users: users, Teams: teams, TeamMemberships: memberships, CurrentSelections: selections}, nil
}

func (s *IdentityStore) IsIdentityNamespaceEmpty() (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	count, err := countPrefix(s.store, IdentityPrefix(), nil)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *IdentityStore) CreateBootstrapIdentityRecords(records BootstrapIdentityRecords) (BootstrapIdentityRecords, error) {
	if err := s.configured(); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if empty, err := s.IsIdentityNamespaceEmpty(); err != nil {
		return BootstrapIdentityRecords{}, err
	} else if !empty {
		return BootstrapIdentityRecords{}, fmt.Errorf("identity store is not empty: %w", ErrIdentityRecordExists)
	}
	normalized := BootstrapIdentityRecords{
		User:             normalizeUserRecord(records.User),
		Team:             normalizeTeamRecord(records.Team),
		Membership:       normalizeTeamMembershipRecord(records.Membership),
		CurrentSelection: normalizeCurrentSelectionRecord(records.CurrentSelection),
	}
	if err := validateBootstrapIdentityRecords(normalized); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if _, ok, err := s.GetUserByUsername(normalized.User.Username); err != nil {
		return BootstrapIdentityRecords{}, err
	} else if ok {
		return BootstrapIdentityRecords{}, fmt.Errorf("username already exists: %w", ErrIdentityRecordExists)
	}
	now := time.Now().UTC()
	if normalized.User.CreatedAt.IsZero() {
		normalized.User.CreatedAt = now
	}
	if normalized.User.UpdatedAt.IsZero() {
		normalized.User.UpdatedAt = normalized.User.CreatedAt
	}
	if normalized.Team.CreatedAt.IsZero() {
		normalized.Team.CreatedAt = now
	}
	if normalized.Team.UpdatedAt.IsZero() {
		normalized.Team.UpdatedAt = normalized.Team.CreatedAt
	}
	if normalized.Membership.CreatedAt.IsZero() {
		normalized.Membership.CreatedAt = now
	}
	if normalized.Membership.UpdatedAt.IsZero() {
		normalized.Membership.UpdatedAt = normalized.Membership.CreatedAt
	}
	if normalized.CurrentSelection.CreatedAt.IsZero() {
		normalized.CurrentSelection.CreatedAt = now
	}
	if normalized.CurrentSelection.UpdatedAt.IsZero() {
		normalized.CurrentSelection.UpdatedAt = normalized.CurrentSelection.CreatedAt
	}

	userPayload, err := json.Marshal(normalized.User)
	if err != nil {
		return BootstrapIdentityRecords{}, fmt.Errorf("marshal bootstrap user: %w", err)
	}
	teamPayload, err := json.Marshal(normalized.Team)
	if err != nil {
		return BootstrapIdentityRecords{}, fmt.Errorf("marshal bootstrap team: %w", err)
	}
	membershipPayload, err := json.Marshal(normalized.Membership)
	if err != nil {
		return BootstrapIdentityRecords{}, fmt.Errorf("marshal bootstrap membership: %w", err)
	}
	selectionPayload, err := json.Marshal(normalized.CurrentSelection)
	if err != nil {
		return BootstrapIdentityRecords{}, fmt.Errorf("marshal bootstrap current selection: %w", err)
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyIdentityUser(normalized.User.ID)), userPayload, nil); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if err := batch.Set([]byte(KeyIdentityUserByUsername(normalized.User.Username)), []byte(normalized.User.ID), nil); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if err := batch.Set([]byte(KeyIdentityTeam(normalized.Team.ID)), teamPayload, nil); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if err := batch.Set([]byte(KeyIdentityTeamMembership(normalized.Membership.TeamID, normalized.Membership.UserID)), membershipPayload, nil); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if err := batch.Set([]byte(KeyIdentityCurrentSelection()), selectionPayload, nil); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return BootstrapIdentityRecords{}, err
	}
	return normalized, nil
}

func validateBootstrapIdentityRecords(records BootstrapIdentityRecords) error {
	if records.User.ID == "" {
		return errors.New("bootstrap user id is required")
	}
	if records.User.Username == "" {
		return errors.New("bootstrap username is required")
	}
	if records.Team.ID == "" {
		return errors.New("bootstrap team id is required")
	}
	if records.Team.Name == "" {
		return errors.New("bootstrap team name is required")
	}
	if records.Membership.TeamID != records.Team.ID {
		return errors.New("bootstrap membership team must match team record")
	}
	if records.Membership.UserID != records.User.ID {
		return errors.New("bootstrap membership user must match user record")
	}
	if records.Membership.Role == "" {
		return errors.New("bootstrap membership role is required")
	}
	if records.CurrentSelection.UserID != records.User.ID {
		return errors.New("bootstrap selection user must match user record")
	}
	if records.CurrentSelection.TeamID != records.Team.ID {
		return errors.New("bootstrap selection team must match team record")
	}
	return nil
}

func (s *IdentityStore) configured() error {
	if s == nil || s.store == nil {
		return ErrIdentityStoreNotConfigured
	}
	return nil
}

func NormalizeIdentityUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func normalizeIdentityID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func normalizeIdentityRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func normalizeUserRecord(record UserRecord) UserRecord {
	record.ID = normalizeIdentityID(record.ID)
	record.Username = NormalizeIdentityUsername(record.Username)
	return record
}

func normalizeTeamRecord(record TeamRecord) TeamRecord {
	record.ID = normalizeIdentityID(record.ID)
	record.Name = strings.TrimSpace(record.Name)
	return record
}

func normalizeTeamMembershipRecord(record TeamMembershipRecord) TeamMembershipRecord {
	record.TeamID = normalizeIdentityID(record.TeamID)
	record.UserID = normalizeIdentityID(record.UserID)
	record.Role = normalizeIdentityRole(record.Role)
	return record
}

func normalizeCurrentSelectionRecord(record CurrentSelectionRecord) CurrentSelectionRecord {
	record.UserID = normalizeIdentityID(record.UserID)
	record.TeamID = normalizeIdentityID(record.TeamID)
	record.WorkspaceID = normalizeIdentityID(record.WorkspaceID)
	return record
}

func listIdentityRecords[T any](store *Store, prefix string, limit int, decode func([]byte) (T, error)) ([]T, error) {
	out := make([]T, 0)
	err := store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		record, err := decode(value)
		if err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func countPrefix(store *Store, prefix string, skip func(key string) bool) (int, error) {
	count := 0
	err := store.IteratePrefix(prefix, 0, func(key string, _ []byte) error {
		if skip != nil && skip(key) {
			return nil
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
