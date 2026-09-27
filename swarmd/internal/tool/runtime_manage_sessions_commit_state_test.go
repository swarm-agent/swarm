package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Git commit authorization must not depend on sidebar attention state.
// Regression: a running checkpoint cannot finish if it must first need review to
// commit. Exercise PrepareManageSessionsCommitManifest and manageSessionsCommit
// against real Git, the narrowest boundary proving both approval and execution.
// Also prove changed approved bytes are rejected without advancing HEAD or staging.
func TestManageSessionsCommitDoesNotRequireReviewState(t *testing.T) {
	for _, phase := range []string{"in_progress", "blocked", "needs_review"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			repo := t.TempDir()
			runManageSessionsGitCommand(t, repo, "init")
			runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
			runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
			path := filepath.Join(repo, "change.txt")
			write := func(content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("base\n")
			runManageSessionsGitCommand(t, repo, "add", "change.txt")
			runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
			base := runManageSessionsGitOutput(t, repo, "rev-parse", "HEAD")
			write("approved\n")
			principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
			service := &gitManageSessionService{
				sessions: map[string]pebblestore.SessionSnapshot{"self": {
					ID: "self", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
					WorkspacePath: repo, WorktreeRootPath: repo, WorktreeEnabled: true,
					UpdatedAt: 10, Lifecycle: &pebblestore.SessionLifecycleSnapshot{Phase: phase},
				}},
				plans: map[string]pebblestore.SessionPlanSnapshot{},
			}
			runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(repo): true}}}
			scope := WorkspaceScope{SessionID: "self", Principal: principal}
			payload, err := runtime.PrepareManageSessionsCommitManifest(ctx, scope, map[string]any{
				"action": "commit", "commits": []any{map[string]any{"session_id": "self", "message": "commit while active"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			write("unapproved\n")
			if _, err := runtime.executeManageSessions(ctx, scope, payload); err == nil || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("changed bytes must reject approval: %v", err)
			}
			if got := runManageSessionsGitOutput(t, repo, "rev-parse", "HEAD"); got != base {
				t.Fatal("rejected commit advanced HEAD")
			}
			if got := runManageSessionsGitOutput(t, repo, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "" {
				t.Fatalf("rejected commit staged files: %s", got)
			}
			write("approved\n")
			output, err := runtime.executeManageSessions(ctx, scope, payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := runManageSessionsGitOutput(t, repo, "show", "HEAD:change.txt"); got != "approved\n" {
				t.Fatalf("committed bytes = %q", got)
			}
			if got := runManageSessionsGitOutput(t, repo, "rev-parse", "HEAD^"); got != base {
				t.Fatal("commit did not advance from approved HEAD")
			}
			if service.sessions["self"].Lifecycle.Phase != phase || !strings.Contains(output, `"session_states_unchanged":true`) {
				t.Fatalf("commit must preserve session state: %s", output)
			}
		})
	}
}
