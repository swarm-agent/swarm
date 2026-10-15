package run

import (
	"encoding/json"
	"strings"
	"testing"

	agent "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: bound first-request overhead at the real instruction, message and
// resolved provider-tool builders, preventing parent tutorials from crowding out
// worker assignments. This fixed small repository fixture measures bytes, not
// provider tokens or live workload performance.
func TestLeanContextBudget(t *testing.T) {
	s := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	scope := tool.WorkspaceScope{PrimaryPath: "/workspace/project", Roots: []string{"/workspace/project"}, WorktreeEnabled: true}
	for _, name := range []string{agent.WorkerCoderAgentID, agent.FinderAgentID, agent.ReviewerAgentID, "swarm"} {
		t.Run(name, func(t *testing.T) {
			registry, err := agent.BuiltinSystemAgentRegistry()
			if err != nil {
				t.Fatal(err)
			}
			id := name
			if name == "swarm" {
				id = agent.SwarmAgentID
			}
			p, err := registry.Materialize(id, store.AgentProfile{})
			if err != nil {
				t.Fatal(err)
			}
			_, _, disabled, err := s.ResolveAgentToolContract(p)
			if err != nil {
				t.Fatal(err)
			}
			base := s.composeInstructionsForScope(scope, p, "Repository instructions: keep changes scoped; never expose secrets.")
			runtime := s.ComposeRuntimeInstructions(scope, "auto", false, p, "Repository instructions: keep changes scoped; never expose secrets.")
			model := AppendResolvedModelPolicyInstructions(runtime, "auto", store.ModelPreference{Provider: "fixture", Model: "fixture", Thinking: "medium"})
			state, err := s.durableRunStateInstructions("fixture-session", "auto", "fixture-run", RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			state = ScopeDurableRunStateInstructions(state, p)
			msg, err := workerDirectRunMessage(store.WorkerRunRecord{Input: map[string]any{"prompt": "Implement the scoped change. Acceptance: focused tests pass.", "project_id": "fixture-project"}}, store.WorkerRecord{}, store.SessionPlanDocument{})
			if err != nil {
				t.Fatal(err)
			}
			schemas, err := json.Marshal(filterToolDefinitions(convertToolDefinitions(s.ListAgentToolDefinitions()), disabled))
			if err != nil {
				t.Fatal(err)
			}
			parts := map[string]int{"base": len(base), "runtime": len(runtime) - len(base), "model": len(model) - len(runtime), "state": len(state), "message": len(msg), "tools": len(schemas)}
			total := len(model) + len(state) + len(msg) + len(schemas)
			t.Logf("bytes parts=%v total=%d", parts, total)
			limit := 28000
			if name == agent.WorkerCoderAgentID {
				limit = 32000
			}
			if name == "swarm" {
				limit = 133780
			}
			if total > limit {
				t.Errorf("context %d exceeds %d: %v", total, limit, parts)
			}
			if name != "swarm" {
				for _, forbidden := range []string{"plan_manage", "checkpoint", "Desktop artifact"} {
					if strings.Contains(strings.ToLower(model+state), strings.ToLower(forbidden)) {
						t.Errorf("worker prompt contains %q", forbidden)
					}
				}
			}
			if strings.Count(msg, "Implement the scoped change.") != 1 {
				t.Fatalf("duplicated assignment: %s", msg)
			}
		})
	}
}

// Purpose: workerDirectRunMessage must carry the brief once without mutating
// durable input or dropping other untrusted data. This builder-level test also
// checks empty/error paths so invalid jobs cannot masquerade as usable requests.
func TestLeanWorkerMessage(t *testing.T) {
	input := map[string]any{"prompt": "Unique brief", "metadata": "untrusted evidence"}
	msg, err := workerDirectRunMessage(store.WorkerRunRecord{Input: input}, store.WorkerRecord{}, store.SessionPlanDocument{})
	if err != nil || strings.Count(msg, "Unique brief") != 1 || !strings.Contains(msg, "untrusted evidence") {
		t.Fatalf("message=%q err=%v", msg, err)
	}
	if input["prompt"] != "Unique brief" {
		t.Fatal("input mutated")
	}
	if _, err := workerDirectRunMessage(store.WorkerRunRecord{}, store.WorkerRecord{}, store.SessionPlanDocument{}); err == nil {
		t.Fatal("empty job accepted")
	}
	input["invalid"] = make(chan int)
	if _, err := workerDirectRunMessage(store.WorkerRunRecord{Input: input}, store.WorkerRecord{}, store.SessionPlanDocument{}); err == nil {
		t.Fatal("unserializable input accepted")
	}
}
