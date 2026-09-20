package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

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
	if _, err := parseTaskCallArguments(mustJSON(t, swarmArgs)); err == nil || !strings.Contains(err.Error(), "unsupported field \"committed_source\"") {
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

	// 6. Cannot combine committed_source with workspace_path (ambiguous retargeting).
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

	// 7. Malformed commit OIDs: short, non-hex, empty.
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

	// 8. Missing task_call_id or child_session_id.
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

	// 9. Top-level committed_source when launches array is present.
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

	// 10. Valid single shorthand and launches array must parse cleanly.
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
// Threat: tampered approval manifests retarget child execution to arbitrary commits or destinations.
// Symbols: run.parseApprovedTaskLaunchManifest, run.taskLaunchSpec, run.taskLaunchManifestRow.
// Narrow layer: approval manifest unmarshaling and validation unit boundary.
func TestTaskCommittedSourceApprovalBindingAndTampering(t *testing.T) {
	validOID := strings.Repeat("a", 40)
	req := tool.CommittedSourceRequest{
		TaskCallID:     "call-1",
		ChildSessionID: "sess-1",
		HeadCommit:     validOID,
	}
	binding := tool.CommittedSourceBinding{
		TaskCallID:            "call-1",
		ChildSessionID:        "sess-1",
		HeadCommit:            validOID,
		CanonicalSourcePath:   "/workspace",
		DestinationKind:       tool.DestinationKindOwnedLane,
		DestinationPath:       "/worktrees/parent",
		DestinationBranch:     "dev",
		SourceWorktreePath:    "/worktrees/child-1",
		SourceBranch:          "agent/child-1",
		SourceBaseCommit:      strings.Repeat("b", 40),
		SourceHeadCommit:      validOID,
		IntegrationBaseCommit: strings.Repeat("b", 40),
		RepositoryIdentity:    "repo-1",
		ChildGeneration:       1,
	}

	spec := taskLaunchSpec{
		RequestedSubagentType:  "coder",
		CommittedSource:        &req,
		CommittedSourceBinding: &binding,
		OwnedScope:             []string{"source.go"},
	}

	manifest := taskLaunchManifest{
		Launches: []taskLaunchManifestRow{
			{
				RequestedSubagentType:  "coder",
				CommittedSource:        &req,
				CommittedSourceBinding: &binding,
				OwnedScope:             []string{"source.go"},
				ProfileSnapshot:        &pebblestore.AgentProfile{},
			},
		},
	}

	hash, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	body := mustJSON(t, map[string]any{"manifest_hash": hash, "manifest": manifest})

	// Valid matching manifest succeeds.
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{spec}); err != nil {
		t.Fatalf("valid approved manifest failed: %v", err)
	}

	// Tampered tuple: changed head_commit.
	tamperedReq := req
	tamperedReq.HeadCommit = strings.Repeat("f", 40)
	tamperedSpec := spec
	tamperedSpec.CommittedSource = &tamperedReq
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{tamperedSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source differs from requested tuple") {
		t.Fatalf("expected tuple tampering rejection, got %v", err)
	}

	// Tampered binding: changed DestinationPath.
	tamperedBinding := binding
	tamperedBinding.DestinationPath = "/different/destination"
	tamperedBindingSpec := spec
	tamperedBindingSpec.CommittedSourceBinding = &tamperedBinding
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{tamperedBindingSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source binding differs from resolved binding") {
		t.Fatalf("expected binding tampering rejection, got %v", err)
	}

	// Removed committed_source from spec.
	removedSpec := spec
	removedSpec.CommittedSource = nil
	removedSpec.CommittedSourceBinding = nil
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{removedSpec}); err == nil || !strings.Contains(err.Error(), "approved committed source differs from requested tuple") {
		t.Fatalf("expected removed tuple rejection, got %v", err)
	}
}

type committedSourceTestHarness struct {
	storeDir     string
	store        *pebblestore.Store
	events       *pebblestore.EventLog
	sessionStore *pebblestore.SessionStore
	sessions     *sessionruntime.Service
	worktrees    *worktreeruntime.Service
	tools        *tool.Runtime
	permissions  *permission.Service
	agents       *agentruntime.Service
	service      *Service
	principal    identity.Principal
}

func newCommittedSourceTestHarness(t *testing.T) *committedSourceTestHarness {
	t.Helper()
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
	worktrees := &worktreeruntime.Service{}
	toolRuntime := tool.NewRuntime(1)
	toolRuntime.SetSessions(sessions)
	toolRuntime.SetWorktrees(worktrees)

	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := agents.EnsureDefaultsForAccount("test-account"); err != nil {
		t.Fatal(err)
	}

	svc := NewService(sessions, nil, nil, toolRuntime, permissions, agents, nil, events)
	svc.worktrees = worktrees

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
		worktrees:    worktrees,
		tools:        toolRuntime,
		permissions:  permissions,
		agents:       agents,
		service:      svc,
		principal:    principal,
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

	// Base commit B in repository.
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
		WorkspacePath:  parentAlloc.WorkspacePath,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
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

	// Child 1 commits a change reaching commit C.
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "feature.go"), []byte("package feature\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "feature.go")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "implement feature")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	// Create Child 1 session snapshot in store.
	child1Metadata := map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-child-1",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitB,
		"worktree_path":                  child1Alloc.WorkspacePath,
		"child_branch":                   child1Alloc.BranchName,
		"worktree_base_branch":           child1Alloc.BaseBranch,
		"swarm_v3_source_workspace_path": repo,
		"target_workspace_path":          parentAlloc.WorkspacePath,
	}
	child1Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "child-1",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child1Alloc.WorkspacePath,
		WorkspaceName:  "child-1",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child1Alloc.WorkspacePath,
			BranchName: child1Alloc.BranchName,
			BaseBranch: child1Alloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	child1Session.Metadata = child1Metadata
	if _, _, err := h.sessions.UpdateMetadata(child1Session.ID, child1Metadata); err != nil {
		t.Fatal(err)
	}

	// Mark child 1 completed in lifecycle and rotation store.
	now := time.Now().UnixMilli()
	if _, err := h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       child1Session.ID,
		UserID:          h.principal.UserID,
		AccountScopeID:  h.principal.AccountScopeID,
		ClientRequestID: "child-1-complete",
		IdempotencyKey:  "child-1-complete",
		PayloadHash:     "child-1-complete",
		RequestHash:     "child-1-complete",
		Kind:            sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child1Session.ID,
			Phase:     "completed",
			Active:    false,
			StartedAt: now - 1000,
			EndedAt:   now,
			UpdatedAt: now,
		},
		NowUnixMs: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Register delegated child generation for child 1.
	genRecord := pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID:      h.principal.AccountScopeID,
		LogicalTaskID:       "call-child-1:1",
		SessionID:           child1Session.ID,
		ParentSessionID:     parentSession.ID,
		WorkspacePath:       child1Alloc.WorkspacePath,
		WorktreeBranch:      child1Alloc.BranchName,
		ParentBranch:        parentAlloc.BranchName,
		ImmutableBaseCommit: commitB,
	}
	if _, _, err := h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID,
		LogicalTaskID:  "call-child-1:1",
	}, genRecord, "lineage-child-1"); err != nil {
		t.Fatal(err)
	}

	// Update parent metadata with task_launches record for child 1.
	parentMetadata := map[string]any{
		"swarm_v3_source_workspace_path": repo,
		"task_launches": map[string]any{
			"call-child-1": map[string]any{
				"call_id":           "call-child-1",
				"parent_session_id": parentSession.ID,
				"subagent":          "coder",
				"child_session_id":  child1Session.ID,
				"launches": []any{
					map[string]any{
						"subagent":              "coder",
						"child_session_id":      child1Session.ID,
						"parent_workspace_path": parentAlloc.WorkspacePath,
						"workspace_path":        child1Alloc.WorkspacePath,
						"worktree_root_path":    child1Alloc.WorkspacePath,
						"worktree_branch":       child1Alloc.BranchName,
						"worktree_base_branch":  parentAlloc.BranchName,
						"parent_branch":         parentAlloc.BranchName,
						"base_commit":           commitB,
						"head_commit":           commitC,
					},
				},
			},
		},
	}
	if _, _, err := h.sessions.UpdateMetadata(parentSession.ID, parentMetadata); err != nil {
		t.Fatal(err)
	}

	// Now build and execute a follow-up corrective task launch with committed_source pointing to Child 1.
	reqCall := tool.Call{
		CallID: "call-correction",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug in feature.go",
			"subagent_type": "coder",
			"meta_prompt":   "Fix the calculation in feature.go",
			"owned_scope":   []string{"feature.go"},
			"committed_source": map[string]any{
				"task_call_id":     "call-child-1",
				"child_session_id": child1Session.ID,
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

	// Prepare execution request.
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

	// Execute task launch tool.
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

	// Inspect Child 2 session in Pebble store.
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

	// Assert child 2 worktree contains feature.go from commit C!
	featureContent, err := os.ReadFile(filepath.Join(child2Session.WorktreeRootPath, "feature.go"))
	if err != nil {
		t.Fatalf("read feature.go from child 2 worktree: %v", err)
	}
	if !strings.Contains(string(featureContent), "var X = 1") {
		t.Fatalf("feature.go in child 2 does not have commit C content: %s", featureContent)
	}

	// Assert child 1 was not mutated or dirtied.
	child1State, err := h.worktrees.InspectTaskWorkspace(child1Alloc.WorkspacePath)
	if err != nil {
		t.Fatalf("inspect child 1: %v", err)
	}
	if !child1State.Clean || child1State.HeadCommit != commitC {
		t.Fatalf("child 1 was mutated: clean=%v head=%s want=%s", child1State.Clean, child1State.HeadCommit, commitC)
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

	// Base commit B in linked repository.
	commitB := runCommittedSourceGit(t, linkedRepo, "rev-parse", "HEAD")

	// Parent session in parentRepo.
	parentBase, err := h.worktrees.ResolveTaskBase(parentRepo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(parentRepo, parentBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.WorkspacePath,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mock workspace authority for linkedRepo.
	scope := tool.WorkspaceScope{SessionID: parentSession.ID, PrimaryPath: parentAlloc.WorkspacePath, Principal: h.principal}
	_ = scope

	// Allocate child 1 in linkedRepo.
	child1Base := worktreeruntime.TaskBase{
		RepoRoot:     linkedRepo,
		ParentBranch: "dev",
		BaseCommit:   commitB,
	}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(linkedRepo, child1Base, "cross-child-1", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Child 1 commits a change reaching commit C.
	if err := os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "cross.go"), []byte("package cross\nvar Y = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "cross.go")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "cross feature")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	// Child 1 session snapshot with cross-workspace metadata.
	child1Metadata := map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-cross-1",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitB,
		"worktree_path":                  child1Alloc.WorkspacePath,
		"child_branch":                   child1Alloc.BranchName,
		"worktree_base_branch":           "dev",
		"swarm_v3_source_workspace_path": linkedRepo,
		"target_workspace_path":          linkedRepo,
	}
	child1Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "cross-child-1",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child1Alloc.WorkspacePath,
		WorkspaceName:  "cross-child-1",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child1Alloc.WorkspacePath,
			BranchName: child1Alloc.BranchName,
			BaseBranch: "dev",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	child1Session.Metadata = child1Metadata
	if _, _, err := h.sessions.UpdateMetadata(child1Session.ID, child1Metadata); err != nil {
		t.Fatal(err)
	}

	// Mark completed in lifecycle.
	now := time.Now().UnixMilli()
	if _, err := h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       child1Session.ID,
		UserID:          h.principal.UserID,
		AccountScopeID:  h.principal.AccountScopeID,
		ClientRequestID: "cross-child-1-complete",
		IdempotencyKey:  "cross-child-1-complete",
		PayloadHash:     "cross-child-1-complete",
		RequestHash:     "cross-child-1-complete",
		Kind:            sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child1Session.ID,
			Phase:     "completed",
			Active:    false,
			StartedAt: now - 1000,
			EndedAt:   now,
			UpdatedAt: now,
		},
		NowUnixMs: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Register delegated child generation for cross-child-1.
	genRecord := pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID:      h.principal.AccountScopeID,
		LogicalTaskID:       "call-cross-1:1",
		SessionID:           child1Session.ID,
		ParentSessionID:     parentSession.ID,
		WorkspacePath:       child1Alloc.WorkspacePath,
		WorktreeBranch:      child1Alloc.BranchName,
		ParentBranch:        "dev",
		ImmutableBaseCommit: commitB,
	}
	if _, _, err := h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID,
		LogicalTaskID:  "call-cross-1:1",
	}, genRecord, "lineage-cross-1"); err != nil {
		t.Fatal(err)
	}

	// Parent task_launches record for cross-child-1.
	parentMetadata := map[string]any{
		"swarm_v3_source_workspace_path": parentRepo,
		"task_launches": map[string]any{
			"call-cross-1": map[string]any{
				"call_id":           "call-cross-1",
				"parent_session_id": parentSession.ID,
				"subagent":          "coder",
				"child_session_id":  child1Session.ID,
				"launches": []any{
					map[string]any{
						"subagent":              "coder",
						"child_session_id":      child1Session.ID,
						"parent_workspace_path": linkedRepo,
						"workspace_path":        child1Alloc.WorkspacePath,
						"worktree_root_path":    child1Alloc.WorkspacePath,
						"worktree_branch":       child1Alloc.BranchName,
						"worktree_base_branch":  "dev",
						"parent_branch":         "dev",
						"base_commit":           commitB,
						"head_commit":           commitC,
					},
				},
			},
		},
	}
	if _, _, err := h.sessions.UpdateMetadata(parentSession.ID, parentMetadata); err != nil {
		t.Fatal(err)
	}

	// Execute corrective launch for cross-workspace child.
	reqCall := tool.Call{
		CallID: "call-cross-correction",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug in cross.go",
			"subagent_type": "coder",
			"meta_prompt":   "Fix the variable in cross.go",
			"owned_scope":   []string{"cross.go"},
			"committed_source": map[string]any{
				"task_call_id":     "call-cross-1",
				"child_session_id": child1Session.ID,
				"head_commit":      commitC,
			},
		}),
	}

	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatalf("buildTaskLaunchPermissionPayload failed: %v", err)
	}
	row := manifest.Launches[0]
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
		t.Fatalf("unmarshal output: %v", err)
	}
	launches := payload["launches"].([]any)
	child2ID := asString(launches[0].(map[string]any)["child_session_id"])
	child2Session, ok, err := h.sessions.GetSession(child2ID)
	if err != nil || !ok {
		t.Fatalf("get child 2 session: %v", err)
	}

	if child2Session.Metadata["destination_kind"] != tool.DestinationKindCapturedPromotionOnly {
		t.Fatalf("child 2 destination_kind = %v, want %q", child2Session.Metadata["destination_kind"], tool.DestinationKindCapturedPromotionOnly)
	}
	if child2Session.Metadata["base_commit"] != commitC {
		t.Fatalf("child 2 base_commit = %v, want %q", child2Session.Metadata["base_commit"], commitC)
	}
	if child2Session.Metadata["integration_base_commit"] != commitB {
		t.Fatalf("child 2 integration_base_commit = %v, want %q", child2Session.Metadata["integration_base_commit"], commitB)
	}

	// Verify child 2 worktree is inside linkedRepo's managed worktrees and has cross.go.
	content, err := os.ReadFile(filepath.Join(child2Session.WorktreeRootPath, "cross.go"))
	if err != nil {
		t.Fatalf("read cross.go from child 2: %v", err)
	}
	if !strings.Contains(string(content), "var Y = 2") {
		t.Fatalf("unexpected content in child 2: %s", content)
	}
}

// Purpose: Repeated correction (Child 3 correcting Child 2 which corrected Child 1) must
// start at the newest selected child commit C2 while preserving the original inherited delivery base B.
// Threat: chained corrections lose delivery base B and attempt 3-way merge from intermediate commit.
// Symbols: run.Service.executeTaskToolWithParsed, tool.CommittedSourceBinding.IntegrationBaseCommit.
// Narrow layer: Run Service multi-generation corrective workflow with synthetic Git fixture.
func TestTaskCommittedSourceRepeatedCorrectionInheritsDeliveryBase(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.WorkspacePath,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Child 1: base B -> commit C1.
	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "step1.txt"), []byte("step1\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "step1.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "step 1")
	commitC1 := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	child1Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "child-1",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child1Alloc.WorkspacePath,
		WorkspaceName:  "child-1",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child1Alloc.WorkspacePath,
			BranchName: child1Alloc.BranchName,
			BaseBranch: child1Alloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, _, err := h.sessions.UpdateMetadata(child1Session.ID, map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-1",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitB,
		"worktree_path":                  child1Alloc.WorkspacePath,
		"child_branch":                   child1Alloc.BranchName,
		"worktree_base_branch":           child1Alloc.BaseBranch,
		"swarm_v3_source_workspace_path": repo,
		"target_workspace_path":          parentAlloc.WorkspacePath,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: child1Session.ID, UserID: h.principal.UserID, AccountScopeID: h.principal.AccountScopeID,
		Kind: sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child1Session.ID, Phase: "completed", Active: false, StartedAt: now - 2000, EndedAt: now - 1500, UpdatedAt: now - 1500,
		}, NowUnixMs: now,
	})
	_, _, _ = h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1",
	}, pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1", SessionID: child1Session.ID,
		ParentSessionID: parentSession.ID, WorkspacePath: child1Alloc.WorkspacePath, WorktreeBranch: child1Alloc.BranchName,
		ParentBranch: parentAlloc.BranchName, ImmutableBaseCommit: commitB,
	}, "child-1-lineage")

	// Child 2: corrected Child 1. Base C1, integration base B. Then commits C2.
	child2Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitC1}
	child2Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child2Base, "child-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child2Alloc.WorkspacePath, "step2.txt"), []byte("step2\n"), 0o644)
	runCommittedSourceGit(t, child2Alloc.WorkspacePath, "add", "step2.txt")
	runCommittedSourceGit(t, child2Alloc.WorkspacePath, "commit", "-m", "step 2")
	commitC2 := runCommittedSourceGit(t, child2Alloc.WorkspacePath, "rev-parse", "HEAD")

	child2Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "child-2",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child2Alloc.WorkspacePath,
		WorkspaceName:  "child-2",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child2Alloc.WorkspacePath,
			BranchName: child2Alloc.BranchName,
			BaseBranch: child2Alloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	child2Binding := tool.CommittedSourceBinding{
		TaskCallID:            "call-1",
		ChildSessionID:        child1Session.ID,
		HeadCommit:            commitC1,
		CanonicalSourcePath:   repo,
		DestinationKind:       tool.DestinationKindOwnedLane,
		DestinationPath:       parentAlloc.WorkspacePath,
		DestinationBranch:     parentAlloc.BranchName,
		SourceWorktreePath:    child1Alloc.WorkspacePath,
		SourceBranch:          child1Alloc.BranchName,
		SourceBaseCommit:      commitB,
		SourceHeadCommit:      commitC1,
		IntegrationBaseCommit: commitB,
		RepositoryIdentity:    "repo",
		ChildGeneration:       1,
	}
	if _, _, err := h.sessions.UpdateMetadata(child2Session.ID, map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-2",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitC1,
		"integration_base_commit":        commitB,
		"committed_source":               map[string]any{"task_call_id": "call-1", "child_session_id": child1Session.ID, "head_commit": commitC1},
		"committed_source_binding":       child2Binding,
		"worktree_path":                  child2Alloc.WorkspacePath,
		"child_branch":                   child2Alloc.BranchName,
		"worktree_base_branch":           child2Alloc.BaseBranch,
		"swarm_v3_source_workspace_path": repo,
		"target_workspace_path":          parentAlloc.WorkspacePath,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: child2Session.ID, UserID: h.principal.UserID, AccountScopeID: h.principal.AccountScopeID,
		Kind: sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child2Session.ID, Phase: "completed", Active: false, StartedAt: now - 1000, EndedAt: now - 500, UpdatedAt: now - 500,
		}, NowUnixMs: now,
	})
	_, _, _ = h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-2:1",
	}, pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-2:1", SessionID: child2Session.ID,
		ParentSessionID: parentSession.ID, WorkspacePath: child2Alloc.WorkspacePath, WorktreeBranch: child2Alloc.BranchName,
		ParentBranch: parentAlloc.BranchName, ImmutableBaseCommit: commitC1,
	}, "child-2-lineage")

	// Update parent metadata with calls for child 1 and child 2.
	parentMetadata := map[string]any{
		"swarm_v3_source_workspace_path": repo,
		"task_launches": map[string]any{
			"call-1": map[string]any{
				"call_id":           "call-1",
				"parent_session_id": parentSession.ID,
				"subagent":          "coder",
				"child_session_id":  child1Session.ID,
				"launches": []any{
					map[string]any{
						"subagent":              "coder",
						"child_session_id":      child1Session.ID,
						"parent_workspace_path": parentAlloc.WorkspacePath,
						"workspace_path":        child1Alloc.WorkspacePath,
						"worktree_root_path":    child1Alloc.WorkspacePath,
						"worktree_branch":       child1Alloc.BranchName,
						"worktree_base_branch":  parentAlloc.BranchName,
						"parent_branch":         parentAlloc.BranchName,
						"base_commit":           commitB,
						"head_commit":           commitC1,
					},
				},
			},
			"call-2": map[string]any{
				"call_id":                 "call-2",
				"parent_session_id":       parentSession.ID,
				"subagent":                "coder",
				"child_session_id":        child2Session.ID,
				"integration_base_commit": commitB,
				"launches": []any{
					map[string]any{
						"subagent":                "coder",
						"child_session_id":        child2Session.ID,
						"parent_workspace_path":   parentAlloc.WorkspacePath,
						"workspace_path":          child2Alloc.WorkspacePath,
						"worktree_root_path":      child2Alloc.WorkspacePath,
						"worktree_branch":         child2Alloc.BranchName,
						"worktree_base_branch":    parentAlloc.BranchName,
						"parent_branch":           parentAlloc.BranchName,
						"base_commit":             commitC1,
						"head_commit":             commitC2,
						"integration_base_commit": commitB,
					},
				},
			},
		},
	}
	if _, _, err := h.sessions.UpdateMetadata(parentSession.ID, parentMetadata); err != nil {
		t.Fatal(err)
	}

	// Now launch Child 3 correcting Child 2 (committed_source pointing to Child 2 with commitC2).
	reqCall := tool.Call{
		CallID: "call-3",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Continue step 2",
			"subagent_type": "coder",
			"meta_prompt":   "Do step 3",
			"owned_scope":   []string{"step3.txt"},
			"committed_source": map[string]any{
				"task_call_id":     "call-2",
				"child_session_id": child2Session.ID,
				"head_commit":      commitC2,
			},
		}),
	}

	manifest, err := h.service.buildTaskLaunchPermissionPayload(parentSession.ID, sessionruntime.ModeAuto, reqCall)
	if err != nil {
		t.Fatalf("permission payload failed: %v", err)
	}
	b := manifest.Launches[0].CommittedSourceBinding
	if b.HeadCommit != commitC2 {
		t.Fatalf("Child 3 allocation base = %q, want %q", b.HeadCommit, commitC2)
	}
	if b.IntegrationBaseCommit != commitB {
		t.Fatalf("Child 3 inherited delivery base = %q, want original %q", b.IntegrationBaseCommit, commitB)
	}

	parsed, err := parseTaskCallArguments(reqCall.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-3",
		Principal:            h.principal,
		ApplySessionMutation: h.sessions.ApplySessionMutation,
	}

	out, err := h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err != nil {
		t.Fatalf("execute task 3 failed: %v", err)
	}

	var payload map[string]any
	_ = json.Unmarshal([]byte(out), &payload)
	child3ID := asString(payload["launches"].([]any)[0].(map[string]any)["child_session_id"])

	child3Session, ok, err := h.sessions.GetSession(child3ID)
	if err != nil || !ok {
		t.Fatalf("get child 3 session: %v", err)
	}

	// Child 3's base_commit is C2, while integration_base_commit is B.
	if baseCommit := asString(child3Session.Metadata["base_commit"]); baseCommit != commitC2 {
		t.Fatalf("child 3 base_commit = %q, want %q", baseCommit, commitC2)
	}
	if integBase := asString(child3Session.Metadata["integration_base_commit"]); integBase != commitB {
		t.Fatalf("child 3 integration_base_commit = %q, want %q", integBase, commitB)
	}

	// Both step1.txt and step2.txt are present in child 3's checkout!
	if _, err := os.Stat(filepath.Join(child3Session.WorktreeRootPath, "step1.txt")); err != nil {
		t.Fatalf("step1.txt missing in child 3: %v", err)
	}
	if _, err := os.Stat(filepath.Join(child3Session.WorktreeRootPath, "step2.txt")); err != nil {
		t.Fatalf("step2.txt missing in child 3: %v", err)
	}
}

// Purpose: If ApplySessionMutation fails during parent registration of "spawned" state,
// the newly allocated child worktree must be rolled back, the error propagated visibly,
// and the original source child must remain completely untouched.
// Threat: partial failure leaves zombie worktrees or hidden errors without lineage record.
// Symbols: run.Service.executeTaskToolWithParsed, run.Service.rollbackPreparedAllocations.
// Narrow layer: Injected failure test at publication boundary.
func TestTaskCommittedSourceFailVisiblePublicationAndRollback(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.WorkspacePath,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create child 1 at commit C.
	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "f.go"), []byte("package f\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "f.go")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "add f")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	child1Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "child-1",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child1Alloc.WorkspacePath,
		WorkspaceName:  "child-1",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child1Alloc.WorkspacePath,
			BranchName: child1Alloc.BranchName,
			BaseBranch: child1Alloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, _, err := h.sessions.UpdateMetadata(child1Session.ID, map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-1",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitB,
		"worktree_path":                  child1Alloc.WorkspacePath,
		"child_branch":                   child1Alloc.BranchName,
		"worktree_base_branch":           child1Alloc.BaseBranch,
		"swarm_v3_source_workspace_path": repo,
		"target_workspace_path":          parentAlloc.WorkspacePath,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: child1Session.ID, UserID: h.principal.UserID, AccountScopeID: h.principal.AccountScopeID,
		Kind: sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child1Session.ID, Phase: "completed", Active: false, StartedAt: now - 1000, EndedAt: now - 500, UpdatedAt: now - 500,
		}, NowUnixMs: now,
	})
	_, _, _ = h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1",
	}, pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1", SessionID: child1Session.ID,
		ParentSessionID: parentSession.ID, WorkspacePath: child1Alloc.WorkspacePath, WorktreeBranch: child1Alloc.BranchName,
		ParentBranch: parentAlloc.BranchName, ImmutableBaseCommit: commitB,
	}, "child-1-lineage")

	parentMetadata := map[string]any{
		"swarm_v3_source_workspace_path": repo,
		"task_launches": map[string]any{
			"call-1": map[string]any{
				"call_id":           "call-1",
				"parent_session_id": parentSession.ID,
				"subagent":          "coder",
				"child_session_id":  child1Session.ID,
				"launches": []any{
					map[string]any{
						"subagent":              "coder",
						"child_session_id":      child1Session.ID,
						"parent_workspace_path": parentAlloc.WorkspacePath,
						"workspace_path":        child1Alloc.WorkspacePath,
						"worktree_root_path":    child1Alloc.WorkspacePath,
						"worktree_branch":       child1Alloc.BranchName,
						"worktree_base_branch":  parentAlloc.BranchName,
						"parent_branch":         parentAlloc.BranchName,
						"base_commit":           commitB,
						"head_commit":           commitC,
					},
				},
			},
		},
	}
	if _, _, err := h.sessions.UpdateMetadata(parentSession.ID, parentMetadata); err != nil {
		t.Fatal(err)
	}

	reqCall := tool.Call{
		CallID: "call-inject-fail",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug",
			"subagent_type": "coder",
			"meta_prompt":   "Do work",
			"owned_scope":   []string{"f.go"},
			"committed_source": map[string]any{
				"task_call_id":     "call-1",
				"child_session_id": child1Session.ID,
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

	// Injected failure on parent SessionMutationUpdateMetadata.
	injectedErr := errors.New("injected parent registration failure")
	faultyApply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.Kind == sessionruntime.SessionMutationUpdateMetadata && input.SessionID == parentSession.ID {
			return sessionruntime.SessionMutationResult{}, injectedErr
		}
		return h.sessions.ApplySessionMutation(input)
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
	if err == nil || !strings.Contains(err.Error(), "injected parent registration failure") {
		t.Fatalf("expected injected failure to be returned visibly, got %v", err)
	}

	// Verify child 1 is untouched.
	child1State, err := h.worktrees.InspectTaskWorkspace(child1Alloc.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if !child1State.Clean || child1State.HeadCommit != commitC {
		t.Fatalf("child 1 was mutated: clean=%v head=%s", child1State.Clean, child1State.HeadCommit)
	}
}

// Purpose: If the selected child HEAD commit or worktree becomes dirty/stale between
// allocation and publication, publication recheck must catch the divergence, roll back
// newly allocated state, and fail visibly before producers run.
// Threat: stale or concurrent mutation between allocation and publication desynchronizes lineage.
// Symbols: run.Service.executeTaskToolWithParsed, run.Service.rollbackPreparedAllocations.
// Narrow layer: Pre-publication recheck boundary test.
func TestTaskCommittedSourceStaleBetweenBoundariesRecheck(t *testing.T) {
	h := newCommittedSourceTestHarness(t)
	repo := initCommittedSourceTestRepo(t)
	commitB := runCommittedSourceGit(t, repo, "rev-parse", "HEAD")

	parentBase, err := h.worktrees.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentAlloc, err := h.worktrees.AllocateTaskWorkspace(repo, parentBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	parentSession, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  parentAlloc.WorkspacePath,
		WorkspaceName:  "parent-workspace",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   parentAlloc.WorkspacePath,
			BranchName: parentAlloc.BranchName,
			BaseBranch: parentAlloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	child1Base := worktreeruntime.TaskBase{RepoRoot: repo, ParentBranch: parentAlloc.BranchName, BaseCommit: commitB}
	child1Alloc, err := h.worktrees.AllocateTaskWorkspace(parentAlloc.WorkspacePath, child1Base, "child-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "file.txt"), []byte("data\n"), 0o644)
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "add", "file.txt")
	runCommittedSourceGit(t, child1Alloc.WorkspacePath, "commit", "-m", "commit C")
	commitC := runCommittedSourceGit(t, child1Alloc.WorkspacePath, "rev-parse", "HEAD")

	child1Session, _, err := h.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "child-1",
		UserID:         h.principal.UserID,
		AccountScopeID: h.principal.AccountScopeID,
		WorkspacePath:  child1Alloc.WorkspacePath,
		WorkspaceName:  "child-1",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   child1Alloc.WorkspacePath,
			BranchName: child1Alloc.BranchName,
			BaseBranch: child1Alloc.BaseBranch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, _, err := h.sessions.UpdateMetadata(child1Session.ID, map[string]any{
		"parent_session_id":              parentSession.ID,
		"parent_task_call_id":            "call-1",
		"lineage_kind":                   "delegated_subagent",
		"subagent":                       "coder",
		"base_commit":                    commitB,
		"worktree_path":                  child1Alloc.WorkspacePath,
		"child_branch":                   child1Alloc.BranchName,
		"worktree_base_branch":           child1Alloc.BaseBranch,
		"swarm_v3_source_workspace_path": repo,
		"target_workspace_path":          parentAlloc.WorkspacePath,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: child1Session.ID, UserID: h.principal.UserID, AccountScopeID: h.principal.AccountScopeID,
		Kind: sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID: child1Session.ID, Phase: "completed", Active: false, StartedAt: now - 1000, EndedAt: now - 500, UpdatedAt: now - 500,
		}, NowUnixMs: now,
	})
	_, _, _ = h.sessions.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1",
	}, pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID: h.principal.AccountScopeID, LogicalTaskID: "call-1:1", SessionID: child1Session.ID,
		ParentSessionID: parentSession.ID, WorkspacePath: child1Alloc.WorkspacePath, WorktreeBranch: child1Alloc.BranchName,
		ParentBranch: parentAlloc.BranchName, ImmutableBaseCommit: commitB,
	}, "child-1-lineage")

	parentMetadata := map[string]any{
		"swarm_v3_source_workspace_path": repo,
		"task_launches": map[string]any{
			"call-1": map[string]any{
				"call_id":           "call-1",
				"parent_session_id": parentSession.ID,
				"subagent":          "coder",
				"child_session_id":  child1Session.ID,
				"launches": []any{
					map[string]any{
						"subagent":              "coder",
						"child_session_id":      child1Session.ID,
						"parent_workspace_path": parentAlloc.WorkspacePath,
						"workspace_path":        child1Alloc.WorkspacePath,
						"worktree_root_path":    child1Alloc.WorkspacePath,
						"worktree_branch":       child1Alloc.BranchName,
						"worktree_base_branch":  parentAlloc.BranchName,
						"parent_branch":         parentAlloc.BranchName,
						"base_commit":           commitB,
						"head_commit":           commitC,
					},
				},
			},
		},
	}
	if _, _, err := h.sessions.UpdateMetadata(parentSession.ID, parentMetadata); err != nil {
		t.Fatal(err)
	}

	reqCall := tool.Call{
		CallID: "call-stale-check",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"prompt":        "Fix bug",
			"subagent_type": "coder",
			"meta_prompt":   "Do work",
			"owned_scope":   []string{"file.txt"},
			"committed_source": map[string]any{
				"task_call_id":     "call-1",
				"child_session_id": child1Session.ID,
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

	// Intercept ApplySessionMutation during child creation to dirty the source child before publication recheck!
	hookedApply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		res, mErr := h.sessions.ApplySessionMutation(input)
		if mErr == nil && input.Kind == sessionruntime.SessionMutationCreateSession {
			// Dirty Child 1's worktree!
			_ = os.WriteFile(filepath.Join(child1Alloc.WorkspacePath, "dirty.txt"), []byte("dirty"), 0o644)
		}
		return res, mErr
	}

	execReq := taskExecutionRequest{
		Parsed:               parsed,
		ParsedProvided:       true,
		ApprovedArguments:    mustJSON(t, manifest.ApprovedArguments),
		RunID:                "run-stale",
		Principal:            h.principal,
		ApplySessionMutation: hookedApply,
	}

	_, err = h.service.executeTaskToolWithParsed(context.Background(), parentSession.ID, sessionruntime.ModeAuto, 1, reqCall, nil, execReq)
	if err == nil || !strings.Contains(err.Error(), "recheck committed source before publication") {
		t.Fatalf("expected pre-publication recheck failure, got %v", err)
	}
}
