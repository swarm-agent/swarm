package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageSessionsCommitCreatesApprovedSameRepositoryChainAndLeavesUnrelatedWork(t *testing.T) {
	repo := t.TempDir()
	runManageSessionsGitCommand(t, repo, "init")
	runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
	runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
	for name := range map[string]bool{"one.txt": true, "two.txt": true, "unrelated.txt": true} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runManageSessionsGitCommand(t, repo, "add", ".")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
	for _, name := range []string{"one.txt", "two.txt", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("changed "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{}, plans: map[string]pebblestore.SessionPlanSnapshot{}}
	for i, file := range []string{"one.txt", "two.txt"} {
		id := "session-" + string(rune('1'+i))
		service.sessions[id] = pebblestore.SessionSnapshot{ID: id, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: repo, UpdatedAt: int64(10 + i), Lifecycle: &pebblestore.SessionLifecycleSnapshot{Phase: "needs_review"}}
		service.plans[id] = pebblestore.SessionPlanSnapshot{ID: "plan-" + id, SessionID: id, Document: &pebblestore.SessionPlanDocument{Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: "needs_review", ChangedFiles: []string{file}}}}}
	}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(repo): true}}}
	scope := WorkspaceScope{Principal: principal}
	args := map[string]any{"action": "commit", "commits": []any{map[string]any{"session_id": "session-1", "message": "commit one"}, map[string]any{"session_id": "session-2", "message": "commit two"}}}
	permissionPayload, err := runtime.PrepareManageSessionsCommitManifest(context.Background(), scope, args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Setenv("GIT_AUTHOR_NAME", "Injected Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "injected-author@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Injected Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "injected-committer@example.invalid")
	output, err := runtime.executeManageSessions(context.Background(), scope, permissionPayload)
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, output)
	}
	var response struct {
		Commits []struct {
			Hash  string   `json:"commit_hash"`
			Files []string `json:"files"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Commits) != 2 || len(response.Commits[0].Files) != 1 || response.Commits[0].Files[0] != "one.txt" || response.Commits[1].Files[0] != "two.txt" {
		t.Fatalf("response = %s", output)
	}
	if parent := strings.TrimSpace(runManageSessionsGitOutput(t, repo, "rev-parse", response.Commits[1].Hash+"^")); parent != response.Commits[0].Hash {
		t.Fatalf("second parent = %s, want %s", parent, response.Commits[0].Hash)
	}
	for _, commit := range response.Commits {
		identity := strings.TrimSpace(runManageSessionsGitOutput(t, repo, "show", "-s", "--format=%an|%ae|%cn|%ce", commit.Hash))
		if identity != "Test User|test@example.invalid|Test User|test@example.invalid" {
			t.Fatalf("commit %s identity = %q, want repository-configured identity", commit.Hash, identity)
		}
	}
	status := runManageSessionsGitOutput(t, repo, "status", "--short")
	if !strings.Contains(status, "unrelated.txt") || strings.Contains(status, "one.txt") || strings.Contains(status, "two.txt") {
		t.Fatalf("status = %q", status)
	}
	for _, session := range service.sessions {
		if session.Lifecycle == nil || session.Lifecycle.Phase != "needs_review" {
			t.Fatalf("session lifecycle changed: %#v", session)
		}
	}
}

func TestManageSessionsCommitRejectsStaleFingerprintAndOverlap(t *testing.T) {
	repo := t.TempDir()
	runManageSessionsGitCommand(t, repo, "init")
	runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
	runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, repo, "add", ".")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{}, plans: map[string]pebblestore.SessionPlanSnapshot{}}
	for _, id := range []string{"s1", "s2"} {
		service.sessions[id] = pebblestore.SessionSnapshot{ID: id, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: repo, UpdatedAt: 1, Lifecycle: &pebblestore.SessionLifecycleSnapshot{Phase: "needs_review"}}
		service.plans[id] = pebblestore.SessionPlanSnapshot{Document: &pebblestore.SessionPlanDocument{Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp", Status: "needs_review", ChangedFiles: []string{"shared.txt"}}}}}
	}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(repo): true}}}
	scope := WorkspaceScope{Principal: principal}
	_, err := runtime.PrepareManageSessionsCommitManifest(context.Background(), scope, map[string]any{"action": "commit", "commits": []any{map[string]any{"session_id": "s1", "message": "one"}, map[string]any{"session_id": "s2", "message": "two"}}})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
	payload, err := runtime.PrepareManageSessionsCommitManifest(context.Background(), scope, map[string]any{"action": "commit", "commits": []any{map[string]any{"session_id": "s1", "message": "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.executeManageSessions(context.Background(), scope, payload); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale error = %v", err)
	}
}

func TestManageSessionsCommitAcceptsNeedsReviewFromDurablePlanAttention(t *testing.T) {
	repo := t.TempDir()
	runManageSessionsGitCommand(t, repo, "init")
	runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
	runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, repo, "add", ".")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{"session-1": {
			ID: "session-1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: repo, UpdatedAt: 10,
		}},
		plans: map[string]pebblestore.SessionPlanSnapshot{"session-1": {
			ID: "plan-1", SessionID: "session-1", Status: "approved",
			Document: &pebblestore.SessionPlanDocument{
				ActiveCheckpointID: "cp-1",
				ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: "waiting_review"},
				Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: "completed", ChangedFiles: []string{"review.txt"}}},
			},
		}},
	}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(repo): true}}}
	payload, err := runtime.PrepareManageSessionsCommitManifest(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":  "commit",
		"commits": []any{map[string]any{"session_id": "session-1", "message": "review commit"}},
	})
	if err != nil {
		t.Fatalf("prepare needs-review plan: %v", err)
	}
	if _, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, payload); err != nil {
		t.Fatalf("execute needs-review plan commit: %v", err)
	}
	if subject := strings.TrimSpace(runManageSessionsGitOutput(t, repo, "log", "-1", "--format=%s")); subject != "review commit" {
		t.Fatalf("commit subject = %q", subject)
	}
}

func runManageSessionsGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := execCommand("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

var execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
