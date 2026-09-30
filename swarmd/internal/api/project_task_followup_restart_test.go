package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: registered reopen/history and canonical V3 completion must retain the
// original request, summaries, sessions and integration evidence through two
// follow-ups and two real store reopens. Threat: isolated happy paths miss loss at
// reservation/hydration/restart boundaries and stale failed-launch guidance after
// handleProjectTaskFollowup accepts a retry. Only launch-owned guidance may clear.
// reconcileProjectTaskRunLifecycle must leave both raw durable bytes and the full
// hydrated task unchanged for a prior attempt; compare snapshots from the same
// boundary, after all retries, rather than attributing retry UpdatedAt to completion.
// API + temporary Pebble + real Git is the narrow joined layer. completeRun persists real final messages and run
// metadata, not lifecycle summary fixtures; no provider executes.
func TestProjectTaskFollowupJoinedRestart(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() { f.db.Close() }()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	wire := func() {
		ss := pebblestore.NewSessionStore(f.db)
		el, err := pebblestore.NewEventLog(f.db)
		if err != nil {
			t.Fatal(err)
		}
		f.server.sessions = sessionruntime.NewService(ss, el)
		f.server.planLifecycle = sessionruntime.NewPlanLifecycleService(f.server.sessions)
		f.server.planLifecycle.SetApplySessionMutation(f.server.applySessionV3PrimaryMutation)
		f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
		f.server.worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
		f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
		f.server.agents = agentruntime.NewService(pebblestore.NewAgentStore(f.db), el)
		f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
		f.server.v3SessionExecutor = newSessionV3Executor(f.server)
		seedTaskSessionBinding(t, f, pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo})
	}
	wire()
	db := f.server.sessions.Store()
	project := &pebblestore.ProjectRecord{ID: "restart-project", Name: "Restart", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	mutate := func(sessionID, key string, kind string, snapshot *pebblestore.SessionSnapshot, run *pebblestore.V3SessionRunIntent) {
		t.Helper()
		_, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: kind, Session: snapshot, RunIntent: run})
		if err != nil {
			t.Fatal(err)
		}
	}
	original := pebblestore.SessionSnapshot{ID: "original", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"project_id": project.ID, "task_id": "task"}}
	mutate(original.ID, "original-create", sessionruntime.SessionMutationCreateSession, &original, nil)
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Task", Description: "Original requirements", Agent: "swarm", Status: "in_progress", SessionID: original.ID, Revision: 1, LastError: "Integration requires repair", ActionNeeded: "Review integration error before promotion", WorkspacePath: repo, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}, Integration: &pebblestore.ProjectTaskIntegration{State: "failed", SessionID: original.ID, SourceHead: base, TargetBranch: "dev", Error: "Fixture receipt; no promotion claimed"}, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "original-output", Title: "Original output", Kind: "report", Status: "ready"}}}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	complete := func(sessionID, runID, summary string) {
		t.Helper()
		run := &pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Status: pebblestore.V3RunIntentCompleted}
		if _, found, err := f.server.sessions.Store().GetV3SessionRunState(sessionID); err != nil {
			t.Fatal(err)
		} else if !found {
			pending := *run
			pending.Status = pebblestore.V3RunIntentPendingExecutor
			mutate(sessionID, runID+":pending", sessionruntime.SessionMutationRecordRunIntent, nil, &pending)
		}
		// Exercise the ordinary Auto final-message + terminal-intent boundary,
		// without lifecycle tools or pre-seeded lifecycle summary metadata.
		executor := newSessionV3Executor(f.server)
		if _, err := executor.completeRun(sessionV3ExecutorJob{SessionID: sessionID, RunID: runID, Principal: p}, sessionV3AssistantResponse{Content: summary, StopReason: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	complete(original.ID, "original-run", "Original outcome; integration pending")
	before, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || before.Status != "needs_review" || before.ActiveAttempt().Summary == "" || before.Integration.Error != task.Integration.Error || before.LastError != task.LastError || before.ActionNeeded != task.ActionNeeded {
		t.Fatalf("original not ready: %+v %v", before, err)
	}
	path := "/" + project.ID + "/tasks/task"
	reopen := func(key, request, guidance string) *pebblestore.ProjectTaskRecord {
		t.Helper()
		current, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"client_request_id": key, "revision": current.Revision, "feedback": request}
		// Persist the real reservation/session/seed/intent, but don't run an executor.
		// Absence is deliberately reported as failure until a fixture wake is present.
		f.server.v3SessionExecutor = nil
		response := f.callAPI(http.MethodPost, path+"/reopen", body, p)
		if response.Code != 503 {
			t.Fatalf("expected absent executor: %d %s", response.Code, response.Body)
		}
		reserved, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || reserved == nil || reserved.ActiveAttempt() == nil {
			t.Fatalf("failed launch reservation: task=%+v err=%v", reserved, err)
		}
		if reserved.ActiveAttempt().LaunchState != "launch_failed" || reserved.LastError == "" || reserved.ActionNeeded != "Follow-up launch incomplete; retry the same request" {
			t.Fatalf("failed launch not truthfully retained: attempt=%+v error=%q guidance=%q", reserved.ActiveAttempt(), reserved.LastError, reserved.ActionNeeded)
		}
		expectedGuidance := "Launching task-linked Swarm follow-up"
		if guidance != "" {
			// Independent review guidance arriving before a retry must survive it.
			if _, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, project.ID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
				current.ActionNeeded = guidance
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			expectedGuidance = guidance
		}
		f.server.v3SessionExecutor = newSessionV3Executor(f.server)
		f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(reserved.SessionID, reserved.ExecutionRunID())] = true
		response = f.callAPI(http.MethodPost, path+"/reopen", body, p)
		if response.Code != 200 {
			t.Fatalf("reopen: %d %s", response.Code, response.Body)
		}
		result, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || result.ActiveAttempt().LaunchState != "launched" {
			t.Fatalf("launch state: %+v %v", result, err)
		}
		if result.LastError != "" || result.ActiveAttempt().LastError != "" || result.ActionNeeded != expectedGuidance {
			t.Fatalf("successful retry guidance/error: guidance=%q want=%q error=%q attempt=%+v", result.ActionNeeded, expectedGuidance, result.LastError, result.ActiveAttempt())
		}
		if result.SessionID != reserved.SessionID || result.ExecutionRunID() != reserved.ExecutionRunID() || len(result.Attempts) != len(reserved.Attempts) || result.Revision != reserved.Revision {
			t.Fatal("successful retry changed reserved identity or revision")
		}
		// Purpose: AI and HTTP retries share the exact reservation/launch authority;
		// concurrent duplicate tool continuations must retain one attempt and intent.
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{Feedback: request, ClientRequestID: key, Revision: current.Revision})
				results <- err
			}()
		}
		for i := 0; i < 2; i++ {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("shared service retry: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("shared service retry timed out")
			}
		}
		retried, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || retried == nil || retried.SessionID != result.SessionID || retried.ExecutionRunID() != result.ExecutionRunID() || retried.Revision != result.Revision || len(retried.Attempts) != len(result.Attempts) || retried.ActionNeeded != result.ActionNeeded || retried.LastError != result.LastError {
			t.Fatalf("launched retry changed identity or guidance: task=%+v err=%v", retried, err)
		}
		intents, intentErr := f.server.sessions.Store().ListRunIntents(retried.SessionID, 10)
		messages, messageErr := f.server.sessions.Store().ListMessages(retried.SessionID, 0, 10)
		if intentErr != nil || len(intents) != 1 || intents[0].RunID != retried.ExecutionRunID() || messageErr != nil || len(messages) != 1 {
			t.Fatal("concurrent shared retries duplicated seed or run intent")
		}
		// Idempotency preserves every task field except the write timestamp;
		// retain the unmodified post-retry timestamp for stale-completion checks.
		retryBaseline := *result
		retryBaseline.UpdatedAt = retried.UpdatedAt
		assertFollowupTaskEqual(t, "launched retry changed more than UpdatedAt", &retryBaseline, retried)
		// The duplicate launched retry still crosses UpdateProjectTask during
		// reservation and persists UpdatedAt. Return its actual post-retry snapshot
		// so later no-op assertions do not compare against an earlier write.
		return retried
	}
	first := reopen("first", "  First follow-up\n", "")
	messages, err := db.ListMessages(first.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, before.ActiveAttempt().Summary) || !strings.Contains(messages[0].Content, "manage_projects get_task") || !strings.Contains(messages[0].Content, first.ActiveAttempt().Request) {
		t.Fatal("seed lost prior context/request")
	}
	complete(first.SessionID, first.ExecutionRunID(), "First follow-up outcome; validation pending")
	firstReady, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || firstReady == nil || firstReady.ActiveAttempt() == nil {
		t.Fatalf("follow-up read: task=%+v err=%v", firstReady, err)
	}
	if firstReady.Status != "needs_review" {
		t.Errorf("follow-up status=%q want needs_review", firstReady.Status)
	}
	if firstReady.ActiveAttempt().Summary != "First follow-up outcome; validation pending" || firstReady.ActiveAttempt().SummaryRunID != first.ExecutionRunID() {
		t.Errorf("follow-up outcome=%q run=%q want run=%q", firstReady.ActiveAttempt().Summary, firstReady.ActiveAttempt().SummaryRunID, first.ExecutionRunID())
	}
	if strings.Contains(firstReady.ActionNeeded, "Launching") || !strings.Contains(firstReady.ActionNeeded, "Review") {
		t.Errorf("follow-up action_needed=%q want review guidance", firstReady.ActionNeeded)
	}
	if firstReady.IsIntegrated {
		t.Error("follow-up with no new commits claimed integration")
	}
	if t.Failed() {
		t.FailNow()
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = pebblestore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	wire()
	db = f.server.sessions.Store()
	restored, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertFollowupTaskEqual(t, "restart changed task lineage", firstReady, restored)
	secondGuidance := "Review integration error before promotion"
	second := reopen("second", "Second day follow-up", secondGuidance)
	if len(second.Attempts) != 3 || second.SessionID == first.SessionID || second.SessionID == original.ID || second.Attempts[0].Integration == nil || second.Attempts[0].Integration.Error != before.Integration.Error || len(second.Attempts[0].Deliverables) != 1 || second.Attempts[1].Summary != firstReady.ActiveAttempt().Summary || second.ActiveAttempt().Summary != "" {
		t.Fatalf("lost original/first evidence: %+v", second)
	}
	// Require a stable full read before the callback as well as byte-identical
	// durable state after it: neither read hydration nor a timestamp-only write
	// may masquerade as a stale-completion mutation or be ignored by the test.
	beforeLate, found, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !found {
		t.Fatalf("pre-completion read: found=%v err=%v", found, err)
	}
	assertFollowupTaskEqual(t, "post-retry read changed task", second, beforeLate)
	taskKey := pebblestore.KeyProjectTask(p.AccountScopeID, project.ID, task.ID)
	rawBeforeLate, found, err := f.db.GetBytes(taskKey)
	if err != nil || !found {
		t.Fatalf("pre-completion durable read: found=%v err=%v", found, err)
	}
	updates := 0
	restoreUpdateHook := db.SetProjectTaskUpdateHookForTest(func(string) error {
		updates++
		return nil
	})
	defer restoreUpdateHook()
	// A late previous-attempt completion cannot mutate the new active attempt.
	if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{SessionID: first.SessionID, RunID: first.ExecutionRunID(), Principal: p}, pebblestore.V3RunIntentCompleted, ""); err != nil {
		t.Fatal(err)
	}
	restoreUpdateHook()
	if updates != 0 {
		t.Fatalf("late completion entered task mutation boundary %d time(s)", updates)
	}
	afterLate, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertFollowupTaskEqual(t, "late completion changed active attempt", beforeLate, afterLate)
	rawAfterLate, found, err := f.db.GetBytes(taskKey)
	if err != nil || !found {
		t.Fatalf("post-completion durable read: found=%v err=%v", found, err)
	}
	if !bytes.Equal(rawBeforeLate, rawAfterLate) {
		t.Fatalf("late completion changed durable task bytes: before=%s after=%s", rawBeforeLate, rawAfterLate)
	}
	if response := f.callAPI(http.MethodGet, path+"/history", nil, identity.Principal{Type: "user", UserID: "other-user", AccountScopeID: "other-account"}); response.Code != http.StatusNotFound {
		t.Fatal("cross-account history exposed outcome")
	}
	history := f.callAPI(http.MethodGet, path+"/history?cursor=0&limit=2", nil, p)
	var page struct {
		Attempts []pebblestore.ProjectTaskAttempt `json:"attempts"`
		Next     int                            `json:"next_cursor"`
	}
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &page) != nil || len(page.Attempts) != 2 || page.Next != 2 || page.Attempts[1].Request != first.ActiveAttempt().Request || page.Attempts[1].Summary != firstReady.ActiveAttempt().Summary || page.Attempts[1].SummaryRunID != first.ExecutionRunID() {
		t.Fatalf("history: %d %s", history.Code, history.Body)
	}
	complete(second.SessionID, second.ExecutionRunID(), "Second follow-up outcome; not promoted")
	secondReady, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || secondReady.Status != "needs_review" || secondReady.ActiveAttempt().Summary != "Second follow-up outcome; not promoted" || secondReady.ActiveAttempt().SummaryRunID != second.ExecutionRunID() || secondReady.Description != "Original requirements" || secondReady.ActionNeeded != secondGuidance || secondReady.IsIntegrated {
		t.Fatalf("second completion lost outcome/requirements/guidance or claimed integration: task=%+v err=%v", secondReady, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = pebblestore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	wire()
	db = f.server.sessions.Store()
	final, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertFollowupTaskEqual(t, "second restart changed retained outcomes", secondReady, final)
	history = f.callAPI(http.MethodGet, path+"/history?cursor=2&limit=2", nil, p)
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &page) != nil || len(page.Attempts) != 1 || page.Next != 0 || page.Attempts[0].Summary != final.ActiveAttempt().Summary || page.Attempts[0].Request != "Second day follow-up" {
		t.Fatalf("trailing history: %d %s", history.Code, history.Body)
	}
	for _, id := range []string{original.ID, first.SessionID, second.SessionID} {
		if _, ok, err := db.GetSession(id); err != nil || !ok {
			t.Fatalf("retained session %s missing", id)
		}
	}
	if git("rev-parse", "HEAD") != base || git("status", "--porcelain") != "" {
		t.Fatal("follow-ups modified captured target")
	}
}

// Keep full DeepEqual semantics (including nil versus empty slices and all
// timestamps), but name nested attempt fields instead of hiding the difference
// behind a generic lifecycle failure.
func assertFollowupTaskEqual(t *testing.T, label string, want, got *pebblestore.ProjectTaskRecord) {
	t.Helper()
	if reflect.DeepEqual(want, got) {
		return
	}
	var differences []string
	var compare func(string, reflect.Value, reflect.Value)
	compare = func(path string, left, right reflect.Value) {
		if reflect.DeepEqual(left.Interface(), right.Interface()) {
			return
		}
		switch left.Kind() {
		case reflect.Pointer:
			if !left.IsNil() && !right.IsNil() {
				compare(path, left.Elem(), right.Elem())
				return
			}
		case reflect.Struct:
			for i := 0; i < left.NumField(); i++ {
				if !left.Field(i).CanInterface() {
					differences = append(differences, fmt.Sprintf("%s: before=%#v after=%#v", path, left.Interface(), right.Interface()))
					return
				}
			}
			for i := 0; i < left.NumField(); i++ {
				compare(path+"."+left.Type().Field(i).Name, left.Field(i), right.Field(i))
			}
			return
		case reflect.Slice:
			if left.IsNil() == right.IsNil() && left.Len() == right.Len() {
				for i := 0; i < left.Len(); i++ {
					compare(fmt.Sprintf("%s[%d]", path, i), left.Index(i), right.Index(i))
				}
				return
			}
		}
		differences = append(differences, fmt.Sprintf("%s: before=%#v after=%#v", path, left.Interface(), right.Interface()))
	}
	compare("task", reflect.ValueOf(want), reflect.ValueOf(got))
	t.Fatalf("%s:\n%s", label, strings.Join(differences, "\n"))
}
