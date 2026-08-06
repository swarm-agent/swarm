package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

var (
	ErrAgentModelSettingsStoreNotConfigured = errors.New("agent model settings store is not configured")
	ErrAgentModelSettingsAccountRequired    = errors.New("agent model settings account scope id is required")
	ErrAgentModelSettingsAccountMismatch    = errors.New("agent model settings account scope id does not match storage scope")
	ErrAgentModelSettingsAssignmentInvalid  = errors.New("agent model assignment is invalid")
	ErrAgentModelSettingsNotFound           = errors.New("agent model settings not found")
	ErrAgentModelSettingsAgentUnknown       = errors.New("unknown system agent assignment")
)

const (
	SystemAgentCompact  = "compact"
	SystemAgentFinder   = "finder"
	SystemAgentCoder    = "coder"
	SystemAgentDesigner = "designer"
	SystemAgentRouter   = "router"
)

// AgentModelAssignment is the one canonical persisted model assignment shape.
type AgentModelAssignment struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

type SwarmAgentModelAssignments struct {
	Action AgentModelAssignment `json:"action"`
	Plan   AgentModelAssignment `json:"plan"`
}

type SystemAgentModelAssignments struct {
	Compact  AgentModelAssignment `json:"compact"`
	Finder   AgentModelAssignment `json:"finder"`
	Coder    AgentModelAssignment `json:"coder"`
	Designer AgentModelAssignment `json:"designer"`
	Router   AgentModelAssignment `json:"router"`
}

type AgentModelSettingsRecord struct {
	AccountScopeID string                      `json:"account_scope_id"`
	Swarm          SwarmAgentModelAssignments  `json:"swarm"`
	SystemAgents   SystemAgentModelAssignments `json:"system_agents"`
	UpdatedAt      int64                       `json:"updated_at"`
}

type AgentModelSettingsStore struct {
	store *Store
}

func NewAgentModelSettingsStore(store *Store) *AgentModelSettingsStore {
	return &AgentModelSettingsStore{store: store}
}

func NormalizeAgentModelAccountScopeID(accountScopeID string) string {
	return strings.ToLower(strings.TrimSpace(accountScopeID))
}

func NormalizeAgentModelAssignment(assignment AgentModelAssignment) AgentModelAssignment {
	assignment.Provider = strings.ToLower(strings.TrimSpace(assignment.Provider))
	assignment.Model = strings.TrimSpace(assignment.Model)
	assignment.Thinking = strings.TrimSpace(assignment.Thinking)
	assignment.ServiceTier = strings.ToLower(strings.TrimSpace(assignment.ServiceTier))
	assignment.ContextMode = strings.ToLower(strings.TrimSpace(assignment.ContextMode))
	return assignment
}

func ValidateAgentModelAssignment(assignment AgentModelAssignment) error {
	assignment = NormalizeAgentModelAssignment(assignment)
	if assignment.Provider == "" || assignment.Model == "" || assignment.Thinking == "" {
		return ErrAgentModelSettingsAssignmentInvalid
	}
	return nil
}

func NormalizeSystemAgentName(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case SystemAgentCompact:
		return SystemAgentCompact, nil
	case SystemAgentFinder:
		return SystemAgentFinder, nil
	case SystemAgentCoder:
		return SystemAgentCoder, nil
	case SystemAgentDesigner:
		return SystemAgentDesigner, nil
	case SystemAgentRouter:
		return SystemAgentRouter, nil
	default:
		return "", ErrAgentModelSettingsAgentUnknown
	}
}

func (s *AgentModelSettingsStore) GetForAccount(accountScopeID string) (AgentModelSettingsRecord, bool, error) {
	accountScopeID = NormalizeAgentModelAccountScopeID(accountScopeID)
	if accountScopeID == "" {
		return AgentModelSettingsRecord{}, false, ErrAgentModelSettingsAccountRequired
	}
	if s == nil || s.store == nil {
		return AgentModelSettingsRecord{}, false, ErrAgentModelSettingsStoreNotConfigured
	}
	payload, found, err := s.store.GetBytes(KeyAgentModelSettingsForAccount(accountScopeID))
	if err != nil || !found {
		return AgentModelSettingsRecord{}, found, err
	}
	var record AgentModelSettingsRecord
	if err := decodeStrictJSON(payload, &record); err != nil {
		return AgentModelSettingsRecord{}, false, fmt.Errorf("decode agent model settings: %w", err)
	}
	record = normalizeAgentModelSettingsRecord(record)
	if record.AccountScopeID != accountScopeID {
		return AgentModelSettingsRecord{}, false, ErrAgentModelSettingsAccountMismatch
	}
	if err := validateAgentModelSettingsRecord(record); err != nil {
		return AgentModelSettingsRecord{}, false, err
	}
	return record, true, nil
}

// PutForAccount replaces the complete canonical record atomically.
func (s *AgentModelSettingsStore) PutForAccount(record AgentModelSettingsRecord) (AgentModelSettingsRecord, error) {
	if s == nil || s.store == nil {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsStoreNotConfigured
	}
	record = normalizeAgentModelSettingsRecord(record)
	if err := validateAgentModelSettingsRecord(record); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return AgentModelSettingsRecord{}, err
	}
	s.store.agentModelSettingsMu.Lock()
	defer s.store.agentModelSettingsMu.Unlock()
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyAgentModelSettingsForAccount(record.AccountScopeID)), payload, nil); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	return record, nil
}

// UpdateSwarmForAccount atomically replaces the required Action and Plan pair.
// It requires an existing canonical record; startup migration or PutForAccount
// establishes that record before targeted updates.
func (s *AgentModelSettingsStore) UpdateSwarmForAccount(accountScopeID string, action, plan AgentModelAssignment, updatedAt int64) (AgentModelSettingsRecord, error) {
	if s == nil || s.store == nil {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsStoreNotConfigured
	}
	accountScopeID = NormalizeAgentModelAccountScopeID(accountScopeID)
	if accountScopeID == "" {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsAccountRequired
	}
	action, plan = NormalizeAgentModelAssignment(action), NormalizeAgentModelAssignment(plan)
	if err := ValidateAgentModelAssignment(action); err != nil {
		return AgentModelSettingsRecord{}, fmt.Errorf("Action: %w", err)
	}
	if err := ValidateAgentModelAssignment(plan); err != nil {
		return AgentModelSettingsRecord{}, fmt.Errorf("Plan: %w", err)
	}
	return s.updateForAccount(accountScopeID, func(record *AgentModelSettingsRecord) {
		record.Swarm = SwarmAgentModelAssignments{Action: action, Plan: plan}
		record.UpdatedAt = updatedAt
	})
}

// UpdateSystemAgentForAccount changes exactly one compiled system-agent assignment.
func (s *AgentModelSettingsStore) UpdateSystemAgentForAccount(accountScopeID, name string, assignment AgentModelAssignment, updatedAt int64) (AgentModelSettingsRecord, error) {
	canonicalName, err := NormalizeSystemAgentName(name)
	if err != nil {
		return AgentModelSettingsRecord{}, err
	}
	assignment = NormalizeAgentModelAssignment(assignment)
	if err := ValidateAgentModelAssignment(assignment); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	return s.updateForAccount(NormalizeAgentModelAccountScopeID(accountScopeID), func(record *AgentModelSettingsRecord) {
		setSystemAgentAssignment(&record.SystemAgents, canonicalName, assignment)
		record.UpdatedAt = updatedAt
	})
}

func (s *AgentModelSettingsStore) updateForAccount(accountScopeID string, mutate func(*AgentModelSettingsRecord)) (AgentModelSettingsRecord, error) {
	if s == nil || s.store == nil {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsStoreNotConfigured
	}
	if accountScopeID == "" {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsAccountRequired
	}
	s.store.agentModelSettingsMu.Lock()
	defer s.store.agentModelSettingsMu.Unlock()

	key := KeyAgentModelSettingsForAccount(accountScopeID)
	var record AgentModelSettingsRecord
	if payload, found, err := s.store.GetBytes(key); err != nil {
		return AgentModelSettingsRecord{}, err
	} else if found {
		if err := decodeStrictJSON(payload, &record); err != nil {
			return AgentModelSettingsRecord{}, fmt.Errorf("decode agent model settings: %w", err)
		}
		record = normalizeAgentModelSettingsRecord(record)
		if record.AccountScopeID != accountScopeID {
			return AgentModelSettingsRecord{}, ErrAgentModelSettingsAccountMismatch
		}
	} else {
		return AgentModelSettingsRecord{}, ErrAgentModelSettingsNotFound
	}
	mutate(&record)
	record = normalizeAgentModelSettingsRecord(record)
	if err := validateAgentModelSettingsRecord(record); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return AgentModelSettingsRecord{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), payload, nil); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return AgentModelSettingsRecord{}, err
	}
	return record, nil
}

func normalizeAgentModelSettingsRecord(record AgentModelSettingsRecord) AgentModelSettingsRecord {
	record.AccountScopeID = NormalizeAgentModelAccountScopeID(record.AccountScopeID)
	record.Swarm.Action = NormalizeAgentModelAssignment(record.Swarm.Action)
	record.Swarm.Plan = NormalizeAgentModelAssignment(record.Swarm.Plan)
	record.SystemAgents.Compact = NormalizeAgentModelAssignment(record.SystemAgents.Compact)
	record.SystemAgents.Finder = NormalizeAgentModelAssignment(record.SystemAgents.Finder)
	record.SystemAgents.Coder = NormalizeAgentModelAssignment(record.SystemAgents.Coder)
	record.SystemAgents.Designer = NormalizeAgentModelAssignment(record.SystemAgents.Designer)
	record.SystemAgents.Router = NormalizeAgentModelAssignment(record.SystemAgents.Router)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func validateAgentModelSettingsRecord(record AgentModelSettingsRecord) error {
	if record.AccountScopeID == "" {
		return ErrAgentModelSettingsAccountRequired
	}
	if err := ValidateAgentModelAssignment(record.Swarm.Action); err != nil {
		return fmt.Errorf("swarm.action: %w", err)
	}
	if err := ValidateAgentModelAssignment(record.Swarm.Plan); err != nil {
		return fmt.Errorf("swarm.plan: %w", err)
	}
	assignments := []struct {
		name  string
		value AgentModelAssignment
	}{
		{SystemAgentCompact, record.SystemAgents.Compact}, {SystemAgentFinder, record.SystemAgents.Finder},
		{SystemAgentCoder, record.SystemAgents.Coder}, {SystemAgentDesigner, record.SystemAgents.Designer},
		{SystemAgentRouter, record.SystemAgents.Router},
	}
	for _, item := range assignments {
		if err := ValidateAgentModelAssignment(item.value); err != nil {
			return fmt.Errorf("system_agents.%s: %w", item.name, err)
		}
	}
	return nil
}

func setSystemAgentAssignment(assignments *SystemAgentModelAssignments, name string, assignment AgentModelAssignment) {
	switch name {
	case SystemAgentCompact:
		assignments.Compact = assignment
	case SystemAgentFinder:
		assignments.Finder = assignment
	case SystemAgentCoder:
		assignments.Coder = assignment
	case SystemAgentDesigner:
		assignments.Designer = assignment
	case SystemAgentRouter:
		assignments.Router = assignment
	}
}
