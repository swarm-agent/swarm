package environments

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvironmentOperation_Validation(t *testing.T) {
	now := time.Now().UnixMilli()

	validOp := EnvironmentOperation{
		OperationID:    "op_test_123",
		AccountScopeID: "acc_test",
		WorkspaceID:    "ws_test",
		Action:         OperationActionExec,
		EnvironmentID:  "env_test_1",
		DeploymentID:   "dep_test_1",
		Attribution: OperationAttribution{
			Actor:     "user",
			SessionID: "sess_1",
		},
		Status:    OperationStatusQueued,
		Revision:  1,
		CreatedAt: now,
		Deadline:  now + 300000,
		Activity: OperationActivity{
			Description: "Running test execution",
			Phase:       "queued",
			ProgressPct: 0,
		},
	}

	if err := validOp.Validate(); err != nil {
		t.Fatalf("expected valid operation, got error: %v", err)
	}

	// Test required fields
	t.Run("empty operation id", func(t *testing.T) {
		op := validOp.Clone()
		op.OperationID = ""
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on empty operation id")
		}
	})

	t.Run("empty account scope id", func(t *testing.T) {
		op := validOp.Clone()
		op.AccountScopeID = ""
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on empty account scope id")
		}
	})

	t.Run("empty workspace id", func(t *testing.T) {
		op := validOp.Clone()
		op.WorkspaceID = ""
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on empty workspace id")
		}
	})

	t.Run("empty action", func(t *testing.T) {
		op := validOp.Clone()
		op.Action = ""
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on empty action")
		}
	})

	t.Run("unsupported status", func(t *testing.T) {
		op := validOp.Clone()
		op.Status = "invalid_status"
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on unsupported status")
		}
	})

	t.Run("deadline earlier than created_at", func(t *testing.T) {
		op := validOp.Clone()
		op.Deadline = op.CreatedAt - 1000
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on deadline earlier than created_at")
		}
	})

	t.Run("completed_at earlier than created_at", func(t *testing.T) {
		op := validOp.Clone()
		op.CompletedAt = op.CreatedAt - 1000
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on completed_at earlier than created_at")
		}
	})

	t.Run("invalid progress pct", func(t *testing.T) {
		op := validOp.Clone()
		op.Activity.ProgressPct = 101
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on progress_pct > 100")
		}
		op.Activity.ProgressPct = -1
		if err := op.Validate(); err == nil {
			t.Fatal("expected error on progress_pct < 0")
		}
	})

	t.Run("forbidden secret detected in activity", func(t *testing.T) {
		// If someone tries to pass an activity containing forbidden credential keys
		rawWithSecret := map[string]any{
			"operation_id":     "op_sec_1",
			"account_scope_id": "acc_test",
			"workspace_id":    "ws_test",
			"action":           "exec",
			"status":           "running",
			"created_at":       now,
			"deadline":         now + 60000,
			"api_key":          "secret123",
		}
		data, _ := json.Marshal(rawWithSecret)
		var op EnvironmentOperation
		_ = json.Unmarshal(data, &op)
		if err := AssertNoSecretsRaw(data); err == nil {
			t.Fatal("expected secret detection error on forbidden secret key")
		}
	})
}

func TestEnvironmentOperation_StatusPredicates(t *testing.T) {
	terminalStatuses := []OperationStatus{
		OperationStatusSucceeded,
		OperationStatusFailed,
		OperationStatusCancelled,
		OperationStatusTimedOut,
	}
	for _, status := range terminalStatuses {
		op := &EnvironmentOperation{Status: status}
		if !op.IsTerminal() {
			t.Fatalf("status %s should be terminal", status)
		}
		if op.IsActive() {
			t.Fatalf("status %s should not be active", status)
		}
		if op.IsUnresolved() {
			t.Fatalf("status %s should not be unresolved", status)
		}
	}

	activeStatuses := []OperationStatus{
		OperationStatusQueued,
		OperationStatusRunning,
		OperationStatusCancelling,
	}
	for _, status := range activeStatuses {
		op := &EnvironmentOperation{Status: status}
		if op.IsTerminal() {
			t.Fatalf("status %s should not be terminal", status)
		}
		if !op.IsActive() {
			t.Fatalf("status %s should be active", status)
		}
		if op.IsUnresolved() {
			t.Fatalf("status %s should not be unresolved", status)
		}
	}

	unresolvedStatuses := []OperationStatus{
		OperationStatusCleanupFailed,
		OperationStatusUnknown,
	}
	for _, status := range unresolvedStatuses {
		op := &EnvironmentOperation{Status: status}
		if op.IsTerminal() {
			t.Fatalf("status %s should not be terminal", status)
		}
		if op.IsActive() {
			t.Fatalf("status %s should not be active", status)
		}
		if !op.IsUnresolved() {
			t.Fatalf("status %s should be unresolved", status)
		}
	}
}

func TestEnvironmentSummary_ValidationAndClone(t *testing.T) {
	s := &EnvironmentSummary{
		AccountScopeID:    "acc-test",
		WorkspaceID:       "ws-test",
		Revision:          5,
		UpdatedAt:         12345,
		ActiveDeployments: 2,
		RunningExecOps:    1,
		QueuedOps:         0,
		RunningOps:        1,
		TotalOps:          10,
	}

	if err := s.Validate(); err != nil {
		t.Fatalf("expected valid summary, got: %v", err)
	}

	cp := s.Clone()
	if cp.Revision != s.Revision || cp.ActiveDeployments != s.ActiveDeployments {
		t.Fatalf("clone mismatch: %+v vs %+v", cp, s)
	}

	// Mutating copy shouldn't affect original
	cp.ActiveDeployments = 99
	if s.ActiveDeployments == 99 {
		t.Fatal("clone was not independent")
	}

	invalid := &EnvironmentSummary{AccountScopeID: ""}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected error on empty account scope id")
	}
}

func TestEnvironmentOperation_NilSafe(t *testing.T) {
	var op *EnvironmentOperation
	if !op.IsTerminal() {
		t.Fatal("nil operation should report terminal")
	}
	if op.IsActive() {
		t.Fatal("nil operation should not be active")
	}
	if op.IsUnresolved() {
		t.Fatal("nil operation should not be unresolved")
	}
	if op.Clone() != nil {
		t.Fatal("cloning nil should return nil")
	}
	if err := op.Validate(); err == nil {
		t.Fatal("validating nil operation should error")
	}
}
