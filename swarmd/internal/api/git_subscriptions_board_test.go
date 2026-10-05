package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Requirement: the project board alone must follow external source commits and
// target integration/reset, without session events, disclosure or polling. This
// test runs the real fsnotify -> authorized HTTP stream -> TypeScript transport
// -> DesktopProjectsRuntime/reducer -> task GET/AssessTaskDelivery path. Only the
// authenticated caller and Git fixture mutations are supplied by the harness;
// no watcher, assessment, session event or Git command is mocked. The temporary
// store/worktrees and Node process are bounded; no provider/daemon is launched.
func TestProjectGitSubscriptionsBoard(t *testing.T) {
	runProjectGitSubscriptionsBoard(t, false)
}

// Requirement: handleGitSubscriptions must isolate pruned and foreign selectors
// while the native HTTP-to-board path keeps healthy ancestry/OIDs current. Keep
// the original producer, fencing and 65-second idle-read assertions in both runs.
func TestProjectGitSubscriptionsSelectorIsolation(t *testing.T) {
	runProjectGitSubscriptionsBoard(t, true)
}

func runProjectGitSubscriptionsBoard(t *testing.T, isolation bool) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Count every actual Git invocation, including production inspection calls.
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git-reads")
	t.Setenv("GIT_TEST_BINARY", gitBinary)
	t.Setenv("GIT_TEST_LOG", logPath)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf x >> \"$GIT_TEST_LOG\"\nexec \"$GIT_TEST_BINARY\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	git(repo, "init", "-b", "dev")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	write(repo, "base", "base\n")
	git(repo, "add", ".")
	git(repo, "commit", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	seedTaskSessionBinding(t, f, binding)
	svc := worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	f.server.worktrees = svc
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	db := f.server.sessions.Store()
	if err := db.PutProject(f.accountID, &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	allocations := make(map[string]worktree.Allocation)
	ids := []string{"integrated", "candidate"}
	if isolation {
		ids = append(ids, "deleted")
	}
	for _, id := range ids {
		alloc, err := svc.AllocateProjectTaskFollowup(p, repo, id, "agent/"+id, base, "dev")
		if err != nil {
			t.Fatal(err)
		}
		allocations[id] = alloc
		write(alloc.WorkspacePath, id, id+"\n")
		git(alloc.WorkspacePath, "add", ".")
		git(alloc.WorkspacePath, "commit", "-m", id)
		task := &pebblestore.ProjectTaskRecord{ID: id, ProjectID: "project", AccountID: f.accountID, Title: id, Revision: 1, Status: "needs_review", SessionID: id, Agent: "coder", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: binding}
		if err := db.PutProjectTask(f.accountID, task); err != nil {
			t.Fatal(err)
		}
		snap := pebblestore.SessionSnapshot{ID: id, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": id, "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": id, "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
		if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: id, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, RequestHash: id, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: id, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if isolation {
		git(repo, "worktree", "remove", allocations["deleted"].WorkspacePath)
		git(repo, "worktree", "prune")
		foreign := pebblestore.SessionSnapshot{ID: "foreign-selector", UserID: "foreign-user", AccountScopeID: "foreign-account", Mode: "auto"}
		if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: foreign.ID, UserID: foreign.UserID, AccountScopeID: foreign.AccountScopeID, ClientRequestID: foreign.ID, IdempotencyKey: foreign.ID, PayloadHash: foreign.ID, RequestHash: foreign.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &foreign}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.BackfillProjectTaskSummaries(f.accountID, "project"); err != nil {
		t.Fatal(err)
	}
	git(repo, "merge", "--ff-only", allocations["integrated"].BranchName)
	originalTarget := git(repo, "rev-parse", "HEAD")
	candidate := allocations["candidate"].WorkspacePath
	f.server.gitRealtime = newGitRealtimeManager(f.server)
	defer f.server.gitRealtime.stopAll()
	mux := f.server.apiMux()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture" {
			switch r.URL.Query().Get("action") {
			case "prune":
				git(repo, "worktree", "remove", allocations["deleted"].WorkspacePath)
				git(repo, "worktree", "prune")
			case "restore":
				git(repo, "worktree", "add", allocations["deleted"].WorkspacePath, allocations["deleted"].BranchName)
			case "source":
				write(candidate, "external", "external\n")
				git(candidate, "add", ".")
				git(candidate, "commit", "--allow-empty", "-m", "external")
			case "integrate":
				git(repo, "merge", "--no-edit", allocations["candidate"].BranchName)
			case "reset":
				git(repo, "reset", "--hard", originalTarget)
			case "dirty-source":
				write(candidate, "dirty-source", "preserve\n")
			case "dirty-target":
				write(repo, "dirty-target", "preserve\n")
			case "clean":
				if err := os.Remove(filepath.Join(candidate, "dirty-source")); err != nil {
					t.Error(err)
				}
				if err := os.Remove(filepath.Join(repo, "dirty-target")); err != nil {
					t.Error(err)
				}
			case "burst":
				for i := 0; i < 30; i++ {
					write(candidate, "burst", strings.Repeat("x", i+1))
				}
			case "count":
				stat, err := os.Stat(logPath)
				if err != nil {
					t.Error(err)
					return
				}
				writeJSON(w, 200, map[string]any{"count": stat.Size()})
				return
			}
			writeJSON(w, 200, map[string]any{"source": git(candidate, "rev-parse", "HEAD"), "target": git(repo, "rev-parse", "HEAD")})
			return
		}
		principal := p
		if r.Header.Get("X-Fixture-Foreign") != "" {
			principal.AccountScopeID = "foreign"
		}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Scopes: []string{"sessions:read", "projects:read"}})
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	if isolation {
		testGitSubscriptionCapacityHTTP(t, server.URL, candidate, f.server.gitRealtime.subscriptions, logPath)
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--import", "tsx", "src/features/desktop/runtime/desktop-projects-native-git.fixture.ts")
	cmd.Dir = web
	cmd.Env = append(os.Environ(), "SWARM_GIT_FIXTURE_URL="+server.URL)
	if isolation {
		cmd.Env = append(cmd.Env, "SWARM_GIT_FIXTURE_ISOLATION=1")
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("board producer-to-consumer: %v\n%s", err, output.String())
	}
	// No observer writes or durable task-label updates are permitted.
	for _, id := range []string{"integrated", "candidate"} {
		row, found, err := db.GetProjectTask(f.accountID, "project", id)
		if err != nil || !found || row.IsIntegrated || row.SessionID != id {
			t.Fatalf("observer changed task authority: %+v %v", row, err)
		}
	}
}
