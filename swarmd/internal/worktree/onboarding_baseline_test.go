package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: a reviewed onboarding baseline must actually support the managed
// allocator and expose selected content, never omitted files. Threat: a nominal
// ready response can mask unusable Git metadata or silently empty child trees.
// Real temporary Git, workspace stores, and the allocator prove exact source HEAD,
// clean isolated content and source preservation without a live provider/session.
func TestOnboardingBaselineAllocatesManagedContent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ws := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "selected.txt"), []byte("baseline"), 0600)
	os.WriteFile(filepath.Join(source, "omitted.txt"), []byte("retain locally"), 0600)
	review, err := ws.ReviewRepositoryForPrincipal(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ws.PrepareRepositoryBaselineForPrincipal(principal, workspace.RepositoryBaselineRequest{Path: source, ExpectedResolvedPath: source, ReviewDigest: review.Digest, SelectedPaths: []string{"selected.txt"}, ConfirmBaseline: true, ConfirmOmissions: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspaceWithBranchMode(source, true, "", "agent/onboarding-fixture", "onboarding-owner", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateSessionRepositoryLane(source, allocation.WorkspacePath, "onboarding-owner", allocation.BranchName); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(allocation.WorkspacePath, "selected.txt"))
	if err != nil || string(data) != "baseline" {
		t.Fatalf("selected content=%q %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(allocation.WorkspacePath, "omitted.txt")); !os.IsNotExist(err) {
		t.Fatal("omitted content copied")
	}
	if head, err := runGit(allocation.WorkspacePath, "rev-parse", "HEAD"); err != nil || head != state.HeadCommit {
		t.Fatalf("allocation HEAD=%q %v", head, err)
	}
	if err := os.WriteFile(filepath.Join(allocation.WorkspacePath, "selected.txt"), []byte("isolated edit"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(source, "selected.txt"))
	if err != nil || string(data) != "baseline" {
		t.Fatal("child edit changed source")
	}
	data, err = os.ReadFile(filepath.Join(source, "omitted.txt"))
	if err != nil || string(data) != "retain locally" {
		t.Fatal("source omission lost")
	}
}
