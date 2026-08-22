package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	DelegatedChildGenerationActive    = "active"
	DelegatedChildGenerationRetiring  = "retiring"
	DelegatedChildGenerationRetired   = "retired"
	DelegatedChildGenerationSucceeded = "succeeded"
	DelegatedChildGenerationFailed    = "failed"

	maxDelegatedChildGenerationHistory = 64
	maxDelegatedChildHandoffRows       = 128
	maxDelegatedChildHandoffTextRunes  = 8192
)

// DelegatedChildLineageRecord is the bounded authority for a stable logical
// delegated job across context-rotation child sessions. Generation details are
// stored separately; History is a bounded navigation projection.
type DelegatedChildLineageRecord struct {
	AccountScopeID    string                            `json:"account_scope_id"`
	LogicalTaskID     string                            `json:"logical_task_id"`
	ProgramID         string                            `json:"program_id,omitempty"`
	JobID             string                            `json:"job_id,omitempty"`
	Revision          uint64                            `json:"revision"`
	CurrentGeneration int                               `json:"current_generation"`
	CurrentSessionID  string                            `json:"current_session_id"`
	CurrentRunID      string                            `json:"current_run_id,omitempty"`
	CurrentAttemptID  string                            `json:"current_attempt_id,omitempty"`
	LastMutationID    string                            `json:"last_mutation_id,omitempty"`
	GenerationHistory []DelegatedChildGenerationSummary `json:"generation_history"`
	CreatedAt         int64                             `json:"created_at"`
	UpdatedAt         int64                             `json:"updated_at"`
}

type DelegatedChildGenerationSummary struct {
	Generation    int    `json:"generation"`
	SessionID     string `json:"session_id"`
	RunID         string `json:"run_id,omitempty"`
	AttemptID     string `json:"attempt_id,omitempty"`
	State         string `json:"state"`
	PredecessorID string `json:"predecessor_session_id,omitempty"`
	SuccessorID   string `json:"successor_session_id,omitempty"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
}

// DelegatedChildGenerationRecord retains identity, permission, reservation,
// run, and managed-worktree lineage without retaining a transcript.
type DelegatedChildGenerationRecord struct {
	AccountScopeID                 string `json:"account_scope_id"`
	LogicalTaskID                  string `json:"logical_task_id"`
	ProgramID                      string `json:"program_id,omitempty"`
	JobID                          string `json:"job_id,omitempty"`
	Generation                     int    `json:"generation"`
	Revision                       uint64 `json:"revision"`
	State                          string `json:"state"`
	SessionID                      string `json:"session_id"`
	PredecessorSessionID           string `json:"predecessor_session_id,omitempty"`
	SuccessorSessionID             string `json:"successor_session_id,omitempty"`
	ParentSessionID                string `json:"parent_session_id,omitempty"`
	RunID                          string `json:"run_id,omitempty"`
	ParentRunID                    string `json:"parent_run_id,omitempty"`
	AttemptID                      string `json:"attempt_id,omitempty"`
	PermissionPrincipalID          string `json:"permission_principal_id,omitempty"`
	PermissionScopeID              string `json:"permission_scope_id,omitempty"`
	ReservationSessionID           string `json:"reservation_session_id,omitempty"`
	ReservationRunID               string `json:"reservation_run_id,omitempty"`
	ReservationCallID              string `json:"reservation_call_id,omitempty"`
	WorkspacePath                  string `json:"workspace_path"`
	WorktreeBranch                 string `json:"worktree_branch"`
	ParentBranch                   string `json:"parent_branch,omitempty"`
	ImmutableBaseCommit            string `json:"immutable_base_commit,omitempty"`
	ManagedArtifactParentSessionID string `json:"managed_artifact_parent_session_id,omitempty"`
	ManagedArtifactCollectionID    string `json:"managed_artifact_collection_id,omitempty"`
	ManagedArtifactVariantID       string `json:"managed_artifact_variant_id,omitempty"`
	ManagedArtifactTaskCallID      string `json:"managed_artifact_task_call_id,omitempty"`
	ManagedArtifactProgramID       string `json:"managed_artifact_program_id,omitempty"`
	ManagedArtifactProgramJobID    string `json:"managed_artifact_program_job_id,omitempty"`
	LastMutationID                 string `json:"last_mutation_id,omitempty"`
	CreatedAt                      int64  `json:"created_at"`
	UpdatedAt                      int64  `json:"updated_at"`
	FinishedAt                     int64  `json:"finished_at,omitempty"`
}

// DelegatedChildTargetedHandoff is deliberately structured and bounded. It is
// successor context, not a retired transcript replay.
type DelegatedChildTargetedHandoff struct {
	AccountScopeID        string   `json:"account_scope_id"`
	LogicalTaskID         string   `json:"logical_task_id"`
	PredecessorGeneration int      `json:"predecessor_generation"`
	SuccessorGeneration   int      `json:"successor_generation"`
	PredecessorSessionID  string   `json:"predecessor_session_id"`
	SuccessorSessionID    string   `json:"successor_session_id"`
	Objective             string   `json:"objective"`
	Completed             []string `json:"completed,omitempty"`
	NextActions           []string `json:"next_actions,omitempty"`
	Decisions             []string `json:"decisions,omitempty"`
	Constraints           []string `json:"constraints,omitempty"`
	RelevantFiles         []string `json:"relevant_files,omitempty"`
	Validation            []string `json:"validation,omitempty"`
	CreatedAt             int64    `json:"created_at"`
}

// ManagedWorktreeOwnerLease is metadata-only ownership. Store operations never
// inspect, clean, reset, or otherwise mutate Git state at WorkspacePath.
type ManagedWorktreeOwnerLease struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspacePath  string `json:"workspace_path"`
	WorktreeBranch string `json:"worktree_branch"`
	ParentBranch   string `json:"parent_branch,omitempty"`
	LogicalTaskID  string `json:"logical_task_id"`
	Generation     int    `json:"generation"`
	SessionID      string `json:"session_id"`
	Revision       uint64 `json:"revision"`
	LastMutationID string `json:"last_mutation_id,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type DelegatedChildRotationStore struct{ store *Store }

func NewDelegatedChildRotationStore(store *Store) *DelegatedChildRotationStore {
	return &DelegatedChildRotationStore{store: store}
}

// Delegated-child rotation is exposed through SessionStore so orchestration
// callers use the canonical session persistence boundary without receiving the
// raw Pebble Store.
func (s *SessionStore) GetDelegatedChildLineage(accountScopeID, logicalTaskID string) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).GetLineage(accountScopeID, logicalTaskID)
}

func (s *SessionStore) GetDelegatedChildGeneration(accountScopeID, logicalTaskID string, generation int) (DelegatedChildGenerationRecord, bool, error) {
	if s == nil {
		return DelegatedChildGenerationRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).GetGeneration(accountScopeID, logicalTaskID, generation)
}

func (s *SessionStore) GetDelegatedChildGenerationBySession(accountScopeID, sessionID string) (DelegatedChildGenerationRecord, bool, error) {
	if s == nil {
		return DelegatedChildGenerationRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).GetGenerationBySession(accountScopeID, sessionID)
}

func (s *SessionStore) GetDelegatedChildHandoff(accountScopeID, logicalTaskID string, predecessorGeneration int) (DelegatedChildTargetedHandoff, bool, error) {
	if s == nil {
		return DelegatedChildTargetedHandoff{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).GetHandoff(accountScopeID, logicalTaskID, predecessorGeneration)
}

func (s *SessionStore) GetDelegatedWorktreeOwner(accountScopeID, workspacePath string) (ManagedWorktreeOwnerLease, bool, error) {
	if s == nil {
		return ManagedWorktreeOwnerLease{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).GetWorktreeOwner(accountScopeID, workspacePath)
}

func (s *SessionStore) CreateDelegatedChildLineage(lineage DelegatedChildLineageRecord, generation DelegatedChildGenerationRecord, mutationID string) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).CreateLineage(lineage, generation, mutationID)
}

func (s *SessionStore) UpdateDelegatedChildRun(input UpdateDelegatedChildRunInput) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).UpdateRun(input)
}

func (s *SessionStore) BeginDelegatedChildRetirement(input RetireDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).BeginRetirement(input)
}

func (s *SessionStore) RotateDelegatedChild(input RotateDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).Rotate(input)
}

func (s *SessionStore) FinishDelegatedChild(input FinishDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if s == nil {
		return DelegatedChildLineageRecord{}, false, errors.New("session store is not configured")
	}
	return NewDelegatedChildRotationStore(s.store).Finish(input)
}

var delegatedChildRotationMu sync.Mutex

func (s *DelegatedChildRotationStore) GetLineage(accountScopeID, logicalTaskID string) (DelegatedChildLineageRecord, bool, error) {
	var record DelegatedChildLineageRecord
	if err := s.configured(); err != nil {
		return record, false, err
	}
	accountScopeID, logicalTaskID = strings.TrimSpace(accountScopeID), strings.TrimSpace(logicalTaskID)
	if accountScopeID == "" || logicalTaskID == "" {
		return record, false, errors.New("account scope and logical task are required")
	}
	ok, err := s.store.GetJSON(KeyDelegatedChildLineage(accountScopeID, logicalTaskID), &record)
	return record, ok, err
}

func (s *DelegatedChildRotationStore) GetGeneration(accountScopeID, logicalTaskID string, generation int) (DelegatedChildGenerationRecord, bool, error) {
	var record DelegatedChildGenerationRecord
	if err := s.configured(); err != nil {
		return record, false, err
	}
	accountScopeID, logicalTaskID = strings.TrimSpace(accountScopeID), strings.TrimSpace(logicalTaskID)
	if accountScopeID == "" || logicalTaskID == "" || generation < 1 {
		return record, false, errors.New("account scope, logical task, and generation are required")
	}
	ok, err := s.store.GetJSON(KeyDelegatedChildGeneration(accountScopeID, logicalTaskID, generation), &record)
	return record, ok, err
}

func (s *DelegatedChildRotationStore) GetGenerationBySession(accountScopeID, sessionID string) (DelegatedChildGenerationRecord, bool, error) {
	var record DelegatedChildGenerationRecord
	if err := s.configured(); err != nil {
		return record, false, err
	}
	accountScopeID, sessionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID)
	if accountScopeID == "" || sessionID == "" {
		return record, false, errors.New("account scope and session are required")
	}
	var ref struct {
		LogicalTaskID string `json:"logical_task_id"`
		Generation    int    `json:"generation"`
	}
	ok, err := s.store.GetJSON(KeyDelegatedChildSessionIndex(accountScopeID, sessionID), &ref)
	if err != nil || !ok {
		return record, ok, err
	}
	return s.GetGeneration(accountScopeID, ref.LogicalTaskID, ref.Generation)
}

func (s *DelegatedChildRotationStore) GetHandoff(accountScopeID, logicalTaskID string, predecessorGeneration int) (DelegatedChildTargetedHandoff, bool, error) {
	var record DelegatedChildTargetedHandoff
	if err := s.configured(); err != nil {
		return record, false, err
	}
	accountScopeID, logicalTaskID = strings.TrimSpace(accountScopeID), strings.TrimSpace(logicalTaskID)
	if accountScopeID == "" || logicalTaskID == "" || predecessorGeneration < 1 {
		return record, false, errors.New("account scope, logical task, and predecessor generation are required")
	}
	ok, err := s.store.GetJSON(KeyDelegatedChildHandoff(accountScopeID, logicalTaskID, predecessorGeneration), &record)
	return record, ok, err
}

func (s *DelegatedChildRotationStore) GetWorktreeOwner(accountScopeID, workspacePath string) (ManagedWorktreeOwnerLease, bool, error) {
	var record ManagedWorktreeOwnerLease
	if err := s.configured(); err != nil {
		return record, false, err
	}
	workspacePath = normalizeDelegatedWorkspacePath(workspacePath)
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" || workspacePath == "" {
		return record, false, errors.New("account scope and workspace path are required")
	}
	ok, err := s.store.GetJSON(KeyManagedWorktreeOwnerLease(accountScopeID, workspacePath), &record)
	return record, ok, err
}

// CreateLineage atomically establishes generation one and, for managed
// worktrees, its exclusive owner lease. Repeating the same MutationID is idempotent.
func (s *DelegatedChildRotationStore) CreateLineage(lineage DelegatedChildLineageRecord, generation DelegatedChildGenerationRecord, mutationID string) (DelegatedChildLineageRecord, bool, error) {
	if err := s.configured(); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	mutationID = strings.TrimSpace(mutationID)
	if mutationID == "" {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child creation requires mutation ID")
	}
	delegatedChildRotationMu.Lock()
	defer delegatedChildRotationMu.Unlock()

	if current, ok, err := s.GetLineage(lineage.AccountScopeID, lineage.LogicalTaskID); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	} else if ok {
		if current.LastMutationID == mutationID {
			return current, false, nil
		}
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child logical task already exists")
	}
	generation.AccountScopeID = strings.TrimSpace(lineage.AccountScopeID)
	generation.LogicalTaskID = strings.TrimSpace(lineage.LogicalTaskID)
	generation.ProgramID = firstDelegatedNonEmpty(generation.ProgramID, lineage.ProgramID)
	generation.JobID = firstDelegatedNonEmpty(generation.JobID, lineage.JobID)
	generation.Generation = 1
	generation.Revision = 1
	generation.State = DelegatedChildGenerationActive
	generation.LastMutationID = mutationID
	generation.WorkspacePath = normalizeDelegatedWorkspacePath(generation.WorkspacePath)
	if err := validateDelegatedGeneration(generation); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if strings.TrimSpace(generation.WorktreeBranch) != "" {
		if !filepath.IsAbs(generation.WorkspacePath) {
			return DelegatedChildLineageRecord{}, false, errors.New("managed worktree owner requires an absolute workspace path")
		}
		if lease, ok, err := s.GetWorktreeOwner(generation.AccountScopeID, generation.WorkspacePath); err != nil {
			return DelegatedChildLineageRecord{}, false, err
		} else if ok {
			return DelegatedChildLineageRecord{}, false, fmt.Errorf("managed worktree already owned by logical task %q generation %d session %q", lease.LogicalTaskID, lease.Generation, lease.SessionID)
		}
	}
	now := time.Now().UnixMilli()
	generation.CreatedAt, generation.UpdatedAt = now, now
	lineage.AccountScopeID = generation.AccountScopeID
	lineage.LogicalTaskID = generation.LogicalTaskID
	lineage.ProgramID, lineage.JobID = generation.ProgramID, generation.JobID
	lineage.Revision, lineage.CurrentGeneration = 1, 1
	lineage.CurrentSessionID, lineage.CurrentRunID, lineage.CurrentAttemptID = generation.SessionID, generation.RunID, generation.AttemptID
	lineage.LastMutationID = mutationID
	lineage.GenerationHistory = []DelegatedChildGenerationSummary{generationSummary(generation)}
	lineage.CreatedAt, lineage.UpdatedAt = now, now
	records := map[string]any{
		KeyDelegatedChildLineage(lineage.AccountScopeID, lineage.LogicalTaskID):             lineage,
		KeyDelegatedChildGeneration(generation.AccountScopeID, generation.LogicalTaskID, 1): generation,
		KeyDelegatedChildSessionIndex(generation.AccountScopeID, generation.SessionID):      map[string]any{"logical_task_id": generation.LogicalTaskID, "generation": generation.Generation},
	}
	if strings.TrimSpace(generation.WorktreeBranch) != "" {
		records[KeyManagedWorktreeOwnerLease(generation.AccountScopeID, generation.WorkspacePath)] = leaseForGeneration(generation, 1, mutationID, now)
	}
	if err := s.commitRecords(records); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	return lineage, true, nil
}

type UpdateDelegatedChildRunInput struct {
	AccountScopeID             string
	LogicalTaskID              string
	ExpectedLineageRevision    uint64
	ExpectedGenerationRevision uint64
	Generation                 int
	SessionID                  string
	RunID                      string
	AttemptID                  string
	MutationID                 string
}

// UpdateRun revision-safely binds a provider run to the current generation.
func (s *DelegatedChildRotationStore) UpdateRun(input UpdateDelegatedChildRunInput) (DelegatedChildLineageRecord, bool, error) {
	if err := s.configured(); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	input.AccountScopeID, input.LogicalTaskID, input.SessionID = strings.TrimSpace(input.AccountScopeID), strings.TrimSpace(input.LogicalTaskID), strings.TrimSpace(input.SessionID)
	input.RunID, input.AttemptID, input.MutationID = strings.TrimSpace(input.RunID), strings.TrimSpace(input.AttemptID), strings.TrimSpace(input.MutationID)
	if input.AccountScopeID == "" || input.LogicalTaskID == "" || input.SessionID == "" || input.RunID == "" || input.MutationID == "" || input.ExpectedLineageRevision == 0 || input.ExpectedGenerationRevision == 0 {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child run update requires identity, run, mutation ID, and expected revisions")
	}
	delegatedChildRotationMu.Lock()
	defer delegatedChildRotationMu.Unlock()
	lineage, ok, err := s.GetLineage(input.AccountScopeID, input.LogicalTaskID)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child lineage not found")
	}
	if lineage.Revision > input.ExpectedLineageRevision && lineage.LastMutationID == input.MutationID {
		return lineage, false, nil
	}
	if lineage.CurrentGeneration == input.Generation && lineage.CurrentSessionID == input.SessionID && lineage.CurrentRunID == input.RunID {
		generation, generationOK, generationErr := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation)
		if generationErr != nil {
			return DelegatedChildLineageRecord{}, false, generationErr
		}
		if generationOK && generation.RunID == input.RunID {
			return lineage, false, nil
		}
	}
	if lineage.Revision != input.ExpectedLineageRevision || lineage.CurrentGeneration != input.Generation || lineage.CurrentSessionID != input.SessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child run revision or ownership mismatch")
	}
	generation, ok, err := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok || generation.Revision != input.ExpectedGenerationRevision || generation.State != DelegatedChildGenerationActive || generation.SessionID != input.SessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child run generation revision or state mismatch")
	}
	now := time.Now().UnixMilli()
	generation.RunID, generation.AttemptID = input.RunID, input.AttemptID
	generation.Revision++
	generation.LastMutationID, generation.UpdatedAt = input.MutationID, now
	lineage.CurrentRunID, lineage.CurrentAttemptID = input.RunID, input.AttemptID
	lineage.Revision++
	lineage.LastMutationID, lineage.UpdatedAt = input.MutationID, now
	updateGenerationSummary(&lineage, generation)
	if err := s.commitRecords(map[string]any{
		KeyDelegatedChildLineage(input.AccountScopeID, input.LogicalTaskID):                      lineage,
		KeyDelegatedChildGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation): generation,
	}); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	return lineage, true, nil
}

type RetireDelegatedChildInput struct {
	AccountScopeID             string
	LogicalTaskID              string
	ExpectedLineageRevision    uint64
	ExpectedGenerationRevision uint64
	Generation                 int
	SessionID                  string
	MutationID                 string
}

// BeginRetirement revision-fences a generation before handoff construction.
// A duplicate MutationID is idempotent; stale owners fail rather than retiring
// a newer generation.
func (s *DelegatedChildRotationStore) BeginRetirement(input RetireDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if err := s.configured(); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	input.AccountScopeID, input.LogicalTaskID, input.SessionID, input.MutationID = strings.TrimSpace(input.AccountScopeID), strings.TrimSpace(input.LogicalTaskID), strings.TrimSpace(input.SessionID), strings.TrimSpace(input.MutationID)
	if input.AccountScopeID == "" || input.LogicalTaskID == "" || input.SessionID == "" || input.MutationID == "" || input.ExpectedLineageRevision == 0 || input.ExpectedGenerationRevision == 0 {
		return DelegatedChildLineageRecord{}, false, errors.New("retirement requires identity, mutation ID, and expected revisions")
	}
	delegatedChildRotationMu.Lock()
	defer delegatedChildRotationMu.Unlock()
	lineage, ok, err := s.GetLineage(input.AccountScopeID, input.LogicalTaskID)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child lineage not found")
	}
	if lineage.Revision > input.ExpectedLineageRevision && lineage.LastMutationID == input.MutationID {
		return lineage, false, nil
	}
	if lineage.Revision != input.ExpectedLineageRevision || lineage.CurrentGeneration != input.Generation || lineage.CurrentSessionID != input.SessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child retirement revision or ownership mismatch")
	}
	generation, ok, err := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok || generation.Revision != input.ExpectedGenerationRevision || generation.State != DelegatedChildGenerationActive || generation.SessionID != input.SessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child generation retirement revision or state mismatch")
	}
	now := time.Now().UnixMilli()
	generation.State, generation.Revision = DelegatedChildGenerationRetiring, generation.Revision+1
	generation.LastMutationID, generation.UpdatedAt = input.MutationID, now
	lineage.Revision++
	lineage.LastMutationID, lineage.UpdatedAt = input.MutationID, now
	updateGenerationSummary(&lineage, generation)
	if err := s.commitRecords(map[string]any{
		KeyDelegatedChildLineage(input.AccountScopeID, input.LogicalTaskID):                      lineage,
		KeyDelegatedChildGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation): generation,
	}); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	return lineage, true, nil
}

type RotateDelegatedChildInput struct {
	AccountScopeID              string
	LogicalTaskID               string
	ExpectedLineageRevision     uint64
	ExpectedPredecessorRevision uint64
	ExpectedLeaseRevision       uint64
	PredecessorGeneration       int
	PredecessorSessionID        string
	MutationID                  string
	Successor                   DelegatedChildGenerationRecord
	Handoff                     DelegatedChildTargetedHandoff
}

// Rotate atomically retires and fences the predecessor, stores its targeted
// handoff, advances the stable logical task, and transfers the exact existing
// workspace/branch lease to one successor. It never touches filesystem state.
func (s *DelegatedChildRotationStore) Rotate(input RotateDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if err := s.configured(); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	input.AccountScopeID, input.LogicalTaskID, input.MutationID = strings.TrimSpace(input.AccountScopeID), strings.TrimSpace(input.LogicalTaskID), strings.TrimSpace(input.MutationID)
	if input.AccountScopeID == "" || input.LogicalTaskID == "" || input.MutationID == "" || input.ExpectedLineageRevision == 0 || input.ExpectedPredecessorRevision == 0 {
		return DelegatedChildLineageRecord{}, false, errors.New("rotation requires account, logical task, mutation ID, and expected revisions")
	}
	delegatedChildRotationMu.Lock()
	defer delegatedChildRotationMu.Unlock()
	lineage, ok, err := s.GetLineage(input.AccountScopeID, input.LogicalTaskID)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child lineage not found")
	}
	if lineage.Revision > input.ExpectedLineageRevision && lineage.LastMutationID == input.MutationID {
		return lineage, false, nil
	}
	if lineage.Revision != input.ExpectedLineageRevision {
		return DelegatedChildLineageRecord{}, false, fmt.Errorf("delegated child lineage revision mismatch: expected %d, current %d", input.ExpectedLineageRevision, lineage.Revision)
	}
	if lineage.CurrentGeneration != input.PredecessorGeneration || lineage.CurrentSessionID != strings.TrimSpace(input.PredecessorSessionID) {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child predecessor ownership mismatch")
	}
	predecessor, ok, err := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, input.PredecessorGeneration)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child predecessor generation not found")
	}
	if predecessor.Revision != input.ExpectedPredecessorRevision || (predecessor.State != DelegatedChildGenerationActive && predecessor.State != DelegatedChildGenerationRetiring) || predecessor.SessionID != lineage.CurrentSessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child predecessor generation revision or state mismatch")
	}
	lease := ManagedWorktreeOwnerLease{}
	managedWorktree := strings.TrimSpace(predecessor.WorktreeBranch) != ""
	if managedWorktree {
		var err error
		lease, ok, err = s.GetWorktreeOwner(input.AccountScopeID, predecessor.WorkspacePath)
		if err != nil {
			return DelegatedChildLineageRecord{}, false, err
		}
		if input.ExpectedLeaseRevision == 0 || !ok || lease.Revision != input.ExpectedLeaseRevision || lease.LogicalTaskID != input.LogicalTaskID || lease.Generation != predecessor.Generation || lease.SessionID != predecessor.SessionID {
			return DelegatedChildLineageRecord{}, false, errors.New("managed worktree owner lease revision or ownership mismatch")
		}
	}

	successor := input.Successor
	successor.AccountScopeID, successor.LogicalTaskID = input.AccountScopeID, input.LogicalTaskID
	successor.ProgramID, successor.JobID = lineage.ProgramID, lineage.JobID
	successor.Generation, successor.Revision, successor.State = predecessor.Generation+1, 1, DelegatedChildGenerationActive
	successor.PredecessorSessionID = predecessor.SessionID
	successor.WorkspacePath, successor.WorktreeBranch, successor.ParentBranch = predecessor.WorkspacePath, predecessor.WorktreeBranch, predecessor.ParentBranch
	successor.ImmutableBaseCommit = firstDelegatedNonEmpty(successor.ImmutableBaseCommit, predecessor.ImmutableBaseCommit)
	successor.PermissionPrincipalID = firstDelegatedNonEmpty(successor.PermissionPrincipalID, predecessor.PermissionPrincipalID)
	successor.PermissionScopeID = firstDelegatedNonEmpty(successor.PermissionScopeID, predecessor.PermissionScopeID)
	successor.ReservationSessionID = firstDelegatedNonEmpty(successor.ReservationSessionID, predecessor.ReservationSessionID)
	successor.ReservationRunID = firstDelegatedNonEmpty(successor.ReservationRunID, predecessor.ReservationRunID)
	successor.ReservationCallID = firstDelegatedNonEmpty(successor.ReservationCallID, predecessor.ReservationCallID)
	successor.ParentSessionID = firstDelegatedNonEmpty(successor.ParentSessionID, predecessor.ParentSessionID)
	successor.ParentRunID = firstDelegatedNonEmpty(successor.ParentRunID, predecessor.ParentRunID)
	successor.ManagedArtifactParentSessionID = firstDelegatedNonEmpty(successor.ManagedArtifactParentSessionID, predecessor.ManagedArtifactParentSessionID)
	successor.ManagedArtifactCollectionID = firstDelegatedNonEmpty(successor.ManagedArtifactCollectionID, predecessor.ManagedArtifactCollectionID)
	successor.ManagedArtifactVariantID = firstDelegatedNonEmpty(successor.ManagedArtifactVariantID, predecessor.ManagedArtifactVariantID)
	successor.ManagedArtifactTaskCallID = firstDelegatedNonEmpty(successor.ManagedArtifactTaskCallID, predecessor.ManagedArtifactTaskCallID)
	successor.ManagedArtifactProgramID = firstDelegatedNonEmpty(successor.ManagedArtifactProgramID, predecessor.ManagedArtifactProgramID)
	successor.ManagedArtifactProgramJobID = firstDelegatedNonEmpty(successor.ManagedArtifactProgramJobID, predecessor.ManagedArtifactProgramJobID)
	successor.LastMutationID = input.MutationID
	successor.WorkspacePath = normalizeDelegatedWorkspacePath(successor.WorkspacePath)
	if err := validateDelegatedGeneration(successor); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if existing, exists, err := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, successor.Generation); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	} else if exists {
		if existing.PredecessorSessionID != predecessor.SessionID || existing.SessionID != successor.SessionID || existing.State != DelegatedChildGenerationActive {
			return DelegatedChildLineageRecord{}, false, errors.New("delegated child successor generation identity mismatch")
		}
		successor = existing
	}

	now := time.Now().UnixMilli()
	predecessor.State, predecessor.SuccessorSessionID = DelegatedChildGenerationRetired, successor.SessionID
	predecessor.Revision++
	predecessor.LastMutationID, predecessor.UpdatedAt, predecessor.FinishedAt = input.MutationID, now, now
	successor.CreatedAt, successor.UpdatedAt = now, now
	handoff := normalizeDelegatedHandoff(input.Handoff)
	handoff.AccountScopeID, handoff.LogicalTaskID = input.AccountScopeID, input.LogicalTaskID
	handoff.PredecessorGeneration, handoff.SuccessorGeneration = predecessor.Generation, successor.Generation
	handoff.PredecessorSessionID, handoff.SuccessorSessionID = predecessor.SessionID, successor.SessionID
	handoff.CreatedAt = now
	if strings.TrimSpace(handoff.Objective) == "" || len(handoff.NextActions) == 0 {
		return DelegatedChildLineageRecord{}, false, errors.New("targeted successor handoff requires objective and next actions")
	}
	lineage.Revision++
	lineage.CurrentGeneration, lineage.CurrentSessionID = successor.Generation, successor.SessionID
	lineage.CurrentRunID, lineage.CurrentAttemptID = successor.RunID, successor.AttemptID
	lineage.LastMutationID, lineage.UpdatedAt = input.MutationID, now
	updateGenerationSummary(&lineage, predecessor)
	lineage.GenerationHistory = appendBoundedGenerationHistory(lineage.GenerationHistory, generationSummary(successor))
	records := map[string]any{
		KeyDelegatedChildLineage(input.AccountScopeID, input.LogicalTaskID):                            lineage,
		KeyDelegatedChildGeneration(input.AccountScopeID, input.LogicalTaskID, predecessor.Generation): predecessor,
		KeyDelegatedChildGeneration(input.AccountScopeID, input.LogicalTaskID, successor.Generation):   successor,
		KeyDelegatedChildHandoff(input.AccountScopeID, input.LogicalTaskID, predecessor.Generation):    handoff,
		KeyDelegatedChildSessionIndex(input.AccountScopeID, successor.SessionID):                       map[string]any{"logical_task_id": input.LogicalTaskID, "generation": successor.Generation},
	}
	if managedWorktree {
		lease.Generation, lease.SessionID, lease.Revision = successor.Generation, successor.SessionID, lease.Revision+1
		lease.LastMutationID, lease.UpdatedAt = input.MutationID, now
		records[KeyManagedWorktreeOwnerLease(input.AccountScopeID, predecessor.WorkspacePath)] = lease
	}
	if err := s.commitRecords(records); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	return lineage, true, nil
}

type FinishDelegatedChildInput struct {
	AccountScopeID             string
	LogicalTaskID              string
	ExpectedLineageRevision    uint64
	ExpectedGenerationRevision uint64
	Generation                 int
	SessionID                  string
	State                      string
	MutationID                 string
}

// Finish terminalizes only the current generation and stable logical task. The
// lease remains as an ownership fence until an explicit higher-level cleanup.
func (s *DelegatedChildRotationStore) Finish(input FinishDelegatedChildInput) (DelegatedChildLineageRecord, bool, error) {
	if err := s.configured(); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	input.AccountScopeID, input.LogicalTaskID, input.SessionID, input.MutationID = strings.TrimSpace(input.AccountScopeID), strings.TrimSpace(input.LogicalTaskID), strings.TrimSpace(input.SessionID), strings.TrimSpace(input.MutationID)
	if input.State != DelegatedChildGenerationSucceeded && input.State != DelegatedChildGenerationFailed {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child finish state must be succeeded or failed")
	}
	delegatedChildRotationMu.Lock()
	defer delegatedChildRotationMu.Unlock()
	lineage, ok, err := s.GetLineage(input.AccountScopeID, input.LogicalTaskID)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child lineage not found")
	}
	if lineage.Revision > input.ExpectedLineageRevision && lineage.LastMutationID == input.MutationID {
		return lineage, false, nil
	}
	if input.MutationID == "" || lineage.Revision != input.ExpectedLineageRevision || lineage.CurrentGeneration != input.Generation || lineage.CurrentSessionID != input.SessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child finish revision or ownership mismatch")
	}
	generation, ok, err := s.GetGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation)
	if err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	if !ok || generation.Revision != input.ExpectedGenerationRevision || (generation.State != DelegatedChildGenerationActive && generation.State != DelegatedChildGenerationRetiring) || generation.SessionID != lineage.CurrentSessionID {
		return DelegatedChildLineageRecord{}, false, errors.New("delegated child generation finish revision or state mismatch")
	}
	now := time.Now().UnixMilli()
	generation.State, generation.Revision, generation.LastMutationID = input.State, generation.Revision+1, input.MutationID
	generation.UpdatedAt, generation.FinishedAt = now, now
	lineage.Revision++
	lineage.LastMutationID, lineage.UpdatedAt = generation.LastMutationID, now
	updateGenerationSummary(&lineage, generation)
	if err := s.commitRecords(map[string]any{
		KeyDelegatedChildLineage(input.AccountScopeID, input.LogicalTaskID):                      lineage,
		KeyDelegatedChildGeneration(input.AccountScopeID, input.LogicalTaskID, input.Generation): generation,
	}); err != nil {
		return DelegatedChildLineageRecord{}, false, err
	}
	return lineage, true, nil
}

func (s *DelegatedChildRotationStore) configured() error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("delegated child rotation store is not configured")
	}
	return nil
}

func (s *DelegatedChildRotationStore) commitRecords(records map[string]any) error {
	batch := s.store.NewBatch()
	defer batch.Close()
	for key, record := range records {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal delegated child record %q: %w", key, err)
		}
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit delegated child transition: %w", err)
	}
	return nil
}

func validateDelegatedGeneration(record DelegatedChildGenerationRecord) error {
	if strings.TrimSpace(record.AccountScopeID) == "" || strings.TrimSpace(record.LogicalTaskID) == "" || record.Generation < 1 || strings.TrimSpace(record.SessionID) == "" {
		return errors.New("delegated child generation requires account, logical task, generation, and session")
	}
	if normalizeDelegatedWorkspacePath(record.WorkspacePath) == "" {
		return errors.New("delegated child generation requires workspace path")
	}
	return nil
}

func normalizeDelegatedWorkspacePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func normalizeDelegatedHandoff(record DelegatedChildTargetedHandoff) DelegatedChildTargetedHandoff {
	record.Objective = boundedDelegatedText(record.Objective)
	record.Completed = boundedDelegatedRows(record.Completed)
	record.NextActions = boundedDelegatedRows(record.NextActions)
	record.Decisions = boundedDelegatedRows(record.Decisions)
	record.Constraints = boundedDelegatedRows(record.Constraints)
	record.RelevantFiles = boundedDelegatedRows(record.RelevantFiles)
	record.Validation = boundedDelegatedRows(record.Validation)
	return record
}

func boundedDelegatedRows(rows []string) []string {
	if len(rows) > maxDelegatedChildHandoffRows {
		rows = rows[:maxDelegatedChildHandoffRows]
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row = boundedDelegatedText(row); row != "" {
			out = append(out, row)
		}
	}
	return out
}

func boundedDelegatedText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxDelegatedChildHandoffTextRunes {
		runes = runes[:maxDelegatedChildHandoffTextRunes]
	}
	return string(runes)
}

func leaseForGeneration(record DelegatedChildGenerationRecord, revision uint64, mutationID string, now int64) ManagedWorktreeOwnerLease {
	return ManagedWorktreeOwnerLease{AccountScopeID: record.AccountScopeID, WorkspacePath: normalizeDelegatedWorkspacePath(record.WorkspacePath), WorktreeBranch: record.WorktreeBranch, ParentBranch: record.ParentBranch, LogicalTaskID: record.LogicalTaskID, Generation: record.Generation, SessionID: record.SessionID, Revision: revision, LastMutationID: mutationID, CreatedAt: now, UpdatedAt: now}
}

func generationSummary(record DelegatedChildGenerationRecord) DelegatedChildGenerationSummary {
	return DelegatedChildGenerationSummary{Generation: record.Generation, SessionID: record.SessionID, RunID: record.RunID, AttemptID: record.AttemptID, State: record.State, PredecessorID: record.PredecessorSessionID, SuccessorID: record.SuccessorSessionID, StartedAt: record.CreatedAt, FinishedAt: record.FinishedAt}
}

func updateGenerationSummary(lineage *DelegatedChildLineageRecord, record DelegatedChildGenerationRecord) {
	for i := range lineage.GenerationHistory {
		if lineage.GenerationHistory[i].Generation == record.Generation {
			lineage.GenerationHistory[i] = generationSummary(record)
			return
		}
	}
}

func appendBoundedGenerationHistory(history []DelegatedChildGenerationSummary, summary DelegatedChildGenerationSummary) []DelegatedChildGenerationSummary {
	history = append(history, summary)
	if len(history) > maxDelegatedChildGenerationHistory {
		history = append([]DelegatedChildGenerationSummary(nil), history[len(history)-maxDelegatedChildGenerationHistory:]...)
	}
	return history
}

func firstDelegatedNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
