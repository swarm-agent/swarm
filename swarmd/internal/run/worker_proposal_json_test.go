package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeCreateOrProposePendingWorker must interpret object and legacy
// JSON-string documents identically, including raw activate_on_accept checks.
// Threat: a second inconsistent decode rejects the first on-demand proposal or
// lets encoded documents bypass policy/workspace checks. The provider invoker
// with real temporary Pebble/session state is the narrowest layer proving both
// tool dispatch and persistence postconditions; no provider execution is needed.
func TestWorkerProposalJSONContract(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		for _, scenario := range []string{"valid", "activation-disabled", "foreign-workspace", "malformed"} {
			label := "object/"
			if encoded {
				label = "json-string/"
			}
			t.Run(label+scenario, func(t *testing.T) {
				svc, ss, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
				settings := map[string]any{"workspace_id": workspaceID, "schedule": map[string]any{"kind": "trigger"}}
				wantError := ""
				switch scenario {
				case "activation-disabled":
					settings["activate_on_accept"] = false
					wantError = "unsupported activate_on_accept=false"
				case "foreign-workspace":
					settings["workspace_id"] = "ws_foreign"
					wantError = "not found or not accessible"
				case "malformed":
					wantError = "document invalid"
				}
				var document any = map[string]any{
					"title":     "On-demand reviewer",
					"info":      map[string]any{"goal": "Review on request", "context": "Preserve these standing instructions"},
					"worker_v2": settings,
				}
				if scenario == "malformed" {
					document = []any{"not a document"}
				}
				if encoded {
					raw, err := json.Marshal(document)
					if err != nil {
						t.Fatal(err)
					}
					document = string(raw)
					if scenario == "malformed" {
						document = `{"worker_v2":`
					}
				}
				args, err := json.Marshal(map[string]any{"action": "propose", "document": document})
				if err != nil {
					t.Fatal(err)
				}
				invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
					sessionID:   "orch-session",
					principal:   identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
					sessionMode: "auto", runID: "proposal-json-test", providerManagedV3: true,
					applySessionMutation: ss.ApplySessionMutation,
					agentProfile:         agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{}),
					terminalPlanState:    &terminalPlanToolState{},
				})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "manage_workers", CallID: "proposal-json", Arguments: string(args)})
				if err != nil {
					t.Fatal(err)
				}
				workers, err := ss.ListWorkers("account", store.ListWorkersQuery{Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				permissions, err := store.NewPermissionStore(ss.Store().Underlying()).ListPendingPermissions("orch-session", 10)
				if err != nil {
					t.Fatal(err)
				}
				if wantError != "" {
					if !strings.Contains(result.Error, wantError) {
						t.Fatalf("expected %q, got %+v", wantError, result)
					}
					if len(workers.Workers) != 0 || len(permissions) != 0 {
						t.Fatalf("rejected proposal mutated state: workers=%+v permissions=%+v", workers.Workers, permissions)
					}
					return
				}
				if result.Error != "" || len(workers.Workers) != 1 {
					t.Fatalf("proposal failed: %+v workers=%+v", result, workers.Workers)
				}
				worker := workers.Workers[0]
				if worker.Name != "On-demand reviewer" || worker.Description != "Review on request" || worker.Instructions != "Preserve these standing instructions" || worker.Revision != 1 || worker.LifecycleState != store.WorkerLifecycleStatePending || len(worker.Automations) != 0 || len(worker.LocalBindings) != 0 || worker.ProposedBindings["primary"] != workspaceID {
					t.Fatalf("proposal lost content or acceptance boundary: %+v", worker)
				}
				var output map[string]any
				if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
					t.Fatal(err)
				}
				if output["status"] != "pending_review" || output["next_action"] != "await_worker_acceptance" || output["worker_id"] != worker.ID {
					t.Fatalf("missing human acceptance review: %+v", output)
				}
				runs, _, err := ss.ListWorkerRuns("account", worker.ID, 10, "")
				if err != nil || len(runs) != 0 {
					t.Fatalf("proposal started execution: %+v %v", runs, err)
				}
				if _, found, err := ss.GetActivePlan("orch-session"); err != nil || found {
					t.Fatalf("proposal created ordinary session plan: found=%v err=%v", found, err)
				}
			})
		}
	}
}
