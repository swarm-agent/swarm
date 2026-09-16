package environments

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ConsumerType categorizes the consumer entity holding a deployment lease.
type ConsumerType string

const (
	// ConsumerTypeSession represents an interactive user or agent session holding the deployment.
	ConsumerTypeSession ConsumerType = "session"

	// ConsumerTypeTestRun represents a temporary automated test execution run holding the deployment.
	ConsumerTypeTestRun ConsumerType = "test_run"

	// ConsumerTypeWorker represents a scheduled or background worker run holding the deployment.
	ConsumerTypeWorker ConsumerType = "worker"

	// ConsumerTypeCustom represents a custom consumer holding the deployment.
	ConsumerTypeCustom ConsumerType = "custom"
)

// DeploymentLease provides atomic ownership tracking for an environment deployment.
// Strictly scoped to AccountScopeID and WorkspaceID.
type DeploymentLease struct {
	ID               string            `json:"id"`
	AccountScopeID   string            `json:"account_scope_id"`
	WorkspaceID      string            `json:"workspace_id"`
	DeploymentID     string            `json:"deployment_id"`
	EnvironmentID    string            `json:"environment_id"`
	ConsumerType     ConsumerType      `json:"consumer_type"`
	ConsumerID       string            `json:"consumer_id"`
	ConsumerMetadata map[string]string `json:"consumer_metadata,omitempty"`
	AcquiredAt       int64             `json:"acquired_at"`
	ReleasedAt       int64             `json:"released_at,omitempty"`
	ExpiresAt        int64             `json:"expires_at,omitempty"` // 0 indicates no expiration (held until explicitly released)
	Active           bool              `json:"active"`
	ReleaseReason    string            `json:"release_reason,omitempty"`
}

// Validate checks that the DeploymentLease conforms to domain rules and scoping requirements.
func (l *DeploymentLease) Validate() error {
	if l == nil {
		return errors.New("deployment lease is nil")
	}

	l.ID = strings.TrimSpace(l.ID)
	if l.ID == "" {
		return errors.New("lease id cannot be empty")
	}
	if len(l.ID) > maxIDBytes || !utf8.ValidString(l.ID) {
		return fmt.Errorf("lease id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	l.AccountScopeID = strings.TrimSpace(l.AccountScopeID)
	if l.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(l.AccountScopeID) > maxIDBytes || !utf8.ValidString(l.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	l.WorkspaceID = strings.TrimSpace(l.WorkspaceID)
	if l.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(l.WorkspaceID) > maxIDBytes || !utf8.ValidString(l.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	l.DeploymentID = strings.TrimSpace(l.DeploymentID)
	if l.DeploymentID == "" {
		return errors.New("deployment_id cannot be empty")
	}

	l.EnvironmentID = strings.TrimSpace(l.EnvironmentID)
	if l.EnvironmentID == "" {
		return errors.New("environment_id cannot be empty")
	}

	l.ConsumerID = strings.TrimSpace(l.ConsumerID)
	if l.ConsumerID == "" {
		return errors.New("consumer_id cannot be empty")
	}

	switch l.ConsumerType {
	case ConsumerTypeSession, ConsumerTypeTestRun, ConsumerTypeWorker, ConsumerTypeCustom:
		// valid
	default:
		return fmt.Errorf("unsupported consumer type: %q", l.ConsumerType)
	}

	if l.AcquiredAt <= 0 {
		return errors.New("acquired_at timestamp must be greater than 0")
	}

	if l.ExpiresAt > 0 && l.ExpiresAt < l.AcquiredAt {
		return errors.New("expires_at cannot be earlier than acquired_at")
	}

	if l.ReleasedAt > 0 && l.ReleasedAt < l.AcquiredAt {
		return errors.New("released_at cannot be earlier than acquired_at")
	}

	return nil
}

// IsExpired checks if the active lease has passed its expiration time.
func (l *DeploymentLease) IsExpired(nowMillis int64) bool {
	if l == nil || !l.Active {
		return false
	}
	return l.ExpiresAt > 0 && nowMillis > l.ExpiresAt
}

// IsHeld checks if the lease is currently active and unexpired.
func (l *DeploymentLease) IsHeld(nowMillis int64) bool {
	if l == nil || !l.Active {
		return false
	}
	if l.ExpiresAt > 0 && nowMillis > l.ExpiresAt {
		return false
	}
	return true
}

// Clone returns a deep copy of DeploymentLease.
func (l *DeploymentLease) Clone() *DeploymentLease {
	if l == nil {
		return nil
	}
	cp := *l
	if l.ConsumerMetadata != nil {
		cp.ConsumerMetadata = make(map[string]string, len(l.ConsumerMetadata))
		for k, v := range l.ConsumerMetadata {
			cp.ConsumerMetadata[k] = v
		}
	}
	return &cp
}
