package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: error/help envelopes are not publication authority. The classifier is
// the narrowest layer proving that error precedence and missing/truncated output
// cannot stop continuation, even if another field looks like a successful plan.
func TestV3TerminalPlanRejectsErrors(t *testing.T) {
	for _, output := range []string{
		`{"tool":"exit_plan_mode","error":"missing document"}`,
		`{"status":"not_available","tool":"exit_plan_mode"}`,
		`{"status":"plan_submitted_for_review"}`,
		`{"status":"plan_submitted_for_review","plan_id":`,
		`{"status":"plan_submitted_for_review","plan_id":"plan","error":"denied"}`,
		`{"status":"plan_submitted_for_review","plan_id":"plan","truncated_for_model":true}`,
		`{"status":"plan_submitted_for_review","plan_id":"plan","details_truncated":true}`,
	} {
		if _, ok := sessionsV3ProviderTerminalPlanToolResult([]provideriface.ToolExecutionResult{{Name: "exit_plan_mode", Output: output}}); ok {
			t.Fatalf("error envelope became terminal: %s", output)
		}
	}
	for _, name := range []string{"exit_plan_mode", "plan_manage"} {
		result := provideriface.ToolExecutionResult{Name: name, Error: "denied", Output: `{"status":"plan_submitted_for_review","plan_id":"plan","next_action":"await_user_approval"}`, TextForModel: `{"tool":"exit_plan_mode"}`}
		if _, ok := sessionsV3ProviderTerminalPlanToolResult([]provideriface.ToolExecutionResult{result}); ok || sessionsV3ProviderCheckpointRunToolResult([]provideriface.ToolExecutionResult{result}) {
			t.Fatalf("errored result became terminal: %+v", result)
		}
	}
}

// Purpose: reproduce discovery -> rejected help/missing document -> corrected
// publication through runProviderToolLoop and the actual run.Service invoker.
// Durable tool messages, exact plan binding and Plan mode are postconditions;
// provider prose and a tool-name wrapper must never substitute for publication.
func TestV3PlanPublicationErrorContinues(t *testing.T) {
	for _, invalid := range []string{`{"action":"help"}`, `{}`, `{"document":{}}`, `{"action":"help","end_without_plan":true}`} {
		t.Run(invalid, func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			configureAssistantOrderTestProvider(t, server)
			runner := installSessionsV3TestProvider(server, "unused")
			server.runner = runruntime.NewService(sessions, server.model, server.providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
			server.SetBypassPermissions(true)
			created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "plan-create", "plan", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
			current, _, err := sessions.SetMode(created.ID, sessionruntime.ModePlan)
			if err != nil {
				t.Fatal(err)
			}
			if current.Metadata == nil {
				current.Metadata = map[string]any{}
			}
			current.Metadata["project_id"] = "project"
			current.Metadata["task_id"] = "task"
			current.Metadata["task_attempt_id"] = "initial"
			if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: current.UserID, AccountScopeID: current.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &current, ClientRequestID: "task-link", IdempotencyKey: "task-link", PayloadHash: "task-link", RequestHash: "task-link"}); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: current.AccountScopeID, Title: "Plan", Agent: "plan", Status: "planning", SessionID: created.ID, WorkspacePath: current.WorkspacePath}
			if err := sessions.Store().PutProjectTask(current.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			exec := newSessionV3Executor(server)
			job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: task.ExecutionRunID(), EpochID: "epoch-00000000000000000001"}
			for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
				if _, err := exec.recordRunStatus(job, status, "", "session.assistant.started"); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := exec.resolveSessionV3Runtime(job)
			if err != nil {
				t.Fatal(err)
			}
			resolved.AgentProfile.RuntimeMode = pebblestore.AgentRuntimeModePlanAuto
			resolved.AgentProfile.ExitPlanModeEnabled = pebblestore.BoolPtr(true)
			baseReq, err := exec.sessionV3ProviderBaseRequest(job, resolved, []map[string]any{{"role": "user", "content": "Publish a task plan"}})
			if err != nil {
				t.Fatal(err)
			}
			runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
				switch runner.callCount {
				case 1:
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "discovery", Name: "list", Arguments: `{"path":"."}`}}}, nil
				case 2:
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "invalid-plan", Name: "exit_plan_mode", Arguments: invalid}}}, nil
				case 3:
					messages, err := sessions.ListSessionMessages(created.ID, 0, 30)
					if err != nil {
						return provideriface.Response{}, err
					}
					attributed := false
					for _, message := range messages {
						record, ok := sessionsV3DecodeProviderToolResultRecord(message.Content)
						if message.Role == "tool" && ok && record.CallID == "invalid-plan" && record.ToolName == "exit_plan_mode" && record.RunID == job.RunID && record.Error != "" {
							attributed = true
						}
					}
					if !attributed || !sessionsV3TraceInputContains(req.Input, "error") {
						t.Fatal("error outcome vanished before continuation")
					}
					before, _, err := sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
					if err != nil || before.Status != "planning" || before.PlanBinding != nil {
						t.Fatalf("premature publication: %+v %v", before, err)
					}
					if strings.Contains(invalid, "end_without_plan") {
						return provideriface.Response{Text: "Investigation done", StopReason: "stop"}, nil
					}
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "valid-plan", Name: "exit_plan_mode", Arguments: `{"document":{"id":"review-plan","title":"Review","info":{"goal":"Implement after acceptance"},"checkpoints":[{"id":"cp-1","title":"Implement","order":1,"status":"pending","tasks":["Implement"],"acceptance_criteria":["Reviewed"]}]}}`}}}, nil
				default:
					return provideriface.Response{}, fmt.Errorf("unexpected provider step %d", runner.callCount)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sink := newSessionV3DurableProgressSink(exec, job, cancel)
			defer sink.CloseAndFlush(ctx)
			result, err := exec.runProviderToolLoop(ctx, job, resolved, runner, baseReq, sink, nil)
			if strings.Contains(invalid, "end_without_plan") {
				if err == nil || !strings.Contains(err.Error(), "without publishing") || result.TerminalPlanHandled {
					t.Fatalf("missing plan falsely succeeded: %+v %v", result, err)
				}
				if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); err != nil {
					t.Fatal(err)
				}
				hydrated, _, err := sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
				if err != nil {
					t.Fatal(err)
				}
				hydrated.Status = "planning"
				syncTaskSessionState(hydrated, sessions.Store())
				if hydrated.Status != "failed" || hydrated.PlanBinding != nil || !strings.Contains(hydrated.ActionNeeded, "Retry planning") {
					t.Fatalf("hydration advertised success: %+v", hydrated)
				}
				if err := server.reconcileProjectTaskRunLifecycle(job, sessionruntime.RunIntentCompleted, ""); err != nil {
					t.Fatal(err)
				}
				failed, _, err := sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
				if err != nil || failed.Status != "failed" || failed.LastError == "" || !strings.Contains(failed.ActionNeeded, "Retry planning") || failed.PlanBinding != nil {
					t.Fatalf("misleading task outcome: %+v %v", failed, err)
				}
				return
			}
			if err != nil || !result.TerminalPlanHandled || runner.callCount != 3 {
				t.Fatalf("loop: %+v err=%v calls=%d", result, err, runner.callCount)
			}
			after, _, err := sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
			plan, found, planErr := sessions.Store().GetPlan(created.ID, "review-plan")
			if err != nil || planErr != nil || !found || after.Status != "pending_approval" || after.PlanBinding == nil || after.PlanBinding.SessionID != created.ID || after.PlanBinding.PlanID != plan.ID || after.PlanBinding.DefinitionRevision != plan.Version || after.PlanBinding.Receipt == "" || after.PlanDocument != nil || plan.ApprovalState != "pending" {
				t.Fatalf("not canonical review: %+v %+v %v %v", after, plan, err, planErr)
			}
			terminal := sessionV3ProviderTerminalPlanResult{Action: "exit_plan_mode", PlanID: plan.ID, Revision: fmt.Sprint(plan.Version), Receipt: after.PlanBinding.Receipt}
			if !exec.sessionV3TerminalPlanPublished(job, terminal) {
				t.Fatal("real publication rejected")
			}
			for _, mismatch := range []string{"receipt", "revision", "run", "account"} {
				bad, badJob := terminal, job
				switch mismatch {
				case "receipt":
					bad.Receipt = "stale"
				case "revision":
					bad.Revision = "0"
				case "run":
					badJob.RunID = "stale"
				case "account":
					badJob.Principal.AccountScopeID = "foreign"
				}
				if exec.sessionV3TerminalPlanPublished(badJob, bad) {
					t.Fatalf("accepted %s mismatch", mismatch)
				}
			}
			reloaded, _, err := sessions.GetSession(created.ID)
			if err != nil || reloaded.Mode != sessionruntime.ModePlan {
				t.Fatalf("publication started Auto: %+v %v", reloaded, err)
			}
		})
	}
}
