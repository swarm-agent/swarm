package pebblestore

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

var (
	ErrWorkerNotFound               = errors.New("worker not found")
	ErrWorkerConflict               = errors.New("worker revision or state conflict")
	ErrActiveScheduleUpdateRejected = errors.New("cannot update active scheduled worker until checkpoint 2 safe stop controls are active")
)

var workerIDRegexp = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{3,128}$`)

type WorkerLifecycleState string

const (
	WorkerLifecycleStateIdle     WorkerLifecycleState = "idle"
	WorkerLifecycleStateActive   WorkerLifecycleState = "active"
	WorkerLifecycleStatePaused   WorkerLifecycleState = "paused"
	WorkerLifecycleStateArchived WorkerLifecycleState = "archived"
	WorkerLifecycleStateDeleted  WorkerLifecycleState = "deleted"
)

type WorkerCapabilityRequest struct {
	Type        string `json:"type"`                  // "tool", "permission", "network", "environment"
	Name        string `json:"name"`                  // name of tool/capability
	Description string `json:"description,omitempty"` // purpose
	Required    bool   `json:"required"`              // whether execution fails without it
}

type WorkerWorkspaceRequirement struct {
	Role        string `json:"role"`                  // e.g. "primary", "docs", "infra"
	Description string `json:"description,omitempty"` // expected repo/workspace content
	Required    bool   `json:"required"`              // whether missing role fails execution
}

type WorkerInputRequirement struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // "string", "number", "boolean", "object", "array"
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Default     any    `json:"default,omitempty"`
}

type WorkerDeliverableRequirement struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // "artifact", "report", "pull_request", "alert", "custom"
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

type WorkerProvenance struct {
	SourceWorkerID   string `json:"source_worker_id,omitempty"`
	SourceRevision   uint64 `json:"source_revision,omitempty"`
	SourceSessionID  string `json:"source_session_id,omitempty"`
	SourceProposalID string `json:"source_proposal_id,omitempty"`
	ImportedAt       int64  `json:"imported_at,omitempty"`
	MigratedAt       int64  `json:"migrated_at,omitempty"`
	ExportedAt       int64  `json:"exported_at,omitempty"`
	Author           string `json:"author,omitempty"`
}

type WorkerTriggerConfig struct {
	TriggerKind string `json:"trigger_kind"`           // "webhook", "event"
	Format      string `json:"format,omitempty"`       // "generic", "slack", "discord", "github"
	SecretRef   string `json:"secret_ref,omitempty"`   // reference only, never raw credentials
}

type WorkerAutomationDefinition struct {
	ID                      string                         `json:"id"`
	WorkerID                string                         `json:"worker_id"`
	Name                    string                         `json:"name"`
	Description             string                         `json:"description,omitempty"`
	ActivationMode          string                         `json:"activation_mode"` // "manual", "interval", "cron", "external_trigger"
	Schedule                *AutomationV2Schedule          `json:"schedule,omitempty"`
	Trigger                 *WorkerTriggerConfig           `json:"trigger,omitempty"`
	Enabled                 bool                           `json:"enabled"`
	PlanDocument            SessionPlanDocument            `json:"plan_document"`
	InputRequirements       []WorkerInputRequirement       `json:"input_requirements,omitempty"`
	DeliverableRequirements []WorkerDeliverableRequirement `json:"deliverable_requirements,omitempty"`
	Revision                uint64                         `json:"revision"`
	CreatedAt               int64                          `json:"created_at"`
	UpdatedAt               int64                          `json:"updated_at"`
}

type WorkerRecord struct {
	ID                    string                       `json:"id"`
	AccountScopeID        string                       `json:"account_scope_id"`
	Name                  string                       `json:"name"`
	Description           string                       `json:"description,omitempty"`
	Instructions          string                       `json:"instructions"`
	LifecycleState        WorkerLifecycleState         `json:"lifecycle_state"`
	Revision              uint64                       `json:"revision"`
	RequestedCapabilities []WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	LocalBindings         map[string]string            `json:"local_bindings,omitempty"`
	Automations           []WorkerAutomationDefinition `json:"automations,omitempty"`
	Metadata              map[string]any               `json:"metadata,omitempty"`
	Provenance            *WorkerProvenance            `json:"provenance,omitempty"`
	CreatedAt             int64                        `json:"created_at"`
	UpdatedAt             int64                        `json:"updated_at"`
}

type WorkerRevisionRecord struct {
	WorkerID       string       `json:"worker_id"`
	AccountScopeID string       `json:"account_scope_id"`
	Revision       uint64       `json:"revision"`
	Worker         WorkerRecord `json:"worker"`
	CommittedAt    int64        `json:"committed_at"`
	CommittedBy    string       `json:"committed_by,omitempty"`
	ChangeSummary  string       `json:"change_summary,omitempty"`
}

type WorkerRunRecord struct {
	ID                 string                         `json:"id"`
	AccountScopeID     string                         `json:"account_scope_id"`
	WorkerID           string                         `json:"worker_id"`
	WorkerRevision     uint64                         `json:"worker_revision"`
	AutomationID       string                         `json:"automation_id,omitempty"`
	AutomationRevision uint64                         `json:"automation_revision,omitempty"`
	OccurrenceID       string                         `json:"occurrence_id,omitempty"`
	SessionID          string                         `json:"session_id,omitempty"`
	RequestSource      string                         `json:"request_source"` // "direct", "schedule", "trigger", "test_run", "orchestrator"
	Input              map[string]any                 `json:"input,omitempty"`
	Status             string                         `json:"status"` // "admitted", "running", "succeeded", "failed", "cancelled"
	Error              string                         `json:"error,omitempty"`
	Deliverables       []SessionPlanArtifactReference `json:"deliverables,omitempty"`
	StartedAt          int64                          `json:"started_at,omitempty"`
	CompletedAt        int64                          `json:"completed_at,omitempty"`
	CreatedAt          int64                          `json:"created_at"`
}

type PortableAutomationDefinition struct {
	ID                      string                         `json:"id,omitempty"`
	Name                    string                         `json:"name"`
	Description             string                         `json:"description,omitempty"`
	ActivationMode          string                         `json:"activation_mode"`
	Schedule                *AutomationV2Schedule          `json:"schedule,omitempty"`
	Trigger                 *WorkerTriggerConfig           `json:"trigger,omitempty"`
	Enabled                 bool                           `json:"enabled"`
	Plan                    SessionPlanDocument            `json:"plan"`
	InputRequirements       []WorkerInputRequirement       `json:"input_requirements,omitempty"`
	DeliverableRequirements []WorkerDeliverableRequirement `json:"deliverable_requirements,omitempty"`
}

type PortableWorkerDefinition struct {
	SchemaVersion         int                            `json:"schema_version"`
	Name                  string                         `json:"name"`
	Description           string                         `json:"description,omitempty"`
	Instructions          string                         `json:"instructions"`
	Metadata              map[string]any                 `json:"metadata,omitempty"`
	Capabilities          []WorkerCapabilityRequest      `json:"capabilities,omitempty"`
	WorkspaceRequirements []WorkerWorkspaceRequirement   `json:"workspace_requirements,omitempty"`
	Automations           []PortableAutomationDefinition `json:"automations,omitempty"`
	Provenance            *WorkerProvenance              `json:"provenance,omitempty"`
}

type ListWorkersQuery struct {
	Limit          int                  `json:"limit,omitempty"`
	After          string               `json:"after,omitempty"`
	LifecycleState WorkerLifecycleState `json:"lifecycle_state,omitempty"`
	IncludeDeleted bool                 `json:"include_deleted,omitempty"`
}

type ListWorkersResult struct {
	Workers    []WorkerRecord `json:"workers"`
	NextCursor string         `json:"next_cursor,omitempty"`
	TotalCount int            `json:"total_count"`
}

type CreateWorkerRequest struct {
	ID                    string                       `json:"id,omitempty"`
	Name                  string                       `json:"name"`
	Description           string                       `json:"description,omitempty"`
	Instructions          string                       `json:"instructions"`
	RequestedCapabilities []WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	LocalBindings         map[string]string            `json:"local_bindings,omitempty"`
	Automations           []WorkerAutomationDefinition `json:"automations,omitempty"`
	Metadata              map[string]any               `json:"metadata,omitempty"`
	IdempotencyKey        string                       `json:"idempotency_key,omitempty"`
}

type UpdateWorkerRequest struct {
	Name                  *string                      `json:"name,omitempty"`
	Description           *string                      `json:"description,omitempty"`
	Instructions          *string                      `json:"instructions,omitempty"`
	RequestedCapabilities []WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	LocalBindings         map[string]string            `json:"local_bindings,omitempty"`
	Automations           []WorkerAutomationDefinition `json:"automations,omitempty"`
	Metadata              map[string]any               `json:"metadata,omitempty"`
	ChangeSummary         string                       `json:"change_summary,omitempty"`
}

type WorkerMigrationSummary struct {
	ScannedCount  int      `json:"scanned_count"`
	MigratedCount int      `json:"migrated_count"`
	SkippedCount  int      `json:"skipped_count"`
	FailedCount   int      `json:"failed_count"`
	Errors        []string `json:"errors,omitempty"`
}

func GenerateWorkerID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("worker_%x", b)
}

func GenerateWorkerAutomationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("wauto_%x", b)
}

func GenerateWorkerRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("wrun_%x", b)
}

func ValidateWorkerRecord(w *WorkerRecord, validate func(*SessionPlanDocument) error) error {
	if w == nil {
		return errors.New("worker record is required")
	}
	if strings.TrimSpace(w.AccountScopeID) == "" {
		return errors.New("account_scope_id is required")
	}
	if !workerIDRegexp.MatchString(w.ID) {
		return errors.New("invalid worker id format")
	}
	name := strings.TrimSpace(w.Name)
	if name == "" {
		return errors.New("worker name is required")
	}
	if len(w.Name) > 256 {
		return errors.New("worker name exceeds 256 characters")
	}
	if len(w.Description) > 4096 {
		return errors.New("worker description exceeds 4096 characters")
	}
	if len(w.Instructions) > 128*1024 {
		return errors.New("worker instructions exceed 128 KiB")
	}
	switch w.LifecycleState {
	case WorkerLifecycleStateIdle, WorkerLifecycleStateActive, WorkerLifecycleStatePaused, WorkerLifecycleStateArchived, WorkerLifecycleStateDeleted:
	default:
		return fmt.Errorf("invalid worker lifecycle state: %q", w.LifecycleState)
	}
	if len(w.RequestedCapabilities) > 64 {
		return errors.New("capabilities exceed maximum of 64")
	}
	for _, cap := range w.RequestedCapabilities {
		if strings.TrimSpace(cap.Name) == "" {
			return errors.New("capability name is required")
		}
		if len(cap.Name) > 128 {
			return errors.New("capability name exceeds 128 characters")
		}
	}
	if len(w.WorkspaceRequirements) > 32 {
		return errors.New("workspace requirements exceed maximum of 32")
	}
	for _, req := range w.WorkspaceRequirements {
		if strings.TrimSpace(req.Role) == "" {
			return errors.New("workspace requirement role is required")
		}
		if len(req.Role) > 64 {
			return errors.New("workspace requirement role exceeds 64 characters")
		}
	}
	if len(w.Automations) > 64 {
		return errors.New("automations exceed maximum of 64")
	}
	seenAutoIDs := make(map[string]struct{}, len(w.Automations))
	for i := range w.Automations {
		auto := &w.Automations[i]
		if !workerIDRegexp.MatchString(auto.ID) {
			return fmt.SprintfError("invalid automation id format: %q", auto.ID)
		}
		if _, ok := seenAutoIDs[auto.ID]; ok {
			return fmt.SprintfError("duplicate automation id: %q", auto.ID)
		}
		seenAutoIDs[auto.ID] = struct{}{}
		if strings.TrimSpace(auto.Name) == "" {
			return errors.New("automation name is required")
		}
		if len(auto.Name) > 256 {
			return errors.New("automation name exceeds 256 characters")
		}
		if len(auto.Description) > 4096 {
			return errors.New("automation description exceeds 4096 characters")
		}
		switch auto.ActivationMode {
		case "manual", "interval", "cron", "external_trigger":
		default:
			return fmt.SprintfError("invalid activation mode: %q", auto.ActivationMode)
		}
		if auto.ActivationMode == "interval" {
			if auto.Schedule == nil || auto.Schedule.IntervalSeconds < 60 || auto.Schedule.IntervalSeconds > 31622400 || auto.Schedule.Cron != "" || auto.Schedule.Timezone != "" {
				return errors.New("interval activation requires interval_seconds between 60 and 31622400 and no cron/timezone")
			}
		} else if auto.ActivationMode == "cron" {
			if auto.Schedule == nil || auto.Schedule.Cron == "" || auto.Schedule.Timezone == "" {
				return errors.New("cron activation requires cron expression and explicit timezone")
			}
			if _, err := time.LoadLocation(auto.Schedule.Timezone); err != nil {
				return errors.New("explicit IANA timezone required for cron")
			}
			fields := strings.Fields(auto.Schedule.Cron)
			if len(fields) != 5 {
				return errors.New("cron requires five fields")
			}
		}
		if err := validateUnexecutedPlanDocument(&auto.PlanDocument, validate); err != nil {
			return fmt.SprintfError("automation %q plan document: %w", auto.ID, err)
		}
	}
	return nil
}

func fmtSprintfError(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}

func validateUnexecutedPlanDocument(doc *SessionPlanDocument, validate func(*SessionPlanDocument) error) error {
	if doc == nil {
		return errors.New("plan document required")
	}
	if doc.ID != "" || doc.RevisionID != "" || doc.ExecutionOrigin != "" || doc.ExecutionState != nil || len(doc.OriginalCheckpoints) != 0 || (doc.Status != "" && doc.Status != "pending") {
		return errors.New("plan document cannot contain execution state")
	}
	for _, c := range doc.Checkpoints {
		if (c.Status != "" && c.Status != "pending") || c.AttemptID != "" || c.RunID != "" || c.SessionID != "" || c.StartedAt != 0 || c.CompletedAt != 0 || len(c.Attempts) != 0 || c.Review != nil || c.Handoff != nil || c.Recommendation != nil || c.Report != "" || c.Result != "" || c.ActiveSubtaskID != "" {
			return errors.New("plan checkpoints must be unexecuted")
		}
		for _, t := range c.Subtasks {
			if (t.Status != "" && t.Status != "pending") || t.StartedAt != 0 || t.CompletedAt != 0 || t.Result != "" {
				return errors.New("plan subtasks must be unexecuted")
			}
		}
	}
	if validate != nil {
		return validate(doc)
	}
	return nil
}

func SanitizePlanForExport(doc SessionPlanDocument) SessionPlanDocument {
	out := doc
	out.ID = ""
	out.RevisionID = ""
	out.ExecutionOrigin = ""
	out.ExecutionState = nil
	out.OriginalCheckpoints = nil
	out.Status = "pending"
	if len(out.Checkpoints) > 0 {
		cps := make([]SessionPlanCheckpoint, len(out.Checkpoints))
		for i, c := range out.Checkpoints {
			cp := c
			cp.AttemptID = ""
			cp.RunID = ""
			cp.SessionID = ""
			cp.StartedAt = 0
			cp.CompletedAt = 0
			cp.Attempts = nil
			cp.Review = nil
			cp.Handoff = nil
			cp.Recommendation = nil
			cp.Report = ""
			cp.Result = ""
			cp.ActiveSubtaskID = ""
			cp.Status = "pending"
			if len(cp.Subtasks) > 0 {
				sts := make([]SessionPlanSubtask, len(cp.Subtasks))
				for j, s := range cp.Subtasks {
					st := s
					st.Status = "pending"
					st.StartedAt = 0
					st.CompletedAt = 0
					st.Result = ""
					sts[j] = st
				}
				cp.Subtasks = sts
			}
			cps[i] = cp
		}
		out.Checkpoints = cps
	}
	return out
}

func ValidatePortableWorkerDefinition(data []byte, validate func(*SessionPlanDocument) error) (PortableWorkerDefinition, error) {
	if len(data) > 512*1024 {
		return PortableWorkerDefinition{}, errors.New("portable worker definition exceeds 512 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var def PortableWorkerDefinition
	if err := dec.Decode(&def); err != nil {
		return PortableWorkerDefinition{}, fmt.Errorf("parse portable worker definition: %w", err)
	}
	if dec.More() {
		return PortableWorkerDefinition{}, errors.New("unexpected trailing data in worker definition")
	}
	if def.SchemaVersion != 1 {
		return PortableWorkerDefinition{}, fmt.Errorf("unsupported schema version: only version 1 is supported, got %d", def.SchemaVersion)
	}
	name := strings.TrimSpace(def.Name)
	if name == "" {
		return PortableWorkerDefinition{}, errors.New("worker name is required")
	}
	if len(def.Name) > 256 {
		return PortableWorkerDefinition{}, errors.New("worker name exceeds 256 characters")
	}
	if len(def.Description) > 4096 {
		return PortableWorkerDefinition{}, errors.New("worker description exceeds 4096 characters")
	}
	if len(def.Instructions) > 128*1024 {
		return PortableWorkerDefinition{}, errors.New("worker instructions exceed 128 KiB")
	}
	if len(def.Capabilities) > 64 {
		return PortableWorkerDefinition{}, errors.New("capabilities exceed maximum of 64")
	}
	for _, cap := range def.Capabilities {
		if strings.TrimSpace(cap.Name) == "" {
			return PortableWorkerDefinition{}, errors.New("capability name is required")
		}
		if len(cap.Name) > 128 {
			return PortableWorkerDefinition{}, errors.New("capability name exceeds 128 characters")
		}
	}
	if len(def.WorkspaceRequirements) > 32 {
		return PortableWorkerDefinition{}, errors.New("workspace requirements exceed maximum of 32")
	}
	for _, req := range def.WorkspaceRequirements {
		if strings.TrimSpace(req.Role) == "" {
			return PortableWorkerDefinition{}, errors.New("workspace requirement role is required")
		}
		if len(req.Role) > 64 {
			return PortableWorkerDefinition{}, errors.New("workspace requirement role exceeds 64 characters")
		}
	}
	if len(def.Automations) > 64 {
		return PortableWorkerDefinition{}, errors.New("automations exceed maximum of 64")
	}
	for _, auto := range def.Automations {
		if strings.TrimSpace(auto.Name) == "" {
			return PortableWorkerDefinition{}, errors.New("automation name is required")
		}
		if len(auto.Name) > 256 {
			return PortableWorkerDefinition{}, errors.New("automation name exceeds 256 characters")
		}
		if len(auto.Description) > 4096 {
			return PortableWorkerDefinition{}, errors.New("automation description exceeds 4096 characters")
		}
		switch auto.ActivationMode {
		case "manual", "interval", "cron", "external_trigger":
		default:
			return PortableWorkerDefinition{}, fmt.Errorf("invalid activation mode: %q", auto.ActivationMode)
		}
		if auto.ActivationMode == "interval" {
			if auto.Schedule == nil || auto.Schedule.IntervalSeconds < 60 || auto.Schedule.IntervalSeconds > 31622400 || auto.Schedule.Cron != "" || auto.Schedule.Timezone != "" {
				return PortableWorkerDefinition{}, errors.New("interval activation requires interval_seconds between 60 and 31622400 and no cron/timezone")
			}
		} else if auto.ActivationMode == "cron" {
			if auto.Schedule == nil || auto.Schedule.Cron == "" || auto.Schedule.Timezone == "" {
				return PortableWorkerDefinition{}, errors.New("cron activation requires cron expression and explicit timezone")
			}
			if _, err := time.LoadLocation(auto.Schedule.Timezone); err != nil {
				return PortableWorkerDefinition{}, errors.New("explicit IANA timezone required for cron")
			}
			fields := strings.Fields(auto.Schedule.Cron)
			if len(fields) != 5 {
				return PortableWorkerDefinition{}, errors.New("cron requires five fields")
			}
		}
		planCopy := auto.Plan
		if err := validateUnexecutedPlanDocument(&planCopy, validate); err != nil {
			return PortableWorkerDefinition{}, fmt.Errorf("automation %q plan document: %w", auto.Name, err)
		}
	}
	return def, nil
}

type WorkerStore struct {
	store *Store
}

func NewWorkerStore(store *Store) *WorkerStore {
	return &WorkerStore{store: store}
}

func (ws *WorkerStore) CreateWorker(account, user string, req CreateWorkerRequest, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return WorkerRecord{}, errors.New("account is required")
	}
	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	idempKey := strings.TrimSpace(req.IdempotencyKey)
	if idempKey != "" {
		var existingWorkerID string
		ok, err := ws.store.GetJSON(KeyWorkerIdempotency(account, idempKey), &existingWorkerID)
		if err == nil && ok && existingWorkerID != "" {
			var existing WorkerRecord
			if ok, err := ws.store.GetJSON(KeyWorker(account, existingWorkerID), &existing); err == nil && ok {
				return existing, nil
			}
		}
	}

	workerID := strings.TrimSpace(req.ID)
	if workerID == "" {
		workerID = GenerateWorkerID()
	}

	var existing WorkerRecord
	if ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &existing); err == nil && ok {
		return WorkerRecord{}, ErrWorkerConflict
	}

	now := time.Now().UnixMilli()
	automations := make([]WorkerAutomationDefinition, len(req.Automations))
	for i, auto := range req.Automations {
		autoCopy := auto
		if strings.TrimSpace(autoCopy.ID) == "" {
			autoCopy.ID = GenerateWorkerAutomationID()
		}
		autoCopy.WorkerID = workerID
		if autoCopy.Revision == 0 {
			autoCopy.Revision = 1
		}
		if autoCopy.CreatedAt == 0 {
			autoCopy.CreatedAt = now
		}
		autoCopy.UpdatedAt = now
		automations[i] = autoCopy
	}

	w := WorkerRecord{
		ID:                    workerID,
		AccountScopeID:        account,
		Name:                  strings.TrimSpace(req.Name),
		Description:           strings.TrimSpace(req.Description),
		Instructions:          req.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              1,
		RequestedCapabilities: req.RequestedCapabilities,
		WorkspaceRequirements: req.WorkspaceRequirements,
		LocalBindings:         req.LocalBindings,
		Automations:           automations,
		Metadata:              req.Metadata,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if err := ValidateWorkerRecord(&w, validate); err != nil {
		return WorkerRecord{}, err
	}

	hist := WorkerRevisionRecord{
		WorkerID:       w.ID,
		AccountScopeID: account,
		Revision:       w.Revision,
		Worker:         w,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  "created worker",
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       w.ID,
	}
	if err := m.put(KeyWorker(account, w.ID), w); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, w.ID, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	for _, auto := range w.Automations {
		m.putBytes(KeyWorkerByAutomation(account, auto.ID), []byte(w.ID))
	}
	if idempKey != "" {
		if err := m.put(KeyWorkerIdempotency(account, idempKey), w.ID); err != nil {
			return WorkerRecord{}, err
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return w, nil
}

func (ws *WorkerStore) GetWorker(account, workerID string) (WorkerRecord, bool, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, false, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return WorkerRecord{}, false, nil
	}
	var w WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &w)
	if err != nil || !ok {
		// Also check if lookup by automation ID resolves it
		var resolvedID string
		autoOk, autoErr := ws.store.GetJSON(KeyWorkerByAutomation(account, workerID), &resolvedID)
		if autoErr == nil && autoOk && resolvedID != "" {
			return ws.GetWorker(account, resolvedID)
		}
		return WorkerRecord{}, false, err
	}
	if w.AccountScopeID != account {
		return WorkerRecord{}, false, nil
	}
	return w, true, nil
}

func (ws *WorkerStore) GetWorkerByAutomation(account, automationID string) (WorkerRecord, bool, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, false, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	automationID = strings.TrimSpace(automationID)
	if account == "" || automationID == "" {
		return WorkerRecord{}, false, nil
	}
	raw, closer, err := ws.store.db.Get([]byte(KeyWorkerByAutomation(account, automationID)))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return WorkerRecord{}, false, nil
		}
		return WorkerRecord{}, false, err
	}
	defer closer.Close()
	workerID := string(raw)
	return ws.GetWorker(account, workerID)
}

func (ws *WorkerStore) ListWorkers(account string, query ListWorkersQuery) (ListWorkersResult, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return ListWorkersResult{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return ListWorkersResult{}, errors.New("account is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	prefix := WorkerAccountPrefix(account)
	lower := prefix
	after := strings.TrimSpace(query.After)
	if after != "" {
		lower = KeyWorker(account, after) + "\x00"
	}

	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return ListWorkersResult{}, err
	}
	defer it.Close()

	var list []WorkerRecord
	var nextCursor string
	total := 0
	for valid := it.First(); valid; valid = it.Next() {
		var w WorkerRecord
		if err := json.Unmarshal(it.Value(), &w); err != nil {
			continue
		}
		if w.AccountScopeID != account {
			continue
		}
		if !query.IncludeDeleted && w.LifecycleState == WorkerLifecycleStateDeleted {
			continue
		}
		if query.LifecycleState != "" && w.LifecycleState != query.LifecycleState {
			continue
		}
		total++
		if len(list) < limit {
			list = append(list, w)
		} else if nextCursor == "" {
			nextCursor = list[len(list)-1].ID
		}
	}
	return ListWorkersResult{
		Workers:    list,
		NextCursor: nextCursor,
		TotalCount: total,
	}, nil
}

func (ws *WorkerStore) UpdateWorker(account, user, workerID string, expectedRevision uint64, req UpdateWorkerRequest, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return WorkerRecord{}, errors.New("account and worker_id are required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Revision != expectedRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	// Check active schedule update rejection until checkpoint 2 safe stop controls exist
	for _, auto := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && auto.Enabled && (auto.ActivationMode == "interval" || auto.ActivationMode == "cron") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	now := time.Now().UnixMilli()
	updated := current
	if req.Name != nil {
		updated.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updated.Description = strings.TrimSpace(*req.Description)
	}
	if req.Instructions != nil {
		updated.Instructions = *req.Instructions
	}
	if req.RequestedCapabilities != nil {
		updated.RequestedCapabilities = req.RequestedCapabilities
	}
	if req.WorkspaceRequirements != nil {
		updated.WorkspaceRequirements = req.WorkspaceRequirements
	}
	if req.LocalBindings != nil {
		updated.LocalBindings = req.LocalBindings
	}
	if req.Metadata != nil {
		updated.Metadata = req.Metadata
	}
	if req.Automations != nil {
		automations := make([]WorkerAutomationDefinition, len(req.Automations))
		for i, a := range req.Automations {
			aCopy := a
			if strings.TrimSpace(aCopy.ID) == "" {
				aCopy.ID = GenerateWorkerAutomationID()
			}
			aCopy.WorkerID = workerID
			if aCopy.Revision == 0 {
				aCopy.Revision = 1
			}
			if aCopy.CreatedAt == 0 {
				aCopy.CreatedAt = now
			}
			aCopy.UpdatedAt = now
			automations[i] = aCopy
		}
		updated.Automations = automations
	}

	updated.Revision++
	updated.UpdatedAt = now

	if err := ValidateWorkerRecord(&updated, validate); err != nil {
		return WorkerRecord{}, err
	}

	hist := WorkerRevisionRecord{
		WorkerID:       updated.ID,
		AccountScopeID: account,
		Revision:       updated.Revision,
		Worker:         updated,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  req.ChangeSummary,
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       updated.ID,
	}
	if err := m.put(KeyWorker(account, updated.ID), updated); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, updated.ID, updated.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}

	// Remove deleted automations from lookup
	newAutoIDs := make(map[string]struct{}, len(updated.Automations))
	for _, a := range updated.Automations {
		newAutoIDs[a.ID] = struct{}{}
		m.putBytes(KeyWorkerByAutomation(account, a.ID), []byte(updated.ID))
	}
	for _, oldA := range current.Automations {
		if _, ok := newAutoIDs[oldA.ID]; !ok {
			m.delete(KeyWorkerByAutomation(account, oldA.ID))
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return updated, nil
}

func (ws *WorkerStore) DeleteWorker(account, user, workerID string, expectedRevision uint64) error {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return errors.New("account and worker_id are required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return err
	}
	if !ok || current.AccountScopeID != account {
		return ErrWorkerNotFound
	}
	if current.Revision != expectedRevision {
		return ErrWorkerConflict
	}
	// Check active schedule rejection
	for _, auto := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && auto.Enabled && (auto.ActivationMode == "interval" || auto.ActivationMode == "cron") {
			return ErrActiveScheduleUpdateRejected
		}
	}

	now := time.Now().UnixMilli()
	current.LifecycleState = WorkerLifecycleStateDeleted
	current.Revision++
	current.UpdatedAt = now

	hist := WorkerRevisionRecord{
		WorkerID:       current.ID,
		AccountScopeID: account,
		Revision:       current.Revision,
		Worker:         current,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  "deleted worker",
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return err
	}
	for _, auto := range current.Automations {
		m.delete(KeyWorkerByAutomation(account, auto.ID))
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return err
	}
	ws.store.publishWorkerRealtime(m)
	return nil
}

func (ws *WorkerStore) GetWorkerRevision(account, workerID string, revision uint64) (WorkerRevisionRecord, bool, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRevisionRecord{}, false, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" || revision == 0 {
		return WorkerRevisionRecord{}, false, nil
	}
	var hist WorkerRevisionRecord
	ok, err := ws.store.GetJSON(KeyWorkerHistory(account, workerID, revision), &hist)
	if err != nil || !ok {
		return WorkerRevisionRecord{}, false, err
	}
	if hist.AccountScopeID != account {
		return WorkerRevisionRecord{}, false, nil
	}
	return hist, true, nil
}

func (ws *WorkerStore) ListWorkerRevisions(account, workerID string, limit int, after string) ([]WorkerRevisionRecord, string, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return nil, "", errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return nil, "", errors.New("account and worker_id are required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	prefix := WorkerHistoryPrefix(account, workerID)
	lower := prefix
	after = strings.TrimSpace(after)
	if after != "" {
		lower = after + "\x00"
	}

	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()

	var list []WorkerRevisionRecord
	var nextCursor string
	for valid := it.First(); valid; valid = it.Next() {
		if len(list) >= limit {
			nextCursor = string(it.Key())
			break
		}
		var h WorkerRevisionRecord
		if err := json.Unmarshal(it.Value(), &h); err != nil {
			continue
		}
		if h.AccountScopeID == account && h.WorkerID == workerID {
			list = append(list, h)
		}
	}
	return list, nextCursor, nil
}

func (ws *WorkerStore) AttachWorkerAutomation(account, user, workerID string, expectedWorkerRevision uint64, auto WorkerAutomationDefinition, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return WorkerRecord{}, errors.New("account and worker_id are required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	for _, a := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	now := time.Now().UnixMilli()
	autoID := strings.TrimSpace(auto.ID)
	if autoID == "" {
		autoID = GenerateWorkerAutomationID()
	}
	for _, a := range current.Automations {
		if a.ID == autoID {
			return WorkerRecord{}, fmt.Errorf("automation with id %q already exists on worker", autoID)
		}
	}

	auto.ID = autoID
	auto.WorkerID = workerID
	auto.Revision = 1
	auto.CreatedAt = now
	auto.UpdatedAt = now

	current.Automations = append(current.Automations, auto)
	current.Revision++
	current.UpdatedAt = now

	if err := ValidateWorkerRecord(&current, validate); err != nil {
		return WorkerRecord{}, err
	}

	hist := WorkerRevisionRecord{
		WorkerID:       current.ID,
		AccountScopeID: account,
		Revision:       current.Revision,
		Worker:         current,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  fmt.Sprintf("attached automation %s", auto.ID),
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	m.putBytes(KeyWorkerByAutomation(account, auto.ID), []byte(current.ID))

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return current, nil
}

func (ws *WorkerStore) UpdateWorkerAutomation(account, user, workerID, automationID string, expectedWorkerRevision uint64, auto WorkerAutomationDefinition, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	automationID = strings.TrimSpace(automationID)
	if account == "" || workerID == "" || automationID == "" {
		return WorkerRecord{}, errors.New("account, worker_id, and automation_id are required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	for _, a := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	foundIdx := -1
	for i, a := range current.Automations {
		if a.ID == automationID {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		return WorkerRecord{}, errors.New("automation not found on worker")
	}

	now := time.Now().UnixMilli()
	priorAuto := current.Automations[foundIdx]
	auto.ID = automationID
	auto.WorkerID = workerID
	auto.Revision = priorAuto.Revision + 1
	auto.CreatedAt = priorAuto.CreatedAt
	auto.UpdatedAt = now

	current.Automations[foundIdx] = auto
	current.Revision++
	current.UpdatedAt = now

	if err := ValidateWorkerRecord(&current, validate); err != nil {
		return WorkerRecord{}, err
	}

	hist := WorkerRevisionRecord{
		WorkerID:       current.ID,
		AccountScopeID: account,
		Revision:       current.Revision,
		Worker:         current,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  fmt.Sprintf("updated automation %s", auto.ID),
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	m.putBytes(KeyWorkerByAutomation(account, auto.ID), []byte(current.ID))

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return current, nil
}

func (ws *WorkerStore) RemoveWorkerAutomation(account, user, workerID, automationID string, expectedWorkerRevision uint64) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	automationID = strings.TrimSpace(automationID)
	if account == "" || workerID == "" || automationID == "" {
		return WorkerRecord{}, errors.New("account, worker_id, and automation_id are required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	for _, a := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	foundIdx := -1
	for i, a := range current.Automations {
		if a.ID == automationID {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		return WorkerRecord{}, errors.New("automation not found on worker")
	}

	now := time.Now().UnixMilli()
	current.Automations = append(current.Automations[:foundIdx], current.Automations[foundIdx+1:]...)
	current.Revision++
	current.UpdatedAt = now

	hist := WorkerRevisionRecord{
		WorkerID:       current.ID,
		AccountScopeID: account,
		Revision:       current.Revision,
		Worker:         current,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  fmt.Sprintf("removed automation %s", automationID),
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	m.delete(KeyWorkerByAutomation(account, automationID))

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return current, nil
}

func (ws *WorkerStore) ExportWorker(account, workerID string) (PortableWorkerDefinition, []byte, error) {
	w, ok, err := ws.GetWorker(account, workerID)
	if err != nil {
		return PortableWorkerDefinition{}, nil, err
	}
	if !ok {
		return PortableWorkerDefinition{}, nil, ErrWorkerNotFound
	}

	automations := make([]PortableAutomationDefinition, len(w.Automations))
	for i, a := range w.Automations {
		automations[i] = PortableAutomationDefinition{
			ID:                      a.ID,
			Name:                    a.Name,
			Description:             a.Description,
			ActivationMode:          a.ActivationMode,
			Schedule:                a.Schedule,
			Trigger:                 a.Trigger,
			Enabled:                 a.Enabled,
			Plan:                    SanitizePlanForExport(a.PlanDocument),
			InputRequirements:       a.InputRequirements,
			DeliverableRequirements: a.DeliverableRequirements,
		}
	}

	def := PortableWorkerDefinition{
		SchemaVersion:         1,
		Name:                  w.Name,
		Description:           w.Description,
		Instructions:          w.Instructions,
		Metadata:              w.Metadata,
		Capabilities:          w.RequestedCapabilities,
		WorkspaceRequirements: w.WorkspaceRequirements,
		Automations:           automations,
		Provenance: &WorkerProvenance{
			SourceWorkerID: w.ID,
			SourceRevision: w.Revision,
			ExportedAt:     time.Now().UnixMilli(),
		},
	}

	b, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return PortableWorkerDefinition{}, nil, err
	}
	return def, b, nil
}

func (ws *WorkerStore) ImportWorkerAsNew(account, user string, data []byte, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	def, err := ValidatePortableWorkerDefinition(data, validate)
	if err != nil {
		return WorkerRecord{}, err
	}

	now := time.Now().UnixMilli()
	newWorkerID := GenerateWorkerID()

	automations := make([]WorkerAutomationDefinition, len(def.Automations))
	for i, a := range def.Automations {
		automations[i] = WorkerAutomationDefinition{
			ID:                      GenerateWorkerAutomationID(),
			WorkerID:                newWorkerID,
			Name:                    a.Name,
			Description:             a.Description,
			ActivationMode:          a.ActivationMode,
			Schedule:                a.Schedule,
			Trigger:                 a.Trigger,
			Enabled:                 a.Enabled,
			PlanDocument:            a.Plan,
			InputRequirements:       a.InputRequirements,
			DeliverableRequirements: a.DeliverableRequirements,
			Revision:                1,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
	}

	prov := def.Provenance
	if prov == nil {
		prov = &WorkerProvenance{}
	}
	prov.ImportedAt = now
	prov.Author = user

	w := WorkerRecord{
		ID:                    newWorkerID,
		AccountScopeID:        account,
		Name:                  def.Name,
		Description:           def.Description,
		Instructions:          def.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              1,
		RequestedCapabilities: def.Capabilities,
		WorkspaceRequirements: def.WorkspaceRequirements,
		Automations:           automations,
		Metadata:              def.Metadata,
		Provenance:            prov,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	hist := WorkerRevisionRecord{
		WorkerID:       w.ID,
		AccountScopeID: account,
		Revision:       w.Revision,
		Worker:         w,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  "imported worker as new",
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       w.ID,
	}
	if err := m.put(KeyWorker(account, w.ID), w); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, w.ID, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	for _, auto := range w.Automations {
		m.putBytes(KeyWorkerByAutomation(account, auto.ID), []byte(w.ID))
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return w, nil
}

func (ws *WorkerStore) ImportWorkerUpdate(account, user, workerID string, expectedRevision uint64, data []byte, validate func(*SessionPlanDocument) error) (WorkerRecord, error) {
	def, err := ValidatePortableWorkerDefinition(data, validate)
	if err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Revision != expectedRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	for _, a := range current.Automations {
		if current.LifecycleState == WorkerLifecycleStateActive && a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	now := time.Now().UnixMilli()
	automations := make([]WorkerAutomationDefinition, len(def.Automations))
	for i, a := range def.Automations {
		autoID := a.ID
		if strings.TrimSpace(autoID) == "" {
			autoID = GenerateWorkerAutomationID()
		}
		rev := uint64(1)
		for _, oldA := range current.Automations {
			if oldA.ID == autoID {
				rev = oldA.Revision + 1
				break
			}
		}
		automations[i] = WorkerAutomationDefinition{
			ID:                      autoID,
			WorkerID:                workerID,
			Name:                    a.Name,
			Description:             a.Description,
			ActivationMode:          a.ActivationMode,
			Schedule:                a.Schedule,
			Trigger:                 a.Trigger,
			Enabled:                 a.Enabled,
			PlanDocument:            a.Plan,
			InputRequirements:       a.InputRequirements,
			DeliverableRequirements: a.DeliverableRequirements,
			Revision:                rev,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
	}

	prov := def.Provenance
	if prov == nil {
		prov = &WorkerProvenance{}
	}
	prov.ImportedAt = now
	prov.Author = user

	updated := WorkerRecord{
		ID:                    workerID,
		AccountScopeID:        account,
		Name:                  def.Name,
		Description:           def.Description,
		Instructions:          def.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              current.Revision + 1,
		RequestedCapabilities: def.Capabilities,
		WorkspaceRequirements: def.WorkspaceRequirements,
		LocalBindings:         current.LocalBindings,
		Automations:           automations,
		Metadata:              def.Metadata,
		Provenance:            prov,
		CreatedAt:             current.CreatedAt,
		UpdatedAt:             now,
	}

	if err := ValidateWorkerRecord(&updated, validate); err != nil {
		return WorkerRecord{}, err
	}

	hist := WorkerRevisionRecord{
		WorkerID:       updated.ID,
		AccountScopeID: account,
		Revision:       updated.Revision,
		Worker:         updated,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  "imported worker update",
	}

	m := &workerRealtimeMutation{
		accountScopeID: account,
		workerID:       updated.ID,
	}
	if err := m.put(KeyWorker(account, updated.ID), updated); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, updated.ID, updated.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}

	newAutoIDs := make(map[string]struct{}, len(updated.Automations))
	for _, a := range updated.Automations {
		newAutoIDs[a.ID] = struct{}{}
		m.putBytes(KeyWorkerByAutomation(account, a.ID), []byte(updated.ID))
	}
	for _, oldA := range current.Automations {
		if _, ok := newAutoIDs[oldA.ID]; !ok {
			m.delete(KeyWorkerByAutomation(account, oldA.ID))
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	ws.store.publishWorkerRealtime(m)
	return updated, nil
}

func (ws *WorkerStore) RecordWorkerRun(account string, run WorkerRunRecord) (WorkerRunRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRunRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID := strings.TrimSpace(run.WorkerID)
	if account == "" || workerID == "" {
		return WorkerRunRecord{}, errors.New("account and worker_id are required")
	}
	if run.AccountScopeID != "" && run.AccountScopeID != account {
		return WorkerRunRecord{}, errors.New("account mismatch")
	}
	run.AccountScopeID = account

	w, ok, err := ws.GetWorker(account, workerID)
	if err != nil {
		return WorkerRunRecord{}, err
	}
	if !ok {
		return WorkerRunRecord{}, ErrWorkerNotFound
	}
	if run.WorkerRevision == 0 {
		run.WorkerRevision = w.Revision
	}
	if strings.TrimSpace(run.ID) == "" {
		run.ID = GenerateWorkerRunID()
	}
	if run.CreatedAt == 0 {
		run.CreatedAt = time.Now().UnixMilli()
	}

	batch := ws.store.NewBatch()
	defer batch.Close()

	payload, err := json.Marshal(run)
	if err != nil {
		return WorkerRunRecord{}, err
	}
	if err := batch.Set([]byte(KeyWorkerRun(account, workerID, run.ID)), payload, nil); err != nil {
		return WorkerRunRecord{}, err
	}
	if strings.TrimSpace(run.OccurrenceID) != "" {
		if err := batch.Set([]byte(KeyWorkerRunByOccurrence(account, run.OccurrenceID)), []byte(workerID+":"+run.ID), nil); err != nil {
			return WorkerRunRecord{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return WorkerRunRecord{}, err
	}
	return run, nil
}

func (ws *WorkerStore) GetWorkerRun(account, workerID, runID string) (WorkerRunRecord, bool, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRunRecord{}, false, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	runID = strings.TrimSpace(runID)
	if account == "" || workerID == "" || runID == "" {
		return WorkerRunRecord{}, false, nil
	}
	var r WorkerRunRecord
	ok, err := ws.store.GetJSON(KeyWorkerRun(account, workerID, runID), &r)
	if err != nil || !ok {
		return WorkerRunRecord{}, false, err
	}
	if r.AccountScopeID != account || r.WorkerID != workerID {
		return WorkerRunRecord{}, false, nil
	}
	return r, true, nil
}

func (ws *WorkerStore) ListWorkerRuns(account, workerID string, limit int, after string) ([]WorkerRunRecord, string, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return nil, "", errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	workerID = strings.TrimSpace(workerID)
	if account == "" || workerID == "" {
		return nil, "", errors.New("account and worker_id are required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	prefix := WorkerRunPrefix(account, workerID)
	lower := prefix
	after = strings.TrimSpace(after)
	if after != "" {
		lower = after + "\x00"
	}

	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()

	var list []WorkerRunRecord
	var nextCursor string
	for valid := it.First(); valid; valid = it.Next() {
		if len(list) >= limit {
			nextCursor = string(it.Key())
			break
		}
		var r WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			continue
		}
		if r.AccountScopeID == account && r.WorkerID == workerID {
			list = append(list, r)
		}
	}
	return list, nextCursor, nil
}

func (ws *WorkerStore) MigrateLegacyAutomationsV2(account string) (WorkerMigrationSummary, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerMigrationSummary{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return WorkerMigrationSummary{}, errors.New("account is required")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	summary := WorkerMigrationSummary{}

	prefix := fmt.Sprintf("automation/v2/accepted/%x/", account)
	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return summary, err
	}
	defer it.Close()

	seenAutomations := make(map[string]struct{})
	var toMigrate []AutomationV2Record

	for valid := it.First(); valid; valid = it.Next() {
		var rec AutomationV2Record
		if err := json.Unmarshal(it.Value(), &rec); err != nil {
			continue
		}
		if rec.AutomationID == "" {
			continue
		}
		if _, ok := seenAutomations[rec.AutomationID]; ok {
			continue
		}
		seenAutomations[rec.AutomationID] = struct{}{}
		toMigrate = append(toMigrate, rec)
	}

	summary.ScannedCount = len(toMigrate)
	now := time.Now().UnixMilli()

	batch := ws.store.NewBatch()
	defer batch.Close()

	for _, rec := range toMigrate {
		if !workerIDRegexp.MatchString(rec.AutomationID) {
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("unconvertible record with invalid automation id %q", rec.AutomationID))
			continue
		}

		targetWorkerID := rec.AutomationID
		if !strings.HasPrefix(targetWorkerID, "worker_") {
			targetWorkerID = "worker_" + rec.AutomationID
		}

		// Check if already migrated
		var existing WorkerRecord
		if ok, err := ws.store.GetJSON(KeyWorker(account, targetWorkerID), &existing); err == nil && ok {
			// Idempotent: check if it matches this legacy record
			if existing.Provenance != nil && (existing.Provenance.SourceProposalID == rec.ProposalID || existing.Provenance.SourceWorkerID == rec.AutomationID) {
				summary.SkippedCount++
				continue
			}
			// Collision! Never merge
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("collision for worker %s: existing record from session %s differs from legacy session %s", targetWorkerID, existing.Provenance.SourceSessionID, rec.SessionID))
			continue
		}

		name := strings.TrimSpace(rec.Document.Title)
		if name == "" {
			name = rec.AutomationID
		}
		desc := strings.TrimSpace(rec.Document.Info.Goal)
		instructions := strings.TrimSpace(rec.Document.Info.Context)
		if instructions == "" {
			instructions = desc
		}
		if instructions == "" {
			instructions = "Migrated standing instructions for " + name
		}

		// Lifecycle state: truthfully represent legacy lifecycle
		lifecycleState := WorkerLifecycleStatePaused
		if rec.Cancelled || rec.Archived {
			lifecycleState = WorkerLifecycleStateArchived
		} else if rec.Enabled {
			lifecycleState = WorkerLifecycleStateActive
		}

		actMode := "manual"
		var sched *AutomationV2Schedule
		if rec.Document.AutomationV2 != nil {
			switch rec.Document.AutomationV2.Schedule.Kind {
			case "interval":
				actMode = "interval"
				s := rec.Document.AutomationV2.Schedule
				sched = &s
			case "cron":
				actMode = "cron"
				s := rec.Document.AutomationV2.Schedule
				sched = &s
			default:
				actMode = "external_trigger"
			}
		}

		var wsReqs []WorkerWorkspaceRequirement
		var bindings map[string]string
		if rec.WorkspaceID != "" {
			wsReqs = []WorkerWorkspaceRequirement{{Role: "primary", Description: "Primary workspace", Required: true}}
			bindings = map[string]string{"primary": rec.WorkspaceID}
		}

		rev := rec.Generation
		if rev == 0 {
			rev = 1
		}

		autoDef := WorkerAutomationDefinition{
			ID:             rec.AutomationID, // Preserving av2 ID
			WorkerID:       targetWorkerID,
			Name:           name,
			Description:    desc,
			ActivationMode: actMode,
			Schedule:       sched,
			Enabled:        rec.Enabled && !rec.Cancelled && !rec.Archived,
			PlanDocument:   rec.Document,
			Revision:       rev,
			CreatedAt:      rec.CreatedAt,
			UpdatedAt:      rec.AcceptedAt,
		}

		w := WorkerRecord{
			ID:                    targetWorkerID,
			AccountScopeID:        account,
			Name:                  name,
			Description:           desc,
			Instructions:          instructions,
			LifecycleState:        lifecycleState,
			Revision:              rev,
			WorkspaceRequirements: wsReqs,
			LocalBindings:         bindings,
			Automations:           []WorkerAutomationDefinition{autoDef},
			Provenance: &WorkerProvenance{
				SourceWorkerID:   rec.AutomationID,
				SourceSessionID:  rec.SessionID,
				SourceProposalID: rec.ProposalID,
				MigratedAt:       now,
			},
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.AcceptedAt,
		}

		hist := WorkerRevisionRecord{
			WorkerID:       w.ID,
			AccountScopeID: account,
			Revision:       w.Revision,
			Worker:         w,
			CommittedAt:    now,
			CommittedBy:    "migration",
			ChangeSummary:  "migrated from legacy automation v2",
		}

		wBytes, err := json.Marshal(w)
		if err != nil {
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("marshal worker %s: %v", w.ID, err))
			continue
		}
		hBytes, err := json.Marshal(hist)
		if err != nil {
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("marshal history %s: %v", w.ID, err))
			continue
		}

		if err := batch.Set([]byte(KeyWorker(account, w.ID)), wBytes, nil); err != nil {
			return summary, err
		}
		if err := batch.Set([]byte(KeyWorkerHistory(account, w.ID, w.Revision)), hBytes, nil); err != nil {
			return summary, err
		}
		if err := batch.Set([]byte(KeyWorkerByAutomation(account, autoDef.ID)), []byte(w.ID), nil); err != nil {
			return summary, err
		}

		// Link occurrences
		occPrefix := automationV2OccurrencePrefix(account, rec.SessionID)
		occIt, occErr := ws.store.db.NewIter(&pebble.IterOptions{
			LowerBound: []byte(occPrefix),
			UpperBound: []byte(occPrefix + "\xff"),
		})
		if occErr == nil {
			for occValid := occIt.First(); occValid; occValid = occIt.Next() {
				var o AutomationV2Occurrence
				if err := json.Unmarshal(occIt.Value(), &o); err != nil {
					continue
				}
				run := WorkerRunRecord{
					ID:                 "wrun_" + o.ID,
					AccountScopeID:     account,
					WorkerID:           targetWorkerID,
					WorkerRevision:     w.Revision, // pinned revision
					AutomationID:       autoDef.ID,
					AutomationRevision: autoDef.Revision, // pinned revision
					OccurrenceID:       o.ID,
					SessionID:          o.SessionID,
					RequestSource:      "schedule",
					Status:             o.State,
					Deliverables:       o.Deliverables,
					StartedAt:          o.AdmittedAt,
					CompletedAt:        o.ObservedAt,
					CreatedAt:          o.AdmittedAt,
				}
				runBytes, rErr := json.Marshal(run)
				if rErr == nil {
					_ = batch.Set([]byte(KeyWorkerRun(account, targetWorkerID, run.ID)), runBytes, nil)
					_ = batch.Set([]byte(KeyWorkerRunByOccurrence(account, o.ID)), []byte(targetWorkerID+":"+run.ID), nil)
				}
			}
			occIt.Close()
		}

		summary.MigratedCount++
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return summary, err
	}
	return summary, nil
}
