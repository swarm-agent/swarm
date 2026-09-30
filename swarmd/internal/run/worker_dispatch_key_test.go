package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: providerToolInvoker -> executeManageWorkersTool -> dispatchWorkerTool
// must preserve caller keys through WorkerExecutionService.Dispatch and durable
// AdmitWorkerRun. Threat: absent keys block assignments, rewritten keys bypass
// validation, retries duplicate execution, or new jobs reuse receipts. Temporary
// Git/Pebble with a counted enqueue boundary proves admission/intent postconditions
// without executing providers; pending acceptance and principal gates remain shut.
func TestWorkerDispatchKeyProviderContract(t *testing.T) {
	for _, name := range []string{"manage_workers", "manage_automation"} {
		for _, action := range []string{"request", "test"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				wakes := 0
				svc, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool {
					wakes++
					return true
				})
				ws := ss.Store().WorkerStore()
				profile, err := svc.ResolveWorkerModelProfile("account", nil)
				if err != nil {
					t.Fatal(err)
				}
				worker, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "summary", Instructions: "Read only", ModelProfile: profile, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				worker, err = execution.Activate("account", "owner", worker.ID, worker.Revision, map[string]string{"primary": workspaceID})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				invocations := 0
				invoke := func(key any, prompt string, profile store.AgentProfile) (string, string) {
					invocations++
					t.Helper()
					args := map[string]any{"action": action, "worker_id": worker.ID, "prompt": prompt}
					if key != nil {
						args["idempotency_key"] = key
					}
					raw, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
						sessionID: "orch-session", principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
						sessionMode: "auto", runID: "dispatch-key-test", providerManagedV3: true,
						applySessionMutation: ss.ApplySessionMutation, agentProfile: profile, terminalPlanState: &terminalPlanToolState{},
					})
					result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: name, CallID: fmt.Sprintf("dispatch-key-%d", invocations), Arguments: string(raw)})
					if err != nil {
						t.Fatal(err)
					}
					return result.Output, result.Error
				}
				orchestrator := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})
				accepted := worker
				worker, err = ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "pending", Instructions: "Read only", InitialLifecycleState: store.WorkerLifecycleStatePending, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ProposedBindings: map[string]string{"primary": workspaceID}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, failure := invoke("pending-job", "Summarize only", orchestrator); !strings.Contains(failure, "human acceptance") {
					t.Fatalf("pending worker accepted assignment: %q", failure)
				}
				pendingRuns, _, err := ss.ListWorkerRuns("account", worker.ID, 10, "")
				if err != nil || len(pendingRuns) != 0 || wakes != 0 {
					t.Fatalf("pending assignment mutated execution: %+v %v", pendingRuns, err)
				}
				worker = accepted
				assertRuns := func(count, wakeCount int) {
					t.Helper()
					runs, _, err := ss.ListWorkerRuns("account", worker.ID, 10, "")
					if err != nil || len(runs) != count || wakes != wakeCount {
						t.Fatalf("unexpected admission/execution: runs=%+v wakes=%d err=%v", runs, wakes, err)
					}
				}
				for _, key := range []any{nil, "", " \t "} {
					if _, failure := invoke(key, "Summarize only", orchestrator); !strings.Contains(failure, "idempotency_key required") {
						t.Fatalf("missing/blank key not rejected: %q", failure)
					}
					assertRuns(0, 0)
				}
				// Leading whitespace must reach store validation, not be silently
				// normalized into a different, accepted logical job key.
				if _, failure := invoke(" summary-job-1", "Summarize only", orchestrator); failure == "" {
					t.Fatal("noncanonical key silently rewritten")
				}
				assertRuns(0, 0)
				if _, failure := invoke("unauthorized", "Summarize only", store.AgentProfile{Name: "coder"}); !strings.Contains(failure, "exclusive") {
					t.Fatalf("non-Orchestrator admitted: %q", failure)
				}
				assertRuns(0, 0)
				decode := func(key, prompt string) store.WorkerRunRecord {
					t.Helper()
					output, failure := invoke(key, prompt, orchestrator)
					if failure != "" {
						t.Fatal(failure)
					}
					var response struct {
						Run store.WorkerRunRecord `json:"run"`
					}
					if err := json.Unmarshal([]byte(output), &response); err != nil {
						t.Fatal(err)
					}
					return response.Run
				}
				first := decode("summary-job-1", "Summarize only")
				wantSource := "orchestrator"
				if action == "test" {
					wantSource = "test_run"
				}
				if first.ID == "" || first.SessionID == "" || first.Status != "running" || first.RequestSource != wantSource || first.Input["prompt"] != "Summarize only" {
					t.Fatalf("key/input/source lost at admission: %+v", first)
				}
				intent, found, err := ss.GetSessionRunIntent(first.SessionID, first.ID)
				if err != nil || !found || intent.PlanID == "" {
					t.Fatalf("no canonical intent: %+v %v", intent, err)
				}
				var keyRecord struct {
					RunID string `json:"run_id"`
				}
				if found, err := ss.Store().Underlying().GetJSON(store.KeyWorkerRunIdempotency("account", "summary-job-1"), &keyRecord); err != nil || !found || keyRecord.RunID != first.ID {
					t.Fatalf("caller key not retained unchanged: %+v %v", keyRecord, err)
				}
				assertRuns(1, 1)
				retry := decode("summary-job-1", "Summarize only")
				if retry.ID != first.ID || retry.SessionID != first.SessionID {
					t.Fatalf("retry allocated duplicate: %+v %+v", first, retry)
				}
				assertRuns(1, 1)
				if _, failure := invoke("summary-job-1", "Different payload", orchestrator); failure == "" {
					t.Fatal("same key with changed payload accepted")
				}
				assertRuns(1, 1)
				next := decode("summary-job-2", "Summarize only")
				if next.ID == first.ID || next.SessionID == first.SessionID {
					t.Fatalf("different key reused receipt: %+v", next)
				}
				assertRuns(2, 2)
				current, found, err := ws.GetWorker("account", worker.ID)
				if err != nil || !found || current.Revision != worker.Revision || current.LifecycleState != worker.LifecycleState {
					t.Fatalf("dispatch changed worker acceptance: %+v %v", current, err)
				}
			})
		}
	}
}
