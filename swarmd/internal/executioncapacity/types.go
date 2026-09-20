package executioncapacity

import (
	"context"
	"errors"
	"fmt"
)

// ExecutionKind distinguishes ordinary execution from deployed execution.
type ExecutionKind string

const (
	ExecutionKindOrdinary ExecutionKind = "ordinary"
	ExecutionKindDeployed ExecutionKind = "deployed"
)

// Normalized returns the normalized ExecutionKind.
func (k ExecutionKind) Normalized() ExecutionKind {
	if k == ExecutionKindDeployed {
		return ExecutionKindDeployed
	}
	return ExecutionKindOrdinary
}

const (
	// DefaultActiveExecutionLimit is the default account-wide execution ceiling.
	DefaultActiveExecutionLimit = 100

	// MinActiveExecutionLimit is the minimum allowable execution limit.
	MinActiveExecutionLimit = 1

	// MaxActiveExecutionLimit is the maximum allowable execution limit.
	MaxActiveExecutionLimit = 10000

	// DeploymentBatchBound is the maximum number of deployments permitted in one batch.
	DeploymentBatchBound = 8

	// SavedQuotaNoneConfigured indicates no external quota authority is active.
	SavedQuotaNoneConfigured = "none configured"

	// DefaultMaxQueueWaiters is the default bound for queued admission requests.
	DefaultMaxQueueWaiters = 1000
)

var (
	ErrQueueFull         = errors.New("execution capacity queue is full")
	ErrLeaseReleased      = errors.New("execution capacity lease already released")
	ErrLeaseAlreadyParked = errors.New("execution capacity lease already parked")
	ErrLeaseNotParked     = errors.New("execution capacity lease is not parked")
	ErrSessionRequired    = errors.New("session ID is required for execution capacity admission")
	ErrRunRequired        = errors.New("run ID is required for execution capacity admission")
	ErrClosed             = errors.New("execution capacity manager is closed")
)

// ValidateLimit ensures the requested active execution limit is within positive bounds.
func ValidateLimit(limit int) error {
	if limit < MinActiveExecutionLimit {
		return fmt.Errorf("active execution limit must be at least %d", MinActiveExecutionLimit)
	}
	if limit > MaxActiveExecutionLimit {
		return fmt.Errorf("active execution limit cannot exceed %d", MaxActiveExecutionLimit)
	}
	return nil
}

// AcquireRequest contains the parameters for execution capacity admission.
type AcquireRequest struct {
	AccountScopeID string        `json:"account_scope_id"`
	SessionID      string        `json:"session_id"`
	RunID          string        `json:"run_id"`
	Kind           ExecutionKind `json:"kind"`
}

// Snapshot provides atomic capacity visibility for an account.
type Snapshot struct {
	AccountScopeID       string `json:"account_scope_id"`
	EffectiveLimit       int    `json:"effective_limit"`
	TotalActive          int    `json:"total_active"`
	DeployedActive       int    `json:"deployed_active"`
	Pending              int    `json:"pending"`
	Available            int    `json:"available"`
	DeploymentBatchBound int    `json:"deployment_batch_bound"`
	SavedQuota           string `json:"saved_quota"`
}

// Lease represents an admitted execution slot and session ownership.
type Lease interface {
	ID() string
	AccountScopeID() string
	SessionID() string
	RunID() string
	Kind() ExecutionKind
	IsActive() bool
	IsParked() bool
	IsReleased() bool
	Park() error
	ParkWithContext(ctx context.Context) error
	Reacquire(ctx context.Context) error
	Release() error
}
