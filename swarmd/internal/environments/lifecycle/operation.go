package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	// ErrOperationCancelled indicates that an operation was cancelled before or during execution.
	ErrOperationCancelled = errors.New("operation was cancelled")
	// ErrOperationTimedOut indicates that an operation exceeded its allotted deadline.
	ErrOperationTimedOut = errors.New("operation timed out")
	// ErrMissingAttribution indicates that attribution lacked any valid owner identity.
	ErrMissingAttribution = errors.New("attribution must specify at least one of actor, session_id, run_id, or worker_id")
	// ErrInvalidWorktreePath indicates an invalid or relative workspace path.
	ErrInvalidWorktreePath = errors.New("invalid worktree path")
)

type operationContextKey struct{}

func withOperationID(ctx context.Context, opID string) context.Context {
	return context.WithValue(ctx, operationContextKey{}, opID)
}

func operationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(operationContextKey{}).(string); ok {
		return v
	}
	return ""
}

// OperationStore abstracts persistence for environment operations and summaries.
type OperationStore interface {
	Get(accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error)
	GetActiveOperationForDeployment(accountScopeID, workspaceID, deploymentID string) (environments.EnvironmentOperation, bool, error)
	AdmitOperation(op environments.EnvironmentOperation) (environments.EnvironmentOperation, bool, error)
	TransitionOperation(input pebblestore.OperationTransitionInput) (environments.EnvironmentOperation, error)
	GetSummary(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error)
	RecalculateSummary(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error)
	UpdateDeploymentCount(accountScopeID, workspaceID string) error
	QueryHistory(q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error)
	ListNonTerminalOperations(limit int) ([]environments.EnvironmentOperation, error)
}

// OperationService defines the durable supervised operation interface.
type OperationService interface {
	// Submit admits and launches an environment operation, returning receipt within 2s.
	Submit(ctx context.Context, req SubmitOperationRequest) (*environments.EnvironmentOperation, error)

	// Get retrieves an operation by ID.
	Get(ctx context.Context, accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error)

	// History queries historical operations with cursor pagination and daily totals.
	History(ctx context.Context, q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error)

	// Summary returns authoritative deployment and operation counts.
	Summary(ctx context.Context, accountScopeID, workspaceID string) (environments.EnvironmentSummary, error)

	// Cancel initiates cancellation for a specific operation, acknowledging promptly and cleaning up within 15s.
	Cancel(ctx context.Context, req CancelOperationRequest) (*environments.EnvironmentOperation, error)

	// CancelOwner cancels all active operations matching the given owner/session/worker.
	CancelOwner(ctx context.Context, req CancelOwnerRequest) (int, error)

	// Recover reconciles non-terminal operations on daemon startup without replaying commands.
	Recover(ctx context.Context) error

	// Close gracefully shuts down supervisor operations on daemon stop.
	Close() error
}

// SubmitOperationRequest specifies parameters for submitting a supervised environment operation.
type SubmitOperationRequest struct {
	AccountScopeID string                            `json:"account_scope_id"`
	WorkspaceID    string                            `json:"workspace_id"`
	Action         string                            `json:"action"` // "ensure", "deploy", "exec", "stop", "start", "release", "destroy", "cancel"
	EnvironmentID  string                            `json:"environment_id,omitempty"`
	DeploymentID   string                            `json:"deployment_id,omitempty"`
	LeaseID        string                            `json:"lease_id,omitempty"`
	Attribution    environments.OperationAttribution `json:"attribution"`
	IdempotencyKey string                            `json:"idempotency_key,omitempty"`
	Deadline       int64                             `json:"deadline,omitempty"` // epoch millis
	Timeout        time.Duration                     `json:"timeout,omitempty"`

	// Deploy / Ensure specific:
	ConnectionID     string                    `json:"connection_id,omitempty"`
	DeploymentName   string                    `json:"deployment_name,omitempty"`
	WorkspacePath    string                    `json:"workspace_path,omitempty"` // Actual canonical worktree path
	EnvOverrides     map[string]string         `json:"env_overrides,omitempty"`
	ConsumerType     environments.ConsumerType `json:"consumer_type,omitempty"`
	ConsumerID       string                    `json:"consumer_id,omitempty"`
	ConsumerMetadata map[string]string         `json:"consumer_metadata,omitempty"`
	TTLMillis        int64                     `json:"ttl_millis,omitempty"`

	// Exec specific:
	Command    []string          `json:"command,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	MaxOutput  int               `json:"max_output,omitempty"`

	// Release / Destroy specific:
	Reason string `json:"reason,omitempty"`

	// Cancel specific:
	TargetOperationID string `json:"target_operation_id,omitempty"`
}

// CancelOperationRequest specifies parameters for cancelling a specific operation.
type CancelOperationRequest struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID    string `json:"workspace_id"`
	OperationID    string `json:"operation_id"`
	Reason         string `json:"reason,omitempty"`
}

// CancelOwnerRequest specifies criteria for cancelling operations owned by a session or worker.
type CancelOwnerRequest struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID    string `json:"workspace_id"`
	SessionID      string `json:"session_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	Actor          string `json:"actor,omitempty"`
	ConsumerID     string `json:"consumer_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// activeOpState tracks in-memory execution state of a supervised operation.
type activeOpState struct {
	opID        string
	accountID   string
	workspaceID string
	action      string
	depID       string
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	isCancelled bool
}

// DeploymentManagerOption configures a DeploymentManager.
type DeploymentManagerOption func(*DeploymentManager)

// WithOperationStore injects a custom or mock OperationStore.
func WithOperationStore(store OperationStore) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		m.operations = store
	}
}

// WithHeartbeatInterval overrides the default operation heartbeat interval.
func WithHeartbeatInterval(d time.Duration) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if d > 0 {
			m.heartbeatInterval = d
		}
	}
}

// WithCleanupTimeout overrides the default cancellation cleanup timeout (max 15s recommended).
func WithCleanupTimeout(d time.Duration) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if d > 0 {
			m.cleanupTimeout = d
		}
	}
}

// WithProbeTimeout overrides the default probe/inspect timeout (10s default).
func WithProbeTimeout(d time.Duration) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if d > 0 {
			m.probeTimeout = d
		}
	}
}

// WithDefaultTimeout overrides the default operation deadline (5m default).
func WithDefaultTimeout(d time.Duration) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if d > 0 {
			m.defaultTimeout = d
		}
	}
}

// WithMaxTimeout overrides the max allowed operation deadline (10m default).
func WithMaxTimeout(d time.Duration) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if d > 0 {
			m.maxTimeout = d
		}
	}
}

// WithMaxConcurrentOps configures maximum concurrent running operations across the manager.
func WithMaxConcurrentOps(limit int) DeploymentManagerOption {
	return func(m *DeploymentManager) {
		if limit > 0 {
			m.maxConcurrentOps = limit
			m.opSem = make(chan struct{}, limit)
		}
	}
}

// SetOperationStore allows dynamic injection of the OperationStore.
func (m *DeploymentManager) SetOperationStore(store OperationStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.operations = store
}

// Operations returns the configured OperationStore.
func (m *DeploymentManager) Operations() OperationStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.operations
}

// validateAdmission performs comprehensive, bounded checks prior to admission.
// Validates saved environment, registered connection, canonical worktree,
// caller attribution, and held lease for exec.
func (m *DeploymentManager) validateAdmission(
	ctx context.Context,
	req *SubmitOperationRequest,
) (*environments.Environment, *environments.Connection, *environments.Deployment, error) {
	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.Action = strings.TrimSpace(req.Action)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.DeploymentID = strings.TrimSpace(req.DeploymentID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Attribution.Actor = strings.TrimSpace(req.Attribution.Actor)
	req.Attribution.SessionID = strings.TrimSpace(req.Attribution.SessionID)
	req.Attribution.RunID = strings.TrimSpace(req.Attribution.RunID)
	req.Attribution.WorkerID = strings.TrimSpace(req.Attribution.WorkerID)

	if req.AccountScopeID == "" || req.WorkspaceID == "" {
		return nil, nil, nil, errors.New("account_scope_id and workspace_id are required")
	}
	if req.Action == "" {
		return nil, nil, nil, errors.New("action is required")
	}

	// 1. Attribution validation
	if req.Attribution.Actor == "" && req.Attribution.SessionID == "" &&
		req.Attribution.RunID == "" && req.Attribution.WorkerID == "" {
		return nil, nil, nil, ErrMissingAttribution
	}

	// 2. Canonical worktree validation: do not replace isolated worktree with workspace root
	if req.WorkspacePath != "" {
		cleaned := filepath.Clean(req.WorkspacePath)
		if !filepath.IsAbs(cleaned) {
			return nil, nil, nil, fmt.Errorf("%w: %q must be an absolute path", ErrInvalidWorktreePath, req.WorkspacePath)
		}
		if strings.Contains(req.WorkspacePath, "..") {
			return nil, nil, nil, fmt.Errorf("%w: %q contains relative traversal", ErrInvalidWorktreePath, req.WorkspacePath)
		}
		req.WorkspacePath = cleaned
	}

	var env *environments.Environment
	var conn *environments.Connection
	var dep *environments.Deployment

	// 3. Action-specific validation
	switch req.Action {
	case environments.OperationActionEnsure, environments.OperationActionDeploy:
		if req.EnvironmentID == "" {
			return nil, nil, nil, errors.New("environment_id is required for deploy/ensure")
		}
		if m.environments == nil {
			return nil, nil, nil, errors.New("environment reader is not configured")
		}
		e, found, err := m.environments.Get(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("lookup environment %q: %w", req.EnvironmentID, err)
		}
		if !found {
			return nil, nil, nil, fmt.Errorf("environment %q: %w", req.EnvironmentID, ErrEnvironmentNotFound)
		}
		env = &e

		// Resolve connection
		c, err := m.ResolveConnection(ctx, req.AccountScopeID, req.WorkspaceID, req.ConnectionID, env)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resolve connection: %w", err)
		}
		conn = c

		// Verify provider registration
		if m.registry == nil {
			return nil, nil, nil, errors.New("provider registry is not configured")
		}
		if _, ok := m.registry.Get(conn.Kind); !ok {
			return nil, nil, nil, fmt.Errorf("provider for kind %q: %w", conn.Kind, ErrProviderNotRegistered)
		}

	case environments.OperationActionExec:
		if req.DeploymentID == "" {
			return nil, nil, nil, errors.New("deployment_id is required for exec")
		}
		if len(req.Command) == 0 {
			return nil, nil, nil, errors.New("command cannot be empty for exec")
		}
		if m.deployments == nil {
			return nil, nil, nil, errors.New("deployment store is not configured")
		}
		d, found, err := m.deployments.Get(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("lookup deployment %q: %w", req.DeploymentID, err)
		}
		if !found {
			return nil, nil, nil, fmt.Errorf("deployment %q: %w", req.DeploymentID, ErrDeploymentNotFound)
		}
		dep = &d
		if !dep.IsUsable() {
			return nil, nil, nil, fmt.Errorf("deployment %q is in status %q (health: %q): %w", dep.ID, dep.Status, dep.Health, ErrDeploymentUnusable)
		}

		// Lookup connection
		if m.connections == nil {
			return nil, nil, nil, errors.New("connection store is not configured")
		}
		c, foundConn, err := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("lookup connection %q: %w", dep.ConnectionID, err)
		}
		if !foundConn {
			return nil, nil, nil, fmt.Errorf("connection %q: %w", dep.ConnectionID, ErrConnectionNotFound)
		}
		conn = &c

		// Verify provider registration
		if m.registry == nil {
			return nil, nil, nil, errors.New("provider registry is not configured")
		}
		if _, ok := m.registry.Get(conn.Kind); !ok {
			return nil, nil, nil, fmt.Errorf("provider for kind %q: %w", conn.Kind, ErrProviderNotRegistered)
		}

		// Validate current held lease for exec
		now := time.Now().UnixMilli()
		callerID := firstNonEmpty(req.Attribution.SessionID, req.Attribution.WorkerID, req.Attribution.Actor)
		if req.LeaseID != "" {
			if m.deployments.Leases() == nil {
				return nil, nil, nil, errors.New("lease store is not configured")
			}
			lease, foundLease, err := m.deployments.Leases().Get(req.AccountScopeID, req.WorkspaceID, req.LeaseID)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("lookup lease %q: %w", req.LeaseID, err)
			}
			if !foundLease {
				return nil, nil, nil, fmt.Errorf("lease %q: %w", req.LeaseID, ErrLeaseNotFound)
			}
			if !lease.Active || lease.IsExpired(now) {
				return nil, nil, nil, fmt.Errorf("lease %q: %w", req.LeaseID, ErrLeaseAlreadyReleased)
			}
			if lease.DeploymentID != dep.ID {
				return nil, nil, nil, fmt.Errorf("lease %q does not match deployment %q", req.LeaseID, dep.ID)
			}
			if lease.ConsumerID != "" && callerID != "" && lease.ConsumerID != callerID && req.ConsumerID != lease.ConsumerID {
				return nil, nil, nil, fmt.Errorf("%w: lease held by consumer %q, caller is %q", ErrDeploymentLeaseHeld, lease.ConsumerID, callerID)
			}
		} else {
			activeLease, hasActive, err := m.deployments.GetActiveLease(req.AccountScopeID, req.WorkspaceID, dep.ID)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("get active lease for deployment: %w", err)
			}
			if !hasActive || !activeLease.Active || activeLease.IsExpired(now) {
				return nil, nil, nil, fmt.Errorf("active lease required to exec in deployment %q", dep.ID)
			}
			if activeLease.ConsumerID != "" && callerID != "" && activeLease.ConsumerID != callerID && req.ConsumerID != activeLease.ConsumerID {
				return nil, nil, nil, fmt.Errorf("%w: active lease held by consumer %q, caller is %q", ErrDeploymentLeaseHeld, activeLease.ConsumerID, callerID)
			}
			req.LeaseID = activeLease.ID
		}

	case environments.OperationActionStop, "start", environments.OperationActionRelease, "destroy":
		if req.DeploymentID == "" {
			return nil, nil, nil, fmt.Errorf("deployment_id is required for action %q", req.Action)
		}
		if m.deployments == nil {
			return nil, nil, nil, errors.New("deployment store is not configured")
		}
		d, found, err := m.deployments.Get(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("lookup deployment %q: %w", req.DeploymentID, err)
		}
		if !found {
			return nil, nil, nil, fmt.Errorf("deployment %q: %w", req.DeploymentID, ErrDeploymentNotFound)
		}
		dep = &d

		if m.connections != nil && dep.ConnectionID != "" {
			c, foundConn, err := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
			if err == nil && foundConn {
				conn = &c
			}
		}

	case environments.OperationActionCancel:
		if req.TargetOperationID == "" && req.DeploymentID == "" {
			return nil, nil, nil, errors.New("target_operation_id or deployment_id is required for cancel")
		}

	default:
		return nil, nil, nil, fmt.Errorf("unsupported operation action: %q", req.Action)
	}

	return env, conn, dep, nil
}

// Submit admits and launches an environment operation, persisting receipt and returning within 2s.
func (m *DeploymentManager) Submit(ctx context.Context, req SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	if m == nil || m.operations == nil {
		return nil, errors.New("operation store is not configured")
	}

	admissionCtx, cancelAdmission := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAdmission()

	if err := admissionCtx.Err(); err != nil {
		return nil, fmt.Errorf("admission aborted before validation: %w", err)
	}

	// 1. Admission validation
	env, conn, dep, err := m.validateAdmission(admissionCtx, &req)
	if err != nil {
		return nil, fmt.Errorf("validate admission: %w", err)
	}

	if err := admissionCtx.Err(); err != nil {
		return nil, fmt.Errorf("admission deadline exceeded (budget: 2s): %w", err)
	}

	// Stop/destroy while exec is active: cancel active exec first rather than permanently rejecting
	if (req.Action == environments.OperationActionStop || req.Action == "destroy" || req.Action == environments.OperationActionRelease) && dep != nil {
		activeOp, hasActive, _ := m.operations.GetActiveOperationForDeployment(req.AccountScopeID, req.WorkspaceID, dep.ID)
		if hasActive && activeOp.Action == environments.OperationActionExec && activeOp.IsActive() {
			_, _ = m.Cancel(admissionCtx, CancelOperationRequest{
				AccountScopeID: req.AccountScopeID,
				WorkspaceID:    req.WorkspaceID,
				OperationID:    activeOp.OperationID,
				Reason:         fmt.Sprintf("cancelled by %s operation", req.Action),
			})
			waitDeadline := time.Now().Add(1 * time.Second)
			for time.Now().Before(waitDeadline) {
				cur, found, gErr := m.operations.Get(req.AccountScopeID, req.WorkspaceID, activeOp.OperationID)
				if gErr == nil && found && !cur.IsActive() {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}

	// 2. Finite deadline calculation (5m default, 10m max)
	now := time.Now().UnixMilli()
	defaultMs := int64(m.defaultTimeout / time.Millisecond)
	maxMs := int64(m.maxTimeout / time.Millisecond)
	deadline := now + defaultMs

	if req.Timeout > 0 {
		t := req.Timeout
		if t > m.maxTimeout {
			t = m.maxTimeout
		}
		deadline = now + int64(t/time.Millisecond)
	} else if req.Deadline > 0 {
		if req.Deadline-now > maxMs {
			deadline = now + maxMs
		} else if req.Deadline > now {
			deadline = req.Deadline
		}
	}

	envID := req.EnvironmentID
	if envID == "" && dep != nil {
		envID = dep.EnvironmentID
	}
	depID := req.DeploymentID
	if depID == "" && dep != nil {
		depID = dep.ID
	}
	// For new deployment provisioning, assign durable IDs before side effects so cancellation/recovery can find partial resources
	if req.Action == environments.OperationActionDeploy && depID == "" {
		depID = "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		req.DeploymentID = depID
	}
	if (req.Action == environments.OperationActionDeploy || req.Action == environments.OperationActionEnsure) && req.ConsumerID != "" && req.LeaseID == "" {
		req.LeaseID = "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}

	opID := "op_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reqHash := environments.ComputeOperationRequestHash(environments.OperationRequestHashInput{
		Action:            req.Action,
		EnvironmentID:     envID,
		DeploymentID:      depID,
		LeaseID:           req.LeaseID,
		TargetOperationID: req.TargetOperationID,
		Attribution:       req.Attribution,
		ConnectionID:      req.ConnectionID,
		DeploymentName:    req.DeploymentName,
		WorkspacePath:     req.WorkspacePath,
		ConsumerType:      req.ConsumerType,
		ConsumerID:        req.ConsumerID,
		ConsumerMetadata:  req.ConsumerMetadata,
		TTLMillis:         req.TTLMillis,
		Command:           req.Command,
		WorkingDir:        req.WorkingDir,
		Env:               req.Env,
		MaxOutput:         req.MaxOutput,
		Reason:            req.Reason,
	})

	op := environments.EnvironmentOperation{
		OperationID:    opID,
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		Action:         req.Action,
		EnvironmentID:  envID,
		DeploymentID:   depID,
		LeaseID:        req.LeaseID,
		Attribution:    req.Attribution,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    reqHash,
		CreatedAt:      now,
		ObservedAt:     now,
		Deadline:       deadline,
		Status:         environments.OperationStatusQueued,
		Activity: environments.OperationActivity{
			Description:    fmt.Sprintf("Queued %s operation", req.Action),
			Phase:          "queued",
			ProgressPct:    0,
			LastObservedAt: now,
		},
	}

	if err := admissionCtx.Err(); err != nil {
		return nil, fmt.Errorf("admission deadline exceeded before admit: %w", err)
	}

	// 3. Atomically admit operation into store
	admittedOp, created, err := m.operations.AdmitOperation(op)
	if err != nil {
		return nil, fmt.Errorf("admit operation: %w", err)
	}

	// 4. If idempotent reuse returned existing record, return immediately without launching duplicate
	if !created {
		return &admittedOp, nil
	}

	// 5. Asynchronously execute under manager-owned supervision
	go m.superviseOperation(admittedOp, req, env, conn, dep)

	return &admittedOp, nil
}

// superviseOperation manages lifecycle, heartbeats, progress, and terminal CAS transitions.
func (m *DeploymentManager) superviseOperation(
	op environments.EnvironmentOperation,
	req SubmitOperationRequest,
	env *environments.Environment,
	conn *environments.Connection,
	dep *environments.Deployment,
) {
	// Acquire bounded manager capacity
	select {
	case m.opSem <- struct{}{}:
	case <-m.rootCtx.Done():
		_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
			AccountScopeID:   op.AccountScopeID,
			WorkspaceID:      op.WorkspaceID,
			OperationID:      op.OperationID,
			ExpectedRevision: op.Revision,
			TargetStatus:     environments.OperationStatusCancelled,
			Result: &environments.OperationResult{
				FailureKind:  "manager_shutdown",
				ErrorMessage: "manager is shutting down",
			},
			ObservedAt: time.Now().UnixMilli(),
		})
		return
	}

	capacityReleased := false
	releaseCapacityOnce := func() {
		if !capacityReleased {
			capacityReleased = true
			select {
			case <-m.opSem:
			default:
			}
		}
	}
	defer func() {
		if !capacityReleased {
			releaseCapacityOnce()
		}
	}()

	// Operation context with finite deadline independent of caller's context
	deadline := time.UnixMilli(op.Deadline)
	opCtx, opCancel := context.WithDeadline(m.rootCtx, deadline)
	state := &activeOpState{
		opID:        op.OperationID,
		accountID:   op.AccountScopeID,
		workspaceID: op.WorkspaceID,
		action:      op.Action,
		depID:       op.DeploymentID,
		cancel:      opCancel,
		done:        make(chan struct{}),
	}

	m.activeOpsMu.Lock()
	m.activeOps[op.OperationID] = state
	m.activeOpsMu.Unlock()

	defer func() {
		opCancel()
		close(state.done)
		m.activeOpsMu.Lock()
		delete(m.activeOps, op.OperationID)
		m.activeOpsMu.Unlock()
	}()

	now := time.Now().UnixMilli()
	runningOp, err := m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
		AccountScopeID:   op.AccountScopeID,
		WorkspaceID:      op.WorkspaceID,
		OperationID:      op.OperationID,
		ExpectedRevision: op.Revision,
		TargetStatus:     environments.OperationStatusRunning,
		Activity: &environments.OperationActivity{
			Description:    fmt.Sprintf("Executing %s", op.Action),
			Phase:          "running",
			ProgressPct:    0,
			LastObservedAt: now,
			HeartbeatSeq:   1,
		},
		ObservedAt: now,
	})
	if err != nil {
		cur, found, gErr := m.operations.Get(op.AccountScopeID, op.WorkspaceID, op.OperationID)
		if gErr == nil && found && cur.Status == environments.OperationStatusCancelling {
			m.finalizeCancellation(cur, nil, errors.New("cancelled while queued"), conn, dep)
		}
		return
	}
	op = runningOp

	// Transition synchronization mutex
	var transMu sync.Mutex
	currentOp := op
	lastProgressWrite := time.Now()

	// Heartbeat ticker in background goroutine (observed at least each 15s)
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		interval := m.heartbeatInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-state.done:
				return
			case <-opCtx.Done():
				return
			case <-heartbeatStop:
				return
			case <-ticker.C:
				transMu.Lock()
				tNow := time.Now().UnixMilli()
				act := currentOp.Activity
				act.LastObservedAt = tNow
				act.HeartbeatSeq++
				up, err := m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
					AccountScopeID:   currentOp.AccountScopeID,
					WorkspaceID:      currentOp.WorkspaceID,
					OperationID:      currentOp.OperationID,
					ExpectedRevision: currentOp.Revision,
					TargetStatus:     environments.OperationStatusRunning,
					Activity:         &act,
					ObservedAt:       tNow,
				})
				if err == nil {
					currentOp = up
				}
				transMu.Unlock()
			}
		}
	}()

	// Asynchronously execute action so hung provider cannot block supervisor
	type actionOutcome struct {
		res *environments.OperationResult
		err error
	}
	actionDoneCh := make(chan actionOutcome, 1)

	go func() {
		r, e := m.executeAction(opCtx, &currentOp, &transMu, &lastProgressWrite, req, env, conn, dep)
		actionDoneCh <- actionOutcome{res: r, err: e}
	}()

	select {
	case outcome := <-actionDoneCh:
		close(heartbeatStop)
		<-heartbeatDone

		transMu.Lock()
		latest := currentOp
		transMu.Unlock()

		state.mu.Lock()
		wasCancelled := state.isCancelled
		state.mu.Unlock()

		tNow := time.Now().UnixMilli()

		if wasCancelled || opCtx.Err() == context.Canceled {
			m.finalizeCancellation(latest, outcome.res, outcome.err, conn, dep)
			return
		}
		if opCtx.Err() == context.DeadlineExceeded {
			m.finalizeTimeout(latest, outcome.res, outcome.err, conn, dep)
			return
		}

		if outcome.err != nil {
			failResult := environments.OperationResult{
				ExitCode:     1,
				ErrorMessage: boundedString(outcome.err.Error(), 2048),
				FailureKind:  "execution_error",
				Summary:      "Operation failed: " + boundedString(outcome.err.Error(), 512),
			}
			if outcome.res != nil {
				if outcome.res.ExitCode != 0 {
					failResult.ExitCode = outcome.res.ExitCode
				}
				if outcome.res.ErrorMessage != "" {
					failResult.ErrorMessage = boundedString(outcome.res.ErrorMessage, 2048)
				}
				if outcome.res.FailureKind != "" {
					failResult.FailureKind = boundedString(outcome.res.FailureKind, 64)
				}
			}
			m.transitionFinalWithRetry(latest, environments.OperationStatusFailed, &failResult, tNow, conn, dep)
			return
		}

		// Succeeded
		succResult := environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("%s completed successfully", req.Action),
		}
		if outcome.res != nil && outcome.res.Summary != "" {
			succResult.Summary = boundedString(outcome.res.Summary, 2048)
		}
		m.transitionFinalWithRetry(latest, environments.OperationStatusSucceeded, &succResult, tNow, conn, dep)

	case <-opCtx.Done():
		// Timeout or cancellation triggered independently of hung provider
		close(heartbeatStop)
		<-heartbeatDone

		transMu.Lock()
		latest := currentOp
		transMu.Unlock()

		state.mu.Lock()
		wasCancelled := state.isCancelled
		state.mu.Unlock()

		var cleanupTerminated bool
		if wasCancelled || opCtx.Err() == context.Canceled {
			cleanupTerminated = m.finalizeCancellation(latest, nil, errors.New("cancelled"), conn, dep)
		} else {
			cleanupTerminated = m.finalizeTimeout(latest, nil, errors.New("timed out"), conn, dep)
		}

		if cleanupTerminated {
			go func() {
				<-actionDoneCh
				releaseCapacityOnce()
			}()
			capacityReleased = true
		} else {
			// Provider refused cleanup or failed: retain blocked capacity to prevent unbounded leaks
			capacityReleased = true
			go func() {
				<-actionDoneCh
				select {
				case <-m.opSem:
				default:
				}
			}()
		}

	case <-m.rootCtx.Done():
		close(heartbeatStop)
		<-heartbeatDone
		transMu.Lock()
		latest := currentOp
		transMu.Unlock()
		m.finalizeCancellation(latest, nil, errors.New("manager shutting down"), conn, dep)
	}
}

// transitionFinalWithRetry retries terminal transitions upon CAS revision conflict,
// ensuring heartbeat CAS races do not mistakenly trigger cancellation.
func (m *DeploymentManager) transitionFinalWithRetry(
	op environments.EnvironmentOperation,
	targetStatus environments.OperationStatus,
	result *environments.OperationResult,
	observedAt int64,
	conn *environments.Connection,
	dep *environments.Deployment,
) {
	for attempt := 0; attempt < 5; attempt++ {
		cur, found, err := m.operations.Get(op.AccountScopeID, op.WorkspaceID, op.OperationID)
		if err != nil || !found {
			return
		}
		if cur.Status == environments.OperationStatusCancelling {
			m.finalizeCancellation(cur, result, errors.New("cancelled by user"), conn, dep)
			return
		}
		if cur.IsTerminal() {
			return
		}
		_, transErr := m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
			AccountScopeID:   cur.AccountScopeID,
			WorkspaceID:      cur.WorkspaceID,
			OperationID:      cur.OperationID,
			ExpectedRevision: cur.Revision,
			TargetStatus:     targetStatus,
			Result:           result,
			ObservedAt:       observedAt,
		})
		if transErr == nil {
			return
		}
	}
}

// executeAction dispatches execution to the corresponding lifecycle method.
func (m *DeploymentManager) executeAction(
	ctx context.Context,
	currentOp *environments.EnvironmentOperation,
	transMu *sync.Mutex,
	lastProgressWrite *time.Time,
	req SubmitOperationRequest,
	env *environments.Environment,
	conn *environments.Connection,
	dep *environments.Deployment,
) (*environments.OperationResult, error) {
	ctx = withOperationID(ctx, currentOp.OperationID)
	switch req.Action {
	case environments.OperationActionEnsure:
		ensureReq := EnsureDeploymentRequest{
			AccountScopeID:   req.AccountScopeID,
			WorkspaceID:      req.WorkspaceID,
			EnvironmentID:    req.EnvironmentID,
			ConnectionID:     req.ConnectionID,
			ConsumerType:     req.ConsumerType,
			ConsumerID:       req.ConsumerID,
			ConsumerMetadata: req.ConsumerMetadata,
			DeploymentID:     req.DeploymentID,
			DeploymentName:   req.DeploymentName,
			WorkspacePath:    req.WorkspacePath,
			EnvOverrides:     req.EnvOverrides,
			TTLMillis:        req.TTLMillis,
		}
		res, err := m.EnsureDeployment(ctx, ensureReq)
		if err != nil {
			return nil, err
		}
		transMu.Lock()
		currentOp.DeploymentID = res.Deployment.ID
		currentOp.LeaseID = res.Lease.ID
		transMu.Unlock()
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s ensured (reused: %v)", res.Deployment.ID, res.Reused),
		}, nil

	case environments.OperationActionDeploy:
		deployReq := DeployDeploymentRequest{
			AccountScopeID:   req.AccountScopeID,
			WorkspaceID:      req.WorkspaceID,
			EnvironmentID:    req.EnvironmentID,
			ConnectionID:     req.ConnectionID,
			DeploymentID:     req.DeploymentID,
			DeploymentName:   req.DeploymentName,
			WorkspacePath:    req.WorkspacePath,
			EnvOverrides:     req.EnvOverrides,
			ConsumerType:     req.ConsumerType,
			ConsumerID:       req.ConsumerID,
			ConsumerMetadata: req.ConsumerMetadata,
			TTLMillis:        req.TTLMillis,
		}
		res, err := m.DeployDeployment(ctx, deployReq)
		if err != nil {
			return nil, err
		}
		transMu.Lock()
		currentOp.DeploymentID = res.Deployment.ID
		if res.Lease != nil {
			currentOp.LeaseID = res.Lease.ID
		}
		transMu.Unlock()
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s provisioned", res.Deployment.ID),
		}, nil

	case environments.OperationActionExec:
		if conn == nil || dep == nil {
			return nil, errors.New("connection or deployment not resolved for exec")
		}
		prov, ok := m.registry.Get(conn.Kind)
		if !ok {
			return nil, fmt.Errorf("provider for kind %q: %w", conn.Kind, ErrProviderNotRegistered)
		}

		execReq := provider.ExecRequest{
			OperationID: currentOp.OperationID,
			Command:     req.Command,
			WorkingDir:  req.WorkingDir,
			Env:         req.Env,
			MaxOutput:   req.MaxOutput,
			OnProgress: func(p provider.ExecProgress) {
				transMu.Lock()
				defer transMu.Unlock()
				now := time.Now()
				if now.Sub(*lastProgressWrite) < 250*time.Millisecond {
					return
				}
				*lastProgressWrite = now
				tNow := p.Timestamp.UnixMilli()
				act := currentOp.Activity
				act.LastObservedAt = tNow
				act.HeartbeatSeq++
				up, err := m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
					AccountScopeID:   currentOp.AccountScopeID,
					WorkspaceID:      currentOp.WorkspaceID,
					OperationID:      currentOp.OperationID,
					ExpectedRevision: currentOp.Revision,
					TargetStatus:     environments.OperationStatusRunning,
					Activity:         &act,
					ObservedAt:       tNow,
				})
				if err == nil {
					*currentOp = up
				}
			},
		}

		execRes, err := prov.Exec(ctx, conn, dep, execReq)
		if err != nil {
			exitCode := 1
			if execRes != nil && execRes.ExitCode != 0 {
				exitCode = execRes.ExitCode
			}
			return &environments.OperationResult{
				ExitCode:     exitCode,
				ErrorMessage: err.Error(),
				FailureKind:  "exec_error",
				Summary:      "Exec failed: " + err.Error(),
			}, err
		}
		if execRes.ExitCode != 0 {
			return &environments.OperationResult{
				ExitCode:     execRes.ExitCode,
				ErrorMessage: fmt.Sprintf("command exited with code %d", execRes.ExitCode),
				FailureKind:  "non_zero_exit",
				Summary:      fmt.Sprintf("Exit code %d", execRes.ExitCode),
			}, fmt.Errorf("command exited with code %d", execRes.ExitCode)
		}

		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  sanitizeOutput(execRes.Stdout),
		}, nil

	case environments.OperationActionStop:
		err := m.StopDeployment(ctx, req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
		if err != nil {
			return nil, err
		}
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s stopped", req.DeploymentID),
		}, nil

	case "start":
		err := m.StartDeployment(ctx, req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
		if err != nil {
			return nil, err
		}
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s started", req.DeploymentID),
		}, nil

	case environments.OperationActionRelease:
		res, err := m.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			LeaseID:        req.LeaseID,
			Reason:         req.Reason,
		})
		if err != nil {
			return nil, err
		}
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s released (action: %s)", res.Deployment.ID, res.ActionTaken),
		}, nil

	case "destroy":
		err := m.DestroyDeployment(ctx, DestroyDeploymentRequest{
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			DeploymentID:   req.DeploymentID,
			Reason:         req.Reason,
		})
		if err != nil {
			return nil, err
		}
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Deployment %s destroyed", req.DeploymentID),
		}, nil

	case environments.OperationActionCancel:
		targetID := req.TargetOperationID
		if targetID == "" {
			targetID = req.DeploymentID
		}
		_, err := m.Cancel(ctx, CancelOperationRequest{
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			OperationID:    targetID,
			Reason:         req.Reason,
		})
		if err != nil {
			return nil, err
		}
		return &environments.OperationResult{
			ExitCode: 0,
			Summary:  fmt.Sprintf("Operation %s cancelled", targetID),
		}, nil

	default:
		return nil, fmt.Errorf("unhandled operation action: %q", req.Action)
	}
}

// finalizeCancellation safely terminates the target process using bounded provider cleanup (<=15s)
// and updates the durable record to cancelled, cleanup_failed, or unknown.
func (m *DeploymentManager) finalizeCancellation(
	op environments.EnvironmentOperation,
	res *environments.OperationResult,
	execErr error,
	conn *environments.Connection,
	dep *environments.Deployment,
) bool {
	cleanupTimeout := m.cleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = provider.CleanupTimeout
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cleanupCancel()

	terminated, cleanupErr := m.cleanupTargetProcess(cleanupCtx, op, conn, dep)

	now := time.Now().UnixMilli()
	latestOp, found, err := m.operations.Get(op.AccountScopeID, op.WorkspaceID, op.OperationID)
	if err == nil && found {
		op = latestOp
	}

	if cleanupErr != nil || !terminated {
		targetStatus := environments.OperationStatusCleanupFailed
		if errors.Is(cleanupErr, provider.ErrOperationNotConfirmed) || errors.Is(cleanupErr, context.DeadlineExceeded) {
			targetStatus = environments.OperationStatusUnknown
		}
		errMsg := "target cleanup failed"
		if cleanupErr != nil {
			errMsg = cleanupErr.Error()
		}
		_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
			AccountScopeID:   op.AccountScopeID,
			WorkspaceID:      op.WorkspaceID,
			OperationID:      op.OperationID,
			ExpectedRevision: op.Revision,
			TargetStatus:     targetStatus,
			Result: &environments.OperationResult{
				ExitCode:     130,
				ErrorMessage: boundedString(errMsg, 2048),
				FailureKind:  string(targetStatus),
				Summary:      "Target process cleanup could not be confirmed; deployment blocked against reuse",
			},
			ObservedAt: now,
		})
		if dep != nil {
			_, _ = m.deployments.UpdateStatus(op.AccountScopeID, op.WorkspaceID, dep.ID, dep.Status, environments.HealthStatusUnhealthy, "cleanup failed: "+errMsg)
		}
		return false
	}

	_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
		AccountScopeID:   op.AccountScopeID,
		WorkspaceID:      op.WorkspaceID,
		OperationID:      op.OperationID,
		ExpectedRevision: op.Revision,
		TargetStatus:     environments.OperationStatusCancelled,
		Result: &environments.OperationResult{
			ExitCode:     130,
			ErrorMessage: "operation cancelled by user",
			FailureKind:  "cancelled",
			Summary:      "Operation was cancelled and target process terminated",
		},
		ObservedAt: now,
	})
	return true
}

// finalizeTimeout handles deadline expiry and target cleanup.
func (m *DeploymentManager) finalizeTimeout(
	op environments.EnvironmentOperation,
	res *environments.OperationResult,
	execErr error,
	conn *environments.Connection,
	dep *environments.Deployment,
) bool {
	cleanupTimeout := m.cleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = provider.CleanupTimeout
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cleanupCancel()

	terminated, cleanupErr := m.cleanupTargetProcess(cleanupCtx, op, conn, dep)

	now := time.Now().UnixMilli()
	latestOp, found, err := m.operations.Get(op.AccountScopeID, op.WorkspaceID, op.OperationID)
	if err == nil && found {
		op = latestOp
	}

	if cleanupErr != nil || !terminated {
		targetStatus := environments.OperationStatusCleanupFailed
		if errors.Is(cleanupErr, provider.ErrOperationNotConfirmed) || errors.Is(cleanupErr, context.DeadlineExceeded) {
			targetStatus = environments.OperationStatusUnknown
		}
		errMsg := "timeout cleanup failed"
		if cleanupErr != nil {
			errMsg = cleanupErr.Error()
		}
		_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
			AccountScopeID:   op.AccountScopeID,
			WorkspaceID:      op.WorkspaceID,
			OperationID:      op.OperationID,
			ExpectedRevision: op.Revision,
			TargetStatus:     targetStatus,
			Result: &environments.OperationResult{
				ExitCode:     124,
				ErrorMessage: boundedString(errMsg, 2048),
				FailureKind:  string(targetStatus),
				Summary:      "Operation timed out and target cleanup failed; deployment blocked against reuse",
			},
			ObservedAt: now,
		})
		if dep != nil {
			_, _ = m.deployments.UpdateStatus(op.AccountScopeID, op.WorkspaceID, dep.ID, dep.Status, environments.HealthStatusUnhealthy, "timeout cleanup failed: "+errMsg)
		}
		return false
	}

	_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
		AccountScopeID:   op.AccountScopeID,
		WorkspaceID:      op.WorkspaceID,
		OperationID:      op.OperationID,
		ExpectedRevision: op.Revision,
		TargetStatus:     environments.OperationStatusTimedOut,
		Result: &environments.OperationResult{
			ExitCode:     124,
			ErrorMessage: "operation exceeded deadline",
			FailureKind:  "timed_out",
			Summary:      "Operation exceeded maximum allotted deadline",
		},
		ObservedAt: now,
	})
	return true
}

// cleanupTargetProcess terminates the owned container process tree via provider.OperationCanceler.
// Fails closed for uncertainty and handles all action kinds (exec, deploy, ensure, start, stop, destroy) appropriately.
func (m *DeploymentManager) cleanupTargetProcess(
	ctx context.Context,
	op environments.EnvironmentOperation,
	conn *environments.Connection,
	dep *environments.Deployment,
) (bool, error) {
	if (conn == nil || dep == nil) && m.connections != nil && m.deployments != nil && op.DeploymentID != "" {
		d, found, _ := m.deployments.Get(op.AccountScopeID, op.WorkspaceID, op.DeploymentID)
		if found {
			dep = &d
			c, foundConn, _ := m.connections.Get(op.AccountScopeID, op.WorkspaceID, dep.ConnectionID)
			if foundConn {
				conn = &c
			}
		}
	}

	// Fail closed if deployment was specified but cannot be resolved
	if op.DeploymentID != "" && (dep == nil || conn == nil) {
		return false, errors.New("cannot resolve target deployment or connection for cleanup")
	}

	if conn == nil || dep == nil || m.registry == nil {
		if op.DeploymentID != "" {
			return false, errors.New("provider registry or targets unavailable")
		}
		return true, nil
	}

	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return false, ErrProviderNotRegistered
	}

	switch op.Action {
	case environments.OperationActionExec:
		canceler, ok := prov.(provider.OperationCanceler)
		if !ok {
			return false, provider.ErrOperationNotConfirmed
		}
		res, err := canceler.CancelExec(ctx, conn, dep, provider.CancelExecRequest{
			OperationID: op.OperationID,
			GracePeriod: 2 * time.Second,
		})
		if err != nil {
			return false, err
		}
		if res == nil || !res.Terminated {
			return false, provider.ErrOperationCleanupFailed
		}
		return true, nil

	case environments.OperationActionDeploy, environments.OperationActionEnsure:
		if err := prov.Destroy(ctx, conn, dep); err != nil {
			return false, fmt.Errorf("destroy partial deployment: %w", err)
		}
		_, _ = m.deployments.UpdateStatus(op.AccountScopeID, op.WorkspaceID, dep.ID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "cancelled during deploy")
		return true, nil

	case "start", environments.OperationActionStop:
		if err := prov.Stop(ctx, conn, dep); err != nil {
			return false, fmt.Errorf("stop container during cleanup: %w", err)
		}
		_, _ = m.deployments.UpdateStatus(op.AccountScopeID, op.WorkspaceID, dep.ID, environments.DeploymentStatusStopped, environments.HealthStatusUnknown, "stopped during cleanup")
		return true, nil

	case "destroy":
		if err := prov.Destroy(ctx, conn, dep); err != nil {
			return false, fmt.Errorf("destroy container during cleanup: %w", err)
		}
		_, _ = m.deployments.UpdateStatus(op.AccountScopeID, op.WorkspaceID, dep.ID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "destroyed during cleanup")
		return true, nil

	default:
		return true, nil
	}
}

// Cancel initiates cancellation for a specific operation, acknowledging promptly and cleaning up within 15s.
func (m *DeploymentManager) Cancel(ctx context.Context, req CancelOperationRequest) (*environments.EnvironmentOperation, error) {
	if m == nil || m.operations == nil {
		return nil, errors.New("operation store is not configured")
	}
	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	if req.AccountScopeID == "" || req.WorkspaceID == "" || req.OperationID == "" {
		return nil, errors.New("account scope id, workspace id, and operation id are required")
	}

	op, found, err := m.operations.Get(req.AccountScopeID, req.WorkspaceID, req.OperationID)
	if err != nil {
		return nil, fmt.Errorf("get operation %q: %w", req.OperationID, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", environments.ErrOperationNotFound, req.OperationID)
	}
	if op.IsTerminal() {
		return &op, nil
	}
	if op.Status == environments.OperationStatusCancelling {
		return &op, nil
	}

	now := time.Now().UnixMilli()
	reason := req.Reason
	if reason == "" {
		reason = "cancelled by user"
	}

	// Promptly acknowledge with durable CAS transition to cancelling
	cancellingOp, err := m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
		AccountScopeID:   req.AccountScopeID,
		WorkspaceID:      req.WorkspaceID,
		OperationID:      req.OperationID,
		ExpectedRevision: op.Revision,
		TargetStatus:     environments.OperationStatusCancelling,
		Activity: &environments.OperationActivity{
			Description:    "Cancellation requested: " + reason,
			Phase:          "cancelling",
			LastObservedAt: now,
		},
		ObservedAt: now,
	})
	if err != nil {
		if cur, foundCur, gErr := m.operations.Get(req.AccountScopeID, req.WorkspaceID, req.OperationID); gErr == nil && foundCur {
			if cur.Status == environments.OperationStatusCancelling || cur.IsTerminal() {
				return &cur, nil
			}
		}
		return nil, fmt.Errorf("transition to cancelling: %w", err)
	}

	// Signal in-memory execution if running
	m.activeOpsMu.Lock()
	state, hasState := m.activeOps[req.OperationID]
	if hasState {
		state.mu.Lock()
		state.isCancelled = true
		state.mu.Unlock()
		state.cancel()
	}
	m.activeOpsMu.Unlock()

	// If not executing in this process (e.g. orphaned from restart), run standalone cleanup
	if !hasState {
		go func() {
			m.finalizeCancellation(cancellingOp, nil, errors.New("cancelled"), nil, nil)
		}()
	}

	return &cancellingOp, nil
}

// CancelOperation is an alias for Cancel.
func (m *DeploymentManager) CancelOperation(ctx context.Context, req CancelOperationRequest) (*environments.EnvironmentOperation, error) {
	return m.Cancel(ctx, req)
}

// CancelOwner cancels all active or queued operations matching the given session, worker, or actor.
func (m *DeploymentManager) CancelOwner(ctx context.Context, req CancelOwnerRequest) (int, error) {
	if m == nil || m.operations == nil {
		return 0, errors.New("operation store is not configured")
	}
	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	if req.AccountScopeID == "" || req.WorkspaceID == "" {
		return 0, errors.New("account scope id and workspace id are required")
	}

	targetOpIDs := make(map[string]bool)

	// Scan in-memory active operations
	m.activeOpsMu.RLock()
	for opID, st := range m.activeOps {
		if st.accountID == req.AccountScopeID && st.workspaceID == req.WorkspaceID {
			targetOpIDs[opID] = true
		}
	}
	m.activeOpsMu.RUnlock()

	// Scan store for queued, running, cancelling, and unresolved operations
	for _, st := range []environments.OperationStatus{
		environments.OperationStatusRunning,
		environments.OperationStatusQueued,
		environments.OperationStatusCancelling,
		environments.OperationStatusCleanupFailed,
		environments.OperationStatusUnknown,
	} {
		cursor := ""
		for pageIdx := 0; pageIdx < 10; pageIdx++ {
			page, err := m.operations.QueryHistory(environments.OperationHistoryQuery{
				AccountScopeID: req.AccountScopeID,
				WorkspaceID:    req.WorkspaceID,
				SessionID:      req.SessionID,
				WorkerID:       req.WorkerID,
				Actor:          req.Actor,
				Status:         st,
				Limit:          100,
				Cursor:         cursor,
			})
			if err != nil {
				break
			}
			for _, op := range page.Operations {
				targetOpIDs[op.OperationID] = true
			}
			if !page.HasMore || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}

	cancelledCount := 0
	var firstErr error
	for opID := range targetOpIDs {
		op, found, err := m.operations.Get(req.AccountScopeID, req.WorkspaceID, opID)
		if err != nil || !found || op.IsTerminal() || op.Status == environments.OperationStatusCancelling {
			continue
		}

		if req.SessionID != "" && op.Attribution.SessionID != req.SessionID {
			continue
		}
		if req.RunID != "" && op.Attribution.RunID != req.RunID {
			continue
		}
		if req.WorkerID != "" && op.Attribution.WorkerID != req.WorkerID {
			continue
		}
		if req.Actor != "" && op.Attribution.Actor != req.Actor {
			continue
		}
		if req.ConsumerID != "" && (op.Attribution.SessionID != req.ConsumerID && op.Attribution.WorkerID != req.ConsumerID && op.LeaseID != req.ConsumerID) {
			continue
		}

		_, cErr := m.Cancel(ctx, CancelOperationRequest{
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			OperationID:    opID,
			Reason:         firstNonEmpty(req.Reason, "cancelled by owner stop"),
		})
		if cErr == nil {
			cancelledCount++
		} else if firstErr == nil {
			firstErr = cErr
		}
	}

	return cancelledCount, firstErr
}

// Recover reconciles non-terminal operations on daemon startup without replaying commands.
func (m *DeploymentManager) Recover(ctx context.Context) error {
	if m == nil || m.operations == nil {
		return errors.New("operation store is not configured")
	}

	nonTerminalOps, err := m.operations.ListNonTerminalOperations(1000)
	if err != nil {
		return fmt.Errorf("list non-terminal operations for recovery: %w", err)
	}

	cleanupTimeout := m.cleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = provider.CleanupTimeout
	}

	workspaces := make(map[string][2]string)
	for _, op := range nonTerminalOps {
		workspaces[op.AccountScopeID+"/"+op.WorkspaceID] = [2]string{op.AccountScopeID, op.WorkspaceID}
		now := time.Now().UnixMilli()

		if op.Status == environments.OperationStatusQueued {
			// Queued operations before restart: discard without replay
			_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
				AccountScopeID:   op.AccountScopeID,
				WorkspaceID:      op.WorkspaceID,
				OperationID:      op.OperationID,
				ExpectedRevision: op.Revision,
				TargetStatus:     environments.OperationStatusFailed,
				Result: &environments.OperationResult{
					ExitCode:     1,
					ErrorMessage: "daemon restarted before operation commenced",
					FailureKind:  "daemon_restart",
					Summary:      "Discarded queued operation on restart without replay",
				},
				ObservedAt: now,
			})
		} else {
			// Running or Cancelling: attempt target process cleanup
			cleanupCtx, cancel := context.WithTimeout(ctx, cleanupTimeout)
			terminated, cErr := m.cleanupTargetProcess(cleanupCtx, op, nil, nil)
			cancel()

			targetStatus := environments.OperationStatusCancelled
			errMsg := "daemon restarted; operation cancelled and process terminated"
			failureKind := "cancelled"
			if cErr != nil || !terminated {
				targetStatus = environments.OperationStatusUnknown
				failureKind = "unknown"
				errMsg = "daemon restarted; target cleanup outcome unconfirmed"
			}

			_, _ = m.operations.TransitionOperation(pebblestore.OperationTransitionInput{
				AccountScopeID:   op.AccountScopeID,
				WorkspaceID:      op.WorkspaceID,
				OperationID:      op.OperationID,
				ExpectedRevision: op.Revision,
				TargetStatus:     targetStatus,
				Result: &environments.OperationResult{
					ExitCode:     130,
					ErrorMessage: errMsg,
					FailureKind:  failureKind,
					Summary:      "Reconciled on daemon restart without command replay",
				},
				ObservedAt: now,
			})
		}
	}

	for _, pair := range workspaces {
		_, _ = m.operations.RecalculateSummary(pair[0], pair[1])
	}
	return nil
}

// Close gracefully terminates supervisor operations on daemon stop.
func (m *DeploymentManager) Close() error {
	if m == nil {
		return nil
	}
	m.rootCancel()

	m.activeOpsMu.RLock()
	doneChans := make([]chan struct{}, 0, len(m.activeOps))
	for _, st := range m.activeOps {
		doneChans = append(doneChans, st.done)
	}
	m.activeOpsMu.RUnlock()

	cleanupTimeout := m.cleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = provider.CleanupTimeout
	}
	deadline := time.After(cleanupTimeout)
	for _, ch := range doneChans {
		select {
		case <-ch:
		case <-deadline:
			return nil
		}
	}
	return nil
}

// Get retrieves an operation by ID (side-effect free).
func (m *DeploymentManager) Get(ctx context.Context, accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error) {
	if m == nil || m.operations == nil {
		return environments.EnvironmentOperation{}, false, errors.New("operation store is not configured")
	}
	return m.operations.Get(accountScopeID, workspaceID, operationID)
}

// GetOperation is an alias for Get.
func (m *DeploymentManager) GetOperation(accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error) {
	return m.Get(context.Background(), accountScopeID, workspaceID, operationID)
}

// History queries historical operations with cursor pagination and daily totals (side-effect free).
func (m *DeploymentManager) History(ctx context.Context, q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error) {
	if m == nil || m.operations == nil {
		return environments.OperationHistoryPage{}, errors.New("operation store is not configured")
	}
	return m.operations.QueryHistory(q)
}

// QueryHistory is an alias for History.
func (m *DeploymentManager) QueryHistory(ctx context.Context, q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error) {
	return m.History(ctx, q)
}

// Summary returns authoritative deployment and operation counts (side-effect free).
func (m *DeploymentManager) Summary(ctx context.Context, accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	if m == nil || m.operations == nil {
		return environments.EnvironmentSummary{}, errors.New("operation store is not configured")
	}
	return m.operations.GetSummary(accountScopeID, workspaceID)
}

// GetSummary is an alias for Summary.
func (m *DeploymentManager) GetSummary(ctx context.Context, accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	return m.Summary(ctx, accountScopeID, workspaceID)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		t := strings.TrimSpace(v)
		if t != "" {
			return t
		}
	}
	return ""
}

func boundedString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		if maxLen > 3 {
			return s[:maxLen-3] + "..."
		}
		return s[:maxLen]
	}
	return s
}

func sanitizeOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if len(trimmed) > 1024 {
		return trimmed[:1024] + "... [truncated]"
	}
	return trimmed
}
