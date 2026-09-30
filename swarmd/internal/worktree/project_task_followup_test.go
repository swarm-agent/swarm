package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestProjectTaskFollowupAllocationRetry(t *testing.T) {
	// Purpose: exact committed-source allocation is retryable after allocation
	// before session persistence. Threat: duplicate worktrees or changed source
	// HEAD adoption. Real Git plus catalog is the narrow allocator authority.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	db, err := pebblestore.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	ws := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(db))
	s := NewService(pebblestore.NewWorktreeStore(db), ws, nil)
	p := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	repo := initRollbackTestRepository(t)
	if _, err := ws.AddForPrincipal(p, repo, "Repo", "", false); err != nil { t.Fatal(err) }
	state, err := s.InspectTaskWorkspace(repo)
	if err != nil { t.Fatal(err) }
	first, err := s.AllocateProjectTaskFollowup(p, repo, "owner", "agent/task-followup-proof", state.HeadCommit, state.BranchName)
	if err != nil { t.Fatal(err) }
	second, err := s.AllocateProjectTaskFollowup(p, repo, "owner", first.BranchName, state.HeadCommit, state.BranchName)
	if err != nil || first != second { t.Fatalf("duplicate/rebound allocation: %+v %+v %v", first, second, err) }
	if err := s.ValidateSessionRepositoryLane(repo, first.WorkspacePath, "owner", first.BranchName); err != nil { t.Fatal(err) }
	if _, err := s.AllocateProjectTaskFollowup(identity.Principal{Type: "user", UserID: "foreign", AccountScopeID: "foreign"}, repo, "owner", first.BranchName, state.HeadCommit, state.BranchName); err == nil { t.Fatal("foreign account allocated") }
	if _, err := s.AllocateProjectTaskFollowup(p, repo, "owner", "agent/invalid-head-proof", "not-a-commit", state.BranchName); err == nil { t.Fatal("missing committed source accepted") }
	if err := os.WriteFile(filepath.Join(first.WorkspacePath, "uncommitted"), []byte("retained work"), 0600); err != nil { t.Fatal(err) }
	if _, err := s.AllocateProjectTaskFollowup(p, repo, "owner", first.BranchName, state.HeadCommit, state.BranchName); err == nil { t.Fatal("dirty partial allocation silently reused") }
	bytes, err := os.ReadFile(filepath.Join(first.WorkspacePath, "uncommitted"))
	if err != nil || string(bytes) != "retained work" { t.Fatal("rejection destroyed retained work") }
	current, err := s.InspectTaskWorkspace(repo)
	if err != nil || current != state { t.Fatal("allocation/rejections changed source checkout") }
}
