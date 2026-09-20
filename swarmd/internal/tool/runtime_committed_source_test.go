package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	runtime             *Runtime
	sessions            *sessionruntime.Service
	workspace           *workspaceruntime.Service
	sessionStore        *pebblestore.SessionStore
	store               *pebblestore.Store
	storeDir            string
	closeStore          func() error
	primarySource       string
	crossSource         string
	primaryLane         string
	parentBranch        string
	primaryWorkspaceID  string
	primaryWorkspaceGen string
	crossWorkspaceID    string
	crossWorkspaceGen   string
	scope               WorkspaceScope
	principal           identity.Principal
	baseCommit          string
	childHead           string
	childSessionID      string
	taskCallID          string
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

type createChildOptions struct {
	childID                  string
	taskCallID               string
	logicalTaskID            string
	allocationParent         string
	parentBranch             string
	targetWorkspacePath      string
	canonicalSourcePath      string
	sourceWorkspaceID        string
	sourceWorkspaceGen       string
	baseCommit               string
	inheritedIntegrationBase string
	fileName                 string
	fileContent              string
	commitMessage            string
	priorBinding             *CommittedSourceBinding
	priorCommittedSource     map[string]any
	destinationKind          string
	promotionOnly            bool
	launchIndex              int
}

// createCommittedSourceChild is a single portable synthetic helper that allocates real managed Git,
// applies session admission with strict Coder evidence, creates durable rotation lineage/generation,
// upserts completed lifecycle, and records the typed launch row in the parent's task_launches metadata.
func createCommittedSourceChild(t *testing.T, f *committedSourceTestFixture, opts createChildOptions) (string, string, worktreeruntime.Allocation) {
	t.Helper()
	wt := &worktreeruntime.Service{}

	taskBase := worktreeruntime.TaskBase{
		RepoRoot:     opts.canonicalSourcePath,
		ParentBranch: opts.parentBranch,
		BaseCommit:   opts.baseCommit,
	}
	alloc, err := wt.AllocateTaskWorkspace(opts.allocationParent, taskBase, opts.childID, nil)
	if err != nil {
		t.Fatalf("AllocateTaskWorkspace for %s: %v", opts.childID, err)
	}

	if opts.fileName != "" {
		filePath := filepath.Join(alloc.WorkspacePath, opts.fileName)
		if err := os.WriteFile(filePath, []byte(opts.fileContent), 0600); err != nil {
			t.Fatalf("write %s: %v", opts.fileName, err)
		}
		committedSourceGit(t, alloc.WorkspacePath, "add", opts.fileName)
		committedSourceGit(t, alloc.WorkspacePath, "commit", "-m", opts.commitMessage)
	}
	childHead := committedSourceGit(t, alloc.WorkspacePath, "rev-parse", "HEAD")

	childMeta := map[string]any{
		"parent_session_id":                  "parent-session",
		"parent_task_call_id":               opts.taskCallID,
		"lineage_kind":                      "delegated_subagent",
		"subagent":                          "system-coder",
		"target_workspace_path":             opts.targetWorkspacePath,
		"swarm_v3_source_workspace_path":   opts.canonicalSourcePath,
		"swarm_v3_runtime_workspace_path":  alloc.WorkspacePath,
		"swarm_v3_worktree_owner_session_id": opts.childID,
		"swarm_v3_worktree_base_commit":    opts.baseCommit,
		"base_commit":                       opts.baseCommit,
		"head_commit":                       childHead,
		"parent_branch":                     opts.parentBranch,
	}
	if opts.sourceWorkspaceID != "" {
		childMeta["swarm_v3_source_workspace_id"] = opts.sourceWorkspaceID
	}
	if opts.sourceWorkspaceGen != "" {
		childMeta["swarm_v3_source_workspace_generation"] = opts.sourceWorkspaceGen
	}
	if opts.inheritedIntegrationBase != "" {
		childMeta["integration_base_commit"] = opts.inheritedIntegrationBase
	}
	if opts.priorCommittedSource != nil {
		childMeta["committed_source"] = opts.priorCommittedSource
	}
	if opts.priorBinding != nil {
		childMeta["committed_source_binding"] = *opts.priorBinding
	}
	if opts.destinationKind != "" {
		childMeta["destination_kind"] = opts.destinationKind
	}
	if opts.promotionOnly {
		childMeta["promotion_only"] = true
	}

	_, _, err = f.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      opts.childID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		WorkspacePath:  alloc.WorkspacePath,
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   alloc.WorkspacePath,
			BranchName: alloc.BranchName,
			BaseBranch: "dev",
		},
		Metadata: childMeta,
	})
	if err != nil {
		t.Fatalf("CreateSessionWithOptions for %s: %v", opts.childID, err)
	}

	snap, found, err := f.sessions.GetSession(opts.childID)
	if err != nil || !found {
		t.Fatalf("GetSession for %s: %v (found=%v)", opts.childID, err, found)
	}

	admitRes, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      opts.childID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &snap,
		IdempotencyKey: "fixture-admit-" + opts.childID,
		RequestHash:    "fixture-admit-" + opts.childID,
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
			Kind:           "allocated",
			Path:           alloc.WorkspacePath,
			SourcePath:     opts.canonicalSourcePath,
			OwnerSessionID: opts.childID,
			Branch:         alloc.BranchName,
			DelegatedCoder: true,
		},
	})
	if err != nil {
		t.Fatalf("ApplySessionMutation admit %s: %v", opts.childID, err)
	}
	if admitRes.Error != nil {
		t.Fatalf("ApplySessionMutation admit %s error: %v", opts.childID, admitRes.Error)
	}
	if admitRes.Conflict != nil {
		t.Fatalf("ApplySessionMutation admit %s conflict: %v", opts.childID, admitRes.Conflict)
	}

	err = f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      opts.childID,
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Phase:          "completed",
		EndedAt:        time.Now().UnixMilli(),
		Generation:     1,
	})
	if err != nil {
		t.Fatalf("UpsertLifecycle for %s: %v", opts.childID, err)
	}

	_, _, err = f.sessions.CreateDelegatedChildLineage(
		pebblestore.DelegatedChildLineageRecord{
			AccountScopeID:    f.principal.AccountScopeID,
			LogicalTaskID:     opts.logicalTaskID,
			CurrentGeneration: 1,
			CurrentSessionID:  opts.childID,
		},
		pebblestore.DelegatedChildGenerationRecord{
			SessionID:           opts.childID,
			AccountScopeID:      f.principal.AccountScopeID,
			ParentSessionID:     "parent-session",
			LogicalTaskID:       opts.logicalTaskID,
			Generation:          1,
			WorkspacePath:       alloc.WorkspacePath,
			WorktreeBranch:      alloc.BranchName,
			ParentBranch:        opts.parentBranch,
			ImmutableBaseCommit: opts.baseCommit,
		},
		"fixture-lineage-" + opts.childID,
	)
	if err != nil {
		t.Fatalf("CreateDelegatedChildLineage for %s: %v", opts.childID, err)
	}

	launchIndex := opts.launchIndex
	if launchIndex <= 0 {
		launchIndex = 1
	}
	launchRow := map[string]any{
		"child_session_id":      opts.childID,
		"subagent":              "system-coder",
		"launch_index":          launchIndex,
		"parent_workspace_path": opts.targetWorkspacePath,
		"worktree_root_path":    alloc.WorkspacePath,
		"worktree_branch":       alloc.BranchName,
		"parent_branch":         opts.parentBranch,
		"base_commit":           opts.baseCommit,
		"head_commit":           childHead,
	}
	if opts.inheritedIntegrationBase != "" {
		launchRow["integration_base_commit"] = opts.inheritedIntegrationBase
	}
	if opts.priorCommittedSource != nil {
		launchRow["committed_source"] = opts.priorCommittedSource
	}
	if opts.priorBinding != nil {
		launchRow["committed_source_binding"] = *opts.priorBinding
	}
	if opts.destinationKind != "" {
		launchRow["destination_kind"] = opts.destinationKind
	}
	if opts.promotionOnly {
		launchRow["promotion_only"] = true
	}

	parentSnap, found, err := f.sessions.GetSession("parent-session")
	if err != nil || !found {
		t.Fatalf("GetSession for parent-session: %v (found=%v)", err, found)
	}
	launches, _ := parentSnap.Metadata["task_launches"].(map[string]any)
	if launches == nil {
		launches = map[string]any{}
	}
	callEntry, _ := launches[opts.taskCallID].(map[string]any)
	if callEntry == nil {
		callEntry = map[string]any{}
	}
	rows, _ := callEntry["launches"].([]any)
	rows = append(rows, launchRow)
	callEntry["launches"] = rows
	launches[opts.taskCallID] = callEntry
	parentSnap.Metadata["task_launches"] = launches

	pMutRes, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      "parent-session",
		UserID:         f.principal.UserID,
		AccountScopeID: f.principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "launch-" + opts.childID,
		RequestHash:    "launch-" + opts.childID,
	})
	if err != nil {
		t.Fatalf("ApplySessionMutation parent launches for %s: %v", opts.childID, err)
	}
	if pMutRes.Error != nil {
		t.Fatalf("ApplySessionMutation parent launches error for %s: %v", opts.childID, pMutRes.Error)
	}
	if pMutRes.Conflict != nil {
		t.Fatalf("ApplySessionMutation parent launches conflict for %s: %v", opts.childID, pMutRes.Conflict)
	}

	return opts.childID, childHead, alloc
}

func createTestCorrectionChildWithBinding(t *testing.T, f *committedSourceTestFixture, child1Binding CommittedSourceBinding) (string, string, worktreeruntime.Allocation) {
	t.Helper()
	child2ID := "coder-child-2"
	taskCallID := "call-2"
	logicalTaskID := "logical-task-2"
	committedSource := map[string]any{
		"task_call_id":     child1Binding.TaskCallID,
		"child_session_id": child1Binding.ChildSessionID,
		"head_commit":      child1Binding.HeadCommit,
	}
	return createCommittedSourceChild(t, f, createChildOptions{
		childID:                  child2ID,
		taskCallID:               taskCallID,
		logicalTaskID:            logicalTaskID,
		allocationParent:         f.primaryLane,
		parentBranch:             f.parentBranch,
		targetWorkspacePath:      f.primaryLane,
		canonicalSourcePath:      f.primarySource,
		sourceWorkspaceID:        f.primaryWorkspaceID,
		sourceWorkspaceGen:       f.primaryWorkspaceGen,
		baseCommit:               f.childHead,
		inheritedIntegrationBase: f.baseCommit,
		fileName:                 "part2.txt",
		fileContent:              "correction 2",
		commitMessage:            "child 2 correction commit",
		priorBinding:             &child1Binding,
		priorCommittedSource:     committedSource,
		launchIndex:              1,
	})
}

func createTestCorrectionChild(t *testing.T, f *committedSourceTestFixture, child1Binding CommittedSourceBinding) (string, string, worktreeruntime.Allocation) {
	return createTestCorrectionChildWithBinding(t, f, child1Binding)
}

func newCommittedSourceTestFixture(t *testing.T) committedSourceTestFixture {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	storeDir := filepath.Join(t.TempDir(), "pebble-state")
	store, err := pebblestore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	var storeClosed bool
	closeStore := func() error {
		if storeClosed {
			return nil
		}
		storeClosed = true
		return store.Close()
	}
	t.Cleanup(func() { _ = closeStore() })

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

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		SessionID:      "parent-session",
	}

	wsStore := pebblestore.NewWorkspaceStore(store)
	workspaceService := workspaceruntime.NewService(wsStore)
	savedPrimary, err := workspaceService.AddForPrincipal(principal, primarySource, filepath.Base(primarySource), "", false)
	if err != nil {
		t.Fatalf("add primary workspace: %v", err)
	}
	savedCross, err := workspaceService.AddForPrincipal(principal, crossSource, filepath.Base(crossSource), "", false)
	if err != nil {
		t.Fatalf("add cross workspace: %v", err)
	}

	primaryBase, err := wt.ResolveTaskBase(primarySource)
	if err != nil {
		t.Fatalf("resolve primary base: %v", err)
	}
	primaryAllocation, err := wt.AllocateTaskWorkspace(primarySource, primaryBase, "parent-session", nil)
	if err != nil {
		t.Fatalf("allocate primary lane: %v", err)
	}

	parentMeta := map[string]any{
		"swarm_v3_source_workspace_path":       primarySource,
		"swarm_v3_source_workspace_id":         savedPrimary.WorkspaceID,
		"swarm_v3_source_workspace_generation": strconv.FormatInt(savedPrimary.WorkspaceGeneration, 10),
		"swarm_v3_runtime_workspace_path":      primaryAllocation.WorkspacePath,
		"swarm_v3_worktree_owner_session_id":   "parent-session",
		"swarm_v3_worktree_base_commit":        primaryBase.BaseCommit,
		"base_commit":                          primaryBase.BaseCommit,
	}

	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "parent-session",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  primarySource,
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"},
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath:   primaryAllocation.WorkspacePath,
			BranchName: primaryAllocation.BranchName,
			BaseBranch: "dev",
		},
		Metadata: parentMeta,
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	parentSnap, found, err := sessions.GetSession("parent-session")
	if err != nil || !found {
		t.Fatalf("get parent session: %v (found=%v)", err, found)
	}

	parentAdmitRes, err := sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      "parent-session",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Kind:           pebblestore.V3SessionMutationUpdateMetadata,
		Session:        &parentSnap,
		IdempotencyKey: "fixture-admit-parent-session",
		RequestHash:    "fixture-admit-parent-session",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{
			Kind:           "allocated",
			Path:           primaryAllocation.WorkspacePath,
			SourcePath:     primarySource,
			OwnerSessionID: "parent-session",
			Branch:         primaryAllocation.BranchName,
			DelegatedCoder: false,
		},
	})
	if err != nil {
		t.Fatalf("admit parent session: %v", err)
	}
	if parentAdmitRes.Error != nil {
		t.Fatalf("admit parent session error: %v", parentAdmitRes.Error)
	}
	if parentAdmitRes.Conflict != nil {
		t.Fatalf("admit parent session conflict: %v", parentAdmitRes.Conflict)
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

	fixture := committedSourceTestFixture{
		runtime:             runtime,
		sessions:            sessions,
		workspace:           workspaceService,
		sessionStore:        sessionStore,
		store:               store,
		storeDir:            storeDir,
		closeStore:          closeStore,
		primarySource:       primarySource,
		crossSource:         crossSource,
		primaryLane:         primaryAllocation.WorkspacePath,
		parentBranch:        primaryAllocation.BranchName,
		primaryWorkspaceID:  savedPrimary.WorkspaceID,
		primaryWorkspaceGen: strconv.FormatInt(savedPrimary.WorkspaceGeneration, 10),
		crossWorkspaceID:    savedCross.WorkspaceID,
		crossWorkspaceGen:   strconv.FormatInt(savedCross.WorkspaceGeneration, 10),
		scope:               scope,
		principal:           principal,
		baseCommit:          primaryBase.BaseCommit,
		taskCallID:          "call-1",
	}

	childID, childHead, _ := createCommittedSourceChild(t, &fixture, createChildOptions{
		childID:             "coder-child-1",
		taskCallID:          "call-1",
		logicalTaskID:       "logical-task-1",
		allocationParent:    primaryAllocation.WorkspacePath,
		parentBranch:        primaryAllocation.BranchName,
		targetWorkspacePath: primaryAllocation.WorkspacePath,
		canonicalSourcePath: primarySource,
		sourceWorkspaceID:   savedPrimary.WorkspaceID,
		sourceWorkspaceGen:  strconv.FormatInt(savedPrimary.WorkspaceGeneration, 10),
		baseCommit:          primaryBase.BaseCommit,
		fileName:            "solution.txt",
		fileContent:         "solution v1",
		commitMessage:       "child feature commit",
		launchIndex:         1,
	})

	fixture.childSessionID = childID
	fixture.childHead = childHead

	return fixture
}

// Purpose: ResolveCommittedSource must authenticate and resolve an immutable binding
// for a same-workspace Coder child whose commits were made in an owned parent lane.
// Threat: unauthorized access or inaccurate lineage binding leading to corrupted allocation.
// Symbols: tool.ResolveCommittedSource, tool.CommittedSourceRequest, tool.CommittedSourceBinding.
// Layer: Unit test on tool.Runtime with real pebble store and git worktrees.
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
// Layer: Unit test on tool.Runtime with real pebble store and git worktrees.
func TestCommittedSourceCrossWorkspaceSuccess(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	wt := &worktreeruntime.Service{}

	crossBase, err := wt.ResolveTaskBase(f.crossSource)
	if err != nil {
		t.Fatal(err)
	}

	crossChildID, crossHead, _ := createCommittedSourceChild(t, &f, createChildOptions{
		childID:             "cross-coder-child",
		taskCallID:          "cross-call",
		logicalTaskID:       "logical-cross-1",
		allocationParent:    f.crossSource,
		parentBranch:        "dev",
		targetWorkspacePath: f.crossSource,
		canonicalSourcePath: f.crossSource,
		sourceWorkspaceID:   f.crossWorkspaceID,
		sourceWorkspaceGen:  f.crossWorkspaceGen,
		baseCommit:          crossBase.BaseCommit,
		fileName:            "cross.txt",
		fileContent:         "cross work",
		commitMessage:       "cross child commit",
		destinationKind:     DestinationKindCapturedPromotionOnly,
		promotionOnly:       true,
		launchIndex:         1,
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
// Layer: Unit test on tool.Runtime with real pebble store and git worktrees.
func TestCommittedSourceAbsentActiveRootsCurrentCatalog(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
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
// Layer: Durability restart test on tool.Runtime and pebblestore.
func TestCommittedSourceStoreCloseReopenRecall(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	// Close the original store via safe helper to prevent double close
	if err := f.closeStore(); err != nil {
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
// Layer: Read-only immutability invariant test on tool.Runtime.
func TestCommittedSourceImmutabilityAndNoStateLeakage(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	snap, found, err := f.sessions.GetSession(f.childSessionID)
	if err != nil || !found {
		t.Fatalf("get child session: %v", err)
	}
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
// dirty, superseded, forged, or malformed states, asserting relevant state remains unchanged.
// Threat: bypassing isolation boundaries or launching corrections on corrupt bases.
// Symbols: tool.ResolveCommittedSource.
// Layer: Negative security and authority boundary tests on tool.Runtime.
func TestCommittedSourceRejections(t *testing.T) {
	assertUnchanged := func(t *testing.T, f committedSourceTestFixture, childID string, childPath string, headBefore, statusBefore string) {
		t.Helper()
		headAfter := committedSourceGit(t, childPath, "rev-parse", "HEAD")
		statusAfter := committedSourceGit(t, childPath, "status", "--porcelain")
		if headBefore != headAfter {
			t.Fatalf("child HEAD unexpectedly modified: before=%q after=%q", headBefore, headAfter)
		}
		if statusBefore != statusAfter {
			t.Fatalf("child status unexpectedly modified: before=%q after=%q", statusBefore, statusAfter)
		}
		parentHead := committedSourceGit(t, f.primaryLane, "rev-parse", "HEAD")
		parentStatus := committedSourceGit(t, f.primaryLane, "status", "--porcelain")
		if parentStatus != "" {
			t.Fatalf("parent lane dirty after rejection: %s", parentStatus)
		}
		if parentHead != f.baseCommit {
			t.Fatalf("parent lane HEAD unexpectedly changed: %s", parentHead)
		}
		sourceStatus := committedSourceGit(t, f.primarySource, "status", "--porcelain")
		if sourceStatus != "" {
			t.Fatalf("primary source dirty after rejection: %s", sourceStatus)
		}
		if _, found, err := f.sessions.GetSession(childID); err != nil || !found {
			t.Fatalf("child session missing after rejection: %v", err)
		}
		if _, found, err := f.sessions.GetSession("parent-session"); err != nil || !found {
			t.Fatalf("parent session missing after rejection: %v", err)
		}
	}

	t.Run("foreign principal", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		foreignScope := f.scope
		foreignScope.Principal.AccountScopeID = "other-account"
		if _, err := f.runtime.ResolveCommittedSource(foreignScope, req); err == nil {
			t.Fatal("expected rejection for foreign principal")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("task call mismatch", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		badReq := req
		badReq.TaskCallID = "wrong-call"
		if _, err := f.runtime.ResolveCommittedSource(f.scope, badReq); err == nil {
			t.Fatal("expected rejection for wrong task_call_id")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("whitespace in commit OID or tuple", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		reqLeading := req
		reqLeading.HeadCommit = " " + f.childHead
		if _, err := f.runtime.ResolveCommittedSource(f.scope, reqLeading); err == nil {
			t.Fatal("expected rejection for leading whitespace in head commit")
		}
		reqTrailing := req
		reqTrailing.HeadCommit = f.childHead + "\n"
		if _, err := f.runtime.ResolveCommittedSource(f.scope, reqTrailing); err == nil {
			t.Fatal("expected rejection for trailing whitespace in head commit")
		}
		reqCallWS := req
		reqCallWS.TaskCallID = " " + f.taskCallID
		if _, err := f.runtime.ResolveCommittedSource(f.scope, reqCallWS); err == nil {
			t.Fatal("expected rejection for whitespace in task_call_id")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("missing child session", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		badReq := req
		badReq.ChildSessionID = "nonexistent"
		if _, err := f.runtime.ResolveCommittedSource(f.scope, badReq); err == nil {
			t.Fatal("expected rejection for nonexistent child session")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("superseded child generation", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		lineage, _, err := f.sessions.GetDelegatedChildLineage(f.principal.AccountScopeID, "logical-task-1")
		if err != nil {
			t.Fatal(err)
		}
		pred, _, err := f.sessions.GetDelegatedChildGenerationBySession(f.principal.AccountScopeID, f.childSessionID)
		if err != nil {
			t.Fatal(err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		lease, _, err := f.sessions.GetDelegatedWorktreeOwner(f.principal.AccountScopeID, snap.WorktreeRootPath)
		if err != nil {
			t.Fatal(err)
		}
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		_, _, err = f.sessions.RotateDelegatedChild(pebblestore.RotateDelegatedChildInput{
			AccountScopeID:              f.principal.AccountScopeID,
			LogicalTaskID:               "logical-task-1",
			ExpectedLineageRevision:     lineage.Revision,
			ExpectedPredecessorRevision: pred.Revision,
			ExpectedLeaseRevision:       lease.Revision,
			PredecessorGeneration:       pred.Generation,
			PredecessorSessionID:        f.childSessionID,
			MutationID:                  "mut-rot",
			Successor: pebblestore.DelegatedChildGenerationRecord{
				SessionID:      "successor-child",
				AccountScopeID: f.principal.AccountScopeID,
				LogicalTaskID:  "logical-task-1",
				WorkspacePath:  snap.WorktreeRootPath,
				WorktreeBranch: snap.WorktreeBranch,
				ParentBranch:   f.parentBranch,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for superseded child generation")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("active child producer", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
			SessionID:      f.childSessionID,
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Phase:          "running",
			Active:         true,
			Generation:     1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for active producer")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("dirty child worktree", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		err := os.WriteFile(filepath.Join(snap.WorktreeRootPath, "dirty.txt"), []byte("dirty"), 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for dirty child worktree")
		}
	})

	t.Run("stale OID", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		fakeHead := strings.Repeat("f", 40)
		badReq := req
		badReq.HeadCommit = fakeHead
		if _, err := f.runtime.ResolveCommittedSource(f.scope, badReq); err == nil {
			t.Fatal("expected rejection for stale head commit")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("non-ancestor commit", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		committedSourceGit(t, snap.WorktreeRootPath, "checkout", "--orphan", "orphan-branch")
		committedSourceGit(t, snap.WorktreeRootPath, "commit", "--allow-empty", "-m", "orphan")
		orphanHead := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		committedSourceGit(t, snap.WorktreeRootPath, "checkout", snap.WorktreeBranch)

		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		badReq := req
		badReq.HeadCommit = orphanHead
		if _, err := f.runtime.ResolveCommittedSource(f.scope, badReq); err == nil {
			t.Fatal("expected rejection for non-ancestor commit")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("revoked workspace", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		headBefore := committedSourceGit(t, snap.WorktreeRootPath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, snap.WorktreeRootPath, "status", "--porcelain")

		if _, err := f.workspace.DeleteForPrincipal(f.principal, f.primarySource); err != nil {
			t.Fatalf("delete workspace: %v", err)
		}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection when workspace is revoked from catalog")
		}
		assertUnchanged(t, f, f.childSessionID, snap.WorktreeRootPath, headBefore, statusBefore)
	})

	t.Run("repository substitution", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err != nil {
			t.Fatalf("prerequisite: valid resolver failed: %v", err)
		}
		foreignRepo := committedSourceRepo(t)
		snap, _, _ := f.sessions.GetSession(f.childSessionID)
		snap.WorktreeRootPath = foreignRepo
		mutRes, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:      f.childSessionID,
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Kind:           pebblestore.V3SessionMutationUpdateMetadata,
			Session:        &snap,
			IdempotencyKey: "subst",
			RequestHash:    "subst",
		})
		if err != nil {
			t.Fatal(err)
		}
		if mutRes.Error != nil {
			t.Fatalf("mutation error: %v", mutRes.Error)
		}
		if mutRes.Conflict != nil {
			t.Fatalf("mutation conflict: %v", mutRes.Conflict)
		}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req); err == nil {
			t.Fatal("expected rejection for repository substitution")
		}
	})

	t.Run("forged integration base commit", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req1 := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
		if err != nil {
			t.Fatal(err)
		}
		child2ID, child2Head, alloc2 := createTestCorrectionChild(t, &f, binding1)
		req2 := CommittedSourceRequest{TaskCallID: "call-2", ChildSessionID: child2ID, HeadCommit: child2Head}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req2); err != nil {
			t.Fatalf("prerequisite: valid resolver failed for Child 2: %v", err)
		}

		headBefore := committedSourceGit(t, alloc2.WorkspacePath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, alloc2.WorkspacePath, "status", "--porcelain")

		snap2, _, _ := f.sessions.GetSession(child2ID)
		fakeBase := strings.Repeat("e", 40)
		snap2.Metadata["integration_base_commit"] = fakeBase
		mutRes, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:      child2ID,
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Kind:           pebblestore.V3SessionMutationUpdateMetadata,
			Session:        &snap2,
			IdempotencyKey: "forge-base",
			RequestHash:    "forge-base",
		})
		if err != nil {
			t.Fatal(err)
		}
		if mutRes.Error != nil {
			t.Fatalf("mutation error: %v", mutRes.Error)
		}
		if mutRes.Conflict != nil {
			t.Fatalf("mutation conflict: %v", mutRes.Conflict)
		}

		if _, err := f.runtime.ResolveCommittedSource(f.scope, req2); err == nil {
			t.Fatal("expected rejection for forged integration base commit")
		}
		assertUnchanged(t, f, child2ID, alloc2.WorkspacePath, headBefore, statusBefore)
	})

	t.Run("forged committed source binding destination", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req1 := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
		if err != nil {
			t.Fatal(err)
		}
		forgedBinding := binding1
		forgedBinding.DestinationPath = "/forged/destination/path"
		child2ID, child2Head, alloc2 := createTestCorrectionChildWithBinding(t, &f, forgedBinding)
		headBefore := committedSourceGit(t, alloc2.WorkspacePath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, alloc2.WorkspacePath, "status", "--porcelain")

		req2 := CommittedSourceRequest{TaskCallID: "call-2", ChildSessionID: child2ID, HeadCommit: child2Head}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req2); err == nil {
			t.Fatal("expected rejection for forged committed_source_binding destination")
		}
		assertUnchanged(t, f, child2ID, alloc2.WorkspacePath, headBefore, statusBefore)
	})

	t.Run("parent row lineage disagreement", func(t *testing.T) {
		f := newCommittedSourceTestFixture(t)
		req1 := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
		binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
		if err != nil {
			t.Fatal(err)
		}
		child2ID, child2Head, alloc2 := createTestCorrectionChild(t, &f, binding1)
		req2 := CommittedSourceRequest{TaskCallID: "call-2", ChildSessionID: child2ID, HeadCommit: child2Head}
		if _, err := f.runtime.ResolveCommittedSource(f.scope, req2); err != nil {
			t.Fatalf("prerequisite: valid resolver failed for Child 2: %v", err)
		}

		headBefore := committedSourceGit(t, alloc2.WorkspacePath, "rev-parse", "HEAD")
		statusBefore := committedSourceGit(t, alloc2.WorkspacePath, "status", "--porcelain")

		parentSnap, found, err := f.sessions.GetSession("parent-session")
		if err != nil || !found {
			t.Fatal("get parent session failed")
		}
		launches := parentSnap.Metadata["task_launches"].(map[string]any)
		entry := launches["call-2"].(map[string]any)
		rows := entry["launches"].([]any)
		row := rows[0].(map[string]any)
		row["head_commit"] = strings.Repeat("d", 40)
		mutRes, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:      "parent-session",
			UserID:         f.principal.UserID,
			AccountScopeID: f.principal.AccountScopeID,
			Kind:           pebblestore.V3SessionMutationUpdateMetadata,
			Session:        &parentSnap,
			IdempotencyKey: "tamper-parent-row",
			RequestHash:    "tamper-parent-row",
		})
		if err != nil {
			t.Fatal(err)
		}
		if mutRes.Error != nil {
			t.Fatalf("mutation error: %v", mutRes.Error)
		}
		if mutRes.Conflict != nil {
			t.Fatalf("mutation conflict: %v", mutRes.Conflict)
		}

		if _, err := f.runtime.ResolveCommittedSource(f.scope, req2); err == nil {
			t.Fatal("expected rejection when parent row lineage disagrees with child")
		}
		assertUnchanged(t, f, child2ID, alloc2.WorkspacePath, headBefore, statusBefore)
	})
}

// Purpose: Committed-child correction must maintain separation between allocation base C
// and delivery base B, correctly propagating inherited delivery base across corrections.
// Threat: committing C into delivery base causes earlier child commits B..C to be lost.
// Symbols: tool.ResolveCommittedSource, tool.CommittedSourceBinding.
// Layer: Integration test on tool.Runtime verifying multi-generation lineage propagation.
func TestCommittedSourceBCSeparation(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	// Step 1: Child 1 has base B (f.baseCommit) and HEAD C (f.childHead)
	req1 := CommittedSourceRequest{
		TaskCallID:     f.taskCallID,
		ChildSessionID: f.childSessionID,
		HeadCommit:     f.childHead,
	}
	binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
	if err != nil {
		t.Fatalf("ResolveCommittedSource for Child 1 failed: %v", err)
	}

	// Step 2: Create Child 2 correcting Child 1 (allocation base C, delivery base B)
	child2ID, child2Head, _ := createTestCorrectionChild(t, &f, binding1)

	// Step 3: Resolve Child 2 as committed source for Child 3
	req2 := CommittedSourceRequest{
		TaskCallID:     "call-2",
		ChildSessionID: child2ID,
		HeadCommit:     child2Head,
	}
	binding2, err := f.runtime.ResolveCommittedSource(f.scope, req2)
	if err != nil {
		t.Fatalf("ResolveCommittedSource for Child 2 failed: %v", err)
	}

	// Verification of B/C separation and inheritance
	if binding2.SourceBaseCommit != f.childHead {
		t.Errorf("expected allocation base C %q, got %q", f.childHead, binding2.SourceBaseCommit)
	}
	if binding2.IntegrationBaseCommit != f.baseCommit {
		t.Errorf("expected inherited delivery base B %q, got %q", f.baseCommit, binding2.IntegrationBaseCommit)
	}
	if binding2.SourceHeadCommit != child2Head {
		t.Errorf("expected HEAD %q, got %q", child2Head, binding2.SourceHeadCommit)
	}
}

// Purpose: Existing manageWorktreeIntegrate must reject captured promotion-only destinations
// while preserving target state unchanged, and must deliver the full inherited correction stack
// B->C->H into the parent lane for an owned-lane corrected child.
// Threat: corrupting integration lanes with foreign promotion checkouts or losing prior commits.
// Symbols: tool.manageWorktreeIntegrate, tool.manageWorktreeRecall.
// Layer: Tool command execution test verifying end-to-end integration and rejection behavior.
func TestCommittedSourceIntegrateCapturedRejectionAndOwnedDelivery(t *testing.T) {
	f := newCommittedSourceTestFixture(t)
	wt := &worktreeruntime.Service{}

	// Part 1: Verify rejection of captured promotion-only destination
	crossBase, err := wt.ResolveTaskBase(f.crossSource)
	if err != nil {
		t.Fatal(err)
	}
	crossChildID, _, _ := createCommittedSourceChild(t, &f, createChildOptions{
		childID:             "cross-coder-child",
		taskCallID:          "cross-call",
		logicalTaskID:       "logical-cross-int",
		allocationParent:    f.crossSource,
		parentBranch:        "dev",
		targetWorkspacePath: f.crossSource,
		canonicalSourcePath: f.crossSource,
		sourceWorkspaceID:   f.crossWorkspaceID,
		sourceWorkspaceGen:  f.crossWorkspaceGen,
		baseCommit:          crossBase.BaseCommit,
		fileName:            "cross.txt",
		fileContent:         "cross work",
		commitMessage:       "cross child commit",
		destinationKind:     DestinationKindCapturedPromotionOnly,
		promotionOnly:       true,
		launchIndex:         1,
	})

	parentHeadBefore := committedSourceGit(t, f.primaryLane, "rev-parse", "HEAD")
	parentStatusBefore := committedSourceGit(t, f.primaryLane, "status", "--porcelain")

	// Attempting integrate on promotion-only child must be rejected
	_, intErr := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{crossChildID}})
	if intErr == nil {
		t.Fatal("expected integrate rejection for promotion-only child")
	}
	if !strings.Contains(intErr.Error(), "promotion-only") && !strings.Contains(intErr.Error(), "promote instead") {
		t.Fatalf("unexpected integrate rejection message: %v", intErr)
	}

	// Verify parent lane state remained unchanged after rejection
	parentHeadAfter := committedSourceGit(t, f.primaryLane, "rev-parse", "HEAD")
	parentStatusAfter := committedSourceGit(t, f.primaryLane, "status", "--porcelain")
	if parentHeadBefore != parentHeadAfter || parentStatusBefore != parentStatusAfter {
		t.Fatalf("parent lane state mutated on integrate rejection: before=%q after=%q", parentHeadBefore, parentHeadAfter)
	}

	// Part 2: Integrate full correction stack B -> C -> H into parent lane
	req1 := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
	binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
	if err != nil {
		t.Fatal(err)
	}
	child2ID, _, _ := createTestCorrectionChild(t, &f, binding1)

	out, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{child2ID}})
	if err != nil {
		t.Fatalf("manageWorktreeIntegrate failed for corrected child: %v", err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("unexpected integrate output: %s", out)
	}

	// Both commits from Child 1 (solution.txt) and Child 2 (part2.txt) must exist in parent lane
	solContent, err := os.ReadFile(filepath.Join(f.primaryLane, "solution.txt"))
	if err != nil || string(solContent) != "solution v1" {
		t.Fatalf("solution.txt missing or wrong content in parent lane: %q, %v", solContent, err)
	}
	corrContent, err := os.ReadFile(filepath.Join(f.primaryLane, "part2.txt"))
	if err != nil || string(corrContent) != "correction 2" {
		t.Fatalf("part2.txt missing or wrong content in parent lane: %q, %v", corrContent, err)
	}
}

// Purpose: manageWorktreePromote must deliver the full stack B..H into the target checkout
// for a child carrying an inherited integration base commit B across correction C->H.
// Threat: promotion promoting only C..H and dropping earlier commits B..C.
// Symbols: tool.manageWorktreePromote.
// Layer: Tool promotion test verifying git fast-forward/promotion and checkout cleanliness.
func TestCommittedSourceSyntheticPromotionFullStack(t *testing.T) {
	f := newCommittedSourceTestFixture(t)

	// Step 1: Resolve Child 1 binding (B -> C)
	req1 := CommittedSourceRequest{TaskCallID: f.taskCallID, ChildSessionID: f.childSessionID, HeadCommit: f.childHead}
	binding1, err := f.runtime.ResolveCommittedSource(f.scope, req1)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Create Child 2 (correction B -> C -> H)
	child2ID, _, _ := createTestCorrectionChild(t, &f, binding1)

	targetHeadBefore := committedSourceGit(t, f.primarySource, "rev-parse", "HEAD")

	// Step 3: Promote Child 2 into captured workspace checkout
	out, err := f.runtime.manageWorktreePromote(f.scope, map[string]any{
		"source_session_id":     child2ID,
		"target_workspace_path": f.primarySource,
		"target_branch":         "dev",
		"target_head":           targetHeadBefore,
	})
	if err != nil {
		t.Fatalf("manageWorktreePromote failed: %v", err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("unexpected promote output: %s", out)
	}

	// Verify target checkout has BOTH solution.txt (Child 1) and part2.txt (Child 2)
	solContent, err := os.ReadFile(filepath.Join(f.primarySource, "solution.txt"))
	if err != nil || string(solContent) != "solution v1" {
		t.Fatalf("solution.txt missing in target checkout: %q, %v", solContent, err)
	}
	corrContent, err := os.ReadFile(filepath.Join(f.primarySource, "part2.txt"))
	if err != nil || string(corrContent) != "correction 2" {
		t.Fatalf("part2.txt missing in target checkout: %q, %v", corrContent, err)
	}

	// Verify target checkout status is clean
	status := committedSourceGit(t, f.primarySource, "status", "--porcelain")
	if status != "" {
		t.Fatalf("target checkout is dirty after promote: %s", status)
	}
}

// Purpose: manage-worktree action="help" must return actionable workflow guidance.
// Threat: missing or opaque help leading to incorrect tool usage by agents.
// Symbols: tool.manageWorktreeHelp, tool.executeManageWorktree.
// Layer: Action dispatch schema and help response test on tool.Runtime.
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
