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

	// 4. Invalid limits are preserved (not reset to default 100) so validation fails closed
	neg := NormalizePolicy(Policy{ActiveExecutionLimit: -10})
	if neg.ActiveExecutionLimit != -10 {
		t.Fatalf("negative limit should be preserved as -10, got %d", neg.ActiveExecutionLimit)
	}
	tooHigh := NormalizePolicy(Policy{ActiveExecutionLimit: 50000})
	if tooHigh.ActiveExecutionLimit != 50000 {
		t.Fatalf("excessive limit should be preserved as 50000, got %d", tooHigh.ActiveExecutionLimit)
	}
	if err := ValidateActiveExecutionLimit(neg.ActiveExecutionLimit); err == nil {
		t.Fatalf("expected ValidateActiveExecutionLimit to reject negative limit")
	}
	if err := ValidateActiveExecutionLimit(tooHigh.ActiveExecutionLimit); err == nil {
		t.Fatalf("expected ValidateActiveExecutionLimit to reject excessive limit")
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
	if snap.DeploymentBatchBound != executioncapacity.DeploymentBatchBound {
		t.Fatalf("DeploymentBatchBound = %d, want %d", snap.DeploymentBatchBound, executioncapacity.DeploymentBatchBound)
	}
	if snap.SavedQuota != executioncapacity.SavedQuotaNoneConfigured {
		t.Fatalf("SavedQuota = %q, want %q", snap.SavedQuota, executioncapacity.SavedQuotaNoneConfigured)
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

// TestUpdateExecutionCapabilityPoliciesForAccountAtomic verifies atomic validation, update,
// and notification across sessionDeploy, planAcceptance, and activeExecutionLimit.
func TestUpdateExecutionCapabilityPoliciesForAccountAtomic(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-atomic.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	accountID := "acc-atomic"

	// 1. Validation failure rejects all changes without partial mutation
	invalidLimit := -5
	deployPolicy := SessionDeployPolicy{RequireApproval: "always"}
	_, err = svc.UpdateExecutionCapabilityPoliciesForAccount(accountID, &deployPolicy, nil, &invalidLimit)
	if err == nil {
		t.Fatalf("expected error for invalid limit in atomic update")
	}
	// Verify state was not modified
	pol, err := svc.CurrentPolicyForAccount(accountID)
	if err != nil {
		t.Fatalf("current policy: %v", err)
	}
	if pol.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("limit should remain default, got %d", pol.ActiveExecutionLimit)
	}

	// 2. Atomic update with all non-nil fields
	validLimit := 55
	planPolicy := PlanAcceptancePolicy{AutoAcceptFastPlan: true}
	updated, err := svc.UpdateExecutionCapabilityPoliciesForAccount(accountID, &deployPolicy, &planPolicy, &validLimit)
	if err != nil {
		t.Fatalf("atomic update failed: %v", err)
	}
	if updated.ActiveExecutionLimit != 55 {
		t.Fatalf("limit = %d, want 55", updated.ActiveExecutionLimit)
	}
	if updated.SessionDeploy.RequireApproval != "always" {
		t.Fatalf("deploy require approval = %s, want always", updated.SessionDeploy.RequireApproval)
	}
	if !updated.PlanAcceptance.AutoAcceptFastPlan {
		t.Fatalf("plan acceptance auto accept fast plan = false, want true")
	}

	// Verify capacity manager synchronized via NotifyLimitChanged
	snap := svc.ExecutionCapacitySnapshot(accountID)
	if snap.EffectiveLimit != 55 {
		t.Fatalf("capacity snapshot effective limit = %d, want 55", snap.EffectiveLimit)
	}

	// 3. Partial update preserves omitted fields
	newLimit := 75
	partialUpdated, err := svc.UpdateExecutionCapabilityPoliciesForAccount(accountID, nil, nil, &newLimit)
	if err != nil {
		t.Fatalf("partial update failed: %v", err)
	}
	if partialUpdated.ActiveExecutionLimit != 75 {
		t.Fatalf("limit = %d, want 75", partialUpdated.ActiveExecutionLimit)
	}
	if partialUpdated.SessionDeploy.RequireApproval != "always" {
		t.Fatalf("preserved deploy require approval = %s, want always", partialUpdated.SessionDeploy.RequireApproval)
	}
	if !partialUpdated.PlanAcceptance.AutoAcceptFastPlan {
		t.Fatalf("preserved plan acceptance auto accept fast plan = false, want true")
	}
	if snap := svc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 75 {
		t.Fatalf("capacity snapshot after partial update = %d, want 75", snap.EffectiveLimit)
	}

	// 4. ResetPolicyForAccount restores default policy and synchronizes capacity
	resetPol, err := svc.ResetPolicyForAccount(accountID)
	if err != nil {
		t.Fatalf("reset policy: %v", err)
	}
	if resetPol.ActiveExecutionLimit != DefaultActiveExecutionLimit {
		t.Fatalf("reset policy limit = %d, want default %d", resetPol.ActiveExecutionLimit, DefaultActiveExecutionLimit)
	}
	if snap := svc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != DefaultActiveExecutionLimit {
		t.Fatalf("capacity snapshot after reset = %d, want default %d", snap.EffectiveLimit, DefaultActiveExecutionLimit)
	}
}

// TestExecutionCapacitySnapshotUnavailable verifies that when capacity service is unavailable
// (e.g. nil capacity manager), Snapshot reports Unavailable=true and Error rather than fabricating 100.
func TestExecutionCapacitySnapshotUnavailable(t *testing.T) {
	svc := &Service{}
	snap := svc.ExecutionCapacitySnapshot("acc-unavail")
	if !snap.Unavailable {
		t.Fatalf("expected Unavailable=true when capacity manager is nil")
	}
	if snap.Error == "" {
		t.Fatalf("expected non-empty Error when capacity manager is nil")
	}
	if snap.EffectiveLimit != 0 || snap.Available != 0 {
		t.Fatalf("expected EffectiveLimit=0 and Available=0 when unavailable, got: %+v", snap)
	}
}

// TestActiveLeaseForSessionLookup verifies lookup of active leases for internal compact.
func TestActiveLeaseForSessionLookup(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "cap-active-lease.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	accountID := "acc-lookup"
	ctx := context.Background()

	// Initially no lease
	if l := svc.ActiveLeaseForSession(accountID, "sess-1"); l != nil {
		t.Fatalf("expected nil active lease initially, got %v", l)
	}

	// Admit session 1
	lease, err := svc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer lease.Release()

	// Found while active
	found := svc.ActiveLeaseForSession(accountID, "sess-1")
	if found == nil || found.ID() != lease.ID() {
		t.Fatalf("expected active lease %s, got %v", lease.ID(), found)
	}

	// Different session returns nil
	if l := svc.ActiveLeaseForSession(accountID, "sess-2"); l != nil {
		t.Fatalf("expected nil for different session, got %v", l)
	}

	// Parked lease returns nil (not active)
	if err := lease.Park(); err != nil {
		t.Fatalf("park: %v", err)
	}
	if l := svc.ActiveLeaseForSession(accountID, "sess-1"); l != nil {
		t.Fatalf("expected nil active lease when parked, got %v", l)
	}

	// Reacquire restores lookup
	if err := lease.Reacquire(ctx); err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if l := svc.ActiveLeaseForSession(accountID, "sess-1"); l == nil {
		t.Fatalf("expected active lease after reacquire")
	}

	// Released lease returns nil
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if l := svc.ActiveLeaseForSession(accountID, "sess-1"); l != nil {
		t.Fatalf("expected nil active lease after release, got %v", l)
	}
}
