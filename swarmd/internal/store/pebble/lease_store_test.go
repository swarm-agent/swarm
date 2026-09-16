package pebblestore

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

func TestLeaseStore_AcquireAndRelease(t *testing.T) {
	store := openEphemeralStore(t)
	ls := NewLeaseStore(store)

	now := time.Now().UnixMilli()
	lease := environments.DeploymentLease{
		ID:             "lease-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		DeploymentID:   "dep-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-abc",
		AcquiredAt:     now,
		ExpiresAt:      now + 10000, // 10s in future
	}

	acquired, err := ls.AcquireLease(lease)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if !acquired.Active || acquired.AcquiredAt <= 0 {
		t.Fatalf("unexpected acquired lease: %+v", acquired)
	}

	// Active lease lookup
	active, found, err := ls.GetActiveLease("acc-test", "ws-test", "dep-1")
	if err != nil || !found {
		t.Fatalf("get active lease: found=%v err=%v", found, err)
	}
	if active.ID != "lease-1" || !active.Active {
		t.Fatalf("unexpected active lease: %+v", active)
	}

	// Attempt to acquire while held by another consumer -> must fail with ErrDeploymentLeaseHeld
	secondLease := environments.DeploymentLease{
		ID:             "lease-2",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		DeploymentID:   "dep-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeTestRun,
		ConsumerID:     "testrun-xyz",
	}
	_, err = ls.AcquireLease(secondLease)
	if !errors.Is(err, ErrDeploymentLeaseHeld) {
		t.Fatalf("expected ErrDeploymentLeaseHeld, got %v", err)
	}

	// Release the first lease
	released, err := ls.ReleaseLease("acc-test", "ws-test", "lease-1", "task_completed")
	if err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if released.Active {
		t.Fatal("expected lease to be inactive after release")
	}
	if released.ReleaseReason != "task_completed" || released.ReleasedAt <= 0 {
		t.Fatalf("unexpected released lease fields: %+v", released)
	}

	// Active lease lookup should now return false
	_, found, err = ls.GetActiveLease("acc-test", "ws-test", "dep-1")
	if err != nil {
		t.Fatalf("get active lease after release: %v", err)
	}
	if found {
		t.Fatal("expected no active lease after release")
	}

	// Now second consumer can acquire
	acquiredSecond, err := ls.AcquireLease(secondLease)
	if err != nil {
		t.Fatalf("acquire second lease after release: %v", err)
	}
	if !acquiredSecond.Active || acquiredSecond.ID != "lease-2" {
		t.Fatalf("unexpected second lease: %+v", acquiredSecond)
	}
}

func TestLeaseStore_ExpiredLeaseAcquisition(t *testing.T) {
	store := openEphemeralStore(t)
	ls := NewLeaseStore(store)

	now := time.Now().UnixMilli()
	// First lease is already expired (ExpiresAt in the past)
	lease1 := environments.DeploymentLease{
		ID:             "lease-exp-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		DeploymentID:   "dep-exp-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-old",
		AcquiredAt:     now - 20000,
		ExpiresAt:      now - 10000, // expired 10s ago
	}

	if _, err := ls.AcquireLease(lease1); err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}

	// Second consumer acquires; should atomically expire the first and succeed
	lease2 := environments.DeploymentLease{
		ID:             "lease-new-2",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		DeploymentID:   "dep-exp-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-new",
		AcquiredAt:     now,
	}

	acquired2, err := ls.AcquireLease(lease2)
	if err != nil {
		t.Fatalf("acquire lease over expired lease: %v", err)
	}
	if !acquired2.Active || acquired2.ID != "lease-new-2" {
		t.Fatalf("unexpected acquired lease: %+v", acquired2)
	}

	// Verify old lease was marked inactive with reason expired
	oldLease, found, err := ls.Get("acc-test", "ws-test", "lease-exp-1")
	if err != nil || !found {
		t.Fatalf("get old lease: %v", err)
	}
	if oldLease.Active {
		t.Fatal("expected old lease to be inactive")
	}
	if oldLease.ReleaseReason != "expired" {
		t.Fatalf("expected reason 'expired', got %q", oldLease.ReleaseReason)
	}
}

func TestLeaseStore_Concurrency(t *testing.T) {
	store := openEphemeralStore(t)
	ls := NewLeaseStore(store)

	const numWorkers = 10
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	successCount := 0
	heldCount := 0
	var mu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()
			lease := environments.DeploymentLease{
				AccountScopeID: "acc-conc",
				WorkspaceID:    "ws-conc",
				DeploymentID:   "dep-conc-1",
				EnvironmentID:  "env-conc-1",
				ConsumerType:   environments.ConsumerTypeWorker,
				ConsumerID:     fmt.Sprintf("worker-%d", workerID),
				AcquiredAt:     time.Now().UnixMilli(),
				ExpiresAt:      time.Now().UnixMilli() + 60000,
			}
			_, err := ls.AcquireLease(lease)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else if errors.Is(err, ErrDeploymentLeaseHeld) {
				heldCount++
			}
		}(i)
	}

	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful lease acquisition, got %d (held=%d)", successCount, heldCount)
	}
	if heldCount != numWorkers-1 {
		t.Fatalf("expected %d held errors, got %d", numWorkers-1, heldCount)
	}
}

func TestLeaseStore_ExpireStaleLeases(t *testing.T) {
	store := openEphemeralStore(t)
	ls := NewLeaseStore(store)

	now := time.Now().UnixMilli()
	// Active, not expired
	_, err := ls.AcquireLease(environments.DeploymentLease{
		ID:             "lease-active",
		AccountScopeID: "acc-sweep",
		WorkspaceID:    "ws-sweep",
		DeploymentID:   "dep-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "s1",
		AcquiredAt:     now,
		ExpiresAt:      now + 50000,
	})
	if err != nil {
		t.Fatalf("acquire active: %v", err)
	}

	// Active, expired
	_, err = ls.AcquireLease(environments.DeploymentLease{
		ID:             "lease-stale",
		AccountScopeID: "acc-sweep",
		WorkspaceID:    "ws-sweep",
		DeploymentID:   "dep-2",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "s2",
		AcquiredAt:     now - 20000,
		ExpiresAt:      now - 5000,
	})
	if err != nil {
		t.Fatalf("acquire stale: %v", err)
	}

	expiredCount, err := ls.ExpireStaleLeases("acc-sweep", "ws-sweep", now)
	if err != nil {
		t.Fatalf("expire stale: %v", err)
	}
	if expiredCount != 1 {
		t.Fatalf("expected 1 expired lease, got %d", expiredCount)
	}

	activeList, err := ls.ListActive("acc-sweep", "ws-sweep", 10)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(activeList) != 1 || activeList[0].ID != "lease-active" {
		t.Fatalf("unexpected active leases after sweep: %+v", activeList)
	}
}

func TestLeaseStore_Isolation(t *testing.T) {
	store := openEphemeralStore(t)
	ls := NewLeaseStore(store)

	now := time.Now().UnixMilli()
	_, err := ls.AcquireLease(environments.DeploymentLease{
		ID:             "lease-iso-1",
		AccountScopeID: "acc-a",
		WorkspaceID:    "ws-a",
		DeploymentID:   "dep-1",
		EnvironmentID:  "env-1",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "s1",
		AcquiredAt:     now,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Cross-account lookup
	_, found, err := ls.Get("acc-b", "ws-a", "lease-iso-1")
	if err != nil {
		t.Fatalf("get cross-account: %v", err)
	}
	if found {
		t.Fatal("cross-account leakage")
	}

	// Cross-workspace lookup
	_, found, err = ls.Get("acc-a", "ws-b", "lease-iso-1")
	if err != nil {
		t.Fatalf("get cross-workspace: %v", err)
	}
	if found {
		t.Fatal("cross-workspace leakage")
	}

	// Cross-account active lease lookup
	_, found, err = ls.GetActiveLease("acc-b", "ws-a", "dep-1")
	if err != nil {
		t.Fatalf("get active cross-account: %v", err)
	}
	if found {
		t.Fatal("cross-account active lease leakage")
	}
}
