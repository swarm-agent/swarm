package permission

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openSubagentReservationTestServices(t *testing.T) (*Service, *Service) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	permissionStore := pebblestore.NewPermissionStore(store)
	return NewService(permissionStore, nil, nil), NewService(permissionStore, nil, nil)
}

func TestCurrentPolicyReloadsAutomaticLaunchLimitChangedByAnotherService(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-1"
	if _, err := reader.CurrentPolicyForAccount(accountScopeID); err != nil {
		t.Fatalf("prime reader policy cache: %v", err)
	}
	updated := DefaultSubagentPolicy()
	updated.AutomaticLaunchesPerParentRun = 7
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, updated); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	policy, err := reader.CurrentPolicyForAccount(accountScopeID)
	if err != nil {
		t.Fatalf("reload current policy: %v", err)
	}
	if got := policy.Subagents.AutomaticLaunchesPerParentRun; got != 7 {
		t.Fatalf("automatic launch limit = %d, want 7", got)
	}
}

func TestReservationReloadsActiveChildLimitChangedDuringRun(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-1"
	first, err := reader.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", AccountScopeID: accountScopeID, RunID: "run-1", CallID: "call-1", ManifestHash: "manifest-1", LaunchCount: 1,
	})
	if err != nil || first.Decision != SubagentReservationApprove {
		t.Fatalf("reserve first wave: decision=%q err=%v", first.Decision, err)
	}
	updated := DefaultSubagentPolicy()
	updated.ActiveChildLimit = 1
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, updated); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	second, err := reader.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", AccountScopeID: accountScopeID, RunID: "run-1", CallID: "call-2", ManifestHash: "manifest-2", LaunchCount: 1,
	})
	if err != nil {
		t.Fatalf("reserve second wave: %v", err)
	}
	if second.Decision != SubagentReservationDeny || second.Reason != "active child concurrency limit would be exceeded" {
		t.Fatalf("second wave did not use updated active child limit: %#v", second)
	}
}

func TestReservationReloadsAutomaticLaunchLimitChangedDuringRun(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-1"
	first, err := reader.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", AccountScopeID: accountScopeID, RunID: "run-1", CallID: "call-1", ManifestHash: "manifest-1", LaunchCount: 1,
	})
	if err != nil || first.Decision != SubagentReservationApprove {
		t.Fatalf("reserve first wave: decision=%q err=%v", first.Decision, err)
	}
	updated := DefaultSubagentPolicy()
	updated.AutomaticLaunchesPerParentRun = 1
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, updated); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	second, err := reader.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", AccountScopeID: accountScopeID, RunID: "run-1", CallID: "call-2", ManifestHash: "manifest-2", LaunchCount: 1,
	})
	if err != nil {
		t.Fatalf("reserve second wave: %v", err)
	}
	if second.Decision != SubagentReservationAsk || second.Reason != "exact wave requires approval because it exceeds the automatic run budget" {
		t.Fatalf("second wave did not use updated automatic launch limit: %#v", second)
	}
}

func TestFailedSubagentWaveReleasesActiveConcurrencyForRetry(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	first, err := svc.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", RunID: "run-1", CallID: "call-1", ManifestHash: "manifest-1", LaunchCount: 5,
	})
	if err != nil || first.Decision != SubagentReservationApprove {
		t.Fatalf("reserve first wave: decision=%q err=%v", first.Decision, err)
	}
	if err := svc.FinishSubagentWave("session-1", "run-1", "call-1", "failed"); err != nil {
		t.Fatalf("finish failed wave: %v", err)
	}
	second, err := svc.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-1", RunID: "run-1", CallID: "call-2", ManifestHash: "manifest-2", LaunchCount: 1,
	})
	if err != nil {
		t.Fatalf("reserve retry wave: %v", err)
	}
	if second.Decision == SubagentReservationDeny || second.Reason == "active child concurrency limit would be exceeded" {
		t.Fatalf("retry was blocked by leaked active reservation: %#v", second)
	}
}
