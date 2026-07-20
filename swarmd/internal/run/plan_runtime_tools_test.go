package run

import (
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestPassiveNativePlanRuntimeCallsAreExcludedFromProviderTranscript(t *testing.T) {
	for _, action := range []string{"activate_plan", "focus_subtask", "complete_subtask", "complete_checkpoint", "checkpoint_outcome"} {
		if !isPassiveNativePlanRuntimeCall(tool.Call{Name: "plan_manage", Arguments: `{"action":"` + action + `"}`}) {
			t.Fatalf("%s should be passive", action)
		}
	}
	if isPassiveNativePlanRuntimeCall(tool.Call{Name: "plan_manage", Arguments: `{"action":"start_checkpoint"}`}) {
		t.Fatal("checkpoint start must retain provider/run lifecycle visibility")
	}
}

func newNativePlanRuntimeToolTestService(t testing.TB, titleBytes int) (*Service, string) {
	t.Helper()
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionStore := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session", UserID: "user", AccountScopeID: "account", Title: "test", WorkspacePath: t.TempDir(), WorkspaceName: "test", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutJSON(pebblestore.KeyPlanRuntimeAuthority(created.ID), pebblestore.PlanRuntimeAuthority{SchemaVersion: 1, SessionID: created.ID, PlanID: "plan", DefinitionRevision: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = sessionStore.PutPlanDefinition(pebblestore.PlanDefinitionWrite{Definition: pebblestore.PlanDefinition{SessionID: created.ID, PlanID: "plan", DefinitionRevision: 1, Title: strings.Repeat("definition", titleBytes/10), CheckpointOrder: []string{"cp-1"}}, Checkpoints: []pebblestore.CheckpointDefinition{{CheckpointID: "cp-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{sessions: sessions}, created.ID
}

func TestNativePlanRuntimeToolReturnsBoundedReceiptWithoutLegacyPlan(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionStore := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session", UserID: "user", AccountScopeID: "account", Title: "test", WorkspacePath: t.TempDir(), WorkspaceName: "test", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutJSON(pebblestore.KeyPlanRuntimeAuthority(created.ID), pebblestore.PlanRuntimeAuthority{SchemaVersion: 1, SessionID: created.ID, PlanID: "plan", DefinitionRevision: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = sessionStore.PutPlanDefinition(pebblestore.PlanDefinitionWrite{Definition: pebblestore.PlanDefinition{SessionID: created.ID, PlanID: "plan", DefinitionRevision: 1, CheckpointOrder: []string{"cp-1"}}, Checkpoints: []pebblestore.CheckpointDefinition{{CheckpointID: "cp-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{sessions: sessions}
	output, err := svc.executePlanManageToolWithLifecycleRunContext(created.ID, `{"action":"activate_plan","plan_id":"plan","definition_revision":1,"expected_execution_seq":0,"client_request_id":"activate"}`, "", nil, planLifecycleRunContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"plan":`, "checkpoint_order", "acceptance_criteria", "execution_state", "document"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("native receipt contains forbidden %q: %s", forbidden, output)
		}
	}
	for _, required := range []string{`"receipt":`, `"execution_seq":1`, `"high_water_mark":1`, `"path_id":"tool.plan-runtime.v3"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("native receipt missing %q: %s", required, output)
		}
	}
}

func BenchmarkNativePlanRuntimeToolReceiptIgnoresDefinitionBytes(b *testing.B) {
	for _, definitionBytes := range []int{100, 10_000, 100_000} {
		b.Run(fmt.Sprintf("definition_bytes_%d", definitionBytes), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				svc, sessionID := newNativePlanRuntimeToolTestService(b, definitionBytes)
				output, err := svc.executePlanManageToolWithLifecycleRunContext(sessionID, fmt.Sprintf(`{"action":"activate_plan","plan_id":"plan","definition_revision":1,"expected_execution_seq":0,"client_request_id":"activate-%d"}`, i), "", nil, planLifecycleRunContext{})
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(output)), "tool-result-bytes/op")
			}
		})
	}
}
