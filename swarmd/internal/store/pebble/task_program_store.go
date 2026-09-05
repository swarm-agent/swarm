package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	TaskProgramStateDeclared  = "declared"
	TaskProgramStateRunning   = "running"
	TaskProgramStateBlocked   = "blocked"
	TaskProgramStateFailed    = "failed"
	TaskProgramStateCancelled = "cancelled"
	TaskProgramStateCompleted = "completed"

	TaskProgramJobDeclared     = "declared"
	TaskProgramJobRunning      = "running"
	TaskProgramJobHandoffReady = "handoff_ready"
	TaskProgramJobIntegrated   = "integrated"
	TaskProgramJobBlocked      = "blocked"
	TaskProgramJobFailed       = "failed"
	TaskProgramJobCancelled    = "cancelled"
	TaskProgramJobCompleted    = "completed"
)

const (
	maxTaskProgramRecordsPerSession = 256
	maxTaskProgramJobs              = 256
	maxTaskProgramStages            = 256
	maxTaskProgramTextRunes         = 4096
	maxTaskProgramScopeRows         = 256
)

// TaskProgramRecord is the bounded, restart-safe authority for one fully
// declared program. It deliberately stores no child transcript, report, or
// artifact content; those remain authoritative in their native stores.
type TaskProgramRecord struct {
	ParentSessionID   string                 `json:"parent_session_id"`
	ProgramID         string                 `json:"program_id"`
	DefinitionHash    string                 `json:"definition_hash"`
	ReservationRunID  string                 `json:"reservation_run_id,omitempty"`
	ReservationCallID string                 `json:"reservation_call_id,omitempty"`
	Definition        TaskProgramDefinition  `json:"definition"`
	Revision          int                    `json:"revision"`
	ActiveStageID     string                 `json:"active_stage_id,omitempty"`
	ParentHead        string                 `json:"parent_head,omitempty"`
	State             string                 `json:"state"`
	NextAction        string                 `json:"next_action"`
	LastMutationID    string                 `json:"last_mutation_id,omitempty"`
	Blocker           *TaskProgramBlocker    `json:"blocker,omitempty"`
	Jobs              []TaskProgramJobRecord `json:"jobs"`
	CreatedAt         int64                  `json:"created_at"`
	UpdatedAt         int64                  `json:"updated_at"`
}

// TaskProgramDefinition is the canonical staged implementation graph. It can
// be embedded in an approved lifecycle checkpoint before runtime state exists,
// then copied unchanged into TaskProgramRecord when execution starts.
type TaskProgramDefinition struct {
	ID             string                 `json:"id,omitempty"`
	MaxConcurrency int                    `json:"max_concurrency,omitempty"`
	Stages         []TaskProgramStageSpec `json:"stages"`
	Jobs           []TaskProgramJobSpec   `json:"jobs"`
}

type TaskProgramStageSpec struct {
	ID                 string   `json:"id"`
	DependsOn          []string `json:"depends_on,omitempty"`
	DependencyEvidence string   `json:"dependency_evidence"`
}

type TaskProgramJobSpec struct {
	ID                 string                             `json:"id"`
	StageID            string                             `json:"stage_id"`
	DependsOn          []string                           `json:"depends_on,omitempty"`
	AgentType          string                             `json:"agent_type"`
	WorkspacePath      string                             `json:"workspace_path,omitempty"`
	Title              string                             `json:"title"`
	MetaPrompt         string                             `json:"meta_prompt"`
	Deliverable        string                             `json:"deliverable"`
	OwnedScope         []string                           `json:"owned_scope,omitempty"`
	OutputMode         string                             `json:"output_mode,omitempty"`
	OutputRequirements *SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	AnimationProfile   *SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	AcceptanceCriteria []string                           `json:"acceptance_criteria"`
	DependencyEvidence string                             `json:"dependency_evidence"`
}

// UnmarshalJSON accepts the tool-advertised alias at the durable plan boundary
// and stores only agent_type. Conflicting identities fail without mutating j.
func (j *TaskProgramJobSpec) UnmarshalJSON(data []byte) error {
	type canonical TaskProgramJobSpec
	var decoded struct {
		canonical
		SubagentType string `json:"subagent_type"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	agentType := strings.TrimSpace(decoded.AgentType)
	alias := strings.TrimSpace(decoded.SubagentType)
	if agentType != "" && alias != "" && !strings.EqualFold(agentType, alias) {
		return errors.New("task program agent_type conflicts with subagent_type")
	}
	if agentType == "" {
		agentType = alias
	}
	decoded.AgentType = strings.ToLower(agentType)
	*j = TaskProgramJobSpec(decoded.canonical)
	return nil
}

type TaskProgramJobRecord struct {
	JobID              string                     `json:"job_id"`
	StageID            string                     `json:"stage_id"`
	State              string                     `json:"state"`
	AttemptNumber      int                        `json:"attempt_number"`
	ChildSessionID     string                     `json:"child_session_id,omitempty"`
	CurrentSessionID   string                     `json:"current_session_id,omitempty"`
	CurrentRunID       string                     `json:"current_run_id,omitempty"`
	CurrentGeneration  int                        `json:"current_generation,omitempty"`
	GenerationHistory  []TaskProgramJobGeneration `json:"generation_history,omitempty"`
	WorkspacePath      string                     `json:"workspace_path,omitempty"`
	WorktreeBranch     string                     `json:"worktree_branch,omitempty"`
	ParentBranch       string                     `json:"parent_branch,omitempty"`
	ImmutableStageBase string                     `json:"immutable_stage_base,omitempty"`
	ChildHead          string                     `json:"child_head,omitempty"`
	IntegrationState   string                     `json:"integration_state,omitempty"`
	HandoffRef         *TaskProgramHandoffRef     `json:"handoff_ref,omitempty"`
	Blocker            *TaskProgramBlocker        `json:"blocker,omitempty"`
	UpdatedAt          int64                      `json:"updated_at"`
}

type TaskProgramHandoffRef struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	GlobalSeq uint64 `json:"global_seq"`
}

type TaskProgramJobGeneration struct {
	Generation           int    `json:"generation"`
	SessionID            string `json:"session_id"`
	RunID                string `json:"run_id,omitempty"`
	State                string `json:"state"`
	PredecessorSessionID string `json:"predecessor_session_id,omitempty"`
	SuccessorSessionID   string `json:"successor_session_id,omitempty"`
	StartedAt            int64  `json:"started_at"`
	FinishedAt           int64  `json:"finished_at,omitempty"`
}

type TaskProgramBlocker struct {
	Code                  string                      `json:"code"`
	Message               string                      `json:"message"`
	Evidence              []string                    `json:"evidence,omitempty"`
	CompletedScope        []string                    `json:"completed_scope,omitempty"`
	ResolutionRequirement string                      `json:"resolution_requirement,omitempty"`
	Dirty                 bool                        `json:"dirty,omitempty"`
	ChangedFiles          []string                    `json:"changed_files,omitempty"`
	NextAction            string                      `json:"next_action"`
	RepairAction          string                      `json:"repair_action,omitempty"`
	ProgramID             string                      `json:"program_id,omitempty"`
	ProgramRevision       int                         `json:"program_revision,omitempty"`
	StageID               string                      `json:"stage_id,omitempty"`
	JobID                 string                      `json:"job_id,omitempty"`
	AttemptNumber         int                         `json:"attempt_number,omitempty"`
	ExpectedParentHead    string                      `json:"expected_parent_head,omitempty"`
	PreservedChildren     []TaskProgramPreservedChild `json:"preserved_children,omitempty"`
}

type TaskProgramPreservedChild struct {
	JobID              string   `json:"job_id"`
	State              string   `json:"state"`
	AttemptNumber      int      `json:"attempt_number"`
	ChildSessionID     string   `json:"child_session_id,omitempty"`
	RunID              string   `json:"run_id,omitempty"`
	WorkspacePath      string   `json:"workspace_path,omitempty"`
	WorktreeBranch     string   `json:"worktree_branch,omitempty"`
	ParentBranch       string   `json:"parent_branch,omitempty"`
	ImmutableStageBase string   `json:"immutable_stage_base,omitempty"`
	ChildHead          string   `json:"child_head,omitempty"`
	IntegrationState   string   `json:"integration_state,omitempty"`
	Dirty              bool     `json:"dirty,omitempty"`
	ChangedFiles       []string `json:"changed_files,omitempty"`
}

type TaskProgramTransition struct {
	ExpectedRevision int
	MutationID       string
	State            *string
	ActiveStageID    *string
	NextAction       *string
	ParentHead       *string
	Blocker          *TaskProgramBlocker
	ClearBlocker     bool
	Jobs             []TaskProgramJobTransition
}

type TaskProgramJobTransition struct {
	JobID              string
	ExpectedState      string
	State              string
	AttemptNumber      int
	ChildSessionID     string
	CurrentSessionID   string
	CurrentRunID       string
	CurrentGeneration  int
	GenerationHistory  []TaskProgramJobGeneration
	WorkspacePath      string
	WorktreeBranch     string
	ParentBranch       string
	ImmutableStageBase string
	ChildHead          string
	IntegrationState   string
	HandoffRef         *TaskProgramHandoffRef
	Blocker            *TaskProgramBlocker
	ClearBlocker       bool
}

var taskProgramLocks sync.Map

func taskProgramLock(parentSessionID, programID string) *sync.Mutex {
	key := KeyTaskProgram(parentSessionID, programID)
	value, _ := taskProgramLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *SessionStore) GetTaskProgram(parentSessionID, programID string) (TaskProgramRecord, bool, error) {
	var record TaskProgramRecord
	if s == nil || s.store == nil {
		return record, false, errors.New("session store is not configured")
	}
	ok, err := s.store.GetJSON(KeyTaskProgram(parentSessionID, programID), &record)
	if err != nil {
		return record, false, fmt.Errorf("get task program: %w", err)
	}
	return record, ok, nil
}

func (s *SessionStore) ListTaskPrograms(parentSessionID string) ([]TaskProgramRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	out := make([]TaskProgramRecord, 0)
	err := s.store.IteratePrefix(TaskProgramSessionPrefix(parentSessionID), maxTaskProgramRecordsPerSession, func(_ string, value []byte) error {
		var record TaskProgramRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	return out, err
}

// CreateTaskProgram is idempotent for an identical validated definition and
// rejects reuse of a parent-scoped program ID for different work.
func (s *SessionStore) CreateTaskProgram(record TaskProgramRecord) (TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return TaskProgramRecord{}, false, errors.New("session store is not configured")
	}
	if err := validateTaskProgramRecord(record); err != nil {
		return TaskProgramRecord{}, false, err
	}
	lock := taskProgramLock(record.ParentSessionID, record.ProgramID)
	lock.Lock()
	defer lock.Unlock()
	if current, ok, err := s.GetTaskProgram(record.ParentSessionID, record.ProgramID); err != nil {
		return TaskProgramRecord{}, false, err
	} else if ok {
		if current.DefinitionHash != record.DefinitionHash {
			return TaskProgramRecord{}, false, errors.New("task program ID already exists with a different validated definition")
		}
		return current, false, nil
	}
	now := time.Now().UnixMilli()
	record.Revision = 1
	if record.State == "" {
		record.State = TaskProgramStateDeclared
	}
	for i := range record.Jobs {
		if record.Jobs[i].State == "" {
			record.Jobs[i].State = TaskProgramJobDeclared
		}
		record.Jobs[i].UpdatedAt = now
	}
	record.CreatedAt, record.UpdatedAt = now, now
	if err := s.store.PutJSON(KeyTaskProgram(record.ParentSessionID, record.ProgramID), record); err != nil {
		return TaskProgramRecord{}, false, fmt.Errorf("persist task program: %w", err)
	}
	return record, true, nil
}

// TransitionTaskProgram applies a revision-guarded mutation. MutationID makes
// duplicate provider callbacks idempotent without retaining an unbounded log:
// callers derive it from the exact expected revision and requested transition.
func (s *SessionStore) TransitionTaskProgram(parentSessionID, programID string, transition TaskProgramTransition) (TaskProgramRecord, bool, error) {
	if transition.ExpectedRevision < 1 || strings.TrimSpace(transition.MutationID) == "" {
		return TaskProgramRecord{}, false, errors.New("task program transition requires expected revision and mutation ID")
	}
	lock := taskProgramLock(parentSessionID, programID)
	lock.Lock()
	defer lock.Unlock()
	record, ok, err := s.GetTaskProgram(parentSessionID, programID)
	if err != nil {
		return TaskProgramRecord{}, false, err
	}
	if !ok {
		return TaskProgramRecord{}, false, errors.New("task program not found for parent session")
	}
	if record.Revision > transition.ExpectedRevision {
		if record.LastMutationID == strings.TrimSpace(transition.MutationID) {
			return record, false, nil
		}
		return TaskProgramRecord{}, false, fmt.Errorf("task program revision mismatch: expected %d, current %d", transition.ExpectedRevision, record.Revision)
	}
	if record.Revision != transition.ExpectedRevision {
		return TaskProgramRecord{}, false, fmt.Errorf("task program revision mismatch: expected %d, current %d", transition.ExpectedRevision, record.Revision)
	}
	if transition.State != nil {
		record.State = strings.TrimSpace(*transition.State)
	}
	if transition.ActiveStageID != nil {
		record.ActiveStageID = strings.TrimSpace(*transition.ActiveStageID)
	}
	if transition.NextAction != nil {
		record.NextAction = boundedTaskProgramText(*transition.NextAction)
	}
	if transition.ParentHead != nil {
		record.ParentHead = strings.TrimSpace(*transition.ParentHead)
	}
	if transition.ClearBlocker {
		record.Blocker = nil
	} else if transition.Blocker != nil {
		blocker := normalizedTaskProgramBlocker(*transition.Blocker)
		record.Blocker = &blocker
	}
	jobIndexes := make(map[string]int, len(record.Jobs))
	for i := range record.Jobs {
		jobIndexes[record.Jobs[i].JobID] = i
	}
	now := time.Now().UnixMilli()
	for _, update := range transition.Jobs {
		index, exists := jobIndexes[strings.TrimSpace(update.JobID)]
		if !exists {
			return TaskProgramRecord{}, false, fmt.Errorf("task program job %q not found", update.JobID)
		}
		job := &record.Jobs[index]
		if expected := strings.TrimSpace(update.ExpectedState); expected != "" && job.State != expected {
			return TaskProgramRecord{}, false, fmt.Errorf("task program job %q state mismatch: expected %s, current %s", job.JobID, expected, job.State)
		}
		applyTaskProgramJobTransition(job, update)
		job.UpdatedAt = now
	}
	record.Revision++
	record.LastMutationID = strings.TrimSpace(transition.MutationID)
	record.UpdatedAt = now
	if err := validateTaskProgramRecord(record); err != nil {
		return TaskProgramRecord{}, false, err
	}
	if err := s.store.PutJSON(KeyTaskProgram(parentSessionID, programID), record); err != nil {
		return TaskProgramRecord{}, false, fmt.Errorf("persist task program transition: %w", err)
	}
	return record, true, nil
}

func applyTaskProgramJobTransition(job *TaskProgramJobRecord, update TaskProgramJobTransition) {
	if state := strings.TrimSpace(update.State); state != "" {
		job.State = state
	}
	if update.AttemptNumber > 0 {
		job.AttemptNumber = update.AttemptNumber
	}
	if value := strings.TrimSpace(update.ChildSessionID); value != "" {
		job.ChildSessionID = value
	}
	if value := strings.TrimSpace(update.CurrentSessionID); value != "" {
		job.CurrentSessionID = value
	}
	if value := strings.TrimSpace(update.CurrentRunID); value != "" {
		job.CurrentRunID = value
	}
	if update.CurrentGeneration > 0 {
		job.CurrentGeneration = update.CurrentGeneration
	}
	if update.GenerationHistory != nil {
		job.GenerationHistory = append([]TaskProgramJobGeneration(nil), update.GenerationHistory...)
	}
	if value := strings.TrimSpace(update.WorkspacePath); value != "" {
		job.WorkspacePath = value
	}
	if value := strings.TrimSpace(update.WorktreeBranch); value != "" {
		job.WorktreeBranch = value
	}
	if value := strings.TrimSpace(update.ParentBranch); value != "" {
		job.ParentBranch = value
	}
	if value := strings.TrimSpace(update.ImmutableStageBase); value != "" {
		job.ImmutableStageBase = value
	}
	if value := strings.TrimSpace(update.ChildHead); value != "" {
		job.ChildHead = value
	}
	if value := strings.TrimSpace(update.IntegrationState); value != "" {
		job.IntegrationState = value
	}
	if update.HandoffRef != nil {
		ref := normalizedTaskProgramHandoffRef(*update.HandoffRef)
		job.HandoffRef = &ref
	}
	if update.ClearBlocker {
		job.Blocker = nil
	} else if update.Blocker != nil {
		blocker := normalizedTaskProgramBlocker(*update.Blocker)
		job.Blocker = &blocker
	}
}

func validateTaskProgramRecord(record TaskProgramRecord) error {
	if strings.TrimSpace(record.ParentSessionID) == "" || strings.TrimSpace(record.ProgramID) == "" || strings.TrimSpace(record.DefinitionHash) == "" {
		return errors.New("task program requires parent session, program ID, and definition hash")
	}
	if len(record.Definition.Stages) < 1 || len(record.Definition.Stages) > maxTaskProgramStages || len(record.Definition.Jobs) < 1 || len(record.Definition.Jobs) > maxTaskProgramJobs {
		return errors.New("task program definition size is invalid")
	}
	if len(record.Jobs) != len(record.Definition.Jobs) {
		return errors.New("task program job projection does not match definition")
	}
	if record.Revision < 0 {
		return errors.New("task program revision is invalid")
	}
	if len([]rune(record.NextAction)) > maxTaskProgramTextRunes {
		return errors.New("task program next action exceeds bounded storage limit")
	}
	for i := range record.Jobs {
		job := &record.Jobs[i]
		if job.CurrentSessionID == "" {
			job.CurrentSessionID = job.ChildSessionID
		}
		if job.CurrentGeneration == 0 && job.CurrentSessionID != "" {
			job.CurrentGeneration = 1
		}
		if len(job.GenerationHistory) > maxDelegatedChildGenerationHistory {
			return fmt.Errorf("task program job %q exceeds bounded generation history", job.JobID)
		}
		if job.HandoffRef != nil {
			ref := normalizedTaskProgramHandoffRef(*job.HandoffRef)
			if ref.SessionID == "" || ref.MessageID == "" || ref.GlobalSeq == 0 || ref.SessionID != job.CurrentSessionID {
				return fmt.Errorf("task program job %q has invalid handoff reference", job.JobID)
			}
			job.HandoffRef = &ref
		}
	}
	for _, job := range record.Definition.Jobs {
		if len(job.OwnedScope) > maxTaskProgramScopeRows || len(job.AcceptanceCriteria) > maxTaskProgramScopeRows || len([]rune(job.WorkspacePath)) > maxTaskProgramTextRunes || len([]rune(job.MetaPrompt)) > maxTaskProgramTextRunes || len([]rune(job.Deliverable)) > maxTaskProgramTextRunes {
			return fmt.Errorf("task program job %q exceeds bounded definition limits", job.ID)
		}
		mode := strings.TrimSpace(job.OutputMode)
		if mode != "" && mode != "managed" && mode != "workspace" {
			return fmt.Errorf("task program job %q has invalid output mode", job.ID)
		}
		if strings.EqualFold(strings.TrimSpace(job.AgentType), "designer") {
			if err := validateArtifactAnimationProfile(job.AnimationProfile); err != nil {
				return fmt.Errorf("task program Designer job %q animation profile is invalid: %w", job.ID, err)
			}
			if mode == "managed" && len(job.OwnedScope) != 0 {
				return fmt.Errorf("task program managed Designer job %q must omit owned scope", job.ID)
			}
			if mode == "workspace" && len(job.OwnedScope) == 0 {
				return fmt.Errorf("task program workspace Designer job %q requires owned scope", job.ID)
			}
		} else if mode != "" {
			return fmt.Errorf("task program non-Designer job %q cannot set output mode", job.ID)
		}
	}
	return nil
}

func normalizedTaskProgramHandoffRef(ref TaskProgramHandoffRef) TaskProgramHandoffRef {
	ref.SessionID = boundedTaskProgramText(ref.SessionID)
	ref.MessageID = boundedTaskProgramText(ref.MessageID)
	return ref
}

func normalizedTaskProgramBlocker(blocker TaskProgramBlocker) TaskProgramBlocker {
	blocker.Code = boundedTaskProgramText(blocker.Code)
	blocker.Message = boundedTaskProgramText(blocker.Message)
	blocker.Evidence = boundedTaskProgramStrings(blocker.Evidence)
	blocker.CompletedScope = boundedTaskProgramStrings(blocker.CompletedScope)
	blocker.ResolutionRequirement = boundedTaskProgramText(blocker.ResolutionRequirement)
	blocker.ChangedFiles = boundedTaskProgramStrings(blocker.ChangedFiles)
	blocker.NextAction = boundedTaskProgramText(blocker.NextAction)
	blocker.RepairAction = boundedTaskProgramText(blocker.RepairAction)
	blocker.ProgramID = boundedTaskProgramText(blocker.ProgramID)
	blocker.StageID = boundedTaskProgramText(blocker.StageID)
	blocker.JobID = boundedTaskProgramText(blocker.JobID)
	blocker.ExpectedParentHead = boundedTaskProgramText(blocker.ExpectedParentHead)
	if len(blocker.PreservedChildren) > maxTaskProgramJobs {
		blocker.PreservedChildren = blocker.PreservedChildren[:maxTaskProgramJobs]
	}
	for i := range blocker.PreservedChildren {
		child := &blocker.PreservedChildren[i]
		child.JobID = boundedTaskProgramText(child.JobID)
		child.State = boundedTaskProgramText(child.State)
		child.ChildSessionID = boundedTaskProgramText(child.ChildSessionID)
		child.RunID = boundedTaskProgramText(child.RunID)
		child.WorkspacePath = boundedTaskProgramText(child.WorkspacePath)
		child.WorktreeBranch = boundedTaskProgramText(child.WorktreeBranch)
		child.ParentBranch = boundedTaskProgramText(child.ParentBranch)
		child.ImmutableStageBase = boundedTaskProgramText(child.ImmutableStageBase)
		child.ChildHead = boundedTaskProgramText(child.ChildHead)
		child.IntegrationState = boundedTaskProgramText(child.IntegrationState)
		child.ChangedFiles = boundedTaskProgramStrings(child.ChangedFiles)
	}
	return blocker
}

func boundedTaskProgramStrings(values []string) []string {
	if len(values) > maxTaskProgramScopeRows {
		values = values[:maxTaskProgramScopeRows]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = boundedTaskProgramText(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func boundedTaskProgramText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxTaskProgramTextRunes {
		runes = runes[:maxTaskProgramTextRunes]
	}
	return string(runes)
}

func maxTaskProgramInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
