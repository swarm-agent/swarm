package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestProjectTaskDeclaredBlockerSurvivesProviderCompletion(t *testing.T) {
	// Purpose: the actual task_progress tool's structured current-run outcome must
	// survive completeRun and both realtime and HTTP projection. This integration
	// layer catches the original lost-blocker regression without a provider/network.
	server, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	storePath := filepath.Join(t.TempDir(), "blocked.pebble")
	store, err := pebblestore.Open(storePath)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	server.sessions = sessionruntime.NewService(pebblestore.NewSessionStore(store), nil)
	server.ConfigureProjectRealtime(store)
	db := server.sessions.Store()
	p := testPrincipal()
	project := &pebblestore.ProjectRecord{Name: "Blocked task"}
	if err := db.PutProject(p.AccountScopeID, project); err != nil { t.Fatal(err) }
	task := &pebblestore.ProjectTaskRecord{ID: "blocked-task", ProjectID: project.ID, AccountID: p.AccountScopeID, SessionID: "blocked-session", Agent: "coder", Status: "in_progress", Title: "Required input", Revision: 1}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil { t.Fatal(err) }
	runID := task.ExecutionRunID()
	now := time.Now().UnixMilli()
	snapshot := pebblestore.SessionSnapshot{ID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", WorkspacePath: t.TempDir(), Metadata: map[string]any{"project_id": project.ID, "task_id": task.ID}}
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		input.UserID, input.AccountScopeID = p.UserID, p.AccountScopeID
		return server.sessions.ApplySessionMutation(input)
	}
	if _, err := apply(sessionruntime.SessionMutationInput{SessionID: task.SessionID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, ClientRequestID: "create-blocked", IdempotencyKey: "create-blocked", PayloadHash: "create-blocked", RequestHash: "create-blocked", NowUnixMs: now}); err != nil { t.Fatal(err) }
	if _, err := apply(sessionruntime.SessionMutationInput{SessionID: task.SessionID, Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning}, ClientRequestID: "running-blocked", IdempotencyKey: "running-blocked", PayloadHash: "running-blocked", RequestHash: "running-blocked", NowUnixMs: now + 1}); err != nil { t.Fatal(err) }
	events, err := pebblestore.NewEventLog(store)
	if err != nil { t.Fatal(err) }
	runner := runruntime.NewService(server.sessions, nil, nil, tool.NewRuntime(1), nil, nil, nil, events)
	invoker := runner.NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{SessionID: task.SessionID, PermissionSessionID: task.SessionID, RunID: runID, SessionMode: "auto", Principal: p, ProviderManagedV3: true, ApplySessionMutation: apply})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "declare-blocker", Name: "task_progress", Arguments: `{"action":"blocked","blocker_code":"missing_selected_prototype","reason":"required prototype missing"}`})
	if err != nil || result.Error != "" || !strings.Contains(result.Output, `"lifecycle_state":"blocked"`) { t.Fatalf("tool: %#v %v", result, err) }
	executor := &sessionV3Executor{server: server}
	job := sessionV3ExecutorJob{SessionID: task.SessionID, RunID: runID, Principal: p}
	if _, err := executor.completeRun(job, sessionV3AssistantResponse{Content: "Waiting for input", StopReason: "stop"}); err != nil { t.Fatal(err) }
	assertBlocked := func(got *pebblestore.ProjectTaskRecord) {
		t.Helper()
		if got.Status != "blocked" || got.LastError != "required prototype missing" || !strings.Contains(got.ActionNeeded, "Supply required input") || got.IsIntegrated { t.Fatalf("lost blocker: %#v", got) }
	}
	persisted, ok, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok { t.Fatalf("task: %v", err) }
	assertBlocked(persisted)
	// A fresh read/reconcile must not overwrite the persisted terminal outcome.
	syncTaskSessionState(persisted, db)
	assertBlocked(persisted)
	t.Setenv("SWARM_API_NO_AUTH", "1")
	r := httptest.NewRequest(http.MethodGet, ProjectsPath+"/"+project.ID+"/tasks/"+task.ID, nil)
	requestContext := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	requestContext = context.WithValue(requestContext, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read", "sessions:read"}})
	w := httptest.NewRecorder()
	server.apiMux().ServeHTTP(w, r.WithContext(requestContext))
	if w.Code != http.StatusOK { t.Fatalf("GET: %d %s", w.Code, w.Body.String()) }
	var response struct { Task pebblestore.ProjectTaskRecord `json:"task"` }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	assertBlocked(&response.Task)
	// Restart discards all process-local lifecycle state; persisted metadata is authority.
	if err := store.Close(); err != nil { t.Fatal(err) }
	store, err = pebblestore.Open(storePath)
	if err != nil { t.Fatal(err) }
	server.sessions = sessionruntime.NewService(pebblestore.NewSessionStore(store), nil)
	db = server.sessions.Store()
	persisted, ok, err = db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok { t.Fatalf("restart task: %v", err) }
	syncTaskSessionState(persisted, db)
	assertBlocked(persisted)
	// Canonical reservation requires the current revision and explicit new input.
	if _, err := db.ReserveTaskFollowup(p.AccountScopeID, project.ID, task.ID, p.UserID, "supply-stale", "prototype supplied", persisted.Revision-1, now+20); err == nil { t.Fatal("stale revision accepted") }
	reopened, err := db.ReserveTaskFollowup(p.AccountScopeID, project.ID, task.ID, p.UserID, "supply", "prototype supplied", persisted.Revision, now+21)
	if err != nil { t.Fatal(err) }
	if reopened.SessionID == task.SessionID || reopened.ExecutionRunID() == runID { t.Fatal("reopen reused blocked execution") }
	syncTaskSessionState(reopened, db)
	if reopened.Status == "blocked" { t.Fatalf("old blocker poisoned new attempt: %#v", reopened) }
}

func TestProjectTaskDeclaredBlockerRejectsStaleAndForeignOutcomes(t *testing.T) {
	// Purpose: projectTaskDeclaredBlocker is the narrow projection boundary that
	// must reject old attempts, cross-account records and prose-only blockers,
	// without changing any task state.
	for _, invalid := range []string{"prior-run", "foreign-session", "foreign-state", "prose-only", "active"} {
		t.Run(invalid, func(t *testing.T) {
			task := pebblestore.ProjectTaskRecord{ID: "task", SessionID: "session", AccountID: "account", Status: "in_progress"}
			state := pebblestore.V3SessionRunState{RunID: task.ExecutionRunID(), AccountScopeID: "account", Status: pebblestore.V3RunIntentCompleted}
			sess := pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", Metadata: map[string]any{"lifecycle_signal": "blocked", "lifecycle_signal_run_id": state.RunID, "blocker_reason": "missing input"}}
			switch invalid {
			case "prior-run": sess.Metadata["lifecycle_signal_run_id"] = "old-run"
			case "foreign-session": sess.AccountScopeID = "foreign"
			case "foreign-state": state.AccountScopeID = "foreign"
			case "prose-only": delete(sess.Metadata, "lifecycle_signal")
			case "active": state.Active = true
			}
			if projectTaskDeclaredBlocker(&task, sess, state) || task.Status != "in_progress" || task.LastError != "" || task.ActionNeeded != "" { t.Fatalf("unauthorized outcome changed task: %#v", task) }
		})
	}
}
