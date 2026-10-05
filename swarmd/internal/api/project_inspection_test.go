package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: no-ambient project inspection must resolve the exact catalog-bound
// task attempt, never current dev, arbitrary siblings or stale identities.
// ResolveProjectInspection plus real Git/store ownership and the exposed tool
// dispatch is the narrowest boundary proving reads and rejection postconditions.
// Cross-task callers must prove durable ownership without becoming project
// mutation owners; real environment tool dispatch exercises that admission.
// This is a hermetic contract test, not provider or live-environment evidence.
func TestProjectInspectionExactResult(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "feature"), []byte("source version"), 0600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	catalog := pebblestore.NewWorkspaceStore(f.db)
	entry, err := catalog.AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	f.server.workspace = workspace.NewService(catalog)
	svc := worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	f.server.worktrees = svc
	seedTaskSessionBinding(t, f, binding)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	alloc, err := svc.AllocateProjectTaskFollowup(p, repo, "inspection-child", "agent/inspection-child", base, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "feature"), []byte("committed result"), 0600); err != nil {
		t.Fatal(err)
	}
	git(alloc.WorkspacePath, "add", ".")
	git(alloc.WorkspacePath, "commit", "-m", "result")
	head := git(alloc.WorkspacePath, "rev-parse", "HEAD")
	db := f.server.sessions.Store()
	proj := &pebblestore.ProjectRecord{ID: "inspection-project", Name: "Inspection", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(f.accountID, proj); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "inspection-task", ProjectID: proj.ID, AccountID: f.accountID, Title: "Inspect result", Revision: 1, Status: "needs_review", SessionID: "inspection-child", Agent: "coder", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: binding}
	if err := db.PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	snap := pebblestore.SessionSnapshot{ID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Mode: "auto", Metadata: map[string]any{"project_id": proj.ID, "task_id": task.ID, "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": task.SessionID, "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snap.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: snap.ID, IdempotencyKey: snap.ID, PayloadHash: snap.ID, RequestHash: snap.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: snap.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	w := projectConversationRequest(t, f.server, p, http.MethodPost, ProjectsPath+"/"+proj.ID+"/sessions", map[string]any{"client_request_id": "inspection-parent", "mode": "auto"})
	if w.Code != http.StatusOK {
		t.Fatalf("parent: %d %s", w.Code, w.Body.String())
	}
	parents, err := db.ListProjectConversations(p.AccountScopeID, p.UserID, proj.ID, 10)
	if err != nil || len(parents) != 1 {
		t.Fatalf("parent lookup: %v", err)
	}
	parent := parents[0]
	task, _, err = db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.EnsureTaskAttempts()
	ref := tool.ProjectInspectionRequest{ProjectID: proj.ID, TaskID: task.ID, AttemptID: task.ActiveAttemptID, SessionID: task.SessionID, HeadCommit: head}
	target, err := f.server.ResolveProjectInspection(ctx, p, parent.ID, ref)
	if err != nil || target.Root != alloc.WorkspacePath || target.Reference.HeadCommit != head {
		t.Fatalf("exact tree: %+v %v", target, err)
	}
	// A different durable task conversation may consume this exact candidate;
	// it must not acquire Orchestrator mutation ownership in the process.
	consumer := &pebblestore.ProjectTaskRecord{ID: "consumer-task", ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: "Validate candidate", Status: "in_progress", SessionID: "consumer-session", Agent: "swarm", SourceWorkspace: binding}
	if err := db.PutProjectTask(p.AccountScopeID, consumer); err != nil {
		t.Fatal(err)
	}
	caller := pebblestore.SessionSnapshot{ID: consumer.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"project_id": proj.ID, "task_id": consumer.ID, "agent_profile": pebblestore.AgentProfile{Name: "swarm"}, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: caller.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: caller.ID, IdempotencyKey: caller.ID, PayloadHash: caller.ID, RequestHash: caller.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &caller}); err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateProjectConversation(caller, p.AccountScopeID, p.UserID); err == nil {
		t.Fatal("task caller acquired project mutation authority")
	}
	if selected, err := f.server.ResolveProjectInspection(ctx, p, caller.ID, ref); err != nil || selected.Root != target.Root || selected.Reference != target.Reference {
		t.Fatalf("cross-conversation candidate: %+v %v", selected, err)
	}
	runtime := tool.NewRuntime(1)
	runtime.SetManageProjectStore(db)
	runtime.SetManageSessionService(f.server.sessions)
	runtime.SetProjectTaskLifecycleService(f.server)
	runtime.SetEnvironmentServices(nil, pebblestore.NewEnvironmentStore(f.db), nil, nil, nil)
	envArgs, err := json.Marshal(map[string]any{"action": "list", "project_result": target.Reference})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: caller.ID, Principal: p}, tool.Call{Name: "manage_environments", Arguments: string(envArgs)}); err != nil || !strings.Contains(out, binding.WorkspaceID) {
		t.Fatalf("cross-task environment resolution: %s %v", out, err)
	}
	// Environment source selection uses the same real project/catalog authority,
	// without borrowing the originating conversation or its lease.
	for _, callerID := range []string{parent.ID, caller.ID} {
		for _, name := range []string{"manage_environments", "manage-environments"} {
			args, _ := json.Marshal(map[string]any{"action": "list", "workspace_id": binding.WorkspaceID})
			if out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: callerID, Principal: p, RejectScopeExpansion: true}, tool.Call{Name: name, Arguments: string(args)}); err != nil || !strings.Contains(out, binding.WorkspaceID) {
				t.Fatalf("no-cwd environment resolution: %s %v", out, err)
			}
			args, _ = json.Marshal(map[string]any{"action": "list", "workspace_id": "nonmember"})
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: callerID, Principal: p, RejectScopeExpansion: true}, tool.Call{Name: name, Arguments: string(args)}); err == nil {
				t.Fatal("nonmember environment source accepted")
			}
		}
	}
	for _, change := range []func(*pebblestore.SessionSnapshot){
		func(s *pebblestore.SessionSnapshot) { s.ID = "forged-session" },
		func(s *pebblestore.SessionSnapshot) { s.Metadata["task_attempt_id"] = "superseded" },
		func(s *pebblestore.SessionSnapshot) { s.AccountScopeID = "foreign" },
		func(s *pebblestore.SessionSnapshot) { s.UserID = "foreign" },
		func(s *pebblestore.SessionSnapshot) { s.Metadata["task_id"] = task.ID },
		func(s *pebblestore.SessionSnapshot) { s.Metadata["project_id"] = "foreign" },
		func(s *pebblestore.SessionSnapshot) { s.Metadata["swarm_v3_source_workspace_id"] = "foreign" },
	} {
		bad := caller
		bad.Metadata = make(map[string]any)
		for key, value := range caller.Metadata {
			bad.Metadata[key] = value
		}
		change(&bad)
		if err := f.server.validateProjectInspectionCaller(p, bad, proj.ID); err == nil {
			t.Fatalf("forged caller accepted: %+v", bad)
		}
	}
	scope := tool.WorkspaceScope{SessionID: parent.ID, Principal: p, RejectScopeExpansion: true}
	for _, taskResult := range []bool{false, true} {
		args := map[string]any{"action": "inspect_files", "project_id": proj.ID, "workspace_id": binding.WorkspaceID, "inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "feature"}}}
		want := "source version"
		if taskResult {
			args["task_id"], args["attempt_id"], args["source_session_id"], args["head_commit"] = ref.TaskID, ref.AttemptID, ref.SessionID, head
			want = "committed result"
		}
		raw, _ := json.Marshal(args)
		out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("read selected tree: %s %v", out, err)
		}
	}
	for _, change := range []func(*tool.ProjectInspectionRequest){
		func(r *tool.ProjectInspectionRequest) { r.AttemptID = "superseded" },
		func(r *tool.ProjectInspectionRequest) { r.SessionID = parent.ID },
		func(r *tool.ProjectInspectionRequest) { r.HeadCommit = base },
		func(r *tool.ProjectInspectionRequest) { r.WorkspaceGeneration = binding.WorkspaceGeneration + 1 },
		func(r *tool.ProjectInspectionRequest) { r.WorkspaceID = "unrelated" },
		func(r *tool.ProjectInspectionRequest) { r.ProjectID = "unrelated" },
	} {
		bad := ref
		change(&bad)
		for _, callerID := range []string{parent.ID, caller.ID} {
			if selected, err := f.server.ResolveProjectInspection(ctx, p, callerID, bad); err == nil || selected.Root != "" {
				t.Fatalf("accepted invalid identity %+v from %s", bad, callerID)
			}
		}
	}
	foreign := p
	foreign.AccountScopeID = "foreign"
	for _, callerID := range []string{parent.ID, caller.ID} {
		if _, err := f.server.ResolveProjectInspection(ctx, foreign, callerID, ref); err == nil {
			t.Fatal("cross-account inspection accepted")
		}
	}
	// Project association is necessary in addition to account catalog access.
	proj.Workspaces = nil
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	if selected, err := f.server.ResolveProjectInspection(ctx, p, caller.ID, ref); err == nil || selected.Root != "" {
		t.Fatal("unassociated repository accepted")
	}
	proj.Workspaces = []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.ResolveProjectInspection(ctx, p, parent.ID, tool.ProjectInspectionRequest{ProjectID: proj.ID}); err == nil {
		t.Fatal("implicit source selected")
	}
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "untracked"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.ResolveProjectInspection(ctx, p, parent.ID, ref); err == nil {
		t.Fatal("dirty result accepted as exact committed tree")
	}
	if data, err := os.ReadFile(filepath.Join(alloc.WorkspacePath, "untracked")); err != nil || string(data) != "preserve" {
		t.Fatal("rejected inspection modified dirty result")
	}
	if git(repo, "rev-parse", "HEAD") != base || git(alloc.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("inspection mutated Git")
	}
	git(alloc.WorkspacePath, "add", "untracked")
	git(alloc.WorkspacePath, "commit", "-m", "advance candidate")
	advanced := git(alloc.WorkspacePath, "rev-parse", "HEAD")
	if selected, err := f.server.ResolveProjectInspection(ctx, p, caller.ID, ref); err == nil || selected.Root != "" {
		t.Fatal("changed HEAD accepted through task conversation")
	}
	if git(alloc.WorkspacePath, "rev-parse", "HEAD") != advanced || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("stale reference rejection changed Git")
	}
	stored, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if stored.Status != "needs_review" || stored.Integration != nil {
		t.Fatal("inspection accepted or integrated the task")
	}
}
