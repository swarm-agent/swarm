package permission

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Purpose: TryAdmitDesign and ReserveSubagentWave must share the account Swarm
// ceiling across parent requests, reload policy before admission, and release
// exactly once. This permission/store layer proves accounting without providers.
func TestDesignAdmissionAggregateAndPolicyChanges(t *testing.T) {
	s, writer := openSubagentReservationTestServices(t)
	policy := DefaultSubagentPolicy()
	policy.SwarmActiveChildLimit = 2
	if _, err := writer.UpdateSubagentPolicyForAccount("account", policy); err != nil {
		t.Fatal(err)
	}
	wave := reserveSubagentWaveWithMode(t, s, "account", "parent-one", "run-one", "wave", 1, true)
	if wave.Decision != SubagentReservationApprove {
		t.Fatal(wave)
	}
	release, err := s.TryAdmitDesign("account", "parent-two", "run-two", "design-one")
	if err != nil || release == nil {
		t.Fatalf("admit: %v", err)
	}
	defer release()
	if extra, err := s.TryAdmitDesign("account", "parent-three", "run-three", "design-two"); err != nil || extra != nil {
		t.Fatalf("aggregate ceiling escaped: %v", err)
	}
	other, err := s.TryAdmitDesign("other-account", "other-parent", "other-run", "other-design")
	if err != nil || other == nil {
		t.Fatalf("account isolation: %v", err)
	}
	other()
	policy.SwarmActiveChildLimit = 1
	if _, err := writer.UpdateSubagentPolicyForAccount("account", policy); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSubagentWave("parent-one", "run-one", "wave", "completed"); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.TryAdmitDesign("account", "parent-three", "run-three", "design-two"); err != nil || extra != nil {
		t.Fatalf("lowered cap cancelled or ignored active slot: %v", err)
	}
	release()
	release()
	next, err := s.TryAdmitDesign("account", "parent-three", "run-three", "design-two")
	if err != nil || next == nil {
		t.Fatalf("release did not drain: %v", err)
	}
	defer next()
	wave = reserveSubagentWaveWithMode(t, s, "account", "another-parent", "another-run", "wave-two", 1, true)
	if wave.Decision != SubagentReservationAsk {
		t.Fatalf("Swarm wave ignored design occupancy: %+v", wave)
	}
	if err := s.FinishSubagentWave("another-parent", "another-run", "wave-two", "denied"); err != nil {
		t.Fatal(err)
	}
	policy.Mode = SubagentModeDirect
	if _, err := writer.UpdateSubagentPolicyForAccount("account", policy); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.TryAdmitDesign("account", "parent", "run", "denied"); !errors.Is(err, ErrDesignAdmissionDenied) || extra != nil {
		t.Fatalf("direct mode bypassed: %v", err)
	}
}

// Purpose: ask mode cannot be bypassed, approval is exact-candidate and durable,
// and a denial creates no slot. Real pending permission records are the narrow
// boundary; no provider invocation or synthetic workload is involved.
func TestDesignAdmissionApprovalAndDenial(t *testing.T) {
	s, _ := openSubagentReservationTestServices(t)
	policy := DefaultSubagentPolicy()
	policy.Mode = SubagentModeAsk
	if _, err := s.UpdateSubagentPolicyForAccount("account", policy); err != nil {
		t.Fatal(err)
	}
	s.SetBypassPermissions(true)
	for _, candidate := range []string{"allow", "deny"} {
		if release, err := s.TryAdmitDesign("account", "parent", "run", candidate); err != nil || release != nil {
			t.Fatalf("ask bypassed: %v", err)
		}
	}
	pending, err := s.ListPending("parent", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending approvals: %+v %v", pending, err)
	}
	for _, record := range pending {
		action := ActionDenyOnce
		if record.CallID == "design-admission-allow" {
			action = ActionAllowOnce
		}
		if _, err := s.Resolve("parent", record.ID, action, "test decision"); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the permission service over the same durable records, as on restart.
	restarted := NewService(s.store, nil, nil)
	release, err := restarted.TryAdmitDesign("account", "parent", "run", "allow")
	if err != nil || release == nil {
		t.Fatalf("approved candidate not admitted: %v", err)
	}
	release()
	if release, err := restarted.TryAdmitDesign("account", "parent", "run", "deny"); !errors.Is(err, ErrDesignAdmissionDenied) || release != nil {
		t.Fatalf("denial bypassed: %v", err)
	}
}

// Purpose: racing parent requests cannot oversubscribe the account pool. The
// permission mutex is the production reservation boundary; all winners are held
// until every contender returns, then release must restore exactly four slots.
func TestDesignAdmissionConcurrentParents(t *testing.T) {
	s, _ := openSubagentReservationTestServices(t)
	policy := DefaultSubagentPolicy()
	policy.SwarmActiveChildLimit = 4
	if _, err := s.UpdateSubagentPolicyForAccount("account", policy); err != nil {
		t.Fatal(err)
	}
	results := make(chan func(), 12)
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			release, err := s.TryAdmitDesign("account", fmt.Sprintf("parent-%d", i), "run", fmt.Sprintf("candidate-%d", i))
			if err != nil {
				t.Error(err)
			}
			results <- release
		}(i)
	}
	workers.Wait()
	close(results)
	count := 0
	for release := range results {
		if release != nil {
			count++
			release()
		}
	}
	if count != 4 {
		t.Fatalf("admitted %d, want four", count)
	}
	if len(s.designSlots) != 0 {
		t.Fatal("released slots leaked")
	}
}
