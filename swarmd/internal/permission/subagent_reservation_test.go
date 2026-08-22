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
	return reserveSubagentWaveWithMode(t, service, accountScopeID, sessionID, runID, callID, launchCount, false)
}

func reserveSubagentWaveWithMode(t *testing.T, service *Service, accountScopeID, sessionID, runID, callID string, launchCount int, swarmMode bool) SubagentReservationResult {
	t.Helper()
	result, err := service.ReserveSubagentWave(SubagentReservationRequest{
		SessionID:      sessionID,
		AccountScopeID: accountScopeID,
		RunID:          runID,
		CallID:         callID,
		ManifestHash:   "manifest-" + callID,
		LaunchCount:    launchCount,
		SwarmMode:      swarmMode,
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
	for _, field := range []string{"mode", "automatic_launches_per_parent_run", "active_child_limit", "swarm_active_child_limit", "over_budget_action", "require_write_isolation"} {
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
		"swarm_active_child_limit":          5,
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
	policy.SwarmActiveChildLimit = 100
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

func TestReservationActiveChildLimitAsksForPerCallAndAggregateOverflow(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-concurrency"
	policy := DefaultSubagentPolicy()
	policy.AutomaticLaunchesPerParentRun = 20
	policy.ActiveChildLimit = 3
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	oversized := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-per-call", "call-oversized", 4)
	if oversized.Decision != SubagentReservationAsk || oversized.Reason != "wave requires approval because subagent wave exceeds default subagent limit of 3" {
		t.Fatalf("oversized single call = %#v, want ask", oversized)
	}
	first := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-aggregate", "call-1", 2)
	if first.Decision != SubagentReservationApprove {
		t.Fatalf("first aggregate wave = %#v, want approve", first)
	}
	second := reserveSubagentWave(t, reader, accountScopeID, "session-concurrency", "run-aggregate", "call-2", 2)
	if second.Decision != SubagentReservationAsk || second.Reason != "wave requires approval because default subagent limit would be exceeded by active child concurrency" {
		t.Fatalf("aggregate overflow = %#v, want ask", second)
	}
}

func TestReservationUsesSeparateRegularAndSwarmLimits(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-split-limits"
	policy := DefaultSubagentPolicy()
	policy.ActiveChildLimit = 10
	policy.SwarmActiveChildLimit = 100
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	regular := reserveSubagentWave(t, reader, accountScopeID, "session-split", "run-regular", "call-regular", 11)
	if regular.Decision != SubagentReservationAsk || regular.Reason != "wave requires approval because subagent wave exceeds default subagent limit of 10" {
		t.Fatalf("regular wave = %#v, want default-limit ask", regular)
	}
	requestedSwarm := reserveSubagentWaveWithMode(t, reader, accountScopeID, "session-split", "run-swarm", "call-swarm", 100, true)
	if requestedSwarm.Decision != SubagentReservationApprove {
		t.Fatalf("swarm wave = %#v, want approve", requestedSwarm)
	}
	oversizedSwarm := reserveSubagentWaveWithMode(t, reader, accountScopeID, "session-split", "run-swarm-oversized", "call-swarm-oversized", 101, true)
	if oversizedSwarm.Decision != SubagentReservationAsk || oversizedSwarm.Reason != "wave requires approval because subagent wave exceeds swarm-mode subagent limit of 100" {
		t.Fatalf("oversized swarm wave = %#v, want swarm-limit ask", oversizedSwarm)
	}

	activeRegular := reserveSubagentWave(t, reader, accountScopeID, "session-split", "run-independent-pools", "call-active-regular", 10)
	if activeRegular.Decision != SubagentReservationApprove {
		t.Fatalf("active regular wave = %#v, want approve", activeRegular)
	}
	concurrentSwarm := reserveSubagentWaveWithMode(t, reader, accountScopeID, "session-split", "run-independent-pools", "call-concurrent-swarm", 100, true)
	if concurrentSwarm.Decision != SubagentReservationApprove {
		t.Fatalf("concurrent swarm wave = %#v, want independent-pool approve", concurrentSwarm)
	}
}

func TestSubagentPolicyMapDefaultsOmittedSwarmLimitToRegularLimit(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	updated, err := service.UpdateSubagentPolicyMapForAccount("account-legacy-client", map[string]any{
		"mode":                              "bounded",
		"automatic_launches_per_parent_run": 5,
		"active_child_limit":                10,
		"over_budget_action":                "ask",
		"require_write_isolation":           true,
	})
	if err != nil {
		t.Fatalf("update legacy policy: %v", err)
	}
	if got := int(updated["swarm_active_child_limit"].(float64)); got != 10 {
		t.Fatalf("swarm active child limit = %d, want legacy active child limit 10", got)
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
	if second.Decision != SubagentReservationAsk || second.Reason != "wave requires approval because default subagent limit would be exceeded by active child concurrency" {
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

func TestReservationUsesConfiguredDenyForRegularAndSwarmLimitOverflow(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-limit-deny"
	policy := DefaultSubagentPolicy()
	policy.ActiveChildLimit = 2
	policy.SwarmActiveChildLimit = 3
	policy.OverBudgetAction = SubagentOverBudgetDeny
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	regular := reserveSubagentWave(t, reader, accountScopeID, "session-limit-deny", "run-regular", "call-regular", 3)
	if regular.Decision != SubagentReservationDeny || regular.Reason != "subagent wave exceeds default subagent limit of 2" {
		t.Fatalf("regular over-limit wave = %#v, want configured deny", regular)
	}
	swarm := reserveSubagentWaveWithMode(t, reader, accountScopeID, "session-limit-deny", "run-swarm", "call-swarm", 4, true)
	if swarm.Decision != SubagentReservationDeny || swarm.Reason != "subagent wave exceeds swarm-mode subagent limit of 3" {
		t.Fatalf("swarm over-limit wave = %#v, want configured deny", swarm)
	}
}

func TestReservationAsksThroughCanonicalPermissionFlow(t *testing.T) {
	service, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-limit-ask"
	policy := DefaultSubagentPolicy()
	policy.ActiveChildLimit = 1
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	reserved := reserveSubagentWave(t, service, accountScopeID, "session-limit-ask", "run-limit-ask", "call-limit-ask", 2)
	authorized, err := service.AuthorizeToolCall(AuthorizationInput{
		SessionID: "session-limit-ask", AccountScopeID: accountScopeID, RunID: "run-limit-ask", CallID: "call-limit-ask",
		ToolName: "task", ToolArguments: `{}`, ToolCallArguments: `{}`, Mode: "auto", SubagentReservation: &reserved,
	})
	if err != nil {
		t.Fatalf("authorize over-limit wave: %v", err)
	}
	if authorized.Decision != AuthorizationPending || authorized.Source != "subagent_orchestration" || authorized.Record == nil {
		t.Fatalf("over-limit authorization = %#v, want pending subagent approval", authorized)
	}
	if reserved.Reservation.ActiveCount != 2 {
		t.Fatalf("reserved active count = %d, want exact approved wave count 2", reserved.Reservation.ActiveCount)
	}
	resolved, err := service.Resolve("session-limit-ask", authorized.Record.ID, "allow_once", "approved exact over-limit wave")
	if err != nil {
		t.Fatalf("approve over-limit wave: %v", err)
	}
	if resolved.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("resolved permission status = %q, want approved", resolved.Status)
	}
	idempotent := reserveSubagentWave(t, service, accountScopeID, "session-limit-ask", "run-limit-ask", "call-limit-ask", 2)
	if idempotent.Decision != SubagentReservationAsk || idempotent.Reservation.ActiveCount != 2 {
		t.Fatalf("idempotent approved reservation = %#v, want intact exact ask reservation", idempotent)
	}
}

func TestReservationPreservesDirectModeAndAbsoluteSafetyDenials(t *testing.T) {
	reader, writer := openSubagentReservationTestServices(t)
	const accountScopeID = "account-hard-denials"
	policy := DefaultSubagentPolicy()
	policy.Mode = SubagentModeDirect
	if _, err := writer.UpdateSubagentPolicyForAccount(accountScopeID, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	direct := reserveSubagentWave(t, reader, accountScopeID, "session-hard-denials", "run-direct", "call-direct", 1)
	if direct.Decision != SubagentReservationDeny || direct.Reason != "direct orchestration mode denies delegation" {
		t.Fatalf("direct mode wave = %#v, want hard deny", direct)
	}

	absolute, err := reader.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "session-hard-denials", AccountScopeID: accountScopeID, RunID: "run-absolute", CallID: "call-absolute",
		ManifestHash: "manifest-absolute", LaunchCount: MaxSubagentWaveSize + 1,
	})
	if err != nil {
		t.Fatalf("reserve absolute overflow: %v", err)
	}
	if absolute.Decision != SubagentReservationDeny || absolute.Reason != "subagent wave exceeds absolute safety bound of 256" {
		t.Fatalf("absolute overflow = %#v, want hard deny", absolute)
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
