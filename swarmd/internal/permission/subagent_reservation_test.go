package permission

import (
	"encoding/json"
	"path/filepath"
	"strings"
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

func reserveSubagentWave(t *testing.T, service *Service, accountScopeID, sessionID, runID, callID string, launchCount int) SubagentReservationResult {
	t.Helper()
	result, err := service.ReserveSubagentWave(SubagentReservationRequest{
		SessionID:      sessionID,
		AccountScopeID: accountScopeID,
		RunID:          runID,
		CallID:         callID,
		ManifestHash:   "manifest-" + callID,
		LaunchCount:    launchCount,
	})
	if err != nil {
		t.Fatalf("reserve %s: %v", callID, err)
	}
	return result
}

func TestSubagentPolicyJSONOmitsRemovedWaveAndDepthFields(t *testing.T) {
	encoded, err := json.Marshal(DefaultSubagentPolicy())
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	payload := string(encoded)
	for _, field := range []string{"mode", "automatic_launches_per_parent_run", "active_child_limit", "over_budget_action", "require_write_isolation"} {
		if !strings.Contains(payload, `"`+field+`"`) {
			t.Fatalf("policy JSON %s is missing %q", payload, field)
		}
	}
	for _, removed := range []string{"absolute_wave_maximum", "max_depth"} {
		if strings.Contains(payload, `"`+removed+`"`) {
			t.Fatalf("policy JSON still contains removed field %q: %s", removed, payload)
		}
	}
}

func TestSubagentPolicyMapRejectsRemovedFields(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	input := map[string]any{
		"mode":                              "bounded",
		"automatic_launches_per_parent_run": 5,
		"active_child_limit":                5,
		"over_budget_action":                "ask",
		"require_write_isolation":           true,
		"absolute_wave_maximum":             16,
	}
	if _, err := service.UpdateSubagentPolicyMapForAccount("account-removed-fields", input); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("removed policy field error = %v, want unknown field rejection", err)
	}
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

func TestPolicyAcceptsPracticalSubagentLimits(t *testing.T) {
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 24
	policy.ActiveChildLimit = 12
	if err := ValidateSubagentPolicy(policy); err != nil {
		t.Fatalf("validate practical policy: %v", err)
	}
}

func TestReservationAutomaticBudgetCountsWavesRegardlessOfChildCount(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-practical"
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 2
	policy.ActiveChildLimit = 10
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	first := reserveSubagentWave(t, reader, accountScopeID, "session-practical", "run-practical", "call-1", 10)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first wave decision = %q, want approve", first.Decision)
	}
	if err := reader.FinishSubagentWave("session-practical", "run-practical", "call-1", "completed"); err != nil {
		t.Fatalf("finish first wave: %v", err)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-practical", "run-practical", "call-2", 10)
	if second.Decision != SubagentReservationApprove {
		t.Fatalf("second wave decision = %q, want approve", second.Decision)
	}
	if err := reader.FinishSubagentWave("session-practical", "run-practical", "call-2", "completed"); err != nil {
		t.Fatalf("finish second wave: %v", err)
	}
	third := reserveSubagentWave(t, reader, accountScopeID, "session-practical", "run-practical", "call-3", 1)
	if third.Decision != SubagentReservationAsk || third.Reason != "wave requires approval because the automatic wave budget is exhausted" {
		t.Fatalf("third wave after two automatic waves = %#v, want ask", third)
	}
}

func TestReservationActiveChildLimitIsPerCallAndAggregateHardCeiling(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-concurrency"
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 20
	policy.ActiveChildLimit = 3
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	oversized := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-per-call", "call-oversized", 4)
	if oversized.Decision != SubagentReservationDeny || oversized.Reason != "subagent wave exceeds active child limit of 3" {
		t.Fatalf("oversized single call = %#v, want hard deny", oversized)
	}
	first := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-aggregate", "call-1", 2)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first aggregate wave = %#v, want approve", first)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-aggregate", "call-2", 2)
	if second.Decision != SubagentReservationDeny || second.Reason != "active child concurrency limit would be exceeded" {
		t.Fatalf("aggregate overflow = %#v, want hard deny", second)
	}
}

func TestReservationReloadsActiveChildLimitChangedDuringRun(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-1"
	first := reserveSubagentWave(t, reader, accountScopeID, "session-1", "run-1", "call-1", 1)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first wave decision = %q, want approve", first.Decision)
	}
	updated := DefaultSubagentPolicy()
	updated.ActiveChildLimit = 1
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, updated); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-1", "run-1", "call-2", 1)
	if second.Decision != SubagentReservationDeny || second.Reason != "active child concurrency limit would be exceeded" {
		t.Fatalf("second wave did not use updated active child limit: %#v", second)
	}
}

func TestReservationReloadsAutomaticLaunchLimitChangedDuringRun(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-1"
	first := reserveSubagentWave(t, reader, accountScopeID, "session-1", "run-1", "call-1", 1)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first wave decision = %q, want approve", first.Decision)
	}
	updated := DefaultSubagentPolicy()
	updated.AutomaticLaunchesPerParentRun = 1
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, updated); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-1", "run-1", "call-2", 1)
	if second.Decision != SubagentReservationAsk || second.Reason != "wave requires approval because the automatic wave budget is exhausted" {
		t.Fatalf("second wave did not use updated automatic wave limit: %#v", second)
	}
}

func TestReservationUsesConfiguredDenyWhenAutomaticBudgetIsExhausted(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-deny"
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 1
	policy.OverBudgetAction = SubagentOverBudgetDeny
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	first := reserveSubagentWave(t, reader, accountScopeID, "session-deny", "run-deny", "call-1", 1)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first wave = %#v, want approve", first)
	}
	if err := reader.FinishSubagentWave("session-deny", "run-deny", "call-1", "completed"); err != nil {
		t.Fatalf("finish first wave: %v", err)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-deny", "run-deny", "call-2", 1)
	if second.Decision != SubagentReservationDeny || second.Reason != "automatic wave budget is exhausted" {
		t.Fatalf("over-budget wave = %#v, want configured deny", second)
	}
}

func TestReservationDeniesDelegationFromChildSession(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	result, err := service.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "child-session", RunID: "child-run", CallID: "child-call", ManifestHash: "child-manifest", LaunchCount: 1, Delegated: true,
	})
	if err != nil {
		t.Fatalf("reserve child delegation: %v", err)
	}
	if result.Decision != SubagentReservationDeny || result.Reason != "task delegation is parent-only; child sessions cannot delegate" {
		t.Fatalf("child delegation = %#v, want parent-only deny", result)
	}
}

func TestFailedSubagentWaveReleasesConcurrencyWithoutRefundingBudget(t *testing.T) {
	service, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-failed"
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 1
	policy.ActiveChildLimit = 5
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	first := reserveSubagentWave(t, service, accountScopeID, "session-failed", "run-failed", "call-1", 5)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first wave = %#v, want approve", first)
	}
	if err := service.FinishSubagentWave("session-failed", "run-failed", "call-1", "failed"); err != nil {
		t.Fatalf("finish failed wave: %v", err)
	}
	second := reserveSubagentWave(t, service, accountScopeID, "session-failed", "run-failed", "call-2", 1)
	if second.Decision != SubagentReservationAsk || second.Reason != "wave requires approval because the automatic wave budget is exhausted" {
		t.Fatalf("retry after failed wave = %#v, want released concurrency but retained one-wave accounting", second)
	}
}
