package run

import (
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: resolveTaskTargetWorkspace and programWorkspacePath must authenticate
// default and explicit roots before allocation. Real Git is the narrowest layer
// proving owned-lane identity, canonical aliases and unchanged repository state;
// catalog membership or a descendant directory must not expand task authority.
func TestTaskTargetCanonicalRootsAndProgramPreflight(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	source := programFixtureRepo(t)
	other := programFixtureRepo(t)
	wt := &worktree.Service{}
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := wt.AllocateTaskWorkspace(source, base, "target-parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := pebblestore.SessionSnapshot{ID: "parent", UserID: "user", AccountScopeID: "account", WorkspacePath: lane.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: lane.WorkspacePath, WorktreeBranch: lane.BranchName, WorktreeBaseBranch: "dev", TemporaryWorkspaceRoots: []string{other}, Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit}}
	svc := &Service{worktrees: wt}
	beforeSource := programFixtureGit(t, source, "worktree", "list", "--porcelain")
	beforeOther := programFixtureGit(t, other, "worktree", "list", "--porcelain")
	for _, agent := range []string{"coder", "finder"} {
		for _, selector := range []string{"", ".", source, lane.WorkspacePath, other} {
			want := lane.WorkspacePath
			if selector == other {
				want = other
			}
			got, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, taskLaunchSpec{RequestedSubagentType: agent, TargetWorkspacePath: selector})
			if err != nil || got != want {
				t.Fatalf("%s selector %q: %q %v", agent, selector, got, err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(other, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{filepath.Join(other, "nested"), filepath.Dir(other)} {
		if _, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: selector}); err == nil {
			t.Fatalf("non-root target accepted: %s", selector)
		}
	}
	stale := parent
	stale.WorktreeBranch = "agent/wrong"
	if _, _, err := svc.resolveTaskTargetWorkspace(stale, identity.Principal{}, taskLaunchSpec{RequestedSubagentType: "coder"}); err == nil {
		t.Fatal("omitted target bypassed stale runtime validation")
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{AgentType: "coder", WorkspacePath: source}, {AgentType: "coder", WorkspacePath: "."}}}}}
	if got, err := p.programWorkspacePath(); err != nil || got != lane.WorkspacePath {
		t.Fatalf("source/runtime aliases: %s %v", got, err)
	}
	p.record.Definition.Jobs[1].WorkspacePath = other
	if _, err := p.programWorkspacePath(); err == nil {
		t.Fatal("cross-repository program accepted")
	}
	if p.record.Revision != 0 || p.record.RepositoryLane != nil || programFixtureGit(t, source, "worktree", "list", "--porcelain") != beforeSource || programFixtureGit(t, other, "worktree", "list", "--porcelain") != beforeOther {
		t.Fatal("preflight mutated program or repository inventory")
	}
	if programFixtureGit(t, source, "rev-parse", "HEAD") != base.BaseCommit || programFixtureGit(t, lane.WorkspacePath, "rev-parse", "HEAD") != base.BaseCommit {
		t.Fatal("resolver advanced source or owned lane")
	}
}

// Purpose: typed program lanes are internal routing evidence, not authority to
// substitute another repository, stale branch or invented base. The resolver's
// real-Git identity check must reject these without modifying either checkout.
func TestTaskTargetTypedLaneIdentity(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	source := programFixtureRepo(t)
	foreign := programFixtureRepo(t)
	wt := &worktree.Service{}
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := wt.AllocateTaskWorkspace(source, base, "typed-lane", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := pebblestore.SessionSnapshot{ID: "parent", UserID: "user", AccountScopeID: "account", WorkspacePath: source}
	svc := &Service{worktrees: wt}
	binding := pebblestore.TaskProgramRepositoryLane{SourcePath: source, WorkspacePath: lane.WorkspacePath, Branch: lane.BranchName, BaseCommit: base.BaseCommit}
	resolve := func(b pebblestore.TaskProgramRepositoryLane) error {
		_, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, taskLaunchSpec{RequestedSubagentType: "coder", ProgramRepositoryLane: &b})
		return err
	}
	if err := resolve(binding); err != nil {
		t.Fatal(err)
	}
	before := programFixtureGit(t, source, "worktree", "list", "--porcelain")
	for _, kind := range []string{"foreign", "branch", "base", "missing-source", "missing-base"} {
		bad := binding
		switch kind {
		case "foreign":
			bad.WorkspacePath = foreign
			bad.Branch = "dev"
		case "branch":
			bad.Branch = "agent/wrong"
		case "base":
			bad.BaseCommit = "0000000000000000000000000000000000000000"
		case "missing-source":
			bad.SourcePath = ""
		case "missing-base":
			bad.BaseCommit = ""
		}
		if err := resolve(bad); err == nil {
			t.Fatalf("accepted %s lane", kind)
		}
	}
	if programFixtureGit(t, source, "worktree", "list", "--porcelain") != before || programFixtureGit(t, source, "status", "--porcelain") != "" || programFixtureGit(t, foreign, "status", "--porcelain") != "" {
		t.Fatal("typed lane validation mutated repository state")
	}
}
