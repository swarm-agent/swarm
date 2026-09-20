package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func runCommittedSourceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initCommittedSourceTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCommittedSourceGit(t, dir, "init", "-b", "dev")
	runCommittedSourceGit(t, dir, "config", "user.name", "Test")
	runCommittedSourceGit(t, dir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommittedSourceGit(t, dir, "add", "README.md")
	runCommittedSourceGit(t, dir, "commit", "-m", "initial commit")
	return dir
}

type committedSourceTestHarness struct {
	storeDir     string
	store        *pebblestore.Store
	events       *pebblestore.EventLog
	sessionStore *pebblestore.SessionStore
	sessions     *sessionruntime.Service
	workspace    *workspaceruntime.Service
	worktrees    *worktreeruntime.Service
	tools        *tool.Runtime
	permissions  *permission.Service
	agents       *agentruntime.Service
	providers    *registry.Registry
	runner       *committedSourceFakeRunner
	service      *Service
	principal    identity.Principal
}

type committedSourceFakeRunner struct {
	t     *testing.T
	calls int
	reqs  []provideriface.Request
}

func (r *committedSourceFakeRunner) ID() string { return "codex" }
func (r *committedSourceFakeRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	r.calls++
	r.reqs = append(r.reqs, req)
	runCommittedSourceGit(r.t, req.WorkspacePath, "commit", "--allow-empty", "-m", "synthetic corrective handoff")
	return provideriface.Response{Text: "Committed correction; parent validation required."}, nil
}
func (r *committedSourceFakeRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}

func newCommittedSourceTestHarness(t *testing.T) *committedSourceTestHarness {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	storeDir := filepath.Join(t.TempDir(), "pebble")
	store, err := pebblestore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	for phase := 0; phase < 4; phase++ {
		ready, bErr := sessionStore.BackfillRepositoryHistory(100)
		if bErr != nil || (phase == 3 && !ready) {
			t.Fatalf("initialize repository history: %v", bErr)
		}
	}
	sessions := sessionruntime.NewService(sessionStore, events)

	wsStore := pebblestore.NewWorkspaceStore(store)
	workspaceService := workspaceruntime.NewService(wsStore)

	worktreeStore := pebblestore.NewWorktreeStore(store)
	worktrees := worktreeruntime.NewService(worktreeStore, workspaceService, events)

	toolRuntime := tool.NewRuntime(1)
	toolRuntime.SetManageSessionService(sessions)
	toolRuntime.SetManageWorktreeServices(sessions, workspaceService, worktrees)

	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := agents.EnsureDefaultsForAccount("test-account"); err != nil {
		t.Fatal(err)
	}

	runner := &committedSourceFakeRunner{t: t}
	providers := registry.New()
	providers.RegisterRunner(runner)

	models := model.NewService(pebblestore.NewModelStore(store), events, model.NewCatalogService(pebblestore.NewModelCatalogStore(store)))
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	svc := NewService(sessions, models, providers, toolRuntime, permissions, agents, nil, events)
	svc.worktrees = worktrees
	svc.workspace = workspaceService
	settings := pebblestore.NewAgentModelSettingsStore(store)
	assignment := pebblestore.AgentModelAssignment{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}
	if _, err := settings.PutForAccount(pebblestore.AgentModelSettingsRecord{AccountScopeID: "test-account", Swarm: pebblestore.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: pebblestore.SystemAgentModelAssignments{Coder: assignment, Compact: assignment, Finder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	svc.SetAgentModelSettingsService(agentmodelsettings.NewService(settings))

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		SessionID:      "parent-session",
	}

	return &committedSourceTestHarness{
		storeDir:     storeDir,
		store:        store,
		events:       events,
		sessionStore: sessionStore,
		sessions:     sessions,
		workspace:    workspaceService,
		worktrees:    worktrees,
		tools:        toolRuntime,
		permissions:  permissions,
		agents:       agents,
		providers:    providers,
		runner:       runner,
		service:      svc,
		principal:    principal,
	}
}

func (h *committedSourceTestHarness) setupPriorCompletedChild(
	t *testing.T,
	parentSession pebblestore.SessionSnapshot,
	taskCallID string,
	childID string,
	logicalTaskID string,
	childAlloc worktreeruntime.Allocation,
	baseCommit string,
	headCommit string,
	targetWorkspacePath string,
	integrationBaseCommit string,
) pebblestore.SessionSnapshot {
	t.Helper()
	h.principal.SessionID = parentSession.ID
	parentSession.Metadata["swarm_v3_worktree_owner_session_id"] = parentSession.ID
	if result, err := h.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: parentSession.ID, UserID: parentSession.UserID, AccountScopeID: parentSession.AccountScopeID,
		Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &parentSession,
		IdempotencyKey: "admit-parent-" + taskCallID, RequestHash: "admit-parent-" + taskCallID,
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: parentSession.WorktreeRootPath, SourcePath: parentSession.WorkspacePath, OwnerSessionID: parentSession.ID, Branch: parentSession.WorktreeBranch},
	}); err != nil || result.Error != nil || result.Conflict != nil {
		t.Fatalf("admit parent: %v %+v", err, result)
	}
	now := time.Now().UnixMilli()
	meta := map[string]any{
		"parent_session_id":                  parentSession.ID,
		"parent_task_call_id":                taskCallID,
		"logical_task_id":                    logicalTaskID,
		"lineage_kind":                       "delegated_subagent",
		"subagent":                           "coder",
		"base_commit":                        baseCommit,
		"head_commit":                        headCommit,
		"worktree_path":                      childAlloc.WorkspacePath,
		"child_branch":                       childAlloc.BranchName,
		"worktree_base_branch":               childAlloc.BaseBranch,
		"swarm_v3_source_workspace_path":     targetWorkspacePath,
		"swarm_v3_runtime_workspace_path":    childAlloc.WorkspacePath,
		"swarm_v3_worktree_owner_session_id": childID,
		"target_workspace_path":              targetWorkspacePath,
	}
	var sourceTuple map[string]any
	var sourceBinding tool.CommittedSourceBinding
	if integrationBaseCommit != "" {
		prior, found, err := h.sessions.GetSession("child-1-rep")
		if err != nil || !found {
			t.Fatalf("prior correction: %v", err)
		}
		req := tool.CommittedSourceRequest{TaskCallID: mapString(prior.Metadata, "parent_task_call_id"), ChildSessionID: prior.ID, HeadCommit: baseCommit}
		sourceBinding, err = h.tools.ResolveCommittedSource(tool.WorkspaceScope{SessionID: parentSession.ID, Principal: h.principal}, req)
		if err != nil {
			t.Fatal(err)
		}
		sourceTuple = map[string]any{"task_call_id": req.TaskCallID, "child_session_id": req.ChildSessionID, "head_commit": req.HeadCommit}
		meta["integration_base_commit"] = integrationBaseCommit
		meta["committed_source"] = sourceTuple
		meta["committed_source_binding"] = sourceBinding
		meta["swarm_v3_source_workspace_path"] = sourceBinding.CanonicalSourcePath
	}
	_, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      childID,
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  childAlloc.WorkspacePath,
		WorkspaceName:  childID,
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   childAlloc.WorkspacePath,
			BranchName: childAlloc.BranchName,
			BaseBranch: childAlloc.BaseBranch,
		},
		Metadata: meta,
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, _, _ := h.sessions.GetSession(childID)
	_, err = h.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      childID,
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &snap,
		IdempotencyKey: "admit-" + childID,
		RequestHash:    "admit-" + childID,
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
			Kind:           "allocated",
			Path:           childAlloc.WorkspacePath,
			SourcePath:     mapString(meta, "swarm_v3_source_workspace_path"),
			OwnerSessionID: childID,
			Branch:         childAlloc.BranchName,
			DelegatedCoder: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = h.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      childID,
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		Phase:          "completed",
		EndedAt:        now,
		Generation:     1,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = h.sessions.CreateDelegatedChildLineage(
		pebblestore.DelegatedChildLineageRecord{
			AccountScopeID:    h.principal.AccountScopeID,
			LogicalTaskID:     logicalTaskID,
			CurrentGeneration: 1,
			CurrentSessionID:  childID,
		},
		pebblestore.DelegatedChildGenerationRecord{
			SessionID:           childID,
			AccountScopeID:      h.principal.AccountScopeID,
			ParentSessionID:     parentSession.ID,
			LogicalTaskID:       logicalTaskID,
			Generation:          1,
			WorkspacePath:       childAlloc.WorkspacePath,
			WorktreeBranch:      childAlloc.BranchName,
			ParentBranch:        childAlloc.BaseBranch,
			ImmutableBaseCommit: baseCommit,
		},
		"init-lineage-"+childID,
	)
	if err != nil {
		t.Fatal(err)
	}

	launchRow := map[string]any{
		"phase":                 "completed",
		"child_session_id":      childID,
		"subagent":              "coder",
		"launch_index":          1,
		"parent_workspace_path": targetWorkspacePath,
		"workspace_path":        childAlloc.WorkspacePath,
		"worktree_root_path":    childAlloc.WorkspacePath,
		"worktree_branch":       childAlloc.BranchName,
		"worktree_base_branch":  childAlloc.BaseBranch,
		"parent_branch":         childAlloc.BaseBranch,
		"base_commit":           baseCommit,
		"head_commit":           headCommit,
	}
	if integrationBaseCommit != "" {
		launchRow["integration_base_commit"] = integrationBaseCommit
		launchRow["committed_source"] = sourceTuple
		launchRow["committed_source_binding"] = sourceBinding
	}
	parentSnap, _, _ := h.sessions.GetSession(parentSession.ID)
	launches, _ := parentSnap.Metadata["task_launches"].(map[string]any)
	if launches == nil {
		launches = map[string]any{}
	}
	launches[taskCallID] = map[string]any{
		"call_id":           taskCallID,
		"parent_session_id": parentSession.ID,
		"subagent":          "coder",
		"child_session_id":  childID,
		"launches":          []any{launchRow},
	}
	parentSnap.Metadata["task_launches"] = launches
	_, err = h.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      parentSession.ID,
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "parent-meta-" + taskCallID,
		RequestHash:    "parent-meta-" + taskCallID,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedParent, _, _ := h.sessions.GetSession(parentSession.ID)
	return updatedParent
}

// Purpose: parseTaskCallArguments and parseLaunchSpec must reject invalid agent types,
// swarm mode, Task Program job definitions, combination with recovery_source_digest,
// simultaneous workspace_path (ambiguous retargeting), and malformed commit OIDs.
// Threat: unauthorized worker types or invalid parameters bypass isolation or retarget work.
// Symbols: run.parseTaskCallArguments, run.parseLaunchSpec, run.parseTaskCommittedSource.
// Narrow layer: parser unit test without store or worktree setup.
func TestTaskCommittedSourceParseRejections(t *testing.T) {
	validOID := strings.Repeat("a", 40)
	validCS := map[string]any{
		"task_call_id":     "call-1",
		"child_session_id": "sess-child",
		"head_commit":      validOID,
	}

	// 1. Non-Coder (Finder, Designer) must be rejected.
	for _, agent := range []string{"finder", "designer"} {
		args := map[string]any{
			"prompt":           "Fix issue",
			"subagent_type":    agent,
			"meta_prompt":      "Perform inspection",
			"committed_source": validCS,
		}
		_, err := parseTaskCallArguments(mustJSON(t, args))
		if err == nil || !strings.Contains(err.Error(), "committed_source is supported only for Coder launches") {
			t.Fatalf("expected non-coder %q rejection, got %v", agent, err)
		}
	}

	// 2. Swarm mode must be rejected.
	swarmArgs := map[string]any{
		"mode":             "swarm",
		"prompt":           "Swarm prompt",
		"agent_type":       "coder",
		"count":            2,
		"committed_source": validCS,
	}
	if _, err := parseTaskCallArguments(mustJSON(t, swarmArgs)); err == nil || !strings.Contains(err.Error(), "does not support committed_source") {
		t.Fatalf("expected swarm mode rejection, got %v", err)
	}

	// 3. Task Program start must reject top-level committed_source.
	progArgs := map[string]any{
		"action":           "start",
		"prompt":           "Run program",
		"committed_source": validCS,
		"program": map[string]any{
			"id": "prog-test",
			"stages": []any{
				map[string]any{"id": "s1", "dependency_evidence": "ready"},
			},
			"jobs": []any{
				map[string]any{
					"id":                  "j1",
					"stage_id":            "s1",
					"agent_type":          "coder",
					"title":               "Job 1",
					"meta_prompt":         "Do work",
					"deliverable":         "code",
					"acceptance_criteria": []string{"done"},
					"dependency_evidence": "ready",
				},
			},
		},
	}
	if _, err := parseTaskCallArguments(mustJSON(t, progArgs)); err == nil || !strings.Contains(err.Error(), "task program start does not support committed_source") {
		t.Fatalf("expected program start committed_source rejection, got %v", err)
	}

	// 4. Task Program job definition must reject committed_source.
	progJobArgs := map[string]any{
		"action": "start",
		"prompt": "Run program",
		"program": map[string]any{
			"id": "prog-test",
			"stages": []any{
				map[string]any{"id": "s1", "dependency_evidence": "ready"},
			},
			"jobs": []any{
				map[string]any{
					"id":                  "j1",
					"stage_id":            "s1",
					"agent_type":          "coder",
					"title":               "Job 1",
					"meta_prompt":         "Do work",
					"deliverable":         "code",
					"acceptance_criteria": []string{"done"},
					"dependency_evidence": "ready",
					"committed_source":    validCS,
				},
			},
		},
	}
	if _, err := parseTaskCallArguments(mustJSON(t, progJobArgs)); err == nil || !strings.Contains(err.Error(), "committed_source is not supported in Task Program job definitions") {
		t.Fatalf("expected program job committed_source rejection, got %v", err)
	}

	// 5. Cannot combine committed_source with recovery_source_digest.
	digest := strings.Repeat("b", 64)
	comboArgs := map[string]any{
		"prompt":                 "Fix issue",
		"subagent_type":          "coder",
		"meta_prompt":            "Fix code",
		"recovery_source_digest": digest,
		"committed_source":       validCS,
	}
	if _, err := parseTaskCallArguments(mustJSON(t, comboArgs)); err == nil || !strings.Contains(err.Error(), "cannot combine committed_source with recovery_source_digest") {
		t.Fatalf("expected combination recovery_source_digest rejection, got %v", err)
	}

	// 6. Cannot combine committed_source with workspace_path.
	wsArgs := map[string]any{
		"prompt":           "Fix issue",
		"subagent_type":    "coder",
		"meta_prompt":      "Fix code",
		"workspace_path":   "/other/repo",
		"committed_source": validCS,
	}
	if _, err := parseTaskCallArguments(mustJSON(t, wsArgs)); err == nil || !strings.Contains(err.Error(), "cannot combine committed_source with workspace_path") {
		t.Fatalf("expected combination workspace_path rejection, got %v", err)
	}

	// 7. Cannot combine top-level workspace_path with launches array having committed_source.
	topWsWithLaunches := map[string]any{
		"prompt":         "Fix issue",
		"workspace_path": "/other/repo",
		"launches": []any{
			map[string]any{
				"subagent_type":    "coder",
				"meta_prompt":      "Fix code",
				"committed_source": validCS,
			},
		},
	}
	if _, err := parseTaskCallArguments(mustJSON(t, topWsWithLaunches)); err == nil || !strings.Contains(err.Error(), "cannot combine committed_source with top-level workspace_path") {
		t.Fatalf("expected top-level workspace_path rejection, got %v", err)
	}

	// 8. Null committed_source rejected.
	nullCS := map[string]any{
		"prompt":           "Fix issue",
		"subagent_type":    "coder",
		"meta_prompt":      "Fix code",
		"committed_source": nil,
	}
	if _, err := parseTaskCallArguments(mustJSON(t, nullCS)); err == nil || !strings.Contains(err.Error(), "committed_source cannot be null") {
		t.Fatalf("expected null committed_source rejection, got %v", err)
	}

	// 9. Unknown field in committed_source rejected.
	unknownFieldCS := map[string]any{
		"task_call_id":     "call-1",
		"child_session_id": "sess-child",
		"head_commit":      validOID,
		"unknown_field":    "tampered",
	}
	unknownCSArgs := map[string]any{
		"prompt":           "Fix issue",
		"subagent_type":    "coder",
		"meta_prompt":      "Fix code",
		"committed_source": unknownFieldCS,
	}
	if _, err := parseTaskCallArguments(mustJSON(t, unknownCSArgs)); err == nil || !strings.Contains(err.Error(), "committed_source contains unknown field") {
		t.Fatalf("expected unknown field in committed_source rejection, got %v", err)
	}

	// 10. Malformed commit OIDs: short, non-hex, empty.
	for _, badOID := range []string{"", "short", "not-hex-characters-here-40-characters-long!!", strings.Repeat("z", 40)} {
		badCS := map[string]any{
			"task_call_id":     "call-1",
			"child_session_id": "sess-child",
			"head_commit":      badOID,
		}
		args := map[string]any{
			"prompt":           "Fix issue",
			"subagent_type":    "coder",
			"meta_prompt":      "Fix code",
			"committed_source": badCS,
		}
		if _, err := parseTaskCallArguments(mustJSON(t, args)); err == nil || !strings.Contains(err.Error(), "valid full hexadecimal head_commit") {
			t.Fatalf("expected bad OID %q rejection, got %v", badOID, err)
		}
	}

	// 11. Missing task_call_id or child_session_id.
	for _, missingField := range []string{"task_call_id", "child_session_id"} {
		m := map[string]any{
			"task_call_id":     "call-1",
			"child_session_id": "sess-child",
			"head_commit":      validOID,
		}
		delete(m, missingField)
		args := map[string]any{
			"prompt":           "Fix issue",
			"subagent_type":    "coder",
			"meta_prompt":      "Fix code",
			"committed_source": m,
		}
		if _, err := parseTaskCallArguments(mustJSON(t, args)); err == nil {
			t.Fatalf("expected missing %q rejection, got nil", missingField)
		}
	}

	// 12. Top-level committed_source when launches array is present.
	topLevelWithLaunches := map[string]any{
		"prompt":           "Fix issue",
		"committed_source": validCS,
		"launches": []any{
			map[string]any{
				"subagent_type": "coder",
				"meta_prompt":   "Do work",
			},
		},
	}
	if _, err := parseTaskCallArguments(mustJSON(t, topLevelWithLaunches)); err == nil || !strings.Contains(err.Error(), "must declare committed_source on each Coder launch, not at top level") {
		t.Fatalf("expected top-level with launches rejection, got %v", err)
	}

	// 13. Valid single shorthand and launches array must parse cleanly.
	validSingle := map[string]any{
		"prompt":           "Fix issue",
		"subagent_type":    "coder",
		"meta_prompt":      "Fix code",
		"committed_source": validCS,
	}
	parsedSingle, err := parseTaskCallArguments(mustJSON(t, validSingle))
	if err != nil {
		t.Fatalf("valid single shorthand failed: %v", err)
	}
	if len(parsedSingle.Launches) != 1 || parsedSingle.Launches[0].CommittedSource == nil || parsedSingle.Launches[0].CommittedSource.HeadCommit != validOID {
		t.Fatalf("parsed single launch lost committed_source: %+v", parsedSingle.Launches[0].CommittedSource)
	}

	validLaunches := map[string]any{
		"prompt": "Fix issues",
		"launches": []any{
			map[string]any{
				"subagent_type":    "coder",
				"meta_prompt":      "Fix code",
				"committed_source": validCS,
			},
		},
	}
	parsedLaunches, err := parseTaskCallArguments(mustJSON(t, validLaunches))
	if err != nil {
		t.Fatalf("valid launches array failed: %v", err)
	}
	if len(parsedLaunches.Launches) != 1 || parsedLaunches.Launches[0].CommittedSource == nil || parsedLaunches.Launches[0].CommittedSource.HeadCommit != validOID {
		t.Fatalf("parsed launch array lost committed_source: %+v", parsedLaunches.Launches[0].CommittedSource)
	}
}

// Purpose: parseApprovedTaskLaunchManifest must verify the approved manifest matches the requested
// committed_source tuple and resolved committed_source_binding exactly, rejecting tampering or removal.
// Threat: attacker modifies approved manifest arguments to point to unauthorized commit or target repository.
// Symbols: run.parseApprovedTaskLaunchManifest, run.taskLaunchSpec, run.taskLaunchManifestRow.
// Narrow layer: manifest verification parsing without environment side effects.
func TestTaskCommittedSourceApprovalBindingAndTampering(t *testing.T) {
	validOID := strings.Repeat("c", 40)
	req := tool.CommittedSourceRequest{
		TaskCallID:     "call-1",
		ChildSessionID: "child-session-1",
		HeadCommit:     validOID,
	}
	binding := tool.CommittedSourceBinding{
		TaskCallID:            "call-1",
		ChildSessionID:        "child-session-1",
		HeadCommit:            validOID,
		CanonicalSourcePath:   "/canonical/repo",
		DestinationPath:       "/destination/repo",
		DestinationBranch:     "agent/dest",
		DestinationKind:       tool.DestinationKindOwnedLane,
		IntegrationBaseCommit: strings.Repeat("b", 40),
	}

	profile := pebblestore.AgentProfile{
		Name:      "coder",
		Mode:      "primary",
		Protected: true,
	}

	spec := taskLaunchSpec{
		RequestedSubagentType:  "coder",
		MetaPrompt:             "Fix issue",
		CommittedSource:        &req,
		CommittedSourceBinding: &binding,
		TargetWorkspacePath:    binding.DestinationPath,
	}

	manifest := taskLaunchManifest{
		PathID: "tool.task.v1",
		Launches: []taskLaunchManifestRow{
			{
				RequestedSubagentType:  "coder",
				ResolvedAgentName:      "coder",
				ParentCopy:             true,
				ProfileSnapshot:        &profile,
				CommittedSource:        &req,
				CommittedSourceBinding: &binding,
				TargetWorkspacePath:    binding.DestinationPath,
			},
		},
	}
	hash, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	envelope := map[string]any{
		"manifest_hash": hash,
		"manifest":      manifest,
	}
	body := mustJSON(t, envelope)

	// Valid manifest must parse without error.
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{spec}); err != nil {
		t.Fatalf("valid manifest failed: %v", err)
	}

	// Tampered committed_source request in launch spec must fail.
	tamperedReq := req
	tamperedReq.ChildSessionID = "foreign-child"
	tamperedSpec := spec
	tamperedSpec.CommittedSource = &tamperedReq
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{tamperedSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source differs from requested tuple") {
		t.Fatalf("expected request tampering rejection, got %v", err)
	}

	// Tampered committed_source_binding in launch spec must fail.
	tamperedBinding := binding
	tamperedBinding.DestinationPath = "/tampered/destination"
	tamperedBindingSpec := spec
	tamperedBindingSpec.CommittedSourceBinding = &tamperedBinding
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{tamperedBindingSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source binding differs from resolved binding") {
		t.Fatalf("expected binding tampering rejection, got %v", err)
	}

	// Removed committed_source from spec must fail.
	removedSpec := spec
	removedSpec.CommittedSource = nil
	removedSpec.CommittedSourceBinding = nil
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{removedSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source differs from requested tuple") {
		t.Fatalf("expected removal rejection, got %v", err)
	}
}

// Purpose: Execute a full corrective Coder allocation in the same repository, starting from
// an exact prior completed child's commit C while retaining destination and inherited base B.
// Threat: child worktree fails to checkout at C or loses inherited delivery base B.
// Symbols: run.Service.executeTaskToolWithParsed, run.Service.buildTaskLaunchPermissionPayload.
// Narrow layer: Run Service task delegation with real synthetic Git repo and Pebble store.
func TestTaskCommittedSourceSameRepositoryCorrectiveAllocation(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)

	// Register repo in workspace catalog.
	if _, err := h.workspace.AddForPrincipal(h.principal, repo, filepath.Base(repo), "", false); err != nil {
		t.Fatal(err)
	}

	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	// Allocate parent worktree.
	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create parent session.
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.RepoRoot,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
			"base_commit":                     commitB,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Allocate child 1 worktree.
	child1Base := worktreeruntime.TaskBase{
		RepoRoot:     repo,
		ParentBranch: parentAlloc.BranchName,
		BaseCommit:   commitB,
	}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Child 1 commits full C tree: text, binary, executable, and out-of-scope files.
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "feature.go"), []byte("package feature\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binaryContent := []byte{0x00, 0xFF, 0xDE, 0xAD, 0xBE, 0xEF}
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "binary.bin"), binaryContent, 0o644); err != nil {
		t.Fatal(err)
	}
	executableContent := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "run.sh"), executableContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "outside.txt"), []byte("readable outside scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", ".")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "implement feature with binary and script")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-child-1", "child-1", "logical-1", child1Alloc, commitB, commitC, parentAlloc.WorkspacePath, "")

	parentHeadBefore := runCommittedSourceGit(t, parentAlloc.WorkspacePath, "rev-parse", "HEAD")

	// Parent launches Child 2 correcting Child 1.
	reqCall := tool.Call{
		CallID: "call-correction-1",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Correct feature in child 1",
			"subagent_type": "coder",
			"meta_prompt":   "Fix bug in feature",
			"owned_scope":   []string{"feature.go"},
			"committed_source": map[string]any{
				"task_call_id":     "call-child-1",
				"child_session_id": "child-1",
				"head_commit":      commitC,
			},
		}),
	}

	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatalf("buildTaskLaunchPermissionPayload failed: %v", err)
	}
	if len(manifest.Launches) != 1 {
		t.Fatalf("expected 1 launch row, got %d", len(manifest.Launches))
	}
	row := manifest.Launches[0]
	if row.CommittedSource == nil || row.CommittedSource.HeadCommit != commitC {
		t.Fatalf("manifest row lost committed_source: %+v", row.CommittedSource)
	}
	if row.CommittedSourceBinding == nil {
		t.Fatalf("manifest row missing committed_source_binding")
	}
	if row.CommittedSourceBinding.IntegrationBaseCommit != commitB {
		t.Fatalf("manifest row delivery base = %q, want %q", row.CommittedSourceBinding.IntegrationBaseCommit, commitB)
	}
	if row.CommittedSourceBinding.HeadCommit != commitC {
		t.Fatalf("manifest row allocation base = %q, want %q", row.CommittedSourceBinding.HeadCommit, commitC)
	}

	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-correction",
		Principal:            h.principal,
		ApplySessionMutation: h.sessions.ApplySessionMutation,
	}

	out, err := h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err != nil {
		t.Fatalf("executeTaskToolWithParsed failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	launches, ok := payload["launches"].([]any)
	if !ok || len(launches) != 1 {
		t.Fatalf("expected 1 launch in output, got: %v", payload)
	}
	launchResult := launches[0].(map[string]any)
	child2SessionID := asString(launchResult["child_session_id"])
	if child2SessionID == "" {
		t.Fatalf("missing child 2 session ID in output")
	}

	child2Session, ok, err := h.sessions.GetSession(child2SessionID)
	if err != nil || !ok {
		t.Fatalf("load child 2 session: %v", err)
	}

	// Assert child 2 was checked out at commit C.
	if baseCommit := asString(child2Session.Metadata["base_commit"]); baseCommit != commitC {
		t.Fatalf("child 2 base_commit = %q, want %q", baseCommit, commitC)
	}
	// Assert child 2 inherited delivery base B.
	if integrationBase := asString(child2Session.Metadata["integration_base_commit"]); integrationBase != commitB {
		t.Fatalf("child 2 integration_base_commit = %q, want %q", integrationBase, commitB)
	}

	// Assert child 2 worktree contains all files from commit C tree: text, binary, executable, and out-of-scope.
	featureContent, err := os.ReadFile(filepath.Join(child2Session.WorktreeRootPath, "feature.go"))
	if err != nil || !strings.Contains(string(featureContent), "var X = 1") {
		t.Fatalf("feature.go in child 2 missing or invalid: %v %s", err, featureContent)
	}
	readBin, err := os.ReadFile(filepath.Join(child2Session.WorktreeRootPath, "binary.bin"))
	if err != nil || !reflect.DeepEqual(readBin, binaryContent) {
		t.Fatalf("binary.bin in child 2 missing or invalid: %v %v", err, readBin)
	}
	info, err := os.Stat(filepath.Join(child2Session.WorktreeRootPath, "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh in child 2: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("run.sh in child 2 is not executable: mode=%v", info.Mode())
	}
	outsideContent, err := os.ReadFile(filepath.Join(child2Session.WorktreeRootPath, "outside.txt"))
	if err != nil || !strings.Contains(string(outsideContent), "readable outside scope") {
		t.Fatalf("outside.txt in child 2 missing: %v", err)
	}

	// Assert parent worktree HEAD, index, and status unchanged.
	parentHeadAfter := runCommittedSourceGit(t, parentAlloc.WorkspacePath, "rev-parse", "HEAD")
	if parentHeadAfter != parentHeadBefore {
		t.Fatalf("parent HEAD changed: before=%s after=%s", parentHeadBefore, parentHeadAfter)
	}
	parentStatus := runCommittedSourceGit(t, parentAlloc.WorkspacePath, "status", "--porcelain")
	if parentStatus != "" {
		t.Fatalf("parent worktree became dirty: %s", parentStatus)
	}

	// Assert child 1 was not mutated or dirtied.
	child1State, err := h.worktrees.InspectTaskWorkspace(child1Alloc.WorkspacePath)
	if err != nil || !child1State.Clean || child1State.HeadCommit != commitC {
		t.Fatalf("child 1 mutated: %v clean=%v head=%s", err, child1State.Clean, child1State.HeadCommit)
	}
}

// Purpose: Execute a corrective Coder allocation in a linked cross-workspace repository,
// ensuring the DestinationKindCapturedPromotionOnly destination path and canonical source
// workspace are preserved while the new worktree is checked out at commit C.
// Threat: cross-workspace corrective launches retarget to parent worktree or advance captured checkout.
// Symbols: run.Service.executeTaskToolWithParsed, tool.DestinationKindCapturedPromotionOnly.
// Narrow layer: Run Service task delegation with real synthetic cross-repo fixture.
func TestTaskCommittedSourceCrossRepositoryCorrectiveAllocation(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	parentRepo := initCommittedSourceTestRepo(t)
	linkedRepo := initCommittedSourceTestRepo(t)

	// Register both repos in workspace catalog for the principal.
	if _, err := h.workspace.AddForPrincipal(h.principal, parentRepo, "parent-repo", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.workspace.AddForPrincipal(h.principal, linkedRepo, "linked-repo", "", false); err != nil {
		t.Fatal(err)
	}

	commitB := runCommittedSourceGit(t, linkedRepo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(parentRepo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(parentRepo, parentBase, "parent-cross-session", nil)
	if err != nil {
		t.Fatal(err)
	}

	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-cross-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.RepoRoot,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  parentRepo,
			"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	linkedBase, err := h.worktrees.ResolveTaskBase(linkedRepo)
	if err != nil {
		t.Fatal(err)
	}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(linkedRepo, linkedBase, "child-cross-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "cross.go"), []byte("package cross\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "cross.go")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "cross feature")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-cross-1", "child-cross-1", "logical-cross-1", child1Alloc, commitB, commitC, linkedRepo, "")

	parentHeadBefore := runCommittedSourceGit(t, parentAlloc.WorkspacePath, "rev-parse", "HEAD")
	linkedHeadBefore := runCommittedSourceGit(t, linkedRepo, "rev-parse", "HEAD")

	reqCall := tool.Call{
		CallID: "call-cross-correction",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix cross feature in linked repo",
			"subagent_type": "coder",
			"meta_prompt":   "Fix cross bug",
			"owned_scope":   []string{"cross.go"},
			"committed_source": map[string]any{
				"task_call_id":     "call-cross-1",
				"child_session_id": "child-cross-1",
				"head_commit":      commitC,
			},
		}),
	}

	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatalf("buildTaskLaunchPermissionPayload failed: %v", err)
	}
	row := manifest.Launches[0]
	if row.CommittedSourceBinding == nil {
		t.Fatal("manifest row missing committed_source_binding")
	}
	if row.CommittedSourceBinding.DestinationKind != tool.DestinationKindCapturedPromotionOnly {
		t.Fatalf("destination kind = %q, want %q", row.CommittedSourceBinding.DestinationKind, tool.DestinationKindCapturedPromotionOnly)
	}
	if row.CommittedSourceBinding.DestinationPath != linkedRepo {
		t.Fatalf("destination path = %q, want %q", row.CommittedSourceBinding.DestinationPath, linkedRepo)
	}

	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-cross-correction",
		Principal:            h.principal,
		ApplySessionMutation: h.sessions.ApplySessionMutation,
	}

	out, err := h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err != nil {
		t.Fatalf("executeTaskToolWithParsed failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	launches := payload["launches"].([]any)
	child2ID := asString(launches[0].(map[string]any)["child_session_id"])
	child2Session, ok, err := h.sessions.GetSession(child2ID)
	if err != nil || !ok {
		t.Fatalf("load child 2: %v", err)
	}

	if baseCommit := asString(child2Session.Metadata["base_commit"]); baseCommit != commitC {
		t.Fatalf("child 2 base_commit = %q, want %q", baseCommit, commitC)
	}
	if integBase := asString(child2Session.Metadata["integration_base_commit"]); integBase != commitB {
		t.Fatalf("child 2 integration_base_commit = %q, want %q", integBase, commitB)
	}

	// Verify child 2 worktree is based on linkedRepo and has cross.go.
	if _, err := os.Stat(filepath.Join(child2Session.WorktreeRootPath, "cross.go")); err != nil {
		t.Fatalf("cross.go missing in child 2: %v", err)
	}

	// Verify captured checkouts are not advanced.
	if hAfter := runCommittedSourceGit(t, parentAlloc.WorkspacePath, "rev-parse", "HEAD"); hAfter != parentHeadBefore {
		t.Fatalf("parent worktree HEAD advanced: %s -> %s", parentHeadBefore, hAfter)
	}
	if lAfter := runCommittedSourceGit(t, linkedRepo, "rev-parse", "HEAD"); lAfter != linkedHeadBefore {
		t.Fatalf("linked repo HEAD advanced: %s -> %s", linkedHeadBefore, lAfter)
	}
}

// Purpose: Multiple consecutive corrections must preserve the original delivery base B
// across iterations: Child 2 correcting Child 1 inherits B; Child 3 correcting Child 2 inherits B.
// Threat: repeated correction loses the original integration base and causes merge anomalies.
// Symbols: run.Service.executeTaskToolWithParsed, tool.CommittedSourceBinding.IntegrationBaseCommit.
func TestTaskCommittedSourceRepeatedCorrectionInheritsDeliveryBase(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	if _, err := h.workspace.AddForPrincipal(h.principal, repo, "repo", "", false); err != nil {
		t.Fatal(err)
	}

	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-iter-session", nil)
	if err != nil {
		t.Fatal(err)
	}

	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-iter-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.RepoRoot,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Child 1 at commit C1.
	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1-rep", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "step1.txt"), []byte("step 1\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "step1.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "step 1")
	commitC1 := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")
	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-step-1", "child-1-rep", "logical-rep-1", child1Alloc, commitB, commitC1, parentAlloc.WorkspacePath, "")

	// Child 2 corrects Child 1 at commit C2.
	child2Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitC1}
	child2Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child2Base, "child-2-rep", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child2Alloc.WorkspacePath, "step2.txt"), []byte("step 2\n"), 0o644)
	runCommittedSourceGit(t, child2Alloc.WorkspacePath, "add", "step2.txt")
	runCommittedSourceGit(t, child2Alloc.WorkspacePath, "commit", "-m", "step 2")
	commitC2 := runCommittedSourceGit(t, child2Alloc.WorkspacePath, "rev-parse", "HEAD")
	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-step-2", "child-2-rep", "logical-rep-2", child2Alloc, commitC1, commitC2, parentAlloc.WorkspacePath, commitB)

	// Now parent launches Child 3 correcting Child 2 at C2.
	reqCall := tool.Call{
		CallID: "call-step-3",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Correct feature step 2",
			"subagent_type": "coder",
			"meta_prompt":   "Fix step 2 bug",
			"owned_scope":   []string{"step2.txt"},
			"committed_source": map[string]any{
				"task_call_id":     "call-step-2",
				"child_session_id": "child-2-rep",
				"head_commit":      commitC2,
			},
		}),
	}

	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatalf("buildTaskLaunchPermissionPayload failed: %v", err)
	}
	b := manifest.Launches[0].CommittedSourceBinding
	if b == nil {
		t.Fatal("missing committed_source_binding")
	}
	if b.HeadCommit != commitC2 {
		t.Fatalf("allocation base = %q, want %q", b.HeadCommit, commitC2)
	}
	if b.IntegrationBaseCommit != commitB {
		t.Fatalf("inherited delivery base = %q, want %q", b.IntegrationBaseCommit, commitB)
	}

	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-step-3",
		Principal:            h.principal,
		ApplySessionMutation: h.sessions.ApplySessionMutation,
	}

	out, err := h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err != nil {
		t.Fatalf("executeTaskToolWithParsed failed: %v", err)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(out), &payload)
	child3ID := asString(payload["launches"].([]any)[0].(map[string]any)["child_session_id"])
	child3Session, ok, err := h.sessions.GetSession(child3ID)
	if err != nil || !ok {
		t.Fatal("load child 3 failed")
	}

	if baseCommit := asString(child3Session.Metadata["base_commit"]); baseCommit != commitC2 {
		t.Fatalf("child 3 base_commit = %q, want %q", baseCommit, commitC2)
	}
	if integBase := asString(child3Session.Metadata["integration_base_commit"]); integBase != commitB {
		t.Fatalf("child 3 integration_base_commit = %q, want %q", integBase, commitB)
	}

	// Both step1.txt and step2.txt are present in child 3's checkout.
	if _, err := os.Stat(filepath.Join(child3Session.WorktreeRootPath, "step1.txt")); err != nil {
		t.Fatalf("step1.txt missing in child 3: %v", err)
	}
	if _, err := os.Stat(filepath.Join(child3Session.WorktreeRootPath, "step2.txt")); err != nil {
		t.Fatalf("step2.txt missing in child 3: %v", err)
	}
}

// Purpose: If ApplySessionMutation fails during parent registration of "spawned" state,
// the newly created child worktree must be preserved (never unsafely deleted when durable),
// the error propagated visibly, and the original source child must remain completely untouched.
// Threat: partial failure leaves zombie worktrees or hidden errors without lineage record.
// Symbols: run.Service.executeTaskToolWithParsed and its recheckCommittedSources/lineageUpdate closures.
// Narrow layer: Injected failure test at publication boundary.
func TestTaskCommittedSourceFailVisiblePublicationAndRollback(t *testing.T) {
	for _, failureMode := range []string{"error", "structured", "stale-after-child-create"} {
		t.Run(failureMode, func(t *testing.T) {
			h := newCommittedSourceTestHarness(t)
			repo := initCommittedSourceTestRepo(t)
			if _, err := h.workspace.AddForPrincipal(h.principal, repo, "repo", "", false); err != nil {
				t.Fatal(err)
			}
			commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

			parentBase, err := h.worktrees.ResolveTaskBase(repo)
			if err != nil {
				t.Fatal(err)
			}
			parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-fail-session", nil)
			if err != nil {
				t.Fatal(err)
			}
			parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
				SessionID:      "parent-fail-session",
				UserID:         h.principal.UserID,
				AccountScopeID: h.principal.AccountScopeID,
				WorkspacePath:  parentAlloc.RepoRoot,
				WorkspaceName:  "parent-workspace",
				Mode:           sessionruntime.ModeAuto,
				Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
				Worktree: &sessionruntime.CreateSessionWorktree{
					RootPath:   parentAlloc.WorkspacePath,
					BranchName: parentAlloc.BranchName,
					BaseBranch: parentAlloc.BaseBranch,
				},
				Metadata: map[string]any{
					"swarm_v3_source_workspace_path":  repo,
					"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
			child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1-fail", nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "f.go"), []byte("package f\n"), 0o644)
			runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "f.go")
			runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "add f")
			commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

			parentSession = h.setupPriorCompletedChild(t, parentSession, "call-f-1", "child-1-fail", "logical-f-1", child1Alloc, commitB, commitC, parentAlloc.WorkspacePath, "")

			reqCall := tool.Call{
				CallID: "call-inject-fail",
				Name:   "task",
				Arguments: mustJSON(t, map[string]any{
					"prompt":        "Fix bug",
					"subagent_type": "coder",
					"meta_prompt":   "Do work",
					"owned_scope":   []string{"f.go"},
					"committed_source": map[string]any{
						"task_call_id":     "call-f-1",
						"child_session_id": "child-1-fail",
						"head_commit":      commitC,
					},
				}),
			}
			manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseTaskCallArguments(reqCall.Arguments)
			if err != nil {
				t.Fatal(err)
			}

			// Injected failure on parent SessionMutationUpdateMetadata during lineage update.
			injectedErr := errors.New("injected parent registration failure")
			faultyApply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
				if input.Kind == sessionruntime.SessionMutationUpdateMetadata && input.SessionID == parentSession.ID && failureMode != "stale-after-child-create" {
					if failureMode == "structured" {
						return sessionruntime.SessionMutationResult{Error: &pebblestore.V3SessionMutationError{Code: "injected", Message: injectedErr.Error()}}, nil
					}
					return sessionruntime.SessionMutationResult{}, injectedErr
				}
				result, err := h.sessions.ApplySessionMutation(input)
				if err == nil && input.Kind == sessionruntime.SessionMutationCreateSession && failureMode == "stale-after-child-create" {
					if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "stale-untracked"), []byte("concurrent edit"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return result, err
			}

			execReq := taskExecutionRequest{
				Parsed:               parsed,
				ParsedProvided:       true,
				ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
				RunID:                "run-fail",
				Principal:            h.principal,
				ApplySessionMutation: faultyApply,
			}

			_, err = h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
			want := "injected parent registration failure"
			if failureMode == "stale-after-child-create" {
				want = "recheck committed source before publication"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected visible %s rejection: %v", failureMode, err)
			}
			if h.runner.calls != 0 {
				t.Fatal("producer started before required registration")
			}
			children, listErr := h.sessions.ListSessionsForAccountUser(parentSession.AccountScopeID, parentSession.UserID, 100)
			if listErr != nil {
				t.Fatal(listErr)
			}
			preserved := 0
			for _, child := range children {
				if mapString(child.Metadata, "parent_task_call_id") != reqCall.CallID {
					continue
				}
				preserved++
				if _, active, err := h.sessions.GetSessionActiveRunIntent(child.ID); err != nil || active {
					t.Fatal("failed registration left runnable intent")
				}
				if _, err := os.Stat(child.WorktreeRootPath); err != nil {
					t.Fatal("registered unused child worktree was deleted")
				}
			}
			if preserved != 1 {
				t.Fatalf("expected one recallable inactive child, found %d", preserved)
			}

			// Verify child 1 is untouched except for the intentional concurrent edit.
			child1State, err := h.worktrees.InspectTaskWorkspace(child1Alloc.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			if (failureMode != "stale-after-child-create" && !child1State.Clean) || child1State.HeadCommit != commitC {
				t.Fatalf("child 1 was mutated: clean=%v head=%s", child1State.Clean, child1State.HeadCommit)
			}
		})
	}
}

// Purpose: A selected child HEAD changed after approval must reject execution
// before allocation, child creation, or provider dispatch, preserving all state.
// Threat: stale approval silently launches from a different committed source.
// Symbols: run.Service.executeTaskToolWithParsed and its recheckCommittedSources/lineageUpdate closures.
// Narrow layer: approved-manifest execution with real Git/Pebble and a fake provider.
func TestTaskCommittedSourceStaleBetweenBoundariesRecheck(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	if _, err := h.workspace.AddForPrincipal(h.principal, repo, "repo", "", false); err != nil {
		t.Fatal(err)
	}
	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-stale-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-stale-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.RepoRoot,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1-stale", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "file.txt"), []byte("base text\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "file.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "commit C")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-stale-1", "child-1-stale", "logical-stale-1", child1Alloc, commitB, commitC, parentAlloc.WorkspacePath, "")

	reqCall := tool.Call{
		CallID: "call-stale-recheck",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug in stale child",
			"subagent_type": "coder",
			"meta_prompt":   "Fix bug",
			"owned_scope":   []string{"file.txt"},
			"committed_source": map[string]any{
				"task_call_id":     "call-stale-1",
				"child_session_id": "child-1-stale",
				"head_commit":      commitC,
			},
		}),
	}
	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate child 1 commit between approval and execution so HEAD is different.
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "file.txt"), []byte("diverged text\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "file.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "commit D concurrent")
	commitD := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")
	if commitD == commitC {
		t.Fatal("commit did not advance")
	}

	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-stale",
		Principal:            h.principal,
		ApplySessionMutation: h.sessions.ApplySessionMutation,
	}

	before, listErr := h.sessions.ListSessionsForAccountUser(parentSession.AccountScopeID, parentSession.UserID, 100)
	if listErr != nil {
		t.Fatal(listErr)
	}
	inventory := runCommittedSourceGit(t, repo, "worktree", "list", "--porcelain")
	_, err = h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err == nil || !strings.Contains(err.Error(), "child live HEAD disagrees with requested head commit") {
		t.Fatalf("expected exact stale HEAD rejection before allocation, got %v", err)
	}
	after, listErr := h.sessions.ListSessionsForAccountUser(parentSession.AccountScopeID, parentSession.UserID, 100)
	if listErr != nil || !reflect.DeepEqual(before, after) || h.runner.calls != 0 || inventory != runCommittedSourceGit(t, repo, "worktree", "list", "--porcelain") {
		t.Fatal("stale source rejection changed sessions, allocated, or dispatched")
	}
}

// Purpose: lineageUpdate bounded CAS retry must handle concurrent parent metadata changes
// without overwriting unrelated keys, and duplicate call IDs must be rejected safely.
// Threat: concurrent metadata update clobbered or duplicate launch creates duplicate runnable workers.
// Symbols: run.Service.executeTaskToolWithParsed, run.Service.lineageUpdate.
func TestTaskCommittedSourceConflictRetryAndDuplicateCall(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	if _, err := h.workspace.AddForPrincipal(h.principal, repo, "repo", "", false); err != nil {
		t.Fatal(err)
	}
	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-cas-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-cas-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.RepoRoot,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": parentAlloc.WorkspacePath,
			"unrelated_concurrent_key":        "initial_value",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1-cas", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "cas.txt"), []byte("cas\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "cas.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "cas commit")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	parentSession = h.setupPriorCompletedChild(t, parentSession, "call-cas-1", "child-1-cas", "logical-cas-1", child1Alloc, commitB, commitC, parentAlloc.WorkspacePath, "")

	reqCall := tool.Call{
		CallID: "call-cas-first",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug",
			"subagent_type": "coder",
			"meta_prompt":   "Do work",
			"owned_scope":   []string{"cas.txt"},
			"committed_source": map[string]any{
				"task_call_id":     "call-cas-1",
				"child_session_id": "child-1-cas",
				"head_commit":      commitC,
			},
		}),
	}
	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}

	// Hook applyMutation to simulate a concurrent turn updating unrelated_concurrent_key on the first attempt
	attempts := 0
	hookedApply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.Kind == sessionruntime.SessionMutationUpdateMetadata && input.SessionID == parentSession.ID && attempts == 0 {
			attempts++
			// Simulate concurrent turn modifying unrelated metadata
			snap, _, _ := h.sessions.GetSession(parentSession.ID)
			snap.Metadata["unrelated_concurrent_key"] = "concurrently_updated_value"
			_, _ = h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
				SessionID:      parentSession.ID,
				UserID:         h.principal.UserID,
				AccountScopeID: h.principal.AccountScopeID,
				Kind:           sessionruntime.SessionMutationUpdateMetadata,
				Session:        &snap,
				IdempotencyKey: "concurrent-turn",
				RequestHash:    "concurrent-turn",
			})
			// The original call with expectedSeq will now conflict and retry
		}
		return h.sessions.ApplySessionMutation(input)
	}

	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-cas",
		Principal:            h.principal,
		ApplySessionMutation: hookedApply,
	}

	_, err = h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err != nil {
		t.Fatalf("executeTaskToolWithParsed failed despite retry: %v", err)
	}

	// Verify that unrelated_concurrent_key was preserved and not overwritten!
	updatedParent, ok, err := h.sessions.GetSession(parentSession.ID)
	if err != nil || !ok {
		t.Fatal("load updated parent failed")
	}
	if val := asString(updatedParent.Metadata["unrelated_concurrent_key"]); val != "concurrently_updated_value" {
		t.Fatalf("unrelated concurrent metadata was overwritten: %q", val)
	}

	// Now try to execute a duplicate call with the EXACT SAME taskCallID ("call-cas-first")
	_, dupErr := h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if dupErr == nil || !strings.Contains(dupErr.Error(), "already exists in session metadata") {
		t.Fatalf("expected duplicate call rejection, got %v", dupErr)
	}
}
