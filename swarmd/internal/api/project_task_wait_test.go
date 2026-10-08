package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: prove the real provider/tool dispatch -> canonical wait -> task
// publication -> executor continuation path. A deterministic provider fixture is
// used, not a live model benchmark. Extra provider steps (including timer-only
// refill/recovery) fail the test. Lifecycle publication and report attention/wake
// requests share this path. This is the narrowest end-to-end runtime layer.
func TestProjectTaskWaitExecutorEndToEnd(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, wakeKind := range []string{"lifecycle", "attention", "wake_request"} {
			t.Run(fmt.Sprintf("%v/%s", managed, wakeKind), func(t *testing.T) {
				server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				p := testPrincipal()
				settings := testAgentModelSettingsRecord(p.AccountScopeID)
				settings.Swarm.Action = pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "medium"}
				if _, err := pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying()).PutForAccount(settings); err != nil {
					t.Fatal(err)
				}
				if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "wait-project", Name: "Wait project"}); err != nil {
					t.Fatal(err)
				}
				w := projectConversationRequest(t, server, p, http.MethodPost, ProjectsPath+"/wait-project/sessions", map[string]any{"client_request_id": "wait-conversation", "mode": "auto"})
				if w.Code != http.StatusOK {
					t.Fatalf("conversation: %d %s", w.Code, w.Body.String())
				}
				parents, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "wait-project", 10)
				if err != nil || len(parents) != 1 {
					t.Fatalf("parents=%+v err=%v", parents, err)
				}
				parent := parents[0]
				createTestSession(t, sessions, "wait-child", p.AccountScopeID, map[string]any{"project_id": "wait-project", "task_id": "wait-task"})
				if err := sessions.Store().PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "wait-task", ProjectID: "wait-project", Title: "Delegated audit", Agent: "finder", SessionID: "wait-child", Status: "in_progress"}); err != nil {
					t.Fatal(err)
				}
				var calls, activeProviders atomic.Int32
				var concurrentProvider atomic.Bool
				runner := &sessionsV3RecordingProviderRunner{id: "codex"}
				runner.handler = func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
					if activeProviders.Add(1) != 1 {
						concurrentProvider.Store(true)
					}
					defer activeProviders.Add(-1)
					switch calls.Add(1) {
					case 1:
						call := provideriface.FunctionCall{CallID: "wait-call", Name: "manage_projects", Arguments: `{"action":"wait_tasks","project_id":"wait-project","task_ids":["wait-task"]}`}
						if managed {
							result, err := req.ToolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
							if err != nil {
								return provideriface.Response{}, err
							}
							if result.Error != "" || !result.RestartTurn {
								return provideriface.Response{}, fmt.Errorf("wait result=%+v", result)
							}
							return provideriface.Response{RestartTurn: true}, nil
						}
						return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{call}}, nil
					case 2:
						if !sessionsV3TraceInputContains(req.Input, "attempt_id") || !sessionsV3TraceInputContains(req.Input, "inspect_files") || !sessionsV3TraceInputContains(req.Input, "project_result") {
							return provideriface.Response{}, fmt.Errorf("missing inspection guidance in resume: %+v", req.Input)
						}
						if !sessionsV3TraceInputContains(req.Input, "Delegated project task outcomes") || !sessionsV3TraceInputContains(req.Input, map[bool]string{true: "needs_review", false: "Report requests attention"}[wakeKind == "lifecycle"]) || !sessionsV3TraceInputContains(req.Input, "wait-task") {
							return provideriface.Response{}, fmt.Errorf("missing outcome in resume: %+v", req.Input)
						}
						return provideriface.Response{Text: "Audit is ready for user review, not accepted.", StopReason: "stop"}, nil
					default:
						return provideriface.Response{}, fmt.Errorf("unexpected provider execution while waiting")
					}
				}
				providers := registry.New()
				providers.RegisterRunner(runner)
				server.providers = providers
				runtime := tool.NewRuntime(1)
				runtime.SetManageProjectStore(sessions.Store())
				runtime.SetManageSessionService(sessions)
				runSvc := runruntime.NewService(sessions, server.model, providers, runtime, server.perm.(*permission.Service), server.agents, nil, nil)
				runSvc.SetAgentModelSettingsService(server.agentModelSettings)
				server.runner = runSvc
				server.SetBypassPermissions(true)
				server.ConfigureProjectRealtime(sessions.Store().Underlying())
				exec := newSessionV3Executor(server)
				exec.startDelay = 0
				server.v3SessionExecutor = exec
				postSessionsV3PrimaryTestMessage(t, server, parent.ID, "wait-goal", "Review the delegated audit when it is ready.")
				deadline := time.Now().Add(5 * time.Second)
				var owner pebblestore.V3SessionRunIntent
				for time.Now().Before(deadline) {
					state, ok, err := sessions.GetSessionRunState(parent.ID)
					if err != nil {
						t.Fatal(err)
					}
					if ok && state.Status == pebblestore.V3RunIntentWaitingTasks {
						owner, _, _ = sessions.GetSessionRunIntent(parent.ID, state.RunID)
						break
					}
					if ok && (state.Status == "failed" || state.Status == "completed") {
						t.Fatalf("did not yield: %+v", state)
					}
					time.Sleep(time.Millisecond)
				}
				if owner.RunID == "" {
					t.Fatal("never registered durable wait")
				}
				for time.Now().Before(deadline) {
					exec.mu.Lock()
					n := len(exec.inFlightRuns)
					exec.mu.Unlock()
					if n == 0 {
						break
					}
					time.Sleep(time.Millisecond)
				}
				exec.mu.Lock()
				remaining := len(exec.inFlightRuns)
				exec.mu.Unlock()
				if remaining != 0 {
					t.Fatal("wait retained executor capacity")
				}
				// Drive the existing time-based executor maintenance entry points explicitly.
				// They may inspect state, but must never admit waiting provider work.
				for i := 0; i < 3; i++ {
					exec.recoverDurableRuns(context.Background())
					exec.refillPendingBacklog()
				}
				time.Sleep(30 * time.Millisecond)
				if calls.Load() != 1 {
					t.Fatalf("time-only calls=%d", calls.Load())
				}
				if wakeKind == "lifecycle" {
					_, err = sessions.Store().UpdateProjectTask(p.AccountScopeID, "wait-project", "wait-task", func(task *pebblestore.ProjectTaskRecord) error {
						task.Status = "needs_review"
						task.ActionNeeded = "Review the completed audit"
						return nil
					})
				} else {
					for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning} {
						_, err = sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "wait-child", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", ParentSessionID: parent.ID, Status: status}})
						if err != nil {
							t.Fatal(err)
						}
					}
					_, err = sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "wait-child", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationReportTask, ClientRequestID: "report", PayloadHash: "derived", TaskReport: &pebblestore.V3ProjectTaskReportMutation{RunID: "child-run", ProjectID: "wait-project", TaskID: "wait-task", Kind: pebblestore.ProjectTaskUpdateKind(wakeKind), Summary: "Report requests attention"}})
				}
				if err != nil {
					t.Fatal(err)
				}
				deadline = time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					state, ok, _ := sessions.GetSessionRunState(parent.ID)
					if ok && state.RunID == pebblestore.ProjectTaskWaitResumeID(owner.RunID) && state.Status == "completed" {
						break
					}
					if ok && state.Status == "failed" {
						t.Fatalf("resume failed: %+v", state)
					}
					time.Sleep(time.Millisecond)
				}
				state, _, _ := sessions.GetSessionRunState(parent.ID)
				if concurrentProvider.Load() || activeProviders.Load() != 0 {
					t.Fatal("parent provider executions overlapped or remained active")
				}
				if calls.Load() != 2 || state.Status != "completed" || state.RunID != pebblestore.ProjectTaskWaitResumeID(owner.RunID) {
					t.Fatalf("resume calls=%d state=%+v", calls.Load(), state)
				}
				messages, err := sessions.ListSessionMessages(parent.ID, 0, 30)
				if err != nil {
					t.Fatal(err)
				}
				outcomes := 0
				for _, message := range messages {
					if strings.HasPrefix(message.Content, "Delegated project task outcomes:") {
						outcomes++
					}
				}
				if outcomes != 1 {
					t.Fatalf("outcome delivery count=%d", outcomes)
				}
			})
		}
	}
}

// Purpose: Stop must cancel an inactive durable wait and the exact queued wake
// that races its visible run ID. Real CancelRun/storage guards, not status-only
// mocks, prove no stale pending continuation can be admitted afterward.
func TestProjectTaskWaitStopAfterWakeClaim(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			p := testPrincipal()
			db := sessions.Store()
			if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"parent", "child"} {
				metadata := map[string]any{"project_id": "project", "task_id": "task"}
				if id == "parent" {
					metadata = map[string]any{"project_id": "project", "swarm_v3_project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
				}
				_, err := db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: id, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &pebblestore.SessionSnapshot{ID: id, Metadata: metadata}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Audit", Agent: "finder", Status: "in_progress", SessionID: "child"}); err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{"pending_executor", "running"} {
				_, err := db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "parent", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "goal", Status: status}})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "parent", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "wait", PayloadHash: "wait", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: "goal", ProjectID: "project", TaskIDs: []string{"task"}}})
			if err != nil {
				t.Fatal(err)
			}
			target := "goal"
			if ready {
				if _, err := db.UpdateProjectTask(p.AccountScopeID, "project", "task", func(task *pebblestore.ProjectTaskRecord) error { task.Status = "needs_review"; return nil }); err != nil {
					t.Fatal(err)
				}
				if err := db.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
				target = pebblestore.ProjectTaskWaitResumeID("goal")
			}
			exec := &sessionV3Executor{server: server, runStates: map[string]*sessionV3ExecutorRunState{}}
			_, cancelled, err := exec.CancelRun(sessionV3ExecutorJob{Principal: p, SessionID: "parent", RunID: "goal"}, "user stop")
			if err != nil || !cancelled {
				t.Fatalf("stop=%v %v", cancelled, err)
			}
			current, _, _ := db.GetV3SessionRunIntent("parent", target)
			if current.Status != "cancelled" {
				t.Fatal(current)
			}
			if err := db.ReconcileProjectTaskWaits("", "", nil); err != nil {
				t.Fatal(err)
			}
			if exec.EnqueueRun(sessionV3ExecutorJob{Principal: p, SessionID: "parent", RunID: target}) {
				t.Fatal("cancelled continuation admitted")
			}
		})
	}
}
