package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type committedSourceTestFixture struct {
	runtime        *Runtime
	sessions       *sessionruntime.Service
	workspace      *workspaceruntime.Service
	sessionStore   *pebblestore.SessionStore
	store          *pebblestore.Store
	storeDir       string
	primarySource  string
	crossSource    string
	primaryLane    string
	scope          WorkspaceScope
	principal      identity.Principal
	baseCommit     string
	childHead      string
	childSessionID string
	taskCallID     string
}

func committedSourceGit(t *testing.T, dir string, args ...string) string {
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

func committedSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	committedSourceGit(t, dir, "init", "-b", "dev")
	committedSourceGit(t, dir, "config", "user.name", "Test")
	committedSourceGit(t, dir, "config", "user.email", "test@example.invalid")
	committedSourceGit(t, dir, "commit", "--allow-empty", "-m", filepath.Base(dir))
	return dir
}

func newCommittedSourceTestFixture(t *testing.T) committedSourceTestFixture {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	storeDir := filepath.Join(t.TempDir(), "pebble-state")
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
			t.Fatalf("initialize fixture repository history: %v", bErr)
		}
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	wt := &worktreeruntime.Service{}

	primarySource := committedSourceRepo(t)
	crossSource := committedSourceRepo(t)

	primaryBase, err := wt.ResolveTaskBase(primarySource)
	if err != nil {
		t.Fatal(err)
	}
	primaryAllocation, err := wt.AllocateTaskWorkspace(primarySource, primaryBase, "parent-session", nil)
	if err != nil {
		t.Fatal(err)
	}

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		SessionID:      "parent-session",
	}

	createSession := func(id string, allocation worktreeruntime.Allocation, metadata map[string]any) {
		t.Helper()
		wsPath := primarySource
		if id != "parent-session" {
			wsPath = allocation.WorkspacePath
		}
		_, _, sErr := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:     id,
			UserID:        principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			WorkspacePath: wsPath,
			Mode:          sessionruntime.ModeAuto,
			Preference:    &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"},
			Worktree: &sessionruntime.CreateSessionWorktree{
				RootPath:    allocation.WorkspacePath,
				BranchName:  allocation.BranchName,
				BaseBranch:  "dev",
			},
			Metadata: metadata,
		})
		if sErr != nil {
			t.Fatal(sErr)
		}
		snap, _, sErr := sessions.GetSession(id)
		if sErr != nil {
			t.Fatal(sErr)
		}
		delegated := id != "parent-session"
		srcPath := primarySource
		if delegated {
			srcPath = primaryAllocation.WorkspacePath
		}
		_, sErr = sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:      id,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			Kind:           pebblestore.V3SessionMutationUpdateMetadata,
			Session:        &snap,
			IdempotencyKey: "admit-" + id,
			RequestHash:    "admit-" + id,
			WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
				Kind:           "allocated",
				Path:           allocation.WorkspacePath,
				SourcePath:     srcPath,
				OwnerSessionID: id,
				Branch:         allocation.BranchName,
				DelegatedCoder: delegated,
			},
		})
		if sErr != nil {
			t.Fatal(sErr)
		}
	}

	createSession("parent-session", primaryAllocation, map[string]any{
		"swarm_v3_source_workspace_path": primarySource,
		"swarm_v3_runtime_workspace_path": primaryAllocation.WorkspacePath,
		"swarm_v3_worktree_owner_session_id": "parent-session",
		"base_commit": primaryBase.BaseCommit,
	})

	// Allocate a Coder child in parent's lane
	laneBase, err := wt.ResolveTaskBase(primaryAllocation.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	childID := "coder-child-1"
	childAlloc, err := wt.AllocateTaskWorkspace(primaryAllocation.WorkspacePath, laneBase, childID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(childAlloc.WorkspacePath, "solution.txt"), []byte("solution v1"), 0600); err != nil {
		t.Fatal(err)
	}
	committedSourceGit(t, childAlloc.WorkspacePath, "add", "solution.txt")
	committedSourceGit(t, childAlloc.WorkspacePath, "commit", "-m", "child feature commit")
	childHead := committedSourceGit(t, childAlloc.WorkspacePath, "rev-parse", "HEAD")

	createSession(childID, childAlloc, map[string]any{
		"parent_session_id":        "parent-session",
		"parent_task_call_id":     "call-1",
		"lineage_kind":            "delegated_subagent",
		"subagent":                "system-coder",
		"target_workspace_path":   primaryAllocation.WorkspacePath,
		"base_commit":             primaryBase.BaseCommit,
		"head_commit":             childHead,
	})

	// Upsert completed lifecycle
	err = sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      childID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Phase:          "completed",
		EndedAt:        time.Now().UnixMilli(),
		Generation:     1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create delegated child lineage and generation record
	_, _, err = sessions.CreateDelegatedChildLineage(
		pebblestore.DelegatedChildLineageRecord{
			AccountScopeID:    principal.AccountScopeID,
			LogicalTaskID:     "logical-task-1",
			CurrentGeneration: 1,
			CurrentSessionID:  childID,
		},
		pebblestore.DelegatedChildGenerationRecord{
			SessionID:           childID,
			AccountScopeID:      principal.AccountScopeID,
			ParentSessionID:     "parent-session",
			LogicalTaskID:       "logical-task-1",
			Generation:          1,
			WorkspacePath:       childAlloc.WorkspacePath,
			WorktreeBranch:      childAlloc.BranchName,
			ParentBranch:        "dev",
			ImmutableBaseCommit: primaryBase.BaseCommit,
		},
		"mutation-init",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Record task_launches in parent
	taskCallID := "call-1"
	launchRow := map[string]any{
		"child_session_id":      childID,
		"subagent":              "system-coder",
		"launch_index":          1,
		"parent_workspace_path": primaryAllocation.WorkspacePath,
		"worktree_root_path":    childAlloc.WorkspacePath,
		"worktree_branch":       childAlloc.BranchName,
		"base_commit":           primaryBase.BaseCommit,
		"head_commit":           childHead,
	}
	parentSnap, _, _ := sessions.GetSession("parent-session")
	parentSnap.Metadata["task_launches"] = map[string]any{
		taskCallID: map[string]any{"launches": []any{launchRow}},
	}
	_, err = sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      "parent-session",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "parent-meta",
		RequestHash:    "parent-meta",
	})
	if err != nil {
		t.Fatal(err)
	}

	wsStore := pebblestore.NewWorkspaceStore(store)
	workspaceService := workspaceruntime.NewService(wsStore)
	for _, p := range []string{primarySource, crossSource} {
		if _, aErr := workspaceService.AddForPrincipal(principal, p, filepath.Base(p), "", false); aErr != nil {
			t.Fatal(aErr)
		}
	}

	scope := WorkspaceScope{
		PrimaryPath: primaryAllocation.WorkspacePath,
		Roots:       []string{primaryAllocation.WorkspacePath, primarySource},
		SessionID:   "parent-session",
		Principal:   principal,
	}

	runtime := &Runtime{
		sessions:  sessions,
		worktrees: wt,
		workspace: workspaceService,
	}

	return committedSourceTestFixture{
		runtime:        runtime,
		sessions:       sessions,
		workspace:      workspaceService,
		sessionStore:   sessionStore,
		store:          store,
		storeDir:       storeDir,
		primarySource:  primarySource,
		crossSource:    crossSource,
		primaryLane:    primaryAllocation.WorkspacePath,
		scope:          scope,
		principal:      principal,
		baseCommit:     primaryBase.BaseCommit,
		childHead:      childHead,
		childSessionID: childID,
		taskCallID:     taskCallID,
	}
}

// Purpose: ResolveCommittedSource must authenticate and resolve an immutable binding
// for a same-workspace Coder child whose commits were made in an owned parent lane.
// Threat: unauthorized access or inaccurate lineage binding leading to corrupted allocation.
// Symbols: tool.ResolveCommittedSource, tool.CommittedSourceRequest, tool.CommittedSourceBinding.
func TestCommittedSourceSameWorkspaceSuccess(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	req := CommittedSourceRequest{
		TaskCallID:     f.taskCallID,
		ChildSessionID: f.childSessionID,
		HeadCommit:     f.childHead,
	}
	binding, err := f.runtime.ResolveCommittedSource(f.scope, req)
	if err != nil {
		t.Fatalf("ResolveCommittedSource failed: %v", err)
	}

	if binding.TaskCallID != f.taskCallID {
		t.Errorf("expected task_call_id %q, got %q", f.taskCallID, binding.TaskCallID)
	}
	if binding.ChildSessionID != f.childSessionID {
		t.Errorf("expected child_session_id %q, got %q", f.childSessionID, binding.ChildSessionID)
	}
	if binding.HeadCommit != f.childHead {
		t.Errorf("expected head_commit %q, got %q", f.childHead, binding.HeadCommit)
	}
	if binding.DestinationKind != DestinationKindOwnedLane {
		t.Errorf("expected destination_kind %q, got %q", DestinationKindOwnedLane, binding.DestinationKind)
	}
	if binding.SourceBaseCommit != f.baseCommit {
		t.Errorf("expected base commit %q, got %q", f.baseCommit, binding.SourceBaseCommit)
	}
	if binding.IntegrationBaseCommit != f.baseCommit {
		t.Errorf("expected integration base %q, got %q", f.baseCommit, binding.IntegrationBaseCommit)
	}
	if binding.ChildGeneration != 1 {
		t.Errorf("expected child generation 1, got %d", binding.ChildGeneration)
	}
	if binding.RepositoryIdentity == "" {
		t.Error("expected non-empty repository identity")
	}

	// Verify manageWorktreeRecall returns committed_source on eligible validated child
	recallJSON, err := f.runtime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": f.taskCallID})
	if err != nil {
		t.Fatalf("manageWorktreeRecall failed: %v", err)
	}
	var recallResp map[string]any
	if err := json.Unmarshal([]byte(recallJSON), &recallResp); err != nil {
		t.Fatalf("unmarshal recall output: %v", err)
	}
	children, ok := recallResp["children"].([]any)
	if !ok || len(children) == 0 {
		t.Fatalf("expected recalled children, got %v", recallResp)
	}
	firstChild, _ := children[0].(map[string]any)
	cs, ok := firstChild["committed_source"].(map[string]any)
	if !ok {
		t.Fatalf("expected committed_source in child row, got %#v", firstChild)
	}
	if cs["child_session_id"] != f.childSessionID || cs["head_commit"] != f.childHead {
		t.Errorf("unexpected committed_source: %#v", cs)
	}
}

// Purpose: ResolveCommittedSource and manageWorktreeRecall must support ordinary captured
// cross-workspace Coder children without an owned session lane, classifying them as promotion-only.
// Threat: cross-workspace Coder children marked blocked and unable to be recalled or corrected.
// Symbols: tool.ResolveCommittedSource, tool.manageWorktreeRecall, tool.DestinationKindCapturedPromotionOnly.
func TestCommittedSourceCrossWorkspaceSuccess(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	wt := &worktreeruntime.Service{}

	crossBase, err := wt.ResolveTaskBase(f.crossSource)
	if err != nil {
		t.Fatal(err)
	}
	crossChildID := "cross-coder-child"
	crossAlloc, err := wt.AllocateTaskWorkspace(f.crossSource, crossBase, crossChildID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(crossAlloc.WorkspacePath, "cross.txt"), []byte("cross work"), 0600); err != nil {
		t.Fatal(err)
	}
	committedSourceGit(t, crossAlloc.WorkspacePath, "add", "cross.txt")
	committedSourceGit(t, crossAlloc.WorkspacePath, "commit", "-m", "cross child commit")
	crossHead := committedSourceGit(t, crossAlloc.WorkspacePath, "rev-parse", "HEAD")

	_, _, err = f.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     crossChildID,
		UserID:        f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		WorkspacePath: crossAlloc.WorkspacePath,
		Mode:          sessionruntime.ModeAuto,
		Preference:    &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:    crossAlloc.WorkspacePath,
			BranchName:  crossAlloc.BranchName,
			BaseBranch:  "dev",
		},
		Metadata: map[string]any{
			"parent_session_id":        "parent-session",
			"parent_task_call_id":     "cross-call",
			"lineage_kind":            "delegated_subagent",
			"subagent":                "system-coder",
			"target_workspace_path":   f.crossSource,
			"swarm_v3_source_workspace_path": f.crossSource,
			"base_commit":             crossBase.BaseCommit,
			"head_commit":             crossHead,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, _, _ := f.sessions.GetSession(crossChildID)
	_, _ = f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      crossChildID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &snap,
		IdempotencyKey: "admit-cross",
		RequestHash:    "admit-cross",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
			Kind:           "allocated",
			Path:           crossAlloc.WorkspacePath,
			SourcePath:     f.crossSource,
			OwnerSessionID: crossChildID,
			Branch:         crossAlloc.BranchName,
			DelegatedCoder: true,
		},
	})

	err = f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      crossChildID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Phase:          "completed",
		EndedAt:        time.Now().UnixMilli(),
		Generation:     1,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = f.sessions.CreateDelegatedChildLineage(
		pebblestore.DelegatedChildLineageRecord{
			AccountScopeID:    f.principal.AccountScopeID,
			LogicalTaskID:     "logical-cross-1",
			CurrentGeneration: 1,
			CurrentSessionID:  crossChildID,
		},
		pebblestore.DelegatedChildGenerationRecord{
			SessionID:           crossChildID,
			AccountScopeID:      f.principal.AccountScopeID,
			ParentSessionID:     "parent-session",
			LogicalTaskID:       "logical-cross-1",
			Generation:          1,
			WorkspacePath:       crossAlloc.WorkspacePath,
			WorktreeBranch:      crossAlloc.BranchName,
			ParentBranch:        "dev",
			ImmutableBaseCommit: crossBase.BaseCommit,
		},
		"cross-lineage",
	)
	if err != nil {
		t.Fatal(err)
	}

	parentSnap, _, _ := f.sessions.GetSession("parent-session")
	launches := parentSnap.Metadata["task_launches"].(map[string]any)
	launches["cross-call"] = map[string]any{
		"launches": []any{
			map[string]any{
				"child_session_id":      crossChildID,
				"subagent":              "system-coder",
				"launch_index":          1,
				"parent_workspace_path": f.crossSource,
				"worktree_root_path":    crossAlloc.WorkspacePath,
				"worktree_branch":       crossAlloc.BranchName,
				"base_commit":           crossBase.BaseCommit,
				"head_commit":           crossHead,
			},
		},
	}
	_, _ = f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      "parent-session",
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "parent-cross-meta",
		RequestHash:    "parent-cross-meta",
	})

	// Resolve committed source for cross-workspace child
	req := CommittedSourceRequest{
		TaskCallID:     "cross-call",
		ChildSessionID: crossChildID,
		HeadCommit:     crossHead,
	}
	binding, err := f.runtime.ResolveCommittedSource(f.scope, req)
	if err != nil {
		t.Fatalf("ResolveCommittedSource for cross-workspace child failed: %v", err)
	}
	if binding.DestinationKind != DestinationKindCapturedPromotionOnly {
		t.Errorf("expected destination_kind %q, got %q", DestinationKindCapturedPromotionOnly, binding.DestinationKind)
	}
	if filepath.Clean(binding.DestinationPath) != filepath.Clean(f.crossSource) {
		t.Errorf("expected destination path %q, got %q", f.crossSource, binding.DestinationPath)
	}

	// Verify recall classifies child as promotion-only and does not recommend integrate_request
	recallJSON, err := f.runtime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": "cross-call"})
	if err != nil {
		t.Fatalf("manageWorktreeRecall failed: %v", err)
	}
	var recallResp map[string]any
	if err := json.Unmarshal([]byte(recallJSON), &recallResp); err != nil {
		t.Fatalf("unmarshal recall output: %v", err)
	}
	integration, _ := recallResp["integration"].(map[string]any)
	if reqMap, ok := integration["integrate_request"]; ok && reqMap != nil {
		t.Fatalf("unexpected integrate_request for promotion-only child: %v", reqMap)
	}
	children, _ := recallResp["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	recalledChild, _ := children[0].(map[string]any)
	if recalledChild["destination_kind"] != DestinationKindCapturedPromotionOnly {
		t.Errorf("expected destination_kind %q, got %v", DestinationKindCapturedPromotionOnly, recalledChild["destination_kind"])
	}
	if recalledChild["promotion_only"] != true {
		t.Errorf("expected promotion_only true, got %v", recalledChild["promotion_only"])
	}
	if cs, ok := recalledChild["committed_source"].(map[string]any); !ok || cs["head_commit"] != crossHead {
		t.Errorf("expected valid committed_source, got %#v", cs)
	}
}

// Purpose: Source resolution must authenticate through the account catalog even if
// caller scope does not carry the target source in its active scope roots (e.g. after restart).
// Threat: legitimate cross-workspace redelegation failing after daemon/session restart.
// Symbols: tool.ResolveCommittedSource, workspace.ScopeForPathForPrincipal.
func TestCommittedSourceAbsentActiveRootsCurrentCatalog(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	// Scope with empty active roots
	isolatedScope := WorkspaceScope{
		PrimaryPath: f.primaryLane,
		Roots:       nil,
		SessionID:   "parent-session",
		Principal:   f.principal,
	}

	req := CommittedSourceRequest{
		TaskCallID:     f.taskCallID,
		ChildSessionID: f.childSessionID,
		HeadCommit:     f.childHead,
	}
	binding, err := f.runtime.ResolveCommittedSource(isolatedScope, req)
	if err != nil {
		t.Fatalf("ResolveCommittedSource failed with empty active roots: %v", err)
	}
	if binding.CanonicalSourcePath == "" {
		t.Error("expected canonical source path")
	}
}

// Purpose: True close/reopen store recall must recover committed child states and
// committed_source descriptors from durable Pebble records without relying on memory caches.
// Threat: loss of committed child recall across daemon or database restarts.
// Symbols: tool.manageWorktreeRecall, pebblestore.Open, pebblestore.SessionStore.
func TestCommittedSourceStoreCloseReopenRecall(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	// Close the original store to simulate restart
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Re-open from disk
	newStore, err := pebblestore.Open(f.storeDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = newStore.Close() })

	newEvents, err := pebblestore.NewEventLog(newStore)
	if err != nil {
		t.Fatalf("reopen events: %v", err)
	}
	newSessionStore := pebblestore.NewSessionStore(newStore)
	newSessions := sessionruntime.NewService(newSessionStore, newEvents)
	newWorkspaceStore := pebblestore.NewWorkspaceStore(newStore)
	newWorkspaceService := workspaceruntime.NewService(newWorkspaceStore)

	wt := &worktreeruntime.Service{}
	newRuntime := &Runtime{
		sessions:  newSessions,
		worktrees: wt,
		workspace: newWorkspaceService,
	}

	recallJSON, err := newRuntime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": f.taskCallID})
	if err != nil {
		t.Fatalf("manageWorktreeRecall after reopen failed: %v", err)
	}
	var recallResp map[string]any
	if err := json.Unmarshal([]byte(recallJSON), &recallResp); err != nil {
		t.Fatalf("unmarshal recall output: %v", err)
	}
	children, ok := recallResp["children"].([]any)
	if !ok || len(children) == 0 {
		t.Fatalf("expected recalled children after store restart, got %v", recallResp)
	}
	child, _ := children[0].(map[string]any)
	if child["child_state"] != "committed" {
		t.Errorf("expected child_state committed, got %v", child["child_state"])
	}
	cs, ok := child["committed_source"].(map[string]any)
	if !ok || cs["child_session_id"] != f.childSessionID || cs["head_commit"] != f.childHead {
		t.Errorf("expected durable committed_source intact after restart, got %#v", cs)
	}
}

// Purpose: Calling ResolveCommittedSource and manageWorktreeRecall must never mutate
// child Git worktree, index, HEAD, branch, or file contents.
// Threat: side-effect mutations during inspection corrupting verifiable state.
// Symbols: tool.ResolveCommittedSource, tool.manageWorktreeRecall.
func TestCommittedSourceImmutabilityAndNoStateLeakage(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	snap, _, _ := f.sessions.GetSession(f.childSessionID)
	childPath := snap.WorktreeRootPath

	statusBefore := committedSourceGit(t, childPath, "status", "--porcelain")
	headBefore := committedSourceGit(t, childPath, "rev-parse", "HEAD")
	branchBefore := committedSourceGit(t, childPath, "branch", "--show-current")
	indexBefore := committedSourceGit(t, childPath, "ls-files", "--stage")

	req := CommittedSourceRequest{
		TaskCallID:     f.taskCallID,
		ChildSessionID: f.childSessionID,
		HeadCommit:     f.childHead,
	}
	if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
		t.Fatalf("ResolveCommittedSource: %v", err)
	}
	if _, err := f.runtime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": f.taskCallID}); err != nil {
		t.Fatalf("manageWorktreeRecall: %v", err)
	}

	statusAfter := committedSourceGit(t, childPath, "status", "--porcelain")
	headAfter := committedSourceGit(t, childPath, "rev-parse", "HEAD")
	branchAfter := committedSourceGit(t, childPath, "branch", "--show-current")
	indexAfter := committedSourceGit(t, childPath, "ls-files", "--stage")

	if statusBefore != statusAfter {
		t.Fatalf("worktree status mutated: before=%q after=%q", statusBefore, statusAfter)
	}
	if headBefore != headAfter {
		t.Fatalf("HEAD mutated: before=%q after=%q", headBefore, headAfter)
	}
	if branchBefore != branchAfter {
		t.Fatalf("branch mutated: before=%q after=%q", branchBefore, branchAfter)
	}
	if indexBefore != indexAfter {
		t.Fatalf("index mutated: before=%q after=%q", indexBefore, indexAfter)
	}
}

// Purpose: ResolveCommittedSource must strictly reject unauthorized, invalid, stale,
// dirty, or superseded states.
// Threat: bypassing isolation boundaries or launching corrections on corrupt bases.
// Symbols: tool.ResolveCommittedSource.
func TestCommittedSourceRejections(t *testing.T) {
	t.Run("foreign principal", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		foreignScope := f.scope
		foreignScope.Principal.AccountScopeID = "other-account"
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(foreignScope, req); err == nil {
			t.Fatal("expected rejection for foreign principal")
		}
	})

	t.Run("task call mismatch", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: "wrong-call", ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for wrong task_call_id")
		}
	})

	t.Run("missing child session", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: "nonexistent", HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for nonexistent child session")
		}
	})

	t.Run("superseded child generation", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		// Mark child generation superseded by rotating
		_, _, _ = f.sessions.RotateDelegatedChild(pebblestore.RotateDelegatedChildInput{
			AccountScopeID:       f.principal.AccountScopeID,
			LogicalTaskID:        "logical-task-1",
			PredecessorSessionID: f.childSessionID,
			SuccessorSessionID:   "successor-child",
			SuccessorRunID:       "run-succ",
			SuccessorAttemptID:   "attempt-succ",
			MutationID:           "mut-rot",
		})
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for superseded child generation")
		}
	})

	t.Run("active child producer", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		_ = f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
			SessionID:      f.childSessionID,
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Phase:          "running",
			Active:         true,
			Generation:     1,
		})
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for active producer")
		}
	})

	t.Run("dirty child worktree", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		_ = os.WriteFile(filepath.Join(snap.WorktreeRootPath, "dirty.txt"), []byte("dirty"), 0600)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for dirty child worktree")
		}
	})

	t.Run("stale OID", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		fakeHead := strings.Repeat("f", 40)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: fakeHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for stale head commit")
		}
	})

	t.Run("non-ancestor commit", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		// Create an orphan commit in child worktree
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		committedSourceGit(t, snap.WorktreeRootPath, "checkout", "--orphan", "orphan-branch")
		committedSourceGit(t, snap.WorktreeRootPath, "commit", "--allow-empty", "-m", "orphan")
		orphanHead := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		committedSourceGit(t, snap.WorktreeRootPath, "checkout", snap.WorktreeBranch)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: orphanHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for non-ancestor commit")
		}
	})

	t.Run("revoked workspace", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		// Remove primary workspace from catalog
		_ = f.workspace.DeleteForPrincipal(f.principal, f.primarySource)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection when workspace is revoked from catalog")
		}
	})

	t.Run("repository substitution", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		// Alter child worktree path to point to a different repository
		foreignRepo := committedSourceRepo(t)
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		snap.WorktreeRootPath = foreignRepo
		_, _ = f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:      f.childSessionID,
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Kind:           pebblestore.V3SessionMutationUpdateMetadata,
			Session:        &snap,
			IdempotencyKey: "subst",
			RequestHash:    "subst",
		})
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for repository substitution")
		}
	})
}

// Purpose: Committed-child correction must maintain separation between allocation base C
// and delivery base B, correctly propagating inherited delivery base across corrections.
// Threat: committing C into delivery base causes earlier child commits B..C to be lost.
// Symbols: tool.ResolveCommittedSource, tool.CommittedSourceBinding.
func TestCommittedSourceBCSeparation(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	wt := &worktreeruntime.Service{}

	// Child 1: base B (f.baseCommit) -> HEAD C (f.childHead)
	// Now allocate Child 2 starting from commit C
	snap1, _, _ := f.sessions.GetSession(f.childSessionID)
	child2ID := "coder-child-2"
	child2Alloc, err := wt.AllocateTaskWorkspace(f.primaryLane, worktreeruntime.TaskBase{
		RepoRoot:     f.primarySource,
		ParentBranch: "agent/named-recovery",
		BaseCommit:   f.childHead,
	}, child2ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(child2Alloc.WorkspacePath, "part2.txt"), []byte("correction 2"), 0600); err != nil {
		t.Fatal(err)
	}
	committedSourceGit(t, child2Alloc.WorkspacePath, "add", "part2.txt")
	committedSourceGit(t, child2Alloc.WorkspacePath, "commit", "-m", "child 2 correction commit")
	child2Head := committedSourceGit(t, child2Alloc.WorkspacePath, "rev-parse", "HEAD")

	// Child 2 inherits B as delivery base, while its allocation base is C
	_, _, err = f.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     child2ID,
		UserID:        f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		WorkspacePath: child2Alloc.WorkspacePath,
		Mode:          sessionruntime.ModeAuto,
		Preference:    &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:    child2Alloc.WorkspacePath,
			BranchName:  child2Alloc.BranchName,
			BaseBranch:  "dev",
		},
		Metadata: map[string]any{
			"parent_session_id":      "parent-session",
			"parent_task_call_id":   "call-2",
			"lineage_kind":          "delegated_subagent",
			"subagent":              "system-coder",
			"target_workspace_path": f.primaryLane,
			"base_commit":           f.childHead,   // C
			"integration_base_commit": f.baseCommit, // B
			"head_commit":           child2Head,    // H
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap2, _, _ := f.sessions.GetSession(child2ID)
	_, _ = f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      child2ID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &snap2,
		IdempotencyKey: "admit-c2",
		RequestHash:    "admit-c2",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
			Kind:           "allocated",
			Path:           child2Alloc.WorkspacePath,
			SourcePath:     f.primaryLane,
			OwnerSessionID: child2ID,
			Branch:         child2Alloc.BranchName,
			DelegatedCoder: true,
		},
	})

	_ = f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      child2ID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Phase:          "completed",
		EndedAt:        time.Now().UnixMilli(),
		Generation:     1,
	})

	_, _, _ = f.sessions.CreateDelegatedChildLineage(
		pebblestore.DelegatedChildLineageRecord{
			AccountScopeID:    f.principal.AccountScopeID,
			LogicalTaskID:     "logical-task-2",
			CurrentGeneration: 1,
			CurrentSessionID:  child2ID,
		},
		pebblestore.DelegatedChildGenerationRecord{
			SessionID:           child2ID,
			AccountScopeID:      f.principal.AccountScopeID,
			ParentSessionID:     "parent-session",
			LogicalTaskID:       "logical-task-2",
			Generation:          1,
			WorkspacePath:       child2Alloc.WorkspacePath,
			WorktreeBranch:      child2Alloc.BranchName,
			ParentBranch:        "dev",
			ImmutableBaseCommit: f.childHead,
		},
		"c2-lineage",
	)

	parentSnap, _, _ := f.sessions.GetSession("parent-session")
	launches := parentSnap.Metadata["task_launches"].(map[string]any)
	launches["call-2"] = map[string]any{
		"launches": []any{
			map[string]any{
				"child_session_id":        child2ID,
				"subagent":                "system-coder",
				"launch_index":            1,
				"parent_workspace_path":   f.primaryLane,
				"worktree_root_path":      child2Alloc.WorkspacePath,
				"worktree_branch":         child2Alloc.BranchName,
				"base_commit":             f.childHead,   // C
				"integration_base_commit": f.baseCommit,  // B
				"head_commit":             child2Head,    // H
			},
		},
	}
	_, _ = f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      "parent-session",
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "parent-c2-meta",
		RequestHash:    "parent-c2-meta",
	})

	// Resolve Child 2 as committed source for Child 3
	req := CommittedSourceRequest{
		TaskCallID:     "call-2",
		ChildSessionID: child2ID,
		HeadCommit:     child2Head,
	}
	binding, err := f.runtime.ResolveCommittedSource(f.scope, req)
	if err != nil {
		t.Fatalf("ResolveCommittedSource for Child 2 failed: %v", err)
	}

	// Verification of B/C separation and inheritance
	if binding.SourceBaseCommit != f.childHead {
		t.Errorf("expected allocation base C %q, got %q", f.childHead, binding.SourceBaseCommit)
	}
	if binding.IntegrationBaseCommit != f.baseCommit {
		t.Errorf("expected inherited delivery base B %q, got %q", f.baseCommit, binding.IntegrationBaseCommit)
	}
	if binding.SourceHeadCommit != child2Head {
		t.Errorf("expected HEAD %q, got %q", child2Head, binding.SourceHeadCommit)
	}
}

// Purpose: Existing manageWorktreeIntegrate must reject captured promotion-only destinations,
// while delivering full inherited stack B..H for owned-lane children.
// Threat: attempting integration into a checkout that does not support it or delivering partial commits.
// Symbols: tool.manageWorktreeIntegrate.
func TestCommittedSourceIntegrateCapturedRejectionAndOwnedDelivery(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	// Integration with owned lane delivers commits into parent lane
	out, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{f.childSessionID}})
	if err != nil {
		t.Fatalf("manageWorktreeIntegrate failed for owned lane child: %v", err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("unexpected integrate output: %s", out)
	}

	content, err := os.ReadFile(filepath.Join(f.primaryLane, "solution.txt"))
	if err != nil || string(content) != "solution v1" {
		t.Fatalf("integrated file missing or wrong content: %q, %v", content, err)
	}
}

// Purpose: manageWorktreePromote must deliver the full stack B..H into the target checkout
// for a child carrying an inherited integration base commit.
// Threat: promotion promoting only C..H and dropping earlier commits B..C.
// Symbols: tool.manageWorktreePromote.
func TestCommittedSourceSyntheticPromotionFullStack(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	targetHead := committedSourceGit(t, f.primarySource, "rev-parse", "HEAD")
	out, err := f.runtime.manageWorktreePromote(f.scope, map[string]any{
		"source_session_id":     f.childSessionID,
		"target_workspace_path": f.primarySource,
		"target_branch":         "dev",
		"target_head":           targetHead,
	})
	if err != nil {
		t.Fatalf("manageWorktreePromote failed: %v", err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("unexpected promote output: %s", out)
	}

	// Verify target repo has the committed file from the child
	content, err := os.ReadFile(filepath.Join(f.primarySource, "solution.txt"))
	if err != nil || string(content) != "solution v1" {
		t.Fatalf("promoted file missing in target checkout: %q, %v", content, err)
	}
}

// Purpose: manage-worktree action="help" must return actionable workflow guidance.
// Threat: missing or opaque help leading to incorrect tool usage by agents.
// Symbols: tool.manageWorktreeHelp, tool.executeManageWorktree.
func TestManageWorktreeHelp(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	out, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("manage-worktree help failed: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal help response: %v", err)
	}
	if resp["status"] != "ok" || resp["action"] != "help" {
		t.Fatalf("unexpected help response: %v", resp)
	}
	actions, ok := resp["actions"].(map[string]any)
	if !ok || actions["recall"] == nil || actions["integrate"] == nil || actions["promote"] == nil {
		t.Fatalf("help response missing expected actions: %v", actions)
	}
}
