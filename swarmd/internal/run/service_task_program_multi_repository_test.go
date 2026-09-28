package run

import (
    "os"
    "path/filepath"
    "strings"
    "testing"

    pebblestore "swarm/packages/swarmd/internal/store/pebble"
    worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: multi-repository admission must reject aliases of one actual Git
// repository before allocating any lane. The production boundary is
// multiRepositoryWorkspacePath/ResolveTaskBase, not lexical path comparison.
func TestMultiRepositoryRejectsGitRootAliasBeforeAllocation(t *testing.T) {
    repo := programFixtureRepo(t)
    alias := filepath.Join(t.TempDir(), "alias")
    if err := os.Symlink(repo, alias); err != nil { t.Fatal(err) }
    svc := &Service{worktrees: &worktree.Service{}}
    p := &taskProgramScheduler{service: svc, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "one", AgentType: "coder", WorkspacePath: repo}, {ID: "two", AgentType: "coder", WorkspacePath: alias}}}}}
    // Without workspace authority admission fails closed before any Git mutation.
    before := programFixtureGit(t, repo, "worktree", "list", "--porcelain")
    if _, err := p.multiRepositoryWorkspacePath(); err == nil { t.Fatal("accepted unbound source aliases") }
    if got := programFixtureGit(t, repo, "worktree", "list", "--porcelain"); got != before { t.Fatal("denial allocated a worktree") }
}

// Purpose: a retained lane must be tied to the canonical workspace ID and
// generation, not just to a path grant. Revocation/rebinding must fail without
// changing the integration destination.
func TestTaskProgramCanonicalSourceGenerationRejectsRebinding(t *testing.T) {
    repo := programFixtureRepo(t)
    parent := pebblestore.SessionSnapshot{ID: "parent", WorkspacePath: repo, WorkspaceGrants: []pebblestore.WorkspaceGrant{{WorkspaceID: "repo", WorkspaceGeneration: 1, Path: repo}}}
    svc := &Service{}
    svc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
        return SessionWorkspaceCanonicalization{WorkspaceID: input.WorkspaceID, WorkspaceGeneration: 2, WorkspaceState: "active", WorkspaceName: "repo", SourceWorkspacePath: repo, RuntimeWorkspacePath: repo, WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
    })
    p := &taskProgramScheduler{service: svc, parentSession: parent}
    if _, _, err := p.canonicalRepositorySource(repo); err == nil || !strings.Contains(err.Error(), "generation") { t.Fatalf("stale workspace generation: %v", err) }
}

// Purpose: a multi-repo job without a pinned source may not silently use the
// parent workspace. The scheduler rejects it before integration or launch.
func TestMultiRepositoryCoderSourceRequiresExplicitTarget(t *testing.T) {
    p := &taskProgramScheduler{record: pebblestore.TaskProgramRecord{RepositoryLanes: map[string]pebblestore.TaskProgramRepositoryLane{"repo": {SourcePath: "repo"}}}}
    _, err := p.coderSourceForJob(pebblestore.TaskProgramJobSpec{ID: "build"})
    if err == nil || !strings.Contains(err.Error(), "explicit workspace_path") { t.Fatalf("missing target err=%v", err) }
}

func TestMultiRepositoryIntegrationRequiresWorktreeAuthority(t *testing.T) {
    p := &taskProgramScheduler{service: &Service{}, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}}}}
    if err := p.integrateMultiRepositoryStage(0); err == nil || !strings.Contains(err.Error(), "canonical task integration") { t.Fatalf("integration without authority err=%v", err) }
}
