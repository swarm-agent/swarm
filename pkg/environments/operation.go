package environments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Domain limits for environment operations
const (
	maxActionBytes             = 64
	maxDescriptionLength       = 512
	maxSummaryLength           = 2048
	maxErrorMessageLength      = 2048
	maxFailureKindLength       = 64
	maxPhaseLength             = 64
	defaultOperationDeadlineMs = 5 * 60 * 1000  // 5 minutes default
	maxOperationDeadlineMs     = 10 * 60 * 1000 // 10 minutes max
)

// OperationStatus represents the lifecycle state of an environment operation.
type OperationStatus string

const (
	OperationStatusQueued        OperationStatus = "queued"
	OperationStatusRunning       OperationStatus = "running"
	OperationStatusCancelling    OperationStatus = "cancelling"
	OperationStatusSucceeded     OperationStatus = "succeeded"
	OperationStatusFailed        OperationStatus = "failed"
	OperationStatusCancelled     OperationStatus = "cancelled"
	OperationStatusTimedOut      OperationStatus = "timed_out"
	OperationStatusCleanupFailed OperationStatus = "cleanup_failed"
	OperationStatusUnknown       OperationStatus = "unknown"
)

// Standard operation actions
const (
	OperationActionDeploy   = "deploy"
	OperationActionEnsure   = "ensure"
	OperationActionExec     = "exec"
	OperationActionStop     = "stop"
	OperationActionCancel   = "cancel"
	OperationActionRelease  = "release"
	OperationActionRestart  = "restart"
	OperationActionRecreate = "recreate"
)

// Domain errors
var (
	ErrOperationNotFound           = errors.New("environment operation not found")
	ErrOperationTerminal           = errors.New("environment operation is already terminal")
	ErrOperationConflict           = errors.New("environment operation revision conflict")
	ErrDeploymentOperationConflict = errors.New("deployment has an active operation in progress")
	ErrDeploymentOperationBlocked  = errors.New("deployment has an unresolved operation requiring resolution")
	ErrIdempotencyConflict         = errors.New("idempotency key reused with different parameters")
	ErrInvalidStateTransition      = errors.New("invalid operation state transition")
	ErrCapacityExceeded            = errors.New("environment capacity exceeded")
	ErrCursorInvalid               = errors.New("invalid environment history cursor")
	ErrStaleCursor                 = errors.New("stale environment history cursor")
)

// OperationAttribution records actor, session, run, or worker attribution.
type OperationAttribution struct {
	Actor     string `json:"actor,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

// OperationActivity captures safe, bounded, redacted progress and activity.
// Strictly contains NO raw commands, environment credentials, or provider output.
type OperationActivity struct {
	Description    string `json:"description,omitempty"`
	Phase          string `json:"phase,omitempty"`
	ProgressPct    int    `json:"progress_pct,omitempty"`
	LastObservedAt int64  `json:"last_observed_at,omitempty"`
	HeartbeatSeq   int64  `json:"heartbeat_seq,omitempty"`
}

// OperationResult captures safe, bounded completion results.
type OperationResult struct {
	ExitCode     int    `json:"exit_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	FailureKind  string `json:"failure_kind,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

// EnvironmentOperation models a durable, supervised operation on an environment or deployment.
type EnvironmentOperation struct {
	OperationID    string               `json:"operation_id"`
	AccountScopeID string               `json:"account_scope_id"`
	WorkspaceID    string               `json:"workspace_id"`
	Action         string               `json:"action"`
	EnvironmentID  string               `json:"environment_id,omitempty"`
	DeploymentID   string               `json:"deployment_id,omitempty"`
	LeaseID        string               `json:"lease_id,omitempty"`
	Attribution    OperationAttribution `json:"attribution"`
	Status         OperationStatus      `json:"status"`
	Revision       uint64               `json:"revision"`
	IdempotencyKey string               `json:"idempotency_key,omitempty"`
	RequestHash    string               `json:"request_hash,omitempty"`

	CreatedAt   int64 `json:"created_at"`
	StartedAt   int64 `json:"started_at,omitempty"`
	ObservedAt  int64 `json:"observed_at,omitempty"`
	CompletedAt int64 `json:"completed_at,omitempty"`
	Deadline    int64 `json:"deadline,omitempty"` // finite epoch millisecond deadline

	Activity OperationActivity `json:"activity,omitempty"`
	Result   OperationResult   `json:"result,omitempty"`
}

// IsTerminal returns true if the operation has concluded its lifecycle.
func (op *EnvironmentOperation) IsTerminal() bool {
	if op == nil {
		return true
	}
	return op.Status.IsTerminal()
}

// IsTerminal checks if the status is one of the completed terminal states.
func (s OperationStatus) IsTerminal() bool {
	switch s {
	case OperationStatusSucceeded, OperationStatusFailed, OperationStatusCancelled, OperationStatusTimedOut:
		return true
	default:
		return false
	}
}

// IsActive returns true if the operation is actively queued, executing, or cancelling.
func (op *EnvironmentOperation) IsActive() bool {
	if op == nil {
		return false
	}
	return op.Status.IsActive()
}

// IsActive checks if the status represents an in-progress operation.
func (s OperationStatus) IsActive() bool {
	switch s {
	case OperationStatusQueued, OperationStatusRunning, OperationStatusCancelling:
		return true
	default:
		return false
	}
}

// IsUnresolved returns true if the operation ended in an unresolved failure state that guards deployment reuse.
func (op *EnvironmentOperation) IsUnresolved() bool {
	if op == nil {
		return false
	}
	return op.Status.IsUnresolved()
}

// IsUnresolved checks if the status is cleanup_failed or unknown.
func (s OperationStatus) IsUnresolved() bool {
	return s == OperationStatusCleanupFailed || s == OperationStatusUnknown
}

// Validate checks domain invariants, string lengths, UTF-8 validity, timestamp bounds, and secret redaction.
func (op *EnvironmentOperation) Validate() error {
	if op == nil {
		return errors.New("environment operation is nil")
	}

	op.OperationID = strings.TrimSpace(op.OperationID)
	if op.OperationID == "" {
		return errors.New("operation id cannot be empty")
	}
	if len(op.OperationID) > maxIDBytes || !utf8.ValidString(op.OperationID) {
		return fmt.Errorf("operation id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.AccountScopeID = strings.TrimSpace(op.AccountScopeID)
	if op.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(op.AccountScopeID) > maxIDBytes || !utf8.ValidString(op.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.WorkspaceID = strings.TrimSpace(op.WorkspaceID)
	if op.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(op.WorkspaceID) > maxIDBytes || !utf8.ValidString(op.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.Action = strings.TrimSpace(op.Action)
	if op.Action == "" {
		return errors.New("action cannot be empty")
	}
	if len(op.Action) > maxActionBytes || !utf8.ValidString(op.Action) {
		return fmt.Errorf("action exceeds %d bytes or is invalid UTF-8", maxActionBytes)
	}

	op.EnvironmentID = strings.TrimSpace(op.EnvironmentID)
	if op.EnvironmentID != "" && (len(op.EnvironmentID) > maxIDBytes || !utf8.ValidString(op.EnvironmentID)) {
		return fmt.Errorf("environment_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.DeploymentID = strings.TrimSpace(op.DeploymentID)
	if op.DeploymentID != "" && (len(op.DeploymentID) > maxIDBytes || !utf8.ValidString(op.DeploymentID)) {
		return fmt.Errorf("deployment_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.LeaseID = strings.TrimSpace(op.LeaseID)
	if op.LeaseID != "" && (len(op.LeaseID) > maxIDBytes || !utf8.ValidString(op.LeaseID)) {
		return fmt.Errorf("lease_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	op.IdempotencyKey = strings.TrimSpace(op.IdempotencyKey)
	if op.IdempotencyKey != "" && (len(op.IdempotencyKey) > maxIDBytes || !utf8.ValidString(op.IdempotencyKey)) {
		return fmt.Errorf("idempotency_key exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}
	op.RequestHash = strings.TrimSpace(op.RequestHash)
	if op.RequestHash != "" && (len(op.RequestHash) > 128 || !utf8.ValidString(op.RequestHash)) {
		return fmt.Errorf("request_hash exceeds 128 bytes or is invalid UTF-8")
	}

	switch op.Status {
	case OperationStatusQueued,
		OperationStatusRunning,
		OperationStatusCancelling,
		OperationStatusSucceeded,
		OperationStatusFailed,
		OperationStatusCancelled,
		OperationStatusTimedOut,
		OperationStatusCleanupFailed,
		OperationStatusUnknown:
		// valid
	default:
		return fmt.Errorf("unsupported operation status: %q", op.Status)
	}

	if op.CreatedAt <= 0 {
		return errors.New("created_at must be positive")
	}
	if op.Deadline <= 0 {
		return errors.New("deadline must be positive")
	}
	if op.Deadline < op.CreatedAt {
		return errors.New("deadline cannot be earlier than created_at")
	}
	if op.CompletedAt > 0 && op.CompletedAt < op.CreatedAt {
		return errors.New("completed_at cannot be earlier than created_at")
	}
	if op.StartedAt > 0 && op.StartedAt < op.CreatedAt {
		return errors.New("started_at cannot be earlier than created_at")
	}

	// Validate activity fields
	if len(op.Activity.Description) > maxDescriptionLength || !utf8.ValidString(op.Activity.Description) {
		return fmt.Errorf("activity description exceeds %d bytes or is invalid UTF-8", maxDescriptionLength)
	}
	if len(op.Activity.Phase) > maxPhaseLength || !utf8.ValidString(op.Activity.Phase) {
		return fmt.Errorf("activity phase exceeds %d bytes or is invalid UTF-8", maxPhaseLength)
	}
	if op.Activity.ProgressPct < 0 || op.Activity.ProgressPct > 100 {
		return errors.New("progress_pct must be between 0 and 100")
	}

	// Validate result fields
	if len(op.Result.ErrorMessage) > maxErrorMessageLength || !utf8.ValidString(op.Result.ErrorMessage) {
		return fmt.Errorf("result error_message exceeds %d bytes or is invalid UTF-8", maxErrorMessageLength)
	}
	if len(op.Result.Summary) > maxSummaryLength || !utf8.ValidString(op.Result.Summary) {
		return fmt.Errorf("result summary exceeds %d bytes or is invalid UTF-8", maxSummaryLength)
	}
	if len(op.Result.FailureKind) > maxFailureKindLength || !utf8.ValidString(op.Result.FailureKind) {
		return fmt.Errorf("result failure_kind exceeds %d bytes or is invalid UTF-8", maxFailureKindLength)
	}

	// Verify no credentials or secret keys are present in the serialized operation
	raw, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("marshal operation: %w", err)
	}
	if err := AssertNoSecretsRaw(raw); err != nil {
		return fmt.Errorf("operation security check failed: %w", err)
	}

	return nil
}

// Clone returns a deep copy of EnvironmentOperation.
func (op *EnvironmentOperation) Clone() *EnvironmentOperation {
	if op == nil {
		return nil
	}
	cp := *op
	return &cp
}

// EnvironmentSummary provides authoritative counts of deployments and operations.
// Strictly scoped to AccountScopeID and WorkspaceID.
type EnvironmentSummary struct {
	AccountScopeID    string `json:"account_scope_id"`
	WorkspaceID       string `json:"workspace_id"`
	Revision          uint64 `json:"revision"`
	UpdatedAt         int64  `json:"updated_at"`
	ActiveDeployments int    `json:"active_deployments"`
	RunningExecOps    int    `json:"running_exec_ops"`
	QueuedOps         int    `json:"queued_ops"`
	RunningOps        int    `json:"running_ops"`
	CancellingOps     int    `json:"cancelling_ops"`
	FailedOps         int    `json:"failed_ops"`
	CleanupFailedOps  int    `json:"cleanup_failed_ops"`
	UnknownOps        int    `json:"unknown_ops"`
	SucceededOps      int    `json:"succeeded_ops"`
	CancelledOps      int    `json:"cancelled_ops"`
	TimedOutOps       int    `json:"timed_out_ops"`
	TotalOps          int    `json:"total_ops"`
}

// Validate checks domain constraints for EnvironmentSummary.
func (s *EnvironmentSummary) Validate() error {
	if s == nil {
		return errors.New("environment summary is nil")
	}
	s.AccountScopeID = strings.TrimSpace(s.AccountScopeID)
	if s.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(s.AccountScopeID) > maxIDBytes || !utf8.ValidString(s.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}
	s.WorkspaceID = strings.TrimSpace(s.WorkspaceID)
	if s.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(s.WorkspaceID) > maxIDBytes || !utf8.ValidString(s.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}
	return nil
}

// Clone returns a deep copy of EnvironmentSummary.
func (s *EnvironmentSummary) Clone() *EnvironmentSummary {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

// DailyOperationCounts records aggregate terminal and execution counts for a single local calendar day.
type DailyOperationCounts struct {
	Date          string `json:"date"` // "YYYY-MM-DD" in requested timezone
	TotalOps      int    `json:"total_ops"`
	Succeeded     int    `json:"succeeded"`
	Failed        int    `json:"failed"`
	Cancelled     int    `json:"cancelled"`
	TimedOut      int    `json:"timed_out"`
	CleanupFailed int    `json:"cleanup_failed"`
	Unknown       int    `json:"unknown"`
	Running       int    `json:"running"`
	ExecOps       int    `json:"exec_ops"`
	DeployOps     int    `json:"deploy_ops"`
}

// OperationHistoryQuery defines search and pagination parameters for historical operations.
type OperationHistoryQuery struct {
	AccountScopeID string          `json:"account_scope_id"`
	WorkspaceID    string          `json:"workspace_id"`
	EnvironmentID  string          `json:"environment_id,omitempty"`
	DeploymentID   string          `json:"deployment_id,omitempty"`
	Actor          string          `json:"actor,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	WorkerID       string          `json:"worker_id,omitempty"`
	Status         OperationStatus `json:"status,omitempty"`
	Action         string          `json:"action,omitempty"`

	Timezone  string `json:"timezone,omitempty"`   // Explicit IANA timezone, defaults to "UTC"
	StartDate string `json:"start_date,omitempty"` // Inclusive YYYY-MM-DD
	EndDate   string `json:"end_date,omitempty"`   // Inclusive YYYY-MM-DD

	Cursor string `json:"cursor,omitempty"` // Opaque cursor bound to query/account/workspace
	Limit  int    `json:"limit,omitempty"`  // Default 50, max 100
}

// OperationRequestHashInput specifies parameters for computing an immutable request hash.
// Secrets in env are never retained raw; values are hashed individually.
type OperationRequestHashInput struct {
	Action            string               `json:"action"`
	EnvironmentID     string               `json:"environment_id,omitempty"`
	DeploymentID      string               `json:"deployment_id,omitempty"`
	LeaseID           string               `json:"lease_id,omitempty"`
	TargetOperationID string               `json:"target_operation_id,omitempty"`
	Attribution       OperationAttribution `json:"attribution"`
	ConnectionID      string               `json:"connection_id,omitempty"`
	DeploymentName    string               `json:"deployment_name,omitempty"`
	WorkspacePath     string               `json:"workspace_path,omitempty"`
	ConsumerType      ConsumerType         `json:"consumer_type,omitempty"`
	ConsumerID        string               `json:"consumer_id,omitempty"`
	ConsumerMetadata  map[string]string    `json:"consumer_metadata,omitempty"`
	TTLMillis         int64                `json:"ttl_millis,omitempty"`
	Command           []string             `json:"command,omitempty"`
	WorkingDir        string               `json:"working_dir,omitempty"`
	Env               map[string]string    `json:"env,omitempty"`
	MaxOutput         int                  `json:"max_output,omitempty"`
	Reason            string               `json:"reason,omitempty"`
}

// ComputeOperationRequestHash calculates an immutable SHA-256 digest of the full operation request parameters.
// Strictly hashes env values without exposing raw secret payloads.
func ComputeOperationRequestHash(in OperationRequestHashInput) string {
	h := sha256.New()
	writePart := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	writePart(strings.TrimSpace(in.Action))
	writePart(strings.TrimSpace(in.EnvironmentID))
	writePart(strings.TrimSpace(in.DeploymentID))
	writePart(strings.TrimSpace(in.LeaseID))
	writePart(strings.TrimSpace(in.TargetOperationID))
	writePart(strings.TrimSpace(in.Attribution.Actor))
	writePart(strings.TrimSpace(in.Attribution.SessionID))
	writePart(strings.TrimSpace(in.Attribution.RunID))
	writePart(strings.TrimSpace(in.Attribution.WorkerID))
	writePart(strings.TrimSpace(in.ConnectionID))
	writePart(strings.TrimSpace(in.DeploymentName))
	writePart(strings.TrimSpace(in.WorkspacePath))
	writePart(string(in.ConsumerType))
	writePart(strings.TrimSpace(in.ConsumerID))
	writePart(fmt.Sprintf("%d", in.TTLMillis))
	writePart(strings.TrimSpace(in.WorkingDir))
	writePart(fmt.Sprintf("%d", in.MaxOutput))
	writePart(strings.TrimSpace(in.Reason))

	// Consumer metadata in deterministic sorted order
	if len(in.ConsumerMetadata) > 0 {
		keys := make([]string, 0, len(in.ConsumerMetadata))
		for k := range in.ConsumerMetadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writePart(k)
			writePart(in.ConsumerMetadata[k])
		}
	}

	// Command arguments
	for _, arg := range in.Command {
		writePart(arg)
	}

	// Env: hash keys and individual SHA-256 of values so raw secrets are never bound
	if len(in.Env) > 0 {
		keys := make([]string, 0, len(in.Env))
		for k := range in.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writePart(k)
			vHash := sha256.Sum256([]byte(in.Env[k]))
			writePart(hex.EncodeToString(vHash[:]))
		}
	}

	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

// OperationHistoryPage returns a paginated slice of operations together with full daily totals and summary.
type OperationHistoryPage struct {
	Operations  []EnvironmentOperation `json:"operations"`
	NextCursor  string                 `json:"next_cursor,omitempty"`
	HasMore     bool                   `json:"has_more"`
	DailyTotals []DailyOperationCounts `json:"daily_totals"`
	Summary     EnvironmentSummary     `json:"summary"`
}
