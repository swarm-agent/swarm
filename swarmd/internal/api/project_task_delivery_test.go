package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the production V3 provider loop must inject in-flight reports only at
// the next boundary, acknowledge only successful consumption, and never create a
// second run to drain a final response. A deterministic adapter plus real V3
// mutations/transport conversion is the narrowest runtime wiring proof, not a
// live provider benchmark.
func TestProjectTaskDeliveryProviderBoundary(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "terminal", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			p := testPrincipal()
			settings := testAgentModelSettingsRecord(p.AccountScopeID)
			settings.Swarm.Action = pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "medium"}
			if _, err := pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying()).PutForAccount(settings); err != nil {
				t.Fatal(err)
			}
			if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "delivery-project", Name: "Delivery"}); err != nil {
				t.Fatal(err)
			}
			w := projectConversationRequest(t, server, p, http.MethodPost, ProjectsPath+"/delivery-project/sessions", map[string]any{"client_request_id": "delivery-conversation", "mode": "auto"})
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			parents, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "delivery-project", 10)
			if err != nil || len(parents) != 1 {
				t.Fatal("missing parent", err)
			}
			parent := parents[0]
			runner := &sessionsV3RecordingProviderRunner{id: "codex"}
			providers := registry.New()
			providers.RegisterRunner(runner)
			server.providers = providers
			runtime := tool.NewRuntime(1)
			runtime.SetManageProjectStore(sessions.Store())
			runtime.SetManageSessionService(sessions)
			runSvc := runruntime.NewService(sessions, server.model, providers, runtime, nil, server.agents, nil, nil)
			runSvc.SetAgentModelSettingsService(server.agentModelSettings)
			server.runner = runSvc
			server.SetBypassPermissions(true)
			exec := newSessionV3Executor(server)
			job := sessionV3ExecutorJob{Principal: p, SessionID: parent.ID, RunID: "delivery-run", EpochID: "epoch-00000000000000000001"}
			for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning} {
				if _, err := exec.recordRunStatus(job, status, "", "session.run."+status); err != nil {
					t.Fatal(err)
				}
			}
			createTestSession(t, sessions, "delivery-child", p.AccountScopeID, map[string]any{"project_id": "delivery-project", "task_id": "delivery-task", "parent_session_id": parent.ID})
			if err := sessions.Store().PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "delivery-task", ProjectID: "delivery-project", Title: "Audit", Agent: "finder", SessionID: "delivery-child", Status: "in_progress"}); err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning} {
				_, err := sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "delivery-child", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", ParentSessionID: parent.ID, Status: status}})
				if err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := exec.resolveSessionV3Runtime(job)
			if err != nil {
				t.Fatal(err)
			}
			base, err := exec.sessionV3ProviderBaseRequest(job, resolved, []map[string]any{{"role": "user", "content": "Review delegated work."}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const marker = "unique-task-report-data"
			calls := 0
			pending := func() int {
				t.Helper()
				rows, _, err := sessions.Store().PendingProjectTaskUpdates(p.AccountScopeID, p.UserID, parent.ID, job.RunID, "")
				if err != nil {
					t.Fatal(err)
				}
				return len(rows)
			}
			runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
				calls++
				raw, _ := json.Marshal(codex.ToRequest(req).Input)
				if calls == 1 {
					if strings.Contains(string(raw), marker) {
						return provideriface.Response{}, errors.New("report preceded publication")
					}
					for i := 0; i < 2; i++ {
						_, err := sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "delivery-child", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationReportTask, ClientRequestID: "report", PayloadHash: "derived", TaskReport: &pebblestore.V3ProjectTaskReportMutation{RunID: "child-run", ProjectID: "delivery-project", TaskID: "delivery-task", Kind: pebblestore.ProjectTaskUpdateAttention, Summary: marker}})
						if err != nil {
							return provideriface.Response{}, err
						}
					}
					if pending() != 1 {
						return provideriface.Response{}, errors.New("enqueue acknowledged report")
					}
					if outcome == "terminal" {
						return provideriface.Response{Text: "Done", StopReason: "stop"}, nil
					}
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "inspect", Name: "manage_projects", Arguments: `{"action":"get","project_id":"delivery-project"}`}}}, nil
				}
				if strings.Count(string(raw), marker) != 1 || !strings.Contains(string(raw), "Untrusted delegated task-result data") {
					return provideriface.Response{}, errors.New("report missing, duplicated or trusted")
				}
				if pending() != 1 {
					return provideriface.Response{}, errors.New("receipt before successful response")
				}
				if outcome == "failure" && calls == 2 {
					return provideriface.Response{}, errors.New("injected transport failure")
				}
				if outcome == "cancelled" {
					cancel()
					return provideriface.Response{}, context.Canceled
				}
				return provideriface.Response{Text: "Report considered", StopReason: "stop"}, nil
			}
			runLoop := func() error {
				sink := newSessionV3DurableProgressSinkWithWriter(exec, job, func() {}, sessionsV3ReadLatencyNoopProgressWriter{})
				_, err := exec.runProviderToolLoop(ctx, job, resolved, runner, base, sink, nil)
				closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
				defer closeCancel()
				closeErr := sink.CloseAndFlush(closeCtx)
				if err != nil {
					return err
				}
				return closeErr
			}
			err = runLoop()
			if outcome == "failure" || outcome == "cancelled" {
				if err == nil || pending() != 1 {
					t.Fatal("failed step consumed update", err)
				}
				if outcome == "failure" {
					if err = runLoop(); err != nil {
						t.Fatal(err)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := 0
			if outcome == "terminal" || outcome == "cancelled" {
				want = 1
			}
			if pending() != want {
				t.Fatal("incorrect consumed state")
			}
			if outcome == "terminal" && calls != 1 {
				t.Fatal("terminal step restarted")
			}
			intents, err := sessions.Store().ListRunIntents(parent.ID, 10)
			if err != nil || len(intents) != 1 || intents[0].RunID != job.RunID {
				t.Fatal("report started another run", err)
			}
		})
	}
}
