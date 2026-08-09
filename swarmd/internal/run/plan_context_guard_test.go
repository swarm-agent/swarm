package run

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanContextGuardPolicyAndThresholdCrossing(t *testing.T) {
	guard := NewPlanContextGuard(map[string]any{
		planContextGuardThresholdMetadataKey:   "75%",
		planContextGuardMaxCompactsMetadataKey: 1,
	})
	summary := pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 749, Source: "codex_api_usage"}
	if guard.Observe(summary) {
		t.Fatal("guard armed below threshold")
	}
	summary.TotalTokens = 750
	if !guard.Observe(summary) {
		t.Fatal("guard did not arm on threshold crossing")
	}
	if guard.Observe(summary) {
		t.Fatal("guard emitted more than one warning while usage stayed above the boundary")
	}
	if !guard.BeginDecision() || !guard.DecisionActive() || guard.FinalizationOnly() {
		t.Fatalf("unexpected first decision state: active=%v finalization_only=%v", guard.DecisionActive(), guard.FinalizationOnly())
	}
	if guard.BeginDecision() {
		t.Fatal("guard re-armed without another threshold crossing")
	}
	guard.RecordCompaction()
	summary.TotalTokens = 0
	guard.Observe(summary)
	summary.TotalTokens = 800
	if !guard.Observe(summary) || !guard.BeginDecision() || !guard.FinalizationOnly() {
		t.Fatal("guard did not enforce finalization-only after compaction budget")
	}
	if !strings.Contains(guard.WarningInstructions(), "Call exit_plan_mode now") || strings.Contains(guard.WarningInstructions(), "call compact") {
		t.Fatalf("finalization-only instructions allow the wrong control: %q", guard.WarningInstructions())
	}
}

func TestPlanContextGuardRejectsUnavailableOrUntrustedUsageAndBoundsRefusals(t *testing.T) {
	guard := NewPlanContextGuard(nil)
	for _, summary := range []pebblestore.SessionUsageSummary{
		{ContextWindow: 0, TotalTokens: 900, Source: "codex_api_usage"},
		{ContextWindow: 1000, TotalTokens: -1, Source: "codex_api_usage"},
		{ContextWindow: 1000, TotalTokens: 900, Source: "estimated"},
	} {
		if guard.Observe(summary) {
			t.Fatalf("guard trusted unavailable or non-provider usage: %+v", summary)
		}
	}
	if !guard.Observe(pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "copilot_session_usage"}) {
		t.Fatal("guard rejected trusted copilot usage")
	}
	if !guard.BeginDecision() {
		t.Fatal("guard decision did not begin")
	}
	if err := guard.RecordRefusal(); err != nil {
		t.Fatalf("first refusal should be bounded retry: %v", err)
	}
	if !guard.BeginDecision() {
		t.Fatal("guard did not re-arm after first refusal")
	}
	if err := guard.RecordRefusal(); err == nil || !strings.Contains(err.Error(), "stopped the run") {
		t.Fatalf("second refusal did not terminate clearly: %v", err)
	}
}

func TestPlanContextGuardExitChoiceLeavesCompactionBudgetUnused(t *testing.T) {
	guard := NewConfiguredPlanContextGuard(true, 80, 1)
	if !guard.Observe(pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 800, Source: "codex_api_usage"}) || !guard.BeginDecision() {
		t.Fatal("guard did not arm an exit decision")
	}
	if guard.FinalizationOnly() {
		t.Fatal("first decision unexpectedly exhausted compaction budget")
	}
	// A successful exit_plan_mode is terminal for the provider loop, so no refusal
	// or compaction transition is recorded. The decision remains distinguishable
	// from a compact choice until that terminal result ends the run.
	if !guard.DecisionActive() {
		t.Fatal("exit choice lost decision state before terminal handling")
	}
}

func TestPlanContextGuardZeroCompactionCapStartsFinalizationOnly(t *testing.T) {
	guard := NewConfiguredPlanContextGuard(true, 80, 0)
	if !guard.Observe(pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 800, Source: "google_api_usage"}) || !guard.BeginDecision() {
		t.Fatal("guard did not arm at the configured threshold")
	}
	if !guard.FinalizationOnly() {
		t.Fatal("zero compact allowance did not require immediate finalization")
	}
}

func TestPlanContextGuardCompactHandoff(t *testing.T) {
	handoff, err := PlanContextGuardCompactHandoff(`{"handoff":" goal, evidence, next action "}`)
	if err != nil {
		t.Fatalf("parse handoff: %v", err)
	}
	if handoff != "goal, evidence, next action" {
		t.Fatalf("handoff = %q", handoff)
	}
	if _, err := PlanContextGuardCompactHandoff(`{"handoff":""}`); err == nil {
		t.Fatal("empty handoff accepted")
	}
	if _, err := PlanContextGuardCompactHandoff(`{"handoff":"` + strings.Repeat("x", 9001) + `"}`); err == nil {
		t.Fatal("oversized handoff accepted")
	}
}

func TestPlanContextGuardCompactionPersistsCanonicalCheckpointAndBuildsContinuation(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "plan-guard-compaction.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionStore := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "plan-guard-session", UserID: "user-1", AccountScopeID: "account-1", Title: "Guard Test", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModePlan,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	plan, _, err := sessions.SavePlan(created.ID, "plan-guard", "Guard Plan", "Implement the context guard", "approved", "approved", true)
	if err != nil {
		t.Fatalf("save active plan: %v", err)
	}
	svc := NewService(sessions, nil, nil, nil, nil, nil, nil, events)
	const handoff = "Goal: finish guard coverage. Evidence: threshold path is implemented. Next: inspect settings tests."
	epochID, err := svc.ApplyPlanContextGuardCompaction(PlanContextGuardCompactionInput{
		SessionID: created.ID, RunID: "run-plan-guard", Handoff: handoff, ContextWindow: 1000, ProviderID: "codex", Model: "gpt-test", Step: 2,
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"},
	})
	if err != nil {
		t.Fatalf("apply guard compaction: %v", err)
	}
	if strings.TrimSpace(epochID) == "" {
		t.Fatal("guard compaction returned an empty continuation epoch")
	}
	epoch, ok, err := sessions.GetActiveExecutionEpoch(created.ID)
	if err != nil || !ok {
		t.Fatalf("get active epoch ok=%v err=%v", ok, err)
	}
	if epoch.EpochID != epochID || epoch.Boundary.Reason != "context_compaction_plan_guard" || epoch.Boundary.RunID != "run-plan-guard" {
		t.Fatalf("unexpected continuation epoch: %+v", epoch)
	}
	messages, err := sessions.ListMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "origin=plan_guard") || !strings.Contains(messages[0].Content, handoff) {
		t.Fatalf("durable compaction checkpoint missing handoff: %+v", messages)
	}
	if got := strings.TrimSpace(mapString(messages[0].Metadata, contextCompactionPlanLabelMetadataKey)); got != "Guard Plan (plan-guard)" {
		t.Fatalf("attached plan label = %q", got)
	}
	continuation := buildCompactedContinuationInput("original request", handoff, &plan, contextCompactionOriginPlanGuard)
	if len(continuation) != 1 {
		t.Fatalf("continuation items = %d, want 1", len(continuation))
	}
	text := strings.TrimSpace(mapString(continuation[0], "role")) + " " + strings.TrimSpace(strings.Join(flattenInputText(continuation), "\n"))
	for _, want := range []string{"user", "explicit research handoff", "original request", "Plan ID: plan-guard", handoff} {
		if !strings.Contains(text, want) {
			t.Fatalf("continuation missing %q: %s", want, text)
		}
	}
}

func flattenInputText(input []map[string]any) []string {
	out := make([]string, 0, len(input))
	for _, item := range input {
		content, _ := item["content"].([]map[string]any)
		for _, part := range content {
			if text, ok := part["text"].(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}
