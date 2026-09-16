package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/tool"
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

// Purpose: runtime preflight (not only JSON parsing) rejects split Coder targets
// before durable program creation, allocation or child session creation. Then
// prepare two regular cross-repository Coders to prove that separate supported
// assignments persist distinct repository/base identities without source writes.
func TestTaskTargetRuntimePreflightAndRegularChildren(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	source, other := programFixtureRepo(t), programFixtureRepo(t)
	wt := &worktree.Service{}
	svc.worktrees = wt
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := wt.AllocateTaskWorkspace(source, base, "preflight-parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent.WorkspacePath, parent.WorktreeRootPath = source, lane.WorkspacePath
	parent.WorktreeEnabled = true
	parent.WorktreeBranch, parent.WorktreeBaseBranch = lane.BranchName, base.ParentBranch
	parent.TemporaryWorkspaceRoots = []string{other}
	parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit}
	parsed := taskCallArguments{Action: "start", Prompt: "Implement scoped work", Program: &taskProgramSpec{ID: "unsupported", Stages: []taskProgramStage{{ID: "build", DependencyEvidence: "ready"}}}}
	for i, target := range []string{source, other} {
		jobID := fmt.Sprintf("job-%d", i)
		parsed.Program.Jobs = append(parsed.Program.Jobs, taskProgramJob{ID: jobID, StageID: "build", RequestedSubagentType: "coder", TargetWorkspacePath: target, MetaPrompt: "Implement source.txt", AssignmentLabel: jobID, Deliverable: "commit", OwnedScope: []string{"source.txt"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"})
		parsed.Launches = append(parsed.Launches, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: target, OwnedScope: []string{"source.txt"}})
	}
	if err := svc.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := svc.sessions.ListSessionsForAccountUser(parent.AccountScopeID, parent.UserID, 100)
	if err != nil {
		t.Fatal(err)
	}
	inventories := []string{programFixtureGit(t, source, "worktree", "list", "--porcelain"), programFixtureGit(t, other, "worktree", "list", "--porcelain")}
	_, err = svc.executeTaskToolWithParsed(context.Background(), id, "auto", 1, tool.Call{Name: "task", CallID: "reject-split"}, nil, taskExecutionRequest{ParentSession: &parent, Parsed: parsed, ParsedProvided: true})
	if err == nil || !strings.Contains(err.Error(), "one repository") {
		t.Fatalf("preflight: %v", err)
	}
	if _, ok, err := svc.sessions.GetTaskProgram(id, "unsupported"); err != nil || ok {
		t.Fatalf("program persisted: %v %v", ok, err)
	}
	after, err := svc.sessions.ListSessionsForAccountUser(parent.AccountScopeID, parent.UserID, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejection created/mutated session")
	}
	for i, path := range []string{source, other} {
		if programFixtureGit(t, path, "worktree", "list", "--porcelain") != inventories[i] {
			t.Fatal("rejection allocated worktree")
		}
	}
	for i, selector := range []string{source, other} {
		target, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: selector})
		if err != nil {
			t.Fatal(err)
		}
		childBase, err := wt.ResolveTaskBase(target)
		if err != nil {
			t.Fatal(err)
		}
		profile, virtual, agentSource, err := svc.resolveTaskLaunchProfile(parent, "coder")
		if err != nil {
			t.Fatal(err)
		}
		launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{RequestedSubagent: "coder", MetaPrompt: "Implement source.txt", TargetWorkspacePath: target, TaskBase: &childBase, OwnedScope: []string{"source.txt"}, VirtualTarget: virtual, LogicalTaskID: fmt.Sprintf("cross-%d", i)}, "scoped work", "", &profile, agentSource, nil)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := svc.resolveRunWorkspaceScope(launch.ChildSession, identity.Principal{})
		if err != nil {
			t.Fatal(err)
		}
		if scope.PrimaryPath != launch.ChildWorkspacePath || scope.WorktreeBaseCommit != childBase.BaseCommit || scope.PrimaryPath == target || len(scope.MutationScopes) != 1 {
			t.Fatalf("child scope: %+v", scope)
		}
		if programFixtureGit(t, scope.PrimaryPath, "rev-parse", "--path-format=absolute", "--git-common-dir") != programFixtureGit(t, selector, "rev-parse", "--path-format=absolute", "--git-common-dir") {
			t.Fatal("child in wrong repository")
		}
		if programFixtureGit(t, scope.PrimaryPath, "rev-parse", "HEAD") != childBase.BaseCommit {
			t.Fatal("child base mismatch")
		}
		if programFixtureGit(t, selector, "status", "--porcelain") != "" || programFixtureGit(t, selector, "rev-parse", "HEAD") != childBase.BaseCommit {
			t.Fatal("captured source mutated")
		}
	}
}
