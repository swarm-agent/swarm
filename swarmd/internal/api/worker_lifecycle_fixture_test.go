package api

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	runruntime "swarm/packages/swarmd/internal/run"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Exercise HTTP lifecycle handlers against the real shared execution service,
// isolated Git allocation and V3/Pebble mutations. Enqueue records dispatch only;
// these deterministic tests do not claim provider execution or live acceptance.
func setupWorkerAPIExecution(t *testing.T, s *Server, db *store.Store) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		if out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	ss := s.sessions.Store()
	if err := ss.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acct-test", UserID: "user-test"}
	entry, err := workspaces.AddForPrincipal(p, repo, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Worker dispatch now requires an unambiguous project for generated tasks.
	if err := ss.PutProject("acct-test", &store.ProjectRecord{ID: "project_workers", Name: "Worker tasks", Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	agents := agent.NewService(store.NewAgentStore(db), nil)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), nil, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, catalog, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatalf("catalog: %v", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: "acct-test", Swarm: store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	runs := runruntime.NewService(s.sessions, models, nil, tool.NewRuntime(1), nil, agents, nil, nil)
	runs.SetWorkspaceService(workspaces)
	runs.SetAgentModelSettingsService(agentmodelsettings.NewService(settings))
	runs.SetSessionDeployCanonicalizer(func(in runruntime.SessionDeployCanonicalizeInput) (runruntime.SessionDeployCanonicalization, error) {
		return runruntime.SessionDeployCanonicalization{SourceWorkspaceID: entry.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	})
	execution, err := runruntime.NewWorkerExecutionService(runs, ss, worktree.NewService(store.NewWorktreeStore(db), workspaces, nil), s.sessions.ApplySessionMutation, func(p identity.Principal, intent store.V3SessionRunIntent) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	runs.SetWorkerExecutionService(execution)
	s.runner = runs
	return entry.WorkspaceID
}

func activateWorkerAPIFixture(t *testing.T, s *Server, w store.WorkerRecord, workspaceID string) store.WorkerRecord {
	t.Helper()
	execution, err := s.workerExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("acct-test", "user-test", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
