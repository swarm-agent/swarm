package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"

	"swarm-refactor/swarmtui/pkg/environments"
)

const (
	defaultOperationDeadlineMs = 5 * 60 * 1000  // 5 minutes default
	maxOperationDeadlineMs     = 10 * 60 * 1000 // 10 minutes max
	maxHistoryScanKeys         = 20000          // bound history scans explicitly
	maxWorkspaceActiveOps      = 100            // bound concurrent admitted operations
)

// OperationTransitionInput specifies CAS transition parameters for an environment operation.
type OperationTransitionInput struct {
	AccountScopeID   string
	WorkspaceID      string
	OperationID      string
	ExpectedRevision uint64
	TargetStatus     environments.OperationStatus
	Activity         *environments.OperationActivity
	Result           *environments.OperationResult
	ObservedAt       int64
}

// EnvironmentOperationStore manages durable persistence, typed admission, CAS transitions,
// and historical aggregation for EnvironmentOperation records in Pebble.
// Strictly scoped to AccountScopeID and WorkspaceID.
type EnvironmentOperationStore struct {
	store *Store
}

// NewEnvironmentOperationStore creates a new EnvironmentOperationStore backed by the given Pebble Store.
func NewEnvironmentOperationStore(store *Store) *EnvironmentOperationStore {
	return &EnvironmentOperationStore{store: store}
}

// Get retrieves an EnvironmentOperation by its account scope, workspace id, and operation id.
func (s *EnvironmentOperationStore) Get(accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentOperation{}, false, errors.New("environment operation store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	operationID = strings.TrimSpace(operationID)
	if accountScopeID == "" || workspaceID == "" || operationID == "" {
		return environments.EnvironmentOperation{}, false, errors.New("account scope id, workspace id, and operation id are required")
	}

	key := KeyEnvironmentOperationForAccount(accountScopeID, workspaceID, operationID)
	var op environments.EnvironmentOperation
	ok, err := s.store.GetJSON(key, &op)
	if err != nil || !ok {
		return environments.EnvironmentOperation{}, ok, err
	}
	if op.AccountScopeID != accountScopeID || op.WorkspaceID != workspaceID {
		return environments.EnvironmentOperation{}, false, nil
	}
	return op, true, nil
}

// GetActiveOperationForDeployment retrieves the currently active or unresolved operation for a deployment.
func (s *EnvironmentOperationStore) GetActiveOperationForDeployment(accountScopeID, workspaceID, deploymentID string) (environments.EnvironmentOperation, bool, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentOperation{}, false, errors.New("environment operation store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return environments.EnvironmentOperation{}, false, errors.New("account scope id, workspace id, and deployment id are required")
	}

	activeKey := KeyEnvironmentActiveOpForAccount(accountScopeID, workspaceID, deploymentID)
	opIDBytes, ok, err := s.store.GetBytes(activeKey)
	if err != nil || !ok {
		return environments.EnvironmentOperation{}, false, err
	}

	opID := string(opIDBytes)
	op, found, err := s.Get(accountScopeID, workspaceID, opID)
	if err != nil || !found {
		return environments.EnvironmentOperation{}, false, err
	}
	if op.IsActive() || op.IsUnresolved() {
		return op, true, nil
	}
	return environments.EnvironmentOperation{}, false, nil
}

// AdmitOperation atomically admits a new operation, enforcing idempotency, per-deployment concurrency guards,
// unresolved cleanup/unknown guards, capacity limits, and outbox integration.
func (s *EnvironmentOperationStore) AdmitOperation(op environments.EnvironmentOperation) (environments.EnvironmentOperation, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentOperation{}, errors.New("environment operation store is not configured")
	}
	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()

	now := time.Now().UnixMilli()
	op.AccountScopeID = strings.TrimSpace(op.AccountScopeID)
	op.WorkspaceID = strings.TrimSpace(op.WorkspaceID)
	op.Action = strings.TrimSpace(op.Action)
	op.EnvironmentID = strings.TrimSpace(op.EnvironmentID)
	op.DeploymentID = strings.TrimSpace(op.DeploymentID)
	op.LeaseID = strings.TrimSpace(op.LeaseID)
	op.IdempotencyKey = strings.TrimSpace(op.IdempotencyKey)
	op.OperationID = strings.TrimSpace(op.OperationID)

	if op.CreatedAt <= 0 {
		op.CreatedAt = now
	}
	if op.Deadline <= 0 {
		op.Deadline = op.CreatedAt + defaultOperationDeadlineMs
	} else if op.Deadline-op.CreatedAt > maxOperationDeadlineMs {
		op.Deadline = op.CreatedAt + maxOperationDeadlineMs
	}
	if op.OperationID == "" {
		op.OperationID = "op_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if op.Status == "" {
		op.Status = environments.OperationStatusQueued
	}
	if op.Revision <= 0 {
		op.Revision = 1
	}
	if op.ObservedAt <= 0 {
		op.ObservedAt = now
	}

	if err := op.Validate(); err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("validate operation: %w", err)
	}

	// 1. Idempotency Key check
	if op.IdempotencyKey != "" {
		idempKey := KeyEnvironmentOpIdempotencyForAccount(op.AccountScopeID, op.WorkspaceID, op.IdempotencyKey)
		if existingIDBytes, ok, err := s.store.GetBytes(idempKey); err != nil {
			return environments.EnvironmentOperation{}, fmt.Errorf("check idempotency key: %w", err)
		} else if ok {
			existingID := string(existingIDBytes)
			existing, found, err := s.Get(op.AccountScopeID, op.WorkspaceID, existingID)
			if err != nil {
				return environments.EnvironmentOperation{}, fmt.Errorf("read idempotent operation: %w", err)
			}
			if found {
				if existing.Action == op.Action && existing.DeploymentID == op.DeploymentID && existing.EnvironmentID == op.EnvironmentID {
					return existing, nil
				}
				return environments.EnvironmentOperation{}, fmt.Errorf("%w: idempotency key %q reused with different action (%s vs %s) or target", environments.ErrIdempotencyConflict, op.IdempotencyKey, op.Action, existing.Action)
			}
		}
	}

	// 2. OperationID check
	if existing, found, err := s.Get(op.AccountScopeID, op.WorkspaceID, op.OperationID); err != nil {
		return environments.EnvironmentOperation{}, err
	} else if found {
		if (op.IdempotencyKey != "" && existing.IdempotencyKey == op.IdempotencyKey) || (existing.Action == op.Action && existing.DeploymentID == op.DeploymentID && existing.EnvironmentID == op.EnvironmentID) {
			return existing, nil
		}
		return environments.EnvironmentOperation{}, fmt.Errorf("%w: operation ID %q already exists", environments.ErrOperationConflict, op.OperationID)
	}

	// 3. Deployment conflicting execution and unresolved cleanup/unknown guards
	if op.DeploymentID != "" {
		activeOp, hasActive, err := s.GetActiveOperationForDeployment(op.AccountScopeID, op.WorkspaceID, op.DeploymentID)
		if err != nil {
			return environments.EnvironmentOperation{}, err
		}
		if hasActive {
			if activeOp.IsActive() {
				return environments.EnvironmentOperation{}, fmt.Errorf("%w: deployment %q has active operation %q in state %q", environments.ErrDeploymentOperationConflict, op.DeploymentID, activeOp.OperationID, activeOp.Status)
			}
			if activeOp.IsUnresolved() {
				return environments.EnvironmentOperation{}, fmt.Errorf("%w: deployment %q has unresolved operation %q in state %q; resolution required before reuse", environments.ErrDeploymentOperationBlocked, op.DeploymentID, activeOp.OperationID, activeOp.Status)
			}
		}
	}

	// 4. Capacity check: reject over-capacity explicitly
	summary, err := s.getSummaryLocked(op.AccountScopeID, op.WorkspaceID)
	if err != nil {
		return environments.EnvironmentOperation{}, err
	}
	if summary.RunningOps+summary.QueuedOps >= maxWorkspaceActiveOps {
		return environments.EnvironmentOperation{}, fmt.Errorf("%w: active operations limit (%d) reached for workspace", environments.ErrCapacityExceeded, maxWorkspaceActiveOps)
	}

	// 5. Prepare atomic batch mutation
	opRaw, err := json.Marshal(op)
	if err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("marshal operation: %w", err)
	}

	mutation := &environmentRealtimeMutation{
		accountScopeID: op.AccountScopeID,
		workspaceID:    op.WorkspaceID,
		writes:         make(map[string][]byte),
	}

	opKey := KeyEnvironmentOperationForAccount(op.AccountScopeID, op.WorkspaceID, op.OperationID)
	mutation.putBytes(opKey, opRaw)

	histKey := KeyEnvironmentOpHistoryForAccount(op.AccountScopeID, op.WorkspaceID, op.CreatedAt, op.OperationID)
	mutation.putBytes(histKey, opRaw)

	if op.DeploymentID != "" {
		activeKey := KeyEnvironmentActiveOpForAccount(op.AccountScopeID, op.WorkspaceID, op.DeploymentID)
		mutation.putBytes(activeKey, []byte(op.OperationID))
	}

	if op.IdempotencyKey != "" {
		idempKey := KeyEnvironmentOpIdempotencyForAccount(op.AccountScopeID, op.WorkspaceID, op.IdempotencyKey)
		mutation.putBytes(idempKey, []byte(op.OperationID))
	}

	// Update summary counts
	summary.Revision++
	summary.UpdatedAt = now
	summary.TotalOps++
	switch op.Status {
	case environments.OperationStatusQueued:
		summary.QueuedOps++
	case environments.OperationStatusRunning:
		summary.RunningOps++
		if op.Action == environments.OperationActionExec {
			summary.RunningExecOps++
		}
	}

	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("marshal summary: %w", err)
	}
	summaryKey := KeyEnvironmentSummaryForAccount(op.AccountScopeID, op.WorkspaceID)
	mutation.putBytes(summaryKey, summaryRaw)

	metaPayload, _ := json.Marshal(map[string]any{
		"account_scope_id": op.AccountScopeID,
		"workspace_id":    op.WorkspaceID,
		"summary_revision": summary.Revision,
	})
	mutation.eventPayload = metaPayload

	if err := s.store.commitEnvironmentRealtime(mutation); err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("commit admit operation: %w", err)
	}

	defer s.store.publishEnvironmentRealtime(mutation)
	return op, nil
}

// TransitionOperation atomically executes a revision-guarded CAS transition on an operation.
// Strictly guards terminal states (rejecting late success/completion after timeout or cancel).
func (s *EnvironmentOperationStore) TransitionOperation(input OperationTransitionInput) (environments.EnvironmentOperation, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentOperation{}, errors.New("environment operation store is not configured")
	}
	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()

	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if input.AccountScopeID == "" || input.WorkspaceID == "" || input.OperationID == "" {
		return environments.EnvironmentOperation{}, errors.New("account scope id, workspace id, and operation id are required")
	}

	op, found, err := s.Get(input.AccountScopeID, input.WorkspaceID, input.OperationID)
	if err != nil {
		return environments.EnvironmentOperation{}, err
	}
	if !found {
		return environments.EnvironmentOperation{}, fmt.Errorf("%w: %q", environments.ErrOperationNotFound, input.OperationID)
	}

	// Guard terminal state (CAS late-success rejection)
	if op.IsTerminal() {
		return environments.EnvironmentOperation{}, fmt.Errorf("%w: operation %q is already terminal with status %q", environments.ErrOperationTerminal, op.OperationID, op.Status)
	}

	// Revision CAS check
	if input.ExpectedRevision > 0 && op.Revision != input.ExpectedRevision {
		return environments.EnvironmentOperation{}, fmt.Errorf("%w: expected revision %d, current revision %d", environments.ErrOperationConflict, input.ExpectedRevision, op.Revision)
	}

	prevStatus := op.Status
	targetStatus := input.TargetStatus
	if targetStatus == "" {
		targetStatus = op.Status
	}

	if targetStatus != prevStatus {
		if !isValidStatusTransition(prevStatus, targetStatus) {
			return environments.EnvironmentOperation{}, fmt.Errorf("%w: cannot transition from %q to %q", environments.ErrInvalidStateTransition, prevStatus, targetStatus)
		}
	}

	now := time.Now().UnixMilli()
	op.Status = targetStatus
	op.Revision++
	if input.ObservedAt > 0 {
		op.ObservedAt = input.ObservedAt
	} else {
		op.ObservedAt = now
	}
	if targetStatus == environments.OperationStatusRunning && op.StartedAt <= 0 {
		op.StartedAt = now
	}
	if targetStatus.IsTerminal() || targetStatus == environments.OperationStatusCleanupFailed {
		if op.CompletedAt <= 0 {
			op.CompletedAt = now
		}
	}
	if input.Activity != nil {
		op.Activity = *input.Activity
	}
	if input.Result != nil {
		op.Result = *input.Result
	}

	if err := op.Validate(); err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("validate transition: %w", err)
	}

	mutation := &environmentRealtimeMutation{
		accountScopeID: op.AccountScopeID,
		workspaceID:    op.WorkspaceID,
		writes:         make(map[string][]byte),
	}

	opRaw, err := json.Marshal(op)
	if err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("marshal transition: %w", err)
	}

	opKey := KeyEnvironmentOperationForAccount(op.AccountScopeID, op.WorkspaceID, op.OperationID)
	mutation.putBytes(opKey, opRaw)

	histKey := KeyEnvironmentOpHistoryForAccount(op.AccountScopeID, op.WorkspaceID, op.CreatedAt, op.OperationID)
	mutation.putBytes(histKey, opRaw)

	// Active deployment pointer handling
	if op.DeploymentID != "" {
		activeKey := KeyEnvironmentActiveOpForAccount(op.AccountScopeID, op.WorkspaceID, op.DeploymentID)
		if op.IsTerminal() {
			// Operation completed; release the active deployment pointer if pointing to this operation
			activeBytes, ok, _ := s.store.GetBytes(activeKey)
			if ok && string(activeBytes) == op.OperationID {
				mutation.delete(activeKey)
			}
		} else {
			// In unresolved or active states (running, cancelling, cleanup_failed, unknown), keep active pointer set
			mutation.putBytes(activeKey, []byte(op.OperationID))
		}
	}

	// Update summary
	summary, err := s.getSummaryLocked(op.AccountScopeID, op.WorkspaceID)
	if err != nil {
		return environments.EnvironmentOperation{}, err
	}
	summary.Revision++
	summary.UpdatedAt = now
	if targetStatus != prevStatus {
		decrementSummaryCount(&summary, prevStatus, op.Action)
		incrementSummaryCount(&summary, targetStatus, op.Action)
	}

	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("marshal summary: %w", err)
	}
	summaryKey := KeyEnvironmentSummaryForAccount(op.AccountScopeID, op.WorkspaceID)
	mutation.putBytes(summaryKey, summaryRaw)

	metaPayload, _ := json.Marshal(map[string]any{
		"account_scope_id": op.AccountScopeID,
		"workspace_id":    op.WorkspaceID,
		"summary_revision": summary.Revision,
	})
	mutation.eventPayload = metaPayload

	if err := s.store.commitEnvironmentRealtime(mutation); err != nil {
		return environments.EnvironmentOperation{}, fmt.Errorf("commit transition: %w", err)
	}

	defer s.store.publishEnvironmentRealtime(mutation)
	return op, nil
}

func isValidStatusTransition(from, to environments.OperationStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case environments.OperationStatusQueued:
		return to == environments.OperationStatusRunning ||
			to == environments.OperationStatusCancelling ||
			to == environments.OperationStatusFailed ||
			to == environments.OperationStatusCancelled ||
			to == environments.OperationStatusTimedOut ||
			to == environments.OperationStatusUnknown
	case environments.OperationStatusRunning:
		return to == environments.OperationStatusCancelling ||
			to == environments.OperationStatusSucceeded ||
			to == environments.OperationStatusFailed ||
			to == environments.OperationStatusTimedOut ||
			to == environments.OperationStatusCleanupFailed ||
			to == environments.OperationStatusUnknown
	case environments.OperationStatusCancelling:
		return to == environments.OperationStatusCancelled ||
			to == environments.OperationStatusFailed ||
			to == environments.OperationStatusCleanupFailed ||
			to == environments.OperationStatusTimedOut ||
			to == environments.OperationStatusUnknown
	case environments.OperationStatusCleanupFailed:
		return to == environments.OperationStatusCancelled ||
			to == environments.OperationStatusFailed ||
			to == environments.OperationStatusSucceeded
	case environments.OperationStatusUnknown:
		return to == environments.OperationStatusRunning ||
			to == environments.OperationStatusSucceeded ||
			to == environments.OperationStatusFailed ||
			to == environments.OperationStatusCancelled ||
			to == environments.OperationStatusCleanupFailed
	default:
		return false
	}
}

func incrementSummaryCount(s *environments.EnvironmentSummary, status environments.OperationStatus, action string) {
	switch status {
	case environments.OperationStatusQueued:
		s.QueuedOps++
	case environments.OperationStatusRunning:
		s.RunningOps++
		if action == environments.OperationActionExec {
			s.RunningExecOps++
		}
	case environments.OperationStatusCancelling:
		s.CancellingOps++
	case environments.OperationStatusFailed:
		s.FailedOps++
	case environments.OperationStatusCleanupFailed:
		s.CleanupFailedOps++
	case environments.OperationStatusUnknown:
		s.UnknownOps++
	case environments.OperationStatusSucceeded:
		s.SucceededOps++
	case environments.OperationStatusCancelled:
		s.CancelledOps++
	case environments.OperationStatusTimedOut:
		s.TimedOutOps++
	}
}

func decrementSummaryCount(s *environments.EnvironmentSummary, status environments.OperationStatus, action string) {
	switch status {
	case environments.OperationStatusQueued:
		if s.QueuedOps > 0 {
			s.QueuedOps--
		}
	case environments.OperationStatusRunning:
		if s.RunningOps > 0 {
			s.RunningOps--
		}
		if action == environments.OperationActionExec && s.RunningExecOps > 0 {
			s.RunningExecOps--
		}
	case environments.OperationStatusCancelling:
		if s.CancellingOps > 0 {
			s.CancellingOps--
		}
	case environments.OperationStatusFailed:
		if s.FailedOps > 0 {
			s.FailedOps--
		}
	case environments.OperationStatusCleanupFailed:
		if s.CleanupFailedOps > 0 {
			s.CleanupFailedOps--
		}
	case environments.OperationStatusUnknown:
		if s.UnknownOps > 0 {
			s.UnknownOps--
		}
	case environments.OperationStatusSucceeded:
		if s.SucceededOps > 0 {
			s.SucceededOps--
		}
	case environments.OperationStatusCancelled:
		if s.CancelledOps > 0 {
			s.CancelledOps--
		}
	case environments.OperationStatusTimedOut:
		if s.TimedOutOps > 0 {
			s.TimedOutOps--
		}
	}
}

// GetSummary retrieves the current EnvironmentSummary for an account scope and workspace.
func (s *EnvironmentOperationStore) GetSummary(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentSummary{}, errors.New("environment operation store is not configured")
	}
	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()
	return s.getSummaryLocked(accountScopeID, workspaceID)
}

func (s *EnvironmentOperationStore) getSummaryLocked(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return environments.EnvironmentSummary{}, errors.New("account scope id and workspace id are required")
	}

	key := KeyEnvironmentSummaryForAccount(accountScopeID, workspaceID)
	var summary environments.EnvironmentSummary
	ok, err := s.store.GetJSON(key, &summary)
	if err != nil {
		return environments.EnvironmentSummary{}, err
	}
	if ok && summary.AccountScopeID == accountScopeID && summary.WorkspaceID == workspaceID {
		return summary, nil
	}

	return s.recalculateSummaryLocked(accountScopeID, workspaceID)
}

// RecalculateSummary rebuilds the EnvironmentSummary from durable records.
func (s *EnvironmentOperationStore) RecalculateSummary(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	if s == nil || s.store == nil {
		return environments.EnvironmentSummary{}, errors.New("environment operation store is not configured")
	}
	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()
	return s.recalculateSummaryLocked(accountScopeID, workspaceID)
}

func (s *EnvironmentOperationStore) recalculateSummaryLocked(accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	now := time.Now().UnixMilli()

	var currentRevision uint64 = 0
	key := KeyEnvironmentSummaryForAccount(accountScopeID, workspaceID)
	var existing environments.EnvironmentSummary
	if ok, _ := s.store.GetJSON(key, &existing); ok {
		currentRevision = existing.Revision
	}

	summary := environments.EnvironmentSummary{
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Revision:       currentRevision + 1,
		UpdatedAt:      now,
	}

	// 1. Count active deployments
	depPrefix := DeploymentPrefixForAccount(accountScopeID, workspaceID)
	_ = s.store.IteratePrefix(depPrefix, 10000, func(_ string, value []byte) error {
		var dep environments.Deployment
		if err := json.Unmarshal(value, &dep); err == nil {
			if dep.AccountScopeID == accountScopeID && dep.WorkspaceID == workspaceID && dep.IsActive() {
				summary.ActiveDeployments++
			}
		}
		return nil
	})

	// 2. Count operations
	opPrefix := EnvironmentOperationPrefixForAccount(accountScopeID, workspaceID)
	_ = s.store.IteratePrefix(opPrefix, 50000, func(_ string, value []byte) error {
		var op environments.EnvironmentOperation
		if err := json.Unmarshal(value, &op); err == nil {
			if op.AccountScopeID == accountScopeID && op.WorkspaceID == workspaceID {
				summary.TotalOps++
				incrementSummaryCount(&summary, op.Status, op.Action)
			}
		}
		return nil
	})

	if err := s.store.PutJSON(key, summary); err != nil {
		return environments.EnvironmentSummary{}, err
	}

	return summary, nil
}

// UpdateDeploymentCount updates the active deployments count in the summary and publishes environment.updated.
func (s *EnvironmentOperationStore) UpdateDeploymentCount(accountScopeID, workspaceID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil
	}

	activeCount := 0
	depPrefix := DeploymentPrefixForAccount(accountScopeID, workspaceID)
	_ = s.store.IteratePrefix(depPrefix, 10000, func(_ string, value []byte) error {
		var dep environments.Deployment
		if err := json.Unmarshal(value, &dep); err == nil {
			if dep.AccountScopeID == accountScopeID && dep.WorkspaceID == workspaceID && dep.IsActive() {
				activeCount++
			}
		}
		return nil
	})

	summary, err := s.getSummaryLocked(accountScopeID, workspaceID)
	if err != nil {
		return err
	}

	if summary.ActiveDeployments == activeCount {
		return nil
	}

	summary.ActiveDeployments = activeCount
	summary.Revision++
	summary.UpdatedAt = time.Now().UnixMilli()

	mutation := &environmentRealtimeMutation{
		accountScopeID: accountScopeID,
		workspaceID:    workspaceID,
		writes:         make(map[string][]byte),
	}

	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	summaryKey := KeyEnvironmentSummaryForAccount(accountScopeID, workspaceID)
	mutation.putBytes(summaryKey, summaryRaw)

	metaPayload, _ := json.Marshal(map[string]any{
		"account_scope_id": accountScopeID,
		"workspace_id":    workspaceID,
		"summary_revision": summary.Revision,
	})
	mutation.eventPayload = metaPayload

	if err := s.store.commitEnvironmentRealtime(mutation); err != nil {
		return err
	}

	defer s.store.publishEnvironmentRealtime(mutation)
	return nil
}

type environmentOpHistoryCursor struct {
	Version        int    `json:"v"`
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID    string `json:"workspace_id"`
	EnvironmentID  string `json:"environment_id,omitempty"`
	DeploymentID   string `json:"deployment_id,omitempty"`
	Actor          string `json:"actor,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Action         string `json:"action,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	StartDate      string `json:"start_date,omitempty"`
	EndDate        string `json:"end_date,omitempty"`
	Key            string `json:"key"`
	SummaryRev     uint64 `json:"summary_rev"`
}

// QueryHistory queries historical operations and aggregates full daily totals across date bounds.
// Daily terminal counts are derived from full durable history, never the visible page.
func (s *EnvironmentOperationStore) QueryHistory(q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error) {
	if s == nil || s.store == nil {
		return environments.OperationHistoryPage{}, errors.New("environment operation store is not configured")
	}

	q.AccountScopeID = strings.TrimSpace(q.AccountScopeID)
	q.WorkspaceID = strings.TrimSpace(q.WorkspaceID)
	if q.AccountScopeID == "" || q.WorkspaceID == "" {
		return environments.OperationHistoryPage{}, errors.New("account scope id and workspace id are required")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}

	tzStr := strings.TrimSpace(q.Timezone)
	if tzStr == "" {
		tzStr = "UTC"
	}
	loc, err := time.LoadLocation(tzStr)
	if err != nil {
		return environments.OperationHistoryPage{}, fmt.Errorf("invalid timezone %q: %w", tzStr, err)
	}

	var startMillis int64 = 0
	var endMillis int64 = 999999999999999999

	q.StartDate = strings.TrimSpace(q.StartDate)
	if q.StartDate != "" {
		startTime, err := time.ParseInLocation("2006-01-02", q.StartDate, loc)
		if err != nil {
			return environments.OperationHistoryPage{}, fmt.Errorf("invalid start_date %q (must be YYYY-MM-DD): %w", q.StartDate, err)
		}
		startMillis = startTime.UnixMilli()
	}

	q.EndDate = strings.TrimSpace(q.EndDate)
	if q.EndDate != "" {
		endTime, err := time.ParseInLocation("2006-01-02", q.EndDate, loc)
		if err != nil {
			return environments.OperationHistoryPage{}, fmt.Errorf("invalid end_date %q (must be YYYY-MM-DD): %w", q.EndDate, err)
		}
		// End date is inclusive: using AddDate(0, 0, 1) properly handles DST
		endMillis = endTime.AddDate(0, 0, 1).UnixMilli() - 1
	}

	if q.StartDate != "" && q.EndDate != "" && startMillis > endMillis {
		return environments.OperationHistoryPage{}, errors.New("start_date cannot be after end_date")
	}

	summary, err := s.GetSummary(q.AccountScopeID, q.WorkspaceID)
	if err != nil {
		return environments.OperationHistoryPage{}, fmt.Errorf("get summary: %w", err)
	}

	var cursorObj *environmentOpHistoryCursor
	if q.Cursor != "" {
		if len(q.Cursor) > 4096 {
			return environments.OperationHistoryPage{}, environments.ErrCursorInvalid
		}
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return environments.OperationHistoryPage{}, fmt.Errorf("%w: invalid base64 encoding", environments.ErrCursorInvalid)
		}
		var c environmentOpHistoryCursor
		if err := json.Unmarshal(data, &c); err != nil {
			return environments.OperationHistoryPage{}, fmt.Errorf("%w: invalid cursor payload", environments.ErrCursorInvalid)
		}
		if c.AccountScopeID != q.AccountScopeID || c.WorkspaceID != q.WorkspaceID ||
			c.EnvironmentID != q.EnvironmentID || c.DeploymentID != q.DeploymentID ||
			c.Actor != q.Actor || c.SessionID != q.SessionID || c.WorkerID != q.WorkerID ||
			c.Status != string(q.Status) || c.Action != q.Action || c.Timezone != tzStr ||
			c.StartDate != q.StartDate || c.EndDate != q.EndDate {
			return environments.OperationHistoryPage{}, fmt.Errorf("%w: cursor parameters do not match query", environments.ErrCursorInvalid)
		}
		if c.SummaryRev > 0 && c.SummaryRev != summary.Revision {
			return environments.OperationHistoryPage{}, fmt.Errorf("%w: workspace environment state changed since cursor was issued (cursor rev %d, current rev %d)", environments.ErrStaleCursor, c.SummaryRev, summary.Revision)
		}
		cursorObj = &c
	}

	historyPrefix := EnvironmentOpHistoryPrefixForAccount(q.AccountScopeID, q.WorkspaceID)
	revStart := reverseMillis(endMillis)
	revEnd := reverseMillis(startMillis)
	lowerBound := []byte(fmt.Sprintf("%s%018d/", historyPrefix, revStart))
	upperBound := []byte(fmt.Sprintf("%s%018d/\xff", historyPrefix, revEnd))

	iter, err := s.store.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return environments.OperationHistoryPage{}, fmt.Errorf("open history iterator: %w", err)
	}
	defer iter.Close()

	page := environments.OperationHistoryPage{
		Operations:  make([]environments.EnvironmentOperation, 0, limit),
		DailyTotals: make([]environments.DailyOperationCounts, 0),
		Summary:     summary,
	}

	dailyMap := make(map[string]*environments.DailyOperationCounts)
	scannedCount := 0
	var lastKey string

	for valid := iter.First(); valid; valid = iter.Next() {
		scannedCount++
		if scannedCount > maxHistoryScanKeys {
			return environments.OperationHistoryPage{}, fmt.Errorf("%w: scanned %d keys in date range; please narrow date range or filter", environments.ErrCapacityExceeded, scannedCount)
		}

		keyStr := string(iter.Key())
		var op environments.EnvironmentOperation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			return environments.OperationHistoryPage{}, fmt.Errorf("unmarshal operation at %s: %w", keyStr, err)
		}

		// Filters
		if q.EnvironmentID != "" && op.EnvironmentID != q.EnvironmentID {
			continue
		}
		if q.DeploymentID != "" && op.DeploymentID != q.DeploymentID {
			continue
		}
		if q.Actor != "" && op.Attribution.Actor != q.Actor {
			continue
		}
		if q.SessionID != "" && op.Attribution.SessionID != q.SessionID {
			continue
		}
		if q.WorkerID != "" && op.Attribution.WorkerID != q.WorkerID {
			continue
		}
		if q.Status != "" && op.Status != q.Status {
			continue
		}
		if q.Action != "" && op.Action != q.Action {
			continue
		}

		// Full daily aggregates across date range
		dateStr := time.UnixMilli(op.CreatedAt).In(loc).Format("2006-01-02")
		daily, exists := dailyMap[dateStr]
		if !exists {
			daily = &environments.DailyOperationCounts{Date: dateStr}
			dailyMap[dateStr] = daily
		}
		daily.TotalOps++
		switch op.Status {
		case environments.OperationStatusSucceeded:
			daily.Succeeded++
		case environments.OperationStatusFailed:
			daily.Failed++
		case environments.OperationStatusCancelled:
			daily.Cancelled++
		case environments.OperationStatusTimedOut:
			daily.TimedOut++
		case environments.OperationStatusCleanupFailed:
			daily.CleanupFailed++
		case environments.OperationStatusUnknown:
			daily.Unknown++
		case environments.OperationStatusRunning:
			daily.Running++
		}
		if op.Action == environments.OperationActionExec {
			daily.ExecOps++
		} else if op.Action == environments.OperationActionDeploy || op.Action == environments.OperationActionEnsure {
			daily.DeployOps++
		}

		// Page collection
		if cursorObj != nil && keyStr <= cursorObj.Key {
			continue
		}

		if len(page.Operations) < limit {
			page.Operations = append(page.Operations, op)
			lastKey = keyStr
		} else {
			page.HasMore = true
		}
	}

	if err := iter.Error(); err != nil {
		return environments.OperationHistoryPage{}, fmt.Errorf("iterate history: %w", err)
	}

	if page.HasMore && lastKey != "" {
		nextC := environmentOpHistoryCursor{
			Version:        1,
			AccountScopeID: q.AccountScopeID,
			WorkspaceID:    q.WorkspaceID,
			EnvironmentID:  q.EnvironmentID,
			DeploymentID:   q.DeploymentID,
			Actor:          q.Actor,
			SessionID:      q.SessionID,
			WorkerID:       q.WorkerID,
			Status:         string(q.Status),
			Action:         q.Action,
			Timezone:       tzStr,
			StartDate:      q.StartDate,
			EndDate:        q.EndDate,
			Key:            lastKey,
			SummaryRev:     summary.Revision,
		}
		data, err := json.Marshal(nextC)
		if err != nil {
			return environments.OperationHistoryPage{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}

	// Sort daily totals descending by date
	dates := make([]string, 0, len(dailyMap))
	for d := range dailyMap {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	for _, d := range dates {
		page.DailyTotals = append(page.DailyTotals, *dailyMap[d])
	}

	return page, nil
}
