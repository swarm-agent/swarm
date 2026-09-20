package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/executioncapacity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TestPolicyActiveExecutionLimitDefaultsAndPreservesExplicit verifies that:
// 1. DefaultPolicy has active_execution_limit = 100.
// 2. NormalizePolicy defaults active_execution_limit to 100 ONLY when omitted (0) or invalid.
// 3. NormalizePolicy preserves explicitly configured values.
// 4. JSON serialization and deserialization preserves explicit values.
func TestPolicyActiveExecutionLimitDefaultsAndPreservesExplicit(t *testing.T) {
	// 1. DefaultPolicy
	def := DefaultPolicy()
	if def.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("DefaultPolicy ActiveExecutionLimit = %d, want %d", def.ActiveExecutionLimit, DefaultActiveExecutionLimit)
	}

	// 2. Normalize empty policy (omitted limit)
	normEmpty := NormalizePolicy(Policy{})
	if normEmpty.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("NormalizePolicy on empty policy = %d, want %d", normEmpty.ActiveExecutionLimit, DefaultActiveExecutionLimit)
	}

	// 3. Preserve explicit valid limit
	explicit := NormalizePolicy(Policy{ActiveExecutionLimit: 42})
	if explicit.ActiveExecutionLimit != 42 {
		t.Fatalf("NormalizePolicy should preserve explicit limit 42, got %d", explicit.ActiveExecutionLimit)
	}

	// 4. Invalid limits reset to default
	neg := NormalizePolicy(Policy{ActiveExecutionLimit: -10})
	if neg.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("negative limit should normalize to %d, got %d", DefaultActiveExecutionLimit, neg.ActiveExecutionLimit)
	}
	tooHigh := NormalizePolicy(Policy{ActiveExecutionLimit: 50000})
	if tooHigh.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("excessive limit should normalize to %d, got %d", DefaultActiveExecutionLimit, tooHigh.ActiveExecutionLimit)
	}

	// 5. JSON serialization with explicit value
	rawJSON := []byte(`{"version":1,"active_execution_limit":25}`)
	var unmarshaled Policy
	if err := json.Unmarshal(rawJSON, &unmarshaled); err != nil {
		t.Fatalf("unmarshal explicit policy: %v", err)
	}
	unmarshaled = NormalizePolicy(unmarshaled)
	if unmarshaled.ActiveExecutionLimit != 25 {
		t.Fatalf("unmarshaled explicit limit = %d, want 25", unmarshaled.ActiveExecutionLimit)
	}

	// 6. JSON serialization with omitted value
	rawOmitted := []byte(`{"version":1}`)
	var unmarshaledOmitted Policy
	if err := json.Unmarshal(rawOmitted, &unmarshaledOmitted); err != nil {
		t.Fatalf("unmarshal omitted policy: %v", err)
	}
	unmarshaledOmitted = NormalizePolicy(unmarshaledOmitted)
	if unmarshaledOmitted.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("unmarshaled omitted limit = %d, want %d", unmarshaledOmitted.ActiveExecutionLimit, DefaultActiveExecutionLimit)
	}
}

// TestPermissionServiceExecutionCapacityAuthority verifies that permission.Service
// serves as the single authority for capacity metrics, including batch bound 8 and saved quota none configured.
func TestPermissionServiceExecutionCapacityAuthority(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-authority.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	snap := svc.ExecutionCapacitySnapshot("account-test")

	if snap.EffectiveLimit != DefaultActiveExecutionLimit {
		t.Fatalf("EffectiveLimit = %d, want %d", snap.EffectiveLimit, DefaultActiveExecutionLimit)
	}
	if snap.DeploymentBatchBound != executioncapacity.DeploymentBatchBound || snap.DeploymentBatchBound != 8 {
		t.Fatalf("DeploymentBatchBound = %d, want 8", snap.DeploymentBatchBound)
	}
	if snap.SavedQuota != executioncapacity.SavedQuotaNoneConfigured || snap.SavedQuota != "none configured" {
		t.Fatalf("SavedQuota = %q, want 'none configured'", snap.SavedQuota)
	}
	if snap.TotalActive != 0 || snap.DeployedActive != 0 || snap.Pending != 0 {
		t.Fatalf("initial counts should be 0: %+v", snap)
	}
	if snap.Available != DefaultActiveExecutionLimit {
		t.Fatalf("Available = %d, want %d", snap.Available, DefaultActiveExecutionLimit)
	}
}

// TestUpdateActiveExecutionLimitForAccountPersistence verifies that updating the
// active execution limit persists to Pebble and synchronizes to the execution capacity manager.
func TestUpdateActiveExecutionLimitForAccountPersistence(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-persist.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	writer := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	reader := NewService(pebblestore.NewPermissionStore(store), nil, nil)

	// Update account-1 to limit 35
	updatedPolicy, err := writer.UpdateActiveExecutionLimitForAccount("account-1", 35)
	if err != nil {
		t.Fatalf("update limit: %v", err)
	}
	if updatedPolicy.ActiveExecutionLimit != 35 {
		t.Fatalf("updated policy limit = %d, want 35", updatedPolicy.ActiveExecutionLimit)
	}

	// Verify manager synchronized
	if snap := writer.ExecutionCapacitySnapshot("account-1"); snap.EffectiveLimit != 35 {
		t.Fatalf("writer manager snapshot limit = %d, want 35", snap.EffectiveLimit)
	}

	// Read from fresh service reading same Pebble store
	gotPolicy, err := reader.CurrentPolicyForAccount("account-1")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if gotPolicy.ActiveExecutionLimit != 35 {
		t.Fatalf("persisted policy limit = %d, want 35", gotPolicy.ActiveExecutionLimit)
	}

	// Verify account isolation: account-2 still has default limit
	snap2 := writer.ExecutionCapacitySnapshot("account-2")
	if snap2.EffectiveLimit != DefaultActiveExecutionLimit {
		t.Fatalf("account-2 should retain default limit %d, got %d", DefaultActiveExecutionLimit, snap2.EffectiveLimit)
	}

	// Validation rejection
	if _, err := writer.UpdateActiveExecutionLimitForAccount("account-1", 0); err == nil {
		t.Fatalf("expected error for limit 0")
	}
	if _, err := writer.UpdateActiveExecutionLimitForAccount("account-1", 10001); err == nil {
		t.Fatalf("expected error for limit 10001")
	}
}

// TestBypassPermissionsDoesNotAffectCapacity verifies that enabling permission bypass
// does NOT bypass execution capacity or change limits.
func TestBypassPermissionsDoesNotAffectCapacity(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-bypass.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	svc.SetBypassPermissions(true)

	snap := svc.ExecutionCapacitySnapshot("account-bypass")
	if snap.EffectiveLimit != DefaultActiveExecutionLimit {
		t.Fatalf("bypass altered effective limit: %d", snap.EffectiveLimit)
	}

	// Also verify with custom limit
	if _, err := svc.UpdateActiveExecutionLimitForAccount("account-bypass", 1); err != nil {
		t.Fatalf("update limit: %v", err)
	}

	ctx := context.Background()
	l1, err := svc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: "account-bypass",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("admit l1: %v", err)
	}
	defer l1.Release()

	// Second request must NOT bypass capacity even though bypassPermissions is true
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately to not block test
	_, err = svc.AdmitExecution(cancelCtx, executioncapacity.AcquireRequest{
		AccountScopeID: "account-bypass",
		SessionID:      "sess-2",
		RunID:          "run-2",
	})
	// Capacity is exhausted (limit=1), so it queued and context cancelled
	if err == nil {
		t.Fatalf("bypass permissions should NOT bypass execution capacity!")
	}
}

// TestAdmitAndReleaseExecutionFlow verifies admission and release via Service methods.
func TestAdmitAndReleaseExecutionFlow(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-flow.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	ctx := context.Background()

	lease, err := svc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: "acc-flow",
		SessionID:      "sess-flow",
		RunID:          "run-flow",
		Kind:           executioncapacity.ExecutionKindDeployed,
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	snap := svc.ExecutionCapacitySnapshot("acc-flow")
	if snap.TotalActive != 1 || snap.DeployedActive != 1 {
		t.Fatalf("expected 1 active and 1 deployed, got: %+v", snap)
	}

	if err := svc.ReleaseExecution(lease); err != nil {
		t.Fatalf("release: %v", err)
	}

	snap = svc.ExecutionCapacitySnapshot("acc-flow")
	if snap.TotalActive != 0 || snap.DeployedActive != 0 {
		t.Fatalf("expected 0 active after release, got: %+v", snap)
	}
}
