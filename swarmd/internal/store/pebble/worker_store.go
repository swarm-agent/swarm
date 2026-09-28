package pebblestore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
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
	TriggerKind string `json:"trigger_kind"`         // "webhook", "event"
	Format      string `json:"format,omitempty"`     // "generic", "slack", "discord", "github"
	SecretRef   string `json:"secret_ref,omitempty"` // reference only, never raw credentials
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

type WorkerIdempotencyRecord struct {
	WorkerID    string       `json:"worker_id"`
	PayloadHash string       `json:"payload_hash"`
	Receipt     WorkerRecord `json:"receipt"`
	CreatedAt   int64        `json:"created_at"`
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

func hashWorkerPayload(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func ValidateSchedule(s *AutomationV2Schedule) error {
	if s == nil {
		return errors.New("schedule is required")
	}
	switch s.Kind {
	case "interval":
		if s.IntervalSeconds < 60 || s.IntervalSeconds > 31622400 || s.Cron != "" || s.Timezone != "" {
			return errors.New("interval requires elapsed interval_seconds from 60 to 31622400 and no cron/timezone")
		}
	case "cron":
		if s.IntervalSeconds != 0 || len(s.Cron) > 128 || s.Timezone == "" || s.Timezone == "Local" {
			return errors.New("cron requires five fields and an explicit IANA timezone, without interval_seconds")
		}
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return errors.New("explicit IANA timezone required for cron")
		}
		fields := strings.Fields(s.Cron)
		if len(fields) != 5 {
			return errors.New("five cron fields required")
		}
		bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		for i, f := range fields {
			if f == "*" {
				continue
			}
			step := strings.HasPrefix(f, "*/")
			if step {
				f = strings.TrimPrefix(f, "*/")
			}
			if f == "" {
				return errors.New("invalid cron field")
			}
			for _, c := range f {
				if c < '0' || c > '9' {
					return errors.New("cron supports numeric fields, * and */n only; ranges, lists and names are unsupported")
				}
			}
			n, err := strconv.Atoi(f)
			lo, hi := bounds[i][0], bounds[i][1]
			if step {
				lo, hi = 1, hi-lo+1
			}
			if err != nil || n < lo || n > hi {
				return errors.New("cron field outside bounds")
			}
		}
		if fields[2] != "*" && fields[4] != "*" {
			return errors.New("both cron day fields cannot be restricted")
		}
	case "trigger", "":
		if s.IntervalSeconds != 0 || s.Cron != "" {
			return errors.New("trigger schedule must not declare interval_seconds or cron")
		}
	default:
		return fmt.Errorf("invalid schedule kind: %q", s.Kind)
	}
	return nil
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
	seenCaps := make(map[string]struct{}, len(w.RequestedCapabilities))
	for _, cap := range w.RequestedCapabilities {
		capName := strings.TrimSpace(cap.Name)
		if capName == "" {
			return errors.New("capability name is required")
		}
		if len(cap.Name) > 128 {
			return errors.New("capability name exceeds 128 characters")
		}
		switch cap.Type {
		case "tool", "permission", "network", "environment":
		default:
			return fmt.Errorf("invalid capability type: %q", cap.Type)
		}
		if _, ok := seenCaps[capName]; ok {
			return fmt.Errorf("duplicate capability name: %q", capName)
		}
		seenCaps[capName] = struct{}{}
	}
	if len(w.WorkspaceRequirements) > 32 {
		return errors.New("workspace requirements exceed maximum of 32")
	}
	seenRoles := make(map[string]struct{}, len(w.WorkspaceRequirements))
	for _, req := range w.WorkspaceRequirements {
		role := strings.TrimSpace(req.Role)
		if role == "" {
			return errors.New("workspace requirement role is required")
		}
		if len(req.Role) > 64 {
			return errors.New("workspace requirement role exceeds 64 characters")
		}
		if _, ok := seenRoles[role]; ok {
			return fmt.Errorf("duplicate workspace requirement role: %q", role)
		}
		seenRoles[role] = struct{}{}
	}
	if len(w.LocalBindings) > 0 && (w.Provenance == nil || w.Provenance.MigratedAt == 0) {
		return errors.New("local bindings cannot be specified directly until approved activation")
	}
	if len(w.Metadata) > 64 {
		return errors.New("metadata keys exceed maximum of 64")
	}
	if len(w.Automations) > 64 {
		return errors.New("automations exceed maximum of 64")
	}
	seenAutoIDs := make(map[string]struct{}, len(w.Automations))
	seenAutoNames := make(map[string]struct{}, len(w.Automations))
	for i := range w.Automations {
		auto := &w.Automations[i]
		if !workerIDRegexp.MatchString(auto.ID) {
			return fmt.Errorf("invalid automation id format: %q", auto.ID)
		}
		if _, ok := seenAutoIDs[auto.ID]; ok {
			return fmt.Errorf("duplicate automation id: %q", auto.ID)
		}
		seenAutoIDs[auto.ID] = struct{}{}
		autoName := strings.TrimSpace(auto.Name)
		if autoName == "" {
			return errors.New("automation name is required")
		}
		if len(auto.Name) > 256 {
			return errors.New("automation name exceeds 256 characters")
		}
		if _, ok := seenAutoNames[autoName]; ok {
			return fmt.Errorf("duplicate automation name: %q", autoName)
		}
		seenAutoNames[autoName] = struct{}{}
		if len(auto.Description) > 4096 {
			return errors.New("automation description exceeds 4096 characters")
		}
		switch auto.ActivationMode {
		case "manual", "interval", "cron", "external_trigger":
		default:
			return fmt.Errorf("invalid activation mode: %q", auto.ActivationMode)
		}
		if auto.ActivationMode == "interval" || auto.ActivationMode == "cron" {
			if err := ValidateSchedule(auto.Schedule); err != nil {
				return fmt.Errorf("automation %q schedule: %w", auto.ID, err)
			}
		}
		if auto.Trigger != nil {
			switch auto.Trigger.TriggerKind {
			case "webhook", "event":
			default:
				return fmt.Errorf("invalid trigger kind: %q", auto.Trigger.TriggerKind)
			}
			switch auto.Trigger.Format {
			case "", "generic", "slack", "discord", "github":
			default:
				return fmt.Errorf("invalid trigger format: %q", auto.Trigger.Format)
			}
			if strings.ContainsAny(auto.Trigger.SecretRef, "\r\n") {
				return errors.New("trigger secret_ref cannot contain newlines")
			}
		}
		seenInputs := make(map[string]struct{}, len(auto.InputRequirements))
		for _, inReq := range auto.InputRequirements {
			inName := strings.TrimSpace(inReq.Name)
			if inName == "" {
				return errors.New("input requirement name is required")
			}
			switch inReq.Kind {
			case "string", "number", "boolean", "object", "array":
			default:
				return fmt.Errorf("invalid input requirement kind: %q", inReq.Kind)
			}
			if _, ok := seenInputs[inName]; ok {
				return fmt.Errorf("duplicate input requirement name: %q", inName)
			}
			seenInputs[inName] = struct{}{}
		}
		seenDelivs := make(map[string]struct{}, len(auto.DeliverableRequirements))
		for _, delReq := range auto.DeliverableRequirements {
			delName := strings.TrimSpace(delReq.Name)
			if delName == "" {
				return errors.New("deliverable requirement name is required")
			}
			switch delReq.Kind {
			case "artifact", "report", "pull_request", "alert", "custom":
			default:
				return fmt.Errorf("invalid deliverable requirement kind: %q", delReq.Kind)
			}
			if _, ok := seenDelivs[delName]; ok {
				return fmt.Errorf("duplicate deliverable requirement name: %q", delName)
			}
			seenDelivs[delName] = struct{}{}
		}
		if err := validateUnexecutedPlanDocument(&auto.PlanDocument, validate); err != nil {
			return fmt.Errorf("automation %q plan document: %w", auto.ID, err)
		}
	}
	return nil
}

func validateUnexecutedPlanDocument(doc *SessionPlanDocument, validate func(*SessionPlanDocument) error) error {
	if doc == nil {
		return errors.New("plan document required")
	}
	if doc.AutomationV2 != nil || doc.WorkerV2 != nil {
		return errors.New("plan document cannot contain nested AutomationV2 or WorkerV2 settings")
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
		if c.TaskProgram != nil {
			if err := validateTaskProgramUnexecuted(c.TaskProgram); err != nil {
				return fmt.Errorf("checkpoint %s task program: %w", c.ID, err)
			}
		}
	}
	if validate != nil {
		return validate(doc)
	}
	return nil
}

func validateTaskProgramUnexecuted(prog *TaskProgramSpec) error {
	if prog == nil {
		return nil
	}
	for _, job := range prog.Jobs {
		for _, scope := range job.OwnedScope {
			if strings.HasPrefix(scope, "/") || strings.Contains(scope, "..") {
				return fmt.Errorf("task program owned_scope cannot declare absolute host paths or traversal: %q", scope)
			}
		}
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
	out.AutomationV2 = nil
	out.WorkerV2 = nil
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
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
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
	seenCaps := make(map[string]struct{}, len(def.Capabilities))
	for _, cap := range def.Capabilities {
		capName := strings.TrimSpace(cap.Name)
		if capName == "" {
			return PortableWorkerDefinition{}, errors.New("capability name is required")
		}
		if len(cap.Name) > 128 {
			return PortableWorkerDefinition{}, errors.New("capability name exceeds 128 characters")
		}
		switch cap.Type {
		case "tool", "permission", "network", "environment":
		default:
			return PortableWorkerDefinition{}, fmt.Errorf("invalid capability type: %q", cap.Type)
		}
		if _, ok := seenCaps[capName]; ok {
			return PortableWorkerDefinition{}, fmt.Errorf("duplicate capability name: %q", capName)
		}
		seenCaps[capName] = struct{}{}
	}
	if len(def.WorkspaceRequirements) > 32 {
		return PortableWorkerDefinition{}, errors.New("workspace requirements exceed maximum of 32")
	}
	seenRoles := make(map[string]struct{}, len(def.WorkspaceRequirements))
	for _, req := range def.WorkspaceRequirements {
		role := strings.TrimSpace(req.Role)
		if role == "" {
			return PortableWorkerDefinition{}, errors.New("workspace requirement role is required")
		}
		if len(req.Role) > 64 {
			return PortableWorkerDefinition{}, errors.New("workspace requirement role exceeds 64 characters")
		}
		if _, ok := seenRoles[role]; ok {
			return PortableWorkerDefinition{}, fmt.Errorf("duplicate workspace requirement role: %q", role)
		}
		seenRoles[role] = struct{}{}
	}
	if len(def.Metadata) > 64 {
		return PortableWorkerDefinition{}, errors.New("metadata keys exceed maximum of 64")
	}
	if len(def.Automations) > 64 {
		return PortableWorkerDefinition{}, errors.New("automations exceed maximum of 64")
	}
	seenAutoNames := make(map[string]struct{}, len(def.Automations))
	for _, auto := range def.Automations {
		autoName := strings.TrimSpace(auto.Name)
		if autoName == "" {
			return PortableWorkerDefinition{}, errors.New("automation name is required")
		}
		if len(auto.Name) > 256 {
			return PortableWorkerDefinition{}, errors.New("automation name exceeds 256 characters")
		}
		if _, ok := seenAutoNames[autoName]; ok {
			return PortableWorkerDefinition{}, fmt.Errorf("duplicate automation name: %q", autoName)
		}
		seenAutoNames[autoName] = struct{}{}
		if len(auto.Description) > 4096 {
			return PortableWorkerDefinition{}, errors.New("automation description exceeds 4096 characters")
		}
		switch auto.ActivationMode {
		case "manual", "interval", "cron", "external_trigger":
		default:
			return PortableWorkerDefinition{}, fmt.Errorf("invalid activation mode: %q", auto.ActivationMode)
		}
		if auto.ActivationMode == "interval" || auto.ActivationMode == "cron" {
			if err := ValidateSchedule(auto.Schedule); err != nil {
				return PortableWorkerDefinition{}, fmt.Errorf("automation %q schedule: %w", auto.Name, err)
			}
		}
		if auto.Trigger != nil {
			switch auto.Trigger.TriggerKind {
			case "webhook", "event":
			default:
				return PortableWorkerDefinition{}, fmt.Errorf("invalid trigger kind: %q", auto.Trigger.TriggerKind)
			}
			switch auto.Trigger.Format {
			case "", "generic", "slack", "discord", "github":
			default:
				return PortableWorkerDefinition{}, fmt.Errorf("invalid trigger format: %q", auto.Trigger.Format)
			}
			if strings.ContainsAny(auto.Trigger.SecretRef, "\r\n") {
				return PortableWorkerDefinition{}, errors.New("trigger secret_ref cannot contain newlines")
			}
		}
		seenInputs := make(map[string]struct{}, len(auto.InputRequirements))
		for _, inReq := range auto.InputRequirements {
			inName := strings.TrimSpace(inReq.Name)
			if inName == "" {
				return PortableWorkerDefinition{}, errors.New("input requirement name is required")
			}
			switch inReq.Kind {
			case "string", "number", "boolean", "object", "array":
			default:
				return PortableWorkerDefinition{}, fmt.Errorf("invalid input requirement kind: %q", inReq.Kind)
			}
			if _, ok := seenInputs[inName]; ok {
				return PortableWorkerDefinition{}, fmt.Errorf("duplicate input requirement name: %q", inName)
			}
			seenInputs[inName] = struct{}{}
		}
		seenDelivs := make(map[string]struct{}, len(auto.DeliverableRequirements))
		for _, delReq := range auto.DeliverableRequirements {
			delName := strings.TrimSpace(delReq.Name)
			if delName == "" {
				return PortableWorkerDefinition{}, errors.New("deliverable requirement name is required")
			}
			switch delReq.Kind {
			case "artifact", "report", "pull_request", "alert", "custom":
			default:
				return PortableWorkerDefinition{}, fmt.Errorf("invalid deliverable requirement kind: %q", delReq.Kind)
			}
			if _, ok := seenDelivs[delName]; ok {
				return PortableWorkerDefinition{}, fmt.Errorf("duplicate deliverable requirement name: %q", delName)
			}
			seenDelivs[delName] = struct{}{}
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
	if strings.TrimSpace(req.ID) != "" {
		return WorkerRecord{}, errors.New("worker id is server-owned and cannot be specified on create")
	}
	if len(req.LocalBindings) > 0 {
		return WorkerRecord{}, errors.New("local bindings cannot be specified until approved activation")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	idempKey := strings.TrimSpace(req.IdempotencyKey)
	var payloadHash string
	if idempKey != "" {
		reqForHash := req
		reqForHash.IdempotencyKey = ""
		var err error
		payloadHash, err = hashWorkerPayload(reqForHash)
		if err != nil {
			return WorkerRecord{}, fmt.Errorf("hash idempotency payload: %w", err)
		}
		var existingIdemp WorkerIdempotencyRecord
		ok, err := ws.store.GetJSON(KeyWorkerIdempotency(account, idempKey), &existingIdemp)
		if err != nil {
			return WorkerRecord{}, err
		}
		if ok {
			if existingIdemp.PayloadHash != payloadHash {
				return WorkerRecord{}, fmt.Errorf("%w: idempotency key reused with different payload", ErrWorkerConflict)
			}
			return existingIdemp.Receipt, nil
		}
	}

	workerID := GenerateWorkerID()
	now := time.Now().UnixMilli()

	automations := make([]WorkerAutomationDefinition, len(req.Automations))
	seenAutoNames := make(map[string]struct{}, len(req.Automations))
	for i, auto := range req.Automations {
		name := strings.TrimSpace(auto.Name)
		if name == "" {
			return WorkerRecord{}, errors.New("automation name is required")
		}
		if _, ok := seenAutoNames[name]; ok {
			return WorkerRecord{}, fmt.Errorf("duplicate automation name: %q", name)
		}
		seenAutoNames[name] = struct{}{}
		autoCopy := auto
		autoCopy.ID = GenerateWorkerAutomationID() // server-owned
		autoCopy.WorkerID = workerID
		autoCopy.Revision = 1 // server-owned
		autoCopy.CreatedAt = now
		autoCopy.UpdatedAt = now
		automations[i] = autoCopy
	}

	reqCaps := req.RequestedCapabilities
	if reqCaps == nil {
		reqCaps = []WorkerCapabilityRequest{}
	}
	wsReqs := req.WorkspaceRequirements
	if wsReqs == nil {
		wsReqs = []WorkerWorkspaceRequirement{}
	}

	w := WorkerRecord{
		ID:                    workerID,
		AccountScopeID:        account,
		Name:                  strings.TrimSpace(req.Name),
		Description:           strings.TrimSpace(req.Description),
		Instructions:          req.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              1,
		RequestedCapabilities: reqCaps,
		WorkspaceRequirements: wsReqs,
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
		userID:         user,
		workerID:       w.ID,
	}
	if err := m.put(KeyWorker(account, w.ID), w); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, w.ID, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	for _, auto := range w.Automations {
		if err := m.put(KeyWorkerByAutomation(account, auto.ID), w.ID); err != nil {
			return WorkerRecord{}, err
		}
	}
	if idempKey != "" {
		idempRec := WorkerIdempotencyRecord{
			WorkerID:    w.ID,
			PayloadHash: payloadHash,
			Receipt:     w,
			CreatedAt:   now,
		}
		if err := m.put(KeyWorkerIdempotency(account, idempKey), idempRec); err != nil {
			return WorkerRecord{}, err
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	// Unlock domain mutex before invoking publisher callbacks
	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
	var workerID string
	ok, err := ws.store.GetJSON(KeyWorkerByAutomation(account, automationID), &workerID)
	if err != nil || !ok || workerID == "" {
		return WorkerRecord{}, false, err
	}
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

	list := make([]WorkerRecord, 0, limit)
	var nextCursor string
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
		if len(list) < limit {
			list = append(list, w)
		} else {
			nextCursor = list[len(list)-1].ID
			break
		}
	}
	if err := it.Error(); err != nil {
		return ListWorkersResult{}, err
	}

	return ListWorkersResult{
		Workers:    list,
		NextCursor: nextCursor,
		TotalCount: len(list),
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
	if req.LocalBindings != nil && len(req.LocalBindings) > 0 {
		return WorkerRecord{}, errors.New("local bindings cannot be specified until approved activation")
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var current WorkerRecord
	ok, err := ws.store.GetJSON(KeyWorker(account, workerID), &current)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrActiveScheduleUpdateRejected
	}
	for _, auto := range current.Automations {
		if auto.Enabled && (auto.ActivationMode == "interval" || auto.ActivationMode == "cron" || auto.ActivationMode == "external_trigger") {
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
	if req.Metadata != nil {
		updated.Metadata = req.Metadata
	}
	if req.Automations != nil {
		automations := make([]WorkerAutomationDefinition, len(req.Automations))
		seenIDs := make(map[string]struct{}, len(req.Automations))
		seenNames := make(map[string]struct{}, len(req.Automations))
		for i, a := range req.Automations {
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return WorkerRecord{}, errors.New("automation name is required")
			}
			if _, ok := seenNames[name]; ok {
				return WorkerRecord{}, fmt.Errorf("duplicate automation name: %q", name)
			}
			seenNames[name] = struct{}{}
			aCopy := a
			autoID := strings.TrimSpace(aCopy.ID)
			var prior *WorkerAutomationDefinition
			if autoID != "" {
				for j := range current.Automations {
					if current.Automations[j].ID == autoID {
						prior = &current.Automations[j]
						break
					}
				}
				if prior == nil {
					// Check cross-worker collision
					var otherWorkerID string
					if ok, err := ws.store.GetJSON(KeyWorkerByAutomation(account, autoID), &otherWorkerID); err == nil && ok && otherWorkerID != "" && otherWorkerID != workerID {
						return WorkerRecord{}, fmt.Errorf("%w: automation id %q is owned by another worker %s", ErrWorkerConflict, autoID, otherWorkerID)
					}
				}
			} else {
				autoID = GenerateWorkerAutomationID()
			}
			if _, ok := seenIDs[autoID]; ok {
				return WorkerRecord{}, fmt.Errorf("duplicate automation id: %q", autoID)
			}
			seenIDs[autoID] = struct{}{}
			aCopy.ID = autoID
			aCopy.WorkerID = workerID
			if prior != nil {
				aCopy.Revision = prior.Revision + 1 // never let bulk update reset revision
				aCopy.CreatedAt = prior.CreatedAt
			} else {
				aCopy.Revision = 1
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
		userID:         user,
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
		if err := m.put(KeyWorkerByAutomation(account, a.ID), updated.ID); err != nil {
			return WorkerRecord{}, err
		}
	}
	for _, oldA := range current.Automations {
		if _, ok := newAutoIDs[oldA.ID]; !ok {
			m.delete(KeyWorkerByAutomation(account, oldA.ID))
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedRevision {
		return ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return errors.New("cannot delete non-idle worker")
	}

	runs, _, err := ws.ListWorkerRuns(account, workerID, 100, "")
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status == "running" || r.Status == "admitted" {
			return errors.New("cannot delete worker with active runs")
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
		userID:         user,
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

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
		if afterRev, err := strconv.ParseUint(after, 10, 64); err == nil {
			lower = KeyWorkerHistory(account, workerID, afterRev) + "\x00"
		} else if strings.HasPrefix(after, prefix) {
			lower = after + "\x00"
		} else {
			return nil, "", errors.New("invalid cursor")
		}
	}

	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()

	list := make([]WorkerRevisionRecord, 0, limit)
	var nextCursor string
	for valid := it.First(); valid; valid = it.Next() {
		var h WorkerRevisionRecord
		if err := json.Unmarshal(it.Value(), &h); err != nil {
			continue
		}
		if h.AccountScopeID == account && h.WorkerID == workerID {
			if len(list) < limit {
				list = append(list, h)
			} else {
				nextCursor = fmt.Sprintf("%d", list[len(list)-1].Revision)
				break
			}
		}
	}
	if err := it.Error(); err != nil {
		return nil, "", err
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
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrActiveScheduleUpdateRejected
	}
	for _, a := range current.Automations {
		if a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron" || a.ActivationMode == "external_trigger") {
			return WorkerRecord{}, ErrActiveScheduleUpdateRejected
		}
	}

	now := time.Now().UnixMilli()
	autoID := GenerateWorkerAutomationID() // server-owned!

	autoName := strings.TrimSpace(auto.Name)
	if autoName == "" {
		return WorkerRecord{}, errors.New("automation name is required")
	}
	for _, a := range current.Automations {
		if strings.TrimSpace(a.Name) == autoName {
			return WorkerRecord{}, fmt.Errorf("automation with name %q already exists on worker", autoName)
		}
	}

	auto.ID = autoID
	auto.WorkerID = workerID
	auto.Revision = 1 // server-owned!
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
		userID:         user,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerByAutomation(account, auto.ID), current.ID); err != nil {
		return WorkerRecord{}, err
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrActiveScheduleUpdateRejected
	}
	for _, a := range current.Automations {
		if a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron" || a.ActivationMode == "external_trigger") {
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

	newName := strings.TrimSpace(auto.Name)
	if newName == "" {
		return WorkerRecord{}, errors.New("automation name is required")
	}
	for i, a := range current.Automations {
		if i != foundIdx && strings.TrimSpace(a.Name) == newName {
			return WorkerRecord{}, fmt.Errorf("duplicate automation name: %q", newName)
		}
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
		userID:         user,
		workerID:       current.ID,
	}
	if err := m.put(KeyWorker(account, current.ID), current); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, current.ID, current.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerByAutomation(account, auto.ID), current.ID); err != nil {
		return WorkerRecord{}, err
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedWorkerRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrActiveScheduleUpdateRejected
	}
	for _, a := range current.Automations {
		if a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron" || a.ActivationMode == "external_trigger") {
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
		userID:         user,
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

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

	return current, nil
}

func (ws *WorkerStore) ExportWorker(account, workerID string) (PortableWorkerDefinition, []byte, error) {
	w, ok, err := ws.GetWorker(account, workerID)
	if err != nil {
		return PortableWorkerDefinition{}, nil, err
	}
	if !ok || w.LifecycleState == WorkerLifecycleStateDeleted {
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

func (ws *WorkerStore) ImportWorkerAsNew(account, user string, data []byte, validate func(*SessionPlanDocument) error, idempotencyKey ...string) (WorkerRecord, error) {
	def, err := ValidatePortableWorkerDefinition(data, validate)
	if err != nil {
		return WorkerRecord{}, err
	}

	var idempKey string
	if len(idempotencyKey) > 0 {
		idempKey = strings.TrimSpace(idempotencyKey[0])
	}

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

	var payloadHash string
	if idempKey != "" {
		h := sha256.Sum256(data)
		payloadHash = hex.EncodeToString(h[:])
		var existingIdemp WorkerIdempotencyRecord
		ok, err := ws.store.GetJSON(KeyWorkerIdempotency(account, idempKey), &existingIdemp)
		if err != nil {
			return WorkerRecord{}, err
		}
		if ok {
			if existingIdemp.PayloadHash != payloadHash {
				return WorkerRecord{}, fmt.Errorf("%w: idempotency key reused with different payload", ErrWorkerConflict)
			}
			return existingIdemp.Receipt, nil
		}
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

	caps := def.Capabilities
	if caps == nil {
		caps = []WorkerCapabilityRequest{}
	}
	wsReqs := def.WorkspaceRequirements
	if wsReqs == nil {
		wsReqs = []WorkerWorkspaceRequirement{}
	}

	w := WorkerRecord{
		ID:                    newWorkerID,
		AccountScopeID:        account,
		Name:                  def.Name,
		Description:           def.Description,
		Instructions:          def.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              1,
		RequestedCapabilities: caps,
		WorkspaceRequirements: wsReqs,
		Automations:           automations,
		Metadata:              def.Metadata,
		Provenance:            prov,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

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
		userID:         user,
		workerID:       w.ID,
	}
	if err := m.put(KeyWorker(account, w.ID), w); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, w.ID, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	for _, auto := range w.Automations {
		if err := m.put(KeyWorkerByAutomation(account, auto.ID), w.ID); err != nil {
			return WorkerRecord{}, err
		}
	}
	if idempKey != "" {
		idempRec := WorkerIdempotencyRecord{
			WorkerID:    w.ID,
			PayloadHash: payloadHash,
			Receipt:     w,
			CreatedAt:   now,
		}
		if err := m.put(KeyWorkerIdempotency(account, idempKey), idempRec); err != nil {
			return WorkerRecord{}, err
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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
	if !ok || current.AccountScopeID != account || current.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt > 0 || current.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, errors.New("mutations to migrated legacy workers are rejected until safe stop barrier is active")
	}
	if current.Revision != expectedRevision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if current.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrActiveScheduleUpdateRejected
	}
	for _, a := range current.Automations {
		if a.Enabled && (a.ActivationMode == "interval" || a.ActivationMode == "cron" || a.ActivationMode == "external_trigger") {
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

	caps := def.Capabilities
	if caps == nil {
		caps = []WorkerCapabilityRequest{}
	}
	wsReqs := def.WorkspaceRequirements
	if wsReqs == nil {
		wsReqs = []WorkerWorkspaceRequirement{}
	}

	updated := WorkerRecord{
		ID:                    workerID,
		AccountScopeID:        account,
		Name:                  def.Name,
		Description:           def.Description,
		Instructions:          def.Instructions,
		LifecycleState:        WorkerLifecycleStateIdle, // Create/import always idle
		Revision:              current.Revision + 1,
		RequestedCapabilities: caps,
		WorkspaceRequirements: wsReqs,
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
		userID:         user,
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
		if err := m.put(KeyWorkerByAutomation(account, a.ID), updated.ID); err != nil {
			return WorkerRecord{}, err
		}
	}
	for _, oldA := range current.Automations {
		if _, ok := newAutoIDs[oldA.ID]; !ok {
			m.delete(KeyWorkerByAutomation(account, oldA.ID))
		}
	}

	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}

	ws.store.workersMu.Unlock()
	ws.store.publishWorkerRealtime(m)
	ws.store.workersMu.Lock()

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

	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()

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
	if run.WorkerRevision > w.Revision {
		return WorkerRunRecord{}, fmt.Errorf("%w: run worker_revision %d exceeds worker revision %d", ErrWorkerConflict, run.WorkerRevision, w.Revision)
	}

	if strings.TrimSpace(run.AutomationID) != "" {
		foundAuto := false
		for _, a := range w.Automations {
			if a.ID == run.AutomationID {
				foundAuto = true
				if run.AutomationRevision == 0 {
					run.AutomationRevision = a.Revision
				}
				if run.AutomationRevision > a.Revision {
					return WorkerRunRecord{}, fmt.Errorf("%w: run automation_revision %d exceeds automation revision %d", ErrWorkerConflict, run.AutomationRevision, a.Revision)
				}
				break
			}
		}
		if !foundAuto {
			return WorkerRunRecord{}, fmt.Errorf("automation %q not found on worker", run.AutomationID)
		}
	}

	if strings.TrimSpace(run.ID) == "" {
		run.ID = GenerateWorkerRunID()
	}
	now := time.Now().UnixMilli()
	if run.CreatedAt == 0 {
		run.CreatedAt = now
	}

	// Validate occurrence ownership
	if strings.TrimSpace(run.OccurrenceID) != "" {
		var existingOccLink string
		occOk, occErr := ws.store.GetJSON(KeyWorkerRunByOccurrence(account, run.OccurrenceID), &existingOccLink)
		if occErr != nil {
			return WorkerRunRecord{}, occErr
		}
		if occOk && existingOccLink != "" {
			expectedLink := workerID + ":" + run.ID
			if existingOccLink != expectedLink {
				return WorkerRunRecord{}, fmt.Errorf("%w: occurrence %q is already linked to %s", ErrWorkerConflict, run.OccurrenceID, existingOccLink)
			}
		}
	}

	// Validate existing run immutable fields
	var existingRun WorkerRunRecord
	existingOk, runErr := ws.store.GetJSON(KeyWorkerRun(account, workerID, run.ID), &existingRun)
	if runErr != nil {
		return WorkerRunRecord{}, runErr
	}
	if existingOk {
		if existingRun.WorkerRevision != run.WorkerRevision {
			return WorkerRunRecord{}, fmt.Errorf("%w: cannot rewrite pinned worker revision from %d to %d", ErrWorkerConflict, existingRun.WorkerRevision, run.WorkerRevision)
		}
		if existingRun.AutomationID != run.AutomationID {
			return WorkerRunRecord{}, fmt.Errorf("%w: cannot change automation id from %q to %q", ErrWorkerConflict, existingRun.AutomationID, run.AutomationID)
		}
		if existingRun.AutomationRevision != run.AutomationRevision {
			return WorkerRunRecord{}, fmt.Errorf("%w: cannot rewrite pinned automation revision from %d to %d", ErrWorkerConflict, existingRun.AutomationRevision, run.AutomationRevision)
		}
		if existingRun.OccurrenceID != "" && run.OccurrenceID != "" && existingRun.OccurrenceID != run.OccurrenceID {
			return WorkerRunRecord{}, fmt.Errorf("%w: cannot change occurrence id from %q to %q", ErrWorkerConflict, existingRun.OccurrenceID, run.OccurrenceID)
		}
		if existingRun.RequestSource != "" && run.RequestSource != "" && existingRun.RequestSource != run.RequestSource {
			return WorkerRunRecord{}, fmt.Errorf("%w: cannot change request source", ErrWorkerConflict)
		}
		run.CreatedAt = existingRun.CreatedAt
	}

	if run.Deliverables == nil {
		run.Deliverables = []SessionPlanArtifactReference{}
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
		occPayload, err := json.Marshal(workerID + ":" + run.ID)
		if err != nil {
			return WorkerRunRecord{}, err
		}
		if err := batch.Set([]byte(KeyWorkerRunByOccurrence(account, run.OccurrenceID)), occPayload, nil); err != nil {
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
		if strings.HasPrefix(after, prefix) {
			lower = after + "\x00"
		} else {
			lower = KeyWorkerRun(account, workerID, after) + "\x00"
		}
	}

	it, err := ws.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()

	list := make([]WorkerRunRecord, 0, limit)
	var nextCursor string
	for valid := it.First(); valid; valid = it.Next() {
		var r WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			continue
		}
		if r.AccountScopeID == account && r.WorkerID == workerID {
			if len(list) < limit {
				list = append(list, r)
			} else {
				nextCursor = list[len(list)-1].ID
				break
			}
		}
	}
	if err := it.Error(); err != nil {
		return nil, "", err
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

	// Preflight: scan all legacy records and detect conflicts or collisions
	seenAutomations := make(map[string]AutomationV2Record)
	var toMigrate []AutomationV2Record
	hasPreflightErrors := false

	for valid := it.First(); valid; valid = it.Next() {
		var rec AutomationV2Record
		if err := json.Unmarshal(it.Value(), &rec); err != nil {
			summary.FailedCount++
			summary.Errors = append(summary.Errors, "unmarshal legacy record failed: corrupt payload")
			hasPreflightErrors = true
			continue
		}
		if rec.AutomationID == "" {
			continue
		}
		if !workerIDRegexp.MatchString(rec.AutomationID) {
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("unconvertible record with invalid automation id %q", rec.AutomationID))
			hasPreflightErrors = true
			continue
		}
		if prev, ok := seenAutomations[rec.AutomationID]; ok {
			// Compare exact provenance and identity
			if prev.SessionID != rec.SessionID || prev.ProposalID != rec.ProposalID {
				summary.FailedCount++
				summary.Errors = append(summary.Errors, fmt.Sprintf("colliding legacy automation id %q across sessions %s and %s", rec.AutomationID, prev.SessionID, rec.SessionID))
				hasPreflightErrors = true
				continue
			}
			continue
		}
		seenAutomations[rec.AutomationID] = rec
		toMigrate = append(toMigrate, rec)
	}
	if err := it.Error(); err != nil {
		return summary, err
	}

	summary.ScannedCount = len(toMigrate)
	if hasPreflightErrors {
		return summary, nil
	}

	now := time.Now().UnixMilli()
	batch := ws.store.NewBatch()
	defer batch.Close()

	for _, rec := range toMigrate {
		targetWorkerID := rec.AutomationID // Preserve exact established av2 ID!

		// Check if already migrated
		var existing WorkerRecord
		if ok, err := ws.store.GetJSON(KeyWorker(account, targetWorkerID), &existing); err == nil && ok {
			// Idempotent: check if it matches this legacy record
			if existing.Provenance != nil && existing.Provenance.MigratedAt > 0 && existing.Provenance.SourceWorkerID == rec.AutomationID && (rec.ProposalID == "" || existing.Provenance.SourceProposalID == rec.ProposalID) {
				summary.SkippedCount++
				continue
			}
			// Collision!
			var sourceSession string
			if existing.Provenance != nil {
				sourceSession = existing.Provenance.SourceSessionID
			}
			summary.FailedCount++
			summary.Errors = append(summary.Errors, fmt.Sprintf("collision for worker %s: existing record from session %s differs from legacy session %s", targetWorkerID, sourceSession, rec.SessionID))
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

		lifecycleState := WorkerLifecycleStatePaused
		if rec.Cancelled || rec.Archived {
			lifecycleState = WorkerLifecycleStateArchived
		} else if rec.Enabled {
			lifecycleState = WorkerLifecycleStateActive
		}

		settings := rec.Document.AutomationV2
		if settings == nil {
			settings = rec.Document.WorkerV2
		}

		actMode := "manual"
		var sched *AutomationV2Schedule
		if settings != nil {
			switch settings.Schedule.Kind {
			case "interval":
				actMode = "interval"
				s := settings.Schedule
				sched = &s
			case "cron":
				actMode = "cron"
				s := settings.Schedule
				sched = &s
			default:
				actMode = "external_trigger"
			}
		}

		// Multi-workspace bindings preservation
		wsReqs := make([]WorkerWorkspaceRequirement, 0)
		bindings := make(map[string]string)
		if rec.WorkspaceID != "" {
			wsReqs = append(wsReqs, WorkerWorkspaceRequirement{Role: "primary", Description: "Primary workspace", Required: true})
			bindings["primary"] = rec.WorkspaceID
		}
		allWS := append([]string(nil), rec.WorkspaceIDs...)
		if settings != nil {
			allWS = append(allWS, settings.WorkspaceIDs...)
		}
		seenWS := map[string]bool{rec.WorkspaceID: true}
		wsIdx := 1
		for _, wid := range allWS {
			wid = strings.TrimSpace(wid)
			if wid == "" || seenWS[wid] {
				continue
			}
			seenWS[wid] = true
			role := fmt.Sprintf("workspace_%d", wsIdx)
			wsIdx++
			wsReqs = append(wsReqs, WorkerWorkspaceRequirement{Role: role, Description: "Workspace grant", Required: false})
			bindings[role] = wid
		}

		rev := rec.Generation
		if rev == 0 {
			rev = 1
		}

		var automations []WorkerAutomationDefinition
		if len(rec.Document.Checkpoints) > 0 {
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
			automations = append(automations, autoDef)
		} else {
			// Zero-checkpoint specialist maps to worker without fake executable auto
			automations = make([]WorkerAutomationDefinition, 0)
		}

		w := WorkerRecord{
			ID:                    targetWorkerID,
			AccountScopeID:        account,
			Name:                  name,
			Description:           desc,
			Instructions:          instructions,
			LifecycleState:        lifecycleState,
			Revision:              rev,
			RequestedCapabilities: []WorkerCapabilityRequest{},
			WorkspaceRequirements: wsReqs,
			LocalBindings:         bindings,
			Automations:           automations,
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
		for _, a := range w.Automations {
			autoIDBytes, err := json.Marshal(w.ID)
			if err != nil {
				return summary, err
			}
			if err := batch.Set([]byte(KeyWorkerByAutomation(account, a.ID)), autoIDBytes, nil); err != nil {
				return summary, err
			}
		}

		// Link occurrences for this specific automation and account
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
				// Verify strict ownership
				if o.Record.AutomationID != rec.AutomationID || o.Record.AccountID != account {
					continue
				}
				occRev := o.Record.Generation
				if occRev == 0 {
					occRev = rev
				}
				delivs := o.Deliverables
				if delivs == nil {
					delivs = []SessionPlanArtifactReference{}
				}
				autoID := ""
				if len(w.Automations) > 0 {
					autoID = w.Automations[0].ID
				}
				run := WorkerRunRecord{
					ID:                 "wrun_" + o.ID,
					AccountScopeID:     account,
					WorkerID:           targetWorkerID,
					WorkerRevision:     occRev, // pinned snapshot revision
					AutomationID:       autoID,
					AutomationRevision: occRev, // pinned snapshot revision
					OccurrenceID:       o.ID,
					SessionID:          o.SessionID,
					RequestSource:      "schedule",
					Input:              o.TriggerContext,
					Status:             o.State,
					Error:              o.Detail,
					Deliverables:       delivs,
					StartedAt:          o.AdmittedAt,
					CompletedAt:        o.ObservedAt,
					CreatedAt:          o.AdmittedAt,
				}
				runBytes, rErr := json.Marshal(run)
				if rErr == nil {
					_ = batch.Set([]byte(KeyWorkerRun(account, targetWorkerID, run.ID)), runBytes, nil)
					occLinkBytes, _ := json.Marshal(targetWorkerID + ":" + run.ID)
					_ = batch.Set([]byte(KeyWorkerRunByOccurrence(account, o.ID)), occLinkBytes, nil)
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
