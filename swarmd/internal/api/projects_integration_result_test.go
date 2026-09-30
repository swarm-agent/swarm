package api

import (
	"context"
	"encoding/json"
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
	store "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: the registered project-task integrate endpoint must return success
// after actually integrating divergent source history, and report real conflicts
// without altering Git or claiming completion. This HTTP/store/real-Git test is
// the narrowest layer proving both the user-visible result and durable receipt.
func TestProjectTaskIntegrationResult(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "success"
		if conflict {
			name = "conflict"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			repo := t.TempDir()
			git := func(path string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git(repo, "init", "-b", "dev")
			git(repo, "config", "user.email", "test@example.invalid")
			git(repo, "config", "user.name", "Test")
			write := func(path, name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(repo, "shared.txt", "base\n")
			git(repo, "add", ".")
			git(repo, "commit", "-m", "base")
			base := git(repo, "rev-parse", "HEAD")
			child := filepath.Join(t.TempDir(), "child")
			git(repo, "worktree", "add", "-b", "agent/source", child, base)
			write(child, "shared.txt", "source\n")
			git(child, "add", ".")
			git(child, "commit", "-m", "source")
			head := git(child, "rev-parse", "HEAD")
			targetFile := "target.txt"
			if conflict {
				targetFile = "shared.txt"
			}
			write(repo, targetFile, "target\n")
			git(repo, "add", ".")
			git(repo, "commit", "-m", "target")
			target := git(repo, "rev-parse", "HEAD")
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := store.NewSessionStore(db)
			el, err := store.NewEventLog(db)
			if err != nil {
				t.Fatal(err)
			}
			const sessionID = "11111111111111111111111111111111"
			if err := ss.CreateSession(store.SessionSnapshot{ID: sessionID, UserID: "owner", AccountScopeID: "account", WorkspacePath: child, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/source", WorktreeBaseBranch: "dev", Metadata: map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": repo}}); err != nil {
				t.Fatal(err)
			}
			if err := ss.PutProject("account", &store.ProjectRecord{ID: "project", Name: "Test"}); err != nil {
				t.Fatal(err)
			}
			if err := ss.PutProjectTask("account", &store.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Integrate", Agent: "coder", Status: "needs_review", SessionID: sessionID, WorkspacePath: child, WorktreeBranch: "agent/source", BaseBranch: "dev", BaseCommit: base}); err != nil {
				t.Fatal(err)
			}
			s := &Server{sessions: sessionruntime.NewService(ss, el), worktrees: &worktreeruntime.Service{}}
			body, err := json.Marshal(map[string]string{"session_id": sessionID, "source_branch": "agent/source", "target_branch": "dev"})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, ProjectsPath+"/project/tasks/task/integrate", strings.NewReader(string(body)))
			r = r.WithContext(context.WithValue(ctx, productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}))
			w := httptest.NewRecorder()
			s.apiMux().ServeHTTP(w, r)
			task, found, err := ss.GetProjectTask("account", "project", "task")
			if err != nil || !found {
				t.Fatalf("task: %v", err)
			}
			if conflict {
				if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "merge") || task.IsIntegrated || task.Integration == nil || task.Integration.State != "conflict" {
					t.Fatalf("conflict result: %d %s %+v", w.Code, w.Body.String(), task)
				}
				if git(repo, "rev-parse", "HEAD") != target || git(repo, "status", "--porcelain") != "" {
					t.Fatal("conflict altered target")
				}
				return
			}
			if w.Code != http.StatusOK || task.Integration == nil || task.Integration.State != "integrated" || !task.IsIntegrated || task.Status != "completed" {
				t.Fatalf("success result: %d %s %+v", w.Code, w.Body.String(), task)
			}
			git(repo, "merge-base", "--is-ancestor", head, "HEAD")
			git(repo, "merge-base", "--is-ancestor", target, "HEAD")
			if task.Integration.ResultingTargetHead != git(repo, "rev-parse", "HEAD") || git(repo, "status", "--porcelain") != "" {
				t.Fatal("receipt does not match clean target")
			}
		})
	}
}
