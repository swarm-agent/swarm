package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/modelprofile"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: person-driven Orchestrator delegation must produce the same run-end
// signals and folder/role-scoped deliveries as direct MCP tasks. Other agent
// origins, missing identity and cross-account origins must not gain eligibility;
// automation tasks must not loop. This store-backed API/tool/runner test is the
// narrowest layer spanning manage_projects, signalTaskRunEnded and Runner.
// Provider execution is not needed: real executor status transitions complete
// the task session after admission, and the workflow runs a local script.
func TestTaskRunOrchestratorWorkflowParity(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db := server.sessions.Store()
	p := testPrincipal()
	folder := t.TempDir()
	integrationGit(t, folder, "init", "-b", "dev")
	integrationGit(t, folder, "commit", "--allow-empty", "-m", "base")
	workspaces := workspace.NewService(pebblestore.NewWorkspaceStore(db.Underlying()))
	added, err := workspaces.AddForPrincipal(p, folder, "source", "", false)
	if err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "signal-project", Name: "Signals", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: folder}}}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	origin := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "orchestrator-origin", "Orchestrator", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	origin.WorkspacePath, origin.WorkspaceName = "", ""
	origin.WorkspaceGrants, origin.TemporaryWorkspaceRoots = nil, nil
	origin.Metadata = map[string]any{
		"agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator", "agent_mode": "primary", "swarm_v3_execution_class": "primary",
		"agent_profile": pebblestore.AgentProfile{Name: "system-orchestrator", Mode: "primary"}, "project_id": project.ID, "swarm_v3_project_id": project.ID,
	}
	if err := db.UpdateSession(origin); err != nil {
		t.Fatal(err)
	}
	exec := newSessionV3Executor(server)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	originJob := sessionV3ExecutorJob{Principal: p, SessionID: origin.ID, RunID: "origin-run", EpochID: "epoch-00000000000000000001"}
	for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
		if _, err := exec.recordRunStatus(originJob, status, "", "session.run."+status); err != nil {
			t.Fatal(err)
		}
	}
	server.workspace = workspaces
	server.SetWorktreeService(worktree.NewService(pebblestore.NewWorktreeStore(db.Underlying()), workspaces, nil))
	if err := db.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(p.AccountScopeID, p.UserID); err != nil {
		t.Fatal(err)
	}
	entry, found, err := workspaces.GetByWorkspaceIDForPrincipal(p, added.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := server.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(p.AccountScopeID, p.UserID, entry); err != nil {
		t.Fatal(err)
	}
	runtime := tool.NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageProjectStore(db)
	runtime.SetProjectTaskLifecycleService(server)
	// The native task must not start on creation: under Start tasks auto every
	// task starts at once, so this account waits (approve).
	hold := pebblestore.DefaultAccountPermissions()
	hold.Tasks = "approve"
	if _, err := db.Underlying().SetAccountPermissions(p.AccountScopeID, 0, hold); err != nil {
		t.Fatal(err)
	}
	runtime.SetManageSessionService(server.sessions)
	args, _ := json.Marshal(map[string]any{"action": "create_task", "project_id": project.ID, "task_id": "native-task", "title": "Fix code", "agent": "coder", "workspace_path": folder, "auto_approve": false})
	nativeCtx := tool.WithVideoRunContext(ctx, tool.VideoRunContext{SessionID: origin.ID, RunID: originJob.RunID})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(nativeCtx, tool.WorkspaceScope{SessionID: origin.ID, Principal: p}, tool.Call{Name: "manage_projects", Arguments: string(args)}); err != nil {
		t.Fatal(err)
	}
	native, found, err := db.GetProjectTask(p.AccountScopeID, project.ID, "native-task")
	if err != nil || !found || native.OriginSessionID != origin.ID {
		t.Fatalf("native origin: %+v %v", native, err)
	}
	feed := pebblestore.NewSignalStore(db.Underlying())
	server.SetSignalStore(feed)
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) {
		return &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}, nil
	}
	writeAutomationScript(t, folder, "follow.sh")
	runner := newAutomationRunner(t, server, db.Underlying(), feed, p.AccountScopeID, newAutomationHost(t))
	if _, _, err := runner.Store().Put(p.AccountScopeID, jobautomation.Automation{Name: "coder-follow", Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, When: jobautomation.WhenSucceeded, Enabled: true, Steps: []jobautomation.Step{{ID: "follow", Type: jobautomation.StepRun, Command: "follow.sh"}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, agent, class, account, parent, automation string
		want                                            bool
	}{
		{name: "native", want: true}, {name: "direct", want: true},
		{name: "coder", agent: "system-coder"}, {name: "reviewer", agent: "system-reviewer"},
		{name: "delegated", class: "subagent"}, {name: "parented", parent: "another-session"},
		{name: "foreign", account: "other-account"}, {name: "missing"},
		{name: "missing-profile"}, {name: "resolved-mismatch"}, {name: "worker-origin"},
		{name: "automation", automation: "workflow:delivery.step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := *native
			task.ID = tc.name + "-task"
			task.Status = "in_progress"
			task.AutomationID = tc.automation
			if tc.name == "direct" {
				task.OriginSessionID = ""
			} else if tc.name == "missing" {
				task.OriginSessionID = "missing-origin"
			} else if tc.name != "native" && tc.name != "automation" {
				copied := origin
				copied.ID = tc.name + "-origin"
				copied.Metadata = map[string]any{}
				for k, v := range origin.Metadata {
					copied.Metadata[k] = v
				}
				if tc.agent != "" {
					copied.Metadata["agent_name"] = tc.agent
					copied.Metadata["resolved_agent_name"] = tc.agent
					copied.Metadata["agent_profile"] = pebblestore.AgentProfile{Name: tc.agent, Mode: "primary"}
				}
				if tc.name == "missing-profile" {
					delete(copied.Metadata, "agent_profile")
				}
				if tc.name == "resolved-mismatch" {
					copied.Metadata["resolved_agent_name"] = "system-coder"
				}
				if tc.name == "worker-origin" {
					copied.Metadata["worker_run_id"] = "worker-run"
				}
				if tc.class != "" {
					copied.Metadata["swarm_v3_execution_class"] = tc.class
				}
				if tc.parent != "" {
					copied.Metadata["parent_session_id"] = tc.parent
				}
				if tc.account != "" {
					copied.AccountScopeID = tc.account
				}
				if err := db.CreateSessionForAccount(copied, copied.UserID, copied.AccountScopeID); err != nil {
					t.Fatal(err)
				}
				task.OriginSessionID = copied.ID
			}
			// The session helper seeds topology directly; it deliberately runs
			// without the task admission workspace service.
			server.workspace = nil
			session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, tc.name+"-session", tc.name, t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
			server.workspace = workspaces
			task = taskSessionFixture(t, server, session, task)
			for _, runID := range []string{task.ExecutionRunID(), "followup-" + tc.name} {
				job := sessionV3ExecutorJob{Principal: p, SessionID: session.ID, RunID: runID, EpochID: "epoch-00000000000000000001"}
				for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning, sessionruntime.RunIntentCompleted, sessionruntime.RunIntentCompleted} {
					if _, err := exec.recordRunStatus(job, status, "", "session.run."+status); err != nil {
						t.Fatal(err)
					}
				}
			}
			page, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID, KindPrefixes: []string{"task.run"}})
			if err != nil {
				t.Fatal(err)
			}
			var signals []pebblestore.Signal
			for _, sig := range page.Signals {
				if sig.Refs["task_id"] == task.ID {
					signals = append(signals, sig)
				}
			}
			wantSignals := 0
			if tc.want || tc.automation != "" {
				wantSignals = 2
			}
			if len(signals) != wantSignals {
				t.Fatalf("signals = %+v, want %d", signals, wantSignals)
			}
			for i, sig := range signals {
				followup, runID, source := "", task.ExecutionRunID(), "task"
				if i == 1 {
					followup = "followup-" + tc.name
					runID = followup
				}
				if tc.automation != "" {
					source = jobautomation.RequestSourceAutomation
				}
				if sig.Kind != "task.run.succeeded" || sig.DedupKey != "task.run:"+task.ID+":"+followup || sig.Refs["project_id"] != project.ID || sig.Refs["session_id"] != session.ID || sig.Refs["run_id"] != runID || sig.Attrs["job_id"] != taskRunJobID(project.ID, task.ID) || sig.Attrs["role"] != "coder" || sig.Attrs["source"] != source {
					t.Fatalf("signal = %+v", sig)
				}
			}
			if err := runner.ProcessOnce(ctx); err != nil {
				t.Fatal(err)
			}
			deliveries, err := runner.Store().JobDeliveries(p.AccountScopeID, taskRunJobID(project.ID, task.ID))
			if err != nil {
				t.Fatal(err)
			}
			wantDeliveries := 0
			if tc.want {
				wantDeliveries = 2
			}
			if len(deliveries) != wantDeliveries {
				t.Fatalf("deliveries = %+v, want %d", deliveries, wantDeliveries)
			}
			for _, d := range deliveries {
				if d.State != jobautomation.StateSucceeded {
					t.Fatalf("delivery = %+v", d)
				}
			}
		})
	}
}

// Purpose: an explicit built-in agent task_progress blocker must survive run
// completion and must never trigger succeeded workflows. A normal completion
// must still succeed. This temporary-store executor/tool/runner test is the
// narrowest layer spanning role tool admission, projectTaskDeclaredBlocker,
// signalTaskRunEnded and workflow delivery selection. The provider is scripted;
// this proves lifecycle behavior, not live model behavior.
func TestTaskRunDeclaredBlockerWorkflowOutcome(t *testing.T) {
	for _, role := range []string{"swarm", "coder", "designer", "finder"} {
		for _, blocked := range []bool{true, false} {
			name := role + "-completed"
			if blocked {
				name = role + "-blocked"
			}
			t.Run(name, func(t *testing.T) {
				f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "store"), nil)
				defer f.close()
				f.server.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(f.sessions.Store().Underlying())))
				const reason = "The required file BRANDBOOK.md is missing"
				f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
					if blocked && !hasFunctionCallOutput(req.Input) {
						args, _ := json.Marshal(map[string]string{"action": "blocked", "reason": reason})
						return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "declare-blocker", Name: "task_progress", Arguments: string(args)}}}, nil
					}
					return provideriface.Response{Text: "Finished reporting the outcome.", StopReason: "stop"}, nil
				}
				folder := t.TempDir()
				binding := seedSessionsV3PrimaryAuthority(t, f.server, folder)
				raw, _ := json.Marshal(map[string]any{"client_request_id": name, "workspace_path": folder, "workspace_name": filepath.Base(folder), "swarm_id": "host-swarm-id", "workspace_binding_id": binding, "target_kind": "host", "target_relationship": "self", "title": name, "mode": sessionruntime.ModeAuto, "model_profile": map[string]any{"temporary": map[string]string{"provider": "test-provider", "model": "test-model", "thinking": "medium"}}, "agent_name": map[string]string{"swarm": "system-orchestrator", "coder": "system-coder", "designer": "system-designer", "finder": "system-finder"}[role], "preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"}})
				code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
				if code != http.StatusOK {
					t.Fatalf("create: %d %s", code, body)
				}
				var created struct {
					Session pebblestore.SessionSnapshot `json:"session"`
				}
				if err := json.Unmarshal([]byte(body), &created); err != nil {
					t.Fatal(err)
				}
				if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
					SessionID: created.Session.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID,
					Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: "brief", PayloadHash: "brief",
					Message: &pebblestore.MessageSnapshot{ID: "brief", Role: "user", Content: "Perform the task or declare the missing input."},
				}); err != nil {
					t.Fatal(err)
				}
				task := taskSessionFixture(t, f.server, created.Session, pebblestore.ProjectTaskRecord{ID: name, ProjectID: "blocker-project", Agent: role, Status: "in_progress", WorkspacePath: folder})
				db := f.sessions.Store()
				feed := pebblestore.NewSignalStore(db.Underlying())
				f.server.SetSignalStore(feed)
				req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
				f.server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) {
					return &controlMCPCall{request: req, next: f.server.internalRouteHandler(), server: f.server}, nil
				}
				writeAutomationScript(t, folder, "follow.sh")
				p := testPrincipal()
				runner := newAutomationRunner(t, f.server, db.Underlying(), feed, p.AccountScopeID, newAutomationHost(t))
				for _, when := range []string{jobautomation.WhenSucceeded, jobautomation.WhenFailed, jobautomation.WhenAlways} {
					if _, _, err := runner.Store().Put(p.AccountScopeID, jobautomation.Automation{Name: when, Scope: jobautomation.Scope{Folder: folder, Role: role}, When: when, Enabled: true, Steps: []jobautomation.Step{{ID: "follow", Type: jobautomation.StepRun, Command: "follow.sh"}}}); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				job := sessionV3ExecutorJob{Principal: p, SessionID: created.Session.ID, RunID: task.ExecutionRunID(), EpochID: "epoch-00000000000000000001"}
				exec := f.server.v3SessionExecutor
				if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentPendingExecutor, "", "session.run.queued"); err != nil {
					t.Fatal(err)
				}
				exec.run(ctx, job)
				intent, found, err := f.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
				if err != nil || !found || intent.Status != sessionruntime.RunIntentCompleted {
					t.Fatalf("run: %+v %v", intent, err)
				}
				stored, found, err := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
				if err != nil || !found {
					t.Fatalf("task: %v %v", found, err)
				}
				wantStatus, wantEnd := "needs_review", jobautomation.StatusSucceeded
				if blocked {
					wantStatus, wantEnd = "blocked", jobautomation.StatusFailed
				}
				if stored.Status != wantStatus || (blocked && stored.LastError != reason) {
					t.Fatalf("task outcome: %+v", stored)
				}
				outcome, err := f.server.waitTaskJob(ctx, p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
				if err != nil || outcome.Status != wantEnd || (blocked && outcome.Error != reason) {
					t.Fatalf("workflow outcome: %+v, error: %v", outcome, err)
				}
				page, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID, KindPrefixes: []string{"task.run"}})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Signals) != 1 || page.Signals[0].Kind != "task.run."+wantEnd {
					t.Fatalf("signals: %+v", page.Signals)
				}
				if blocked && page.Signals[0].Attrs["blocker_reason"] != reason {
					t.Fatalf("missing blocker reason: %+v", page.Signals[0])
				}
				if err := runner.ProcessOnce(ctx); err != nil {
					t.Fatal(err)
				}
				deliveries, err := runner.Store().JobDeliveries(p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
				if err != nil {
					t.Fatal(err)
				}
				if len(deliveries) != 2 {
					t.Fatalf("deliveries: %+v", deliveries)
				}
				seen := map[string]bool{}
				for _, d := range deliveries {
					seen[d.AutomationName] = true
					if d.State != jobautomation.StateSucceeded {
						t.Fatalf("delivery failed: %+v", d)
					}
				}
				if !seen[wantEnd] || !seen[jobautomation.WhenAlways] {
					t.Fatalf("wrong workflow deliveries: %+v", deliveries)
				}
			})
		}
	}
}
