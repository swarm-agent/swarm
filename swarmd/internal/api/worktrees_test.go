package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type fakeWorktreeService struct {
	config          worktreeruntime.Config
	allocation      worktreeruntime.Allocation
	managed         []worktreeruntime.ManagedWorktree
	managedErr      error
	prune           worktreeruntime.PruneResult
	attachBranch    *string
	allocationErr   error
	configReadCount int
	lastWorkspace   string
	lastNameSeed    string
	lastBaseBranch  string
	lastBranchName  string
	inspectStates   map[string]worktreeruntime.TaskWorkspaceState
}

func TestSessionsV3ReviewSearchItemPreservesWorktreeClassificationFields(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		ID: "session-one", UserID: "user-one", AccountScopeID: "account-one",
		WorkspacePath: "/workspace", WorkspaceName: "workspace", Title: "Review", Mode: "auto",
		WorktreeEnabled: true, WorktreeRootPath: "/worktree", WorktreeBaseBranch: "dev", WorktreeBranch: "agent/review",
		Metadata: map[string]any{"swarm_v3_source_workspace_path": "/workspace"}, CreatedAt: 1, UpdatedAt: 2,
	}

	item := sessionsV3ReviewSearchItem(session)
	if item.ID != session.ID || item.WorktreeRootPath != session.WorktreeRootPath || item.WorktreeBaseBranch != session.WorktreeBaseBranch || item.WorktreeBranch != session.WorktreeBranch {
		t.Fatalf("search item lost review authority: %+v", item)
	}
	if got := item.Metadata["swarm_v3_source_workspace_path"]; got != "/workspace" {
		t.Fatalf("source workspace = %v, want /workspace", got)
	}
}

func TestSearchSessionsV3ReviewWorktreePagesIncludesOlderUnresolvedSessions(t *testing.T) {
	items := make([]pebblestore.V3SessionSearchItem, 57)
	for i := range items {
		items[i] = pebblestore.V3SessionSearchItem{ID: fmt.Sprintf("session-%02d", i)}
	}
	calls := 0
	search := func(options pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error) {
		calls++
		start := 0
		if options.BeforeSessionID != "" {
			if _, err := fmt.Sscanf(options.BeforeSessionID, "session-%02d", &start); err != nil {
				return pebblestore.V3SessionSearchResult{}, err
			}
		}
		end := min(start+options.Limit, len(items))
		page := pebblestore.V3SessionSearchResult{Items: append([]pebblestore.V3SessionSearchItem(nil), items[start:end]...)}
		if end < len(items) {
			updatedAt := int64(end)
			page.Pagination = pebblestore.V3SessionSearchPagination{
				HasMore:             true,
				NextBeforeUpdatedAt: &updatedAt,
				NextBeforeSessionID: fmt.Sprintf("session-%02d", end),
			}
		}
		return page, nil
	}

	got, err := searchSessionsV3ReviewWorktreePages(search, pebblestore.V3SessionSearchOptions{}, sessionsV3ReviewWorktreeLimit)
	if err != nil {
		t.Fatalf("page review worktree sessions: %v", err)
	}
	if calls != 2 {
		t.Fatalf("search calls = %d, want 2", calls)
	}
	if len(got.Items) != len(items) {
		t.Fatalf("items = %d, want %d", len(got.Items), len(items))
	}
	if got.Items[56].ID != "session-56" {
		t.Fatalf("last item = %q, want session-56", got.Items[56].ID)
	}
}

func TestSessionsV3ReviewRepositoryBulkInventoryIncludesSiblingAndExcludesUnrelated(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	sibling := t.TempDir()
	runGitCommitTestCommand(t, repo, "worktree", "add", "-b", "agent/review-inventory", sibling, "HEAD")
	otherRepo := initGitCommitTestRepo(t)
	otherWorktree := t.TempDir()
	runGitCommitTestCommand(t, otherRepo, "worktree", "add", "-b", "agent/other-inventory", otherWorktree, "HEAD")

	checkout, commonDir := sessionsV3ReviewCheckoutTarget(context.Background(), repo)
	repository := newSessionsV3ReviewRepository(context.Background(), checkout, commonDir)
	if !repository.inventoryLoaded {
		t.Fatal("expected bulk worktree inventory")
	}
	if !repository.worktreeMatchesCheckout(sibling) {
		t.Fatal("expected sibling worktree in bulk repository inventory")
	}
	if !repository.worktreeMatchesCheckout(filepath.Join(sibling, "nested")) {
		t.Fatal("expected sibling worktree subdirectory in bulk repository inventory")
	}
	if repository.worktreeMatchesCheckout(otherWorktree) {
		t.Fatal("expected unrelated worktree excluded by bulk repository inventory")
	}
}

func TestSessionsV3ReviewRepositoryPrefetchSnapshotsDeduplicatesWorktrees(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	worktree := t.TempDir()
	runGitCommitTestCommand(t, repo, "worktree", "add", "-b", "agent/review-prefetch", worktree, "HEAD")
	checkout, commonDir := sessionsV3ReviewCheckoutTarget(context.Background(), repo)
	repository := newSessionsV3ReviewRepository(context.Background(), checkout, commonDir)
	sessions := []pebblestore.SessionSnapshot{
		{WorktreeEnabled: true, WorktreeRootPath: worktree},
		{WorktreeEnabled: true, WorktreeRootPath: worktree},
	}

	started := time.Now()
	repository.prefetchSnapshots(context.Background(), sessions)
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshot cache size = %d, want 1", len(repository.snapshots))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("prefetch took %s, want under 2s", elapsed)
	}
}

func TestSessionsV3ReviewSessionMatchesCheckoutIncludesSiblingWorktree(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	worktree := t.TempDir()
	runGitCommitTestCommand(t, repo, "worktree", "add", "-b", "agent/review-sibling", worktree, "HEAD")

	checkout, commonDir := sessionsV3ReviewCheckoutTarget(context.Background(), repo)
	if !checkout.HasGit || commonDir == "" {
		t.Fatal("expected available checkout repository")
	}
	session := pebblestore.SessionSnapshot{
		WorkspacePath:    worktree,
		WorktreeEnabled:  true,
		WorktreeRootPath: worktree,
		WorktreeBranch:   "agent/review-sibling",
	}
	if !sessionsV3ReviewSessionMatchesCheckout(context.Background(), session, checkout, commonDir) {
		t.Fatal("expected sibling managed worktree to remain in the current repository review set")
	}

	otherRepo := initGitCommitTestRepo(t)
	otherWorktree := t.TempDir()
	runGitCommitTestCommand(t, otherRepo, "worktree", "add", "-b", "agent/other-review", otherWorktree, "HEAD")
	session.WorkspacePath = otherWorktree
	session.WorktreeRootPath = otherWorktree
	session.WorktreeBranch = "agent/other-review"
	if sessionsV3ReviewSessionMatchesCheckout(context.Background(), session, checkout, commonDir) {
		t.Fatal("expected an unrelated repository worktree to be excluded")
	}
}

func (f *fakeWorktreeService) GetConfig(workspacePath string) (worktreeruntime.Config, error) {
	f.configReadCount++
	cfg := f.config
	cfg.WorkspacePath = workspacePath
	return cfg, nil
}

func (f *fakeWorktreeService) GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return f.GetConfig(workspacePath)
}

// Keep the overview fixture aligned with the API's exact saved-root read contract.
// Account and path rejection are exercised against the real service in
// worktree.TestSavedWorkspaceConfigExactAccountRead, not this response-only fake.
func (f *fakeWorktreeService) GetConfigForSavedWorkspaceForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return f.GetConfig(workspacePath)
}

func (f *fakeWorktreeService) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	f.config = worktreeruntime.Config{WorkspacePath: workspacePath, Enabled: enabled, UseCurrentBranch: useCurrentBranch, BaseBranch: baseBranch, BranchName: branchName}
	return f.config, nil, nil
}

func (f *fakeWorktreeService) SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return f.SetConfig(workspacePath, enabled, useCurrentBranch, baseBranch, branchName)
}

func (f *fakeWorktreeService) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	f.lastWorkspace = workspacePath
	f.lastNameSeed = nameSeed
	if f.allocationErr != nil {
		return worktreeruntime.Allocation{}, f.allocationErr
	}
	allocation := f.allocation
	if strings.TrimSpace(allocation.RepoRoot) == "" {
		allocation.RepoRoot = workspacePath
	}
	if strings.TrimSpace(allocation.WorkspaceID) == "" {
		allocation.WorkspaceID = worktreeruntime.WorkspaceIdentityForSession(nameSeed)
	}
	if strings.TrimSpace(allocation.BranchName) != "" {
		if branchWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(allocation.BranchName); err == nil {
			allocation.WorkspaceID = branchWorkspaceID
		}
	}
	if strings.TrimSpace(allocation.WorkspacePath) == "" {
		allocation.WorkspacePath = strings.TrimRight(allocation.RepoRoot, "/") + "/.swarm/worktrees/" + allocation.WorkspaceID
	}
	if strings.TrimSpace(allocation.BaseBranch) == "" {
		allocation.BaseBranch = "main"
	}
	return allocation, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return f.AllocateDetachedWorkspace(workspacePath, nameSeed)
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	f.lastBaseBranch = strings.TrimSpace(baseBranch)
	f.lastBranchName = strings.TrimSpace(branchName)
	allocation, err := f.AllocateDetachedWorkspace(workspacePath, nameSeed)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if strings.TrimSpace(baseBranch) != "" {
		allocation.BaseBranch = strings.TrimSpace(baseBranch)
	}
	if strings.TrimSpace(branchName) != "" {
		allocation.BranchName = strings.TrimSpace(branchName)
		if branchWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(allocation.BranchName); err == nil {
			allocation.WorkspaceID = branchWorkspaceID
			allocation.WorkspacePath = strings.TrimRight(allocation.RepoRoot, "/") + "/.swarm/worktrees/" + allocation.WorkspaceID
		}
	}
	return allocation, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return f.AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName)
}

func (f *fakeWorktreeService) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	if f.attachBranch != nil {
		return *f.attachBranch, nil
	}
	return "agent/test", nil
}

func (f *fakeWorktreeService) ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return f.managed, f.managedErr
}

func (f *fakeWorktreeService) ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return f.ListManaged(workspacePath)
}

func (f *fakeWorktreeService) PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error) {
	return f.prune, nil
}

func (f *fakeWorktreeService) PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.PruneResult, error) {
	return f.PruneManaged(workspacePath)
}

func (f *fakeWorktreeService) InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error) {
	if state, ok := f.inspectStates[workspacePath]; ok {
		return state, nil
	}
	branch := strings.TrimSpace(f.lastBranchName)
	if branch == "" {
		branch = strings.TrimSpace(f.allocation.BranchName)
	}
	return worktreeruntime.TaskWorkspaceState{WorkspacePath: workspacePath, BranchName: branch, Clean: true}, nil
}

func testPrincipal() identity.Principal {
	actor := testActorContext()
	principal, err := identity.PrincipalFromActor(actor)
	if err != nil {
		return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	}
	return principal
}

func testActorContext() identity.ActorContext {
	now := time.Unix(1_700_000_000, 0).UTC()
	user := pebblestore.UserRecord{ID: "user-1", Username: "test-user", AccountScopeID: "account-1", CreatedAt: now, UpdatedAt: now}
	account := pebblestore.AccountScopeRecord{ID: "account-1", Type: "personal", CreatedByUserID: "user-1", UserID: "user-1", Role: "owner", CreatedAt: now, UpdatedAt: now}
	accountUser := pebblestore.AccountUserRecord{ID: "acct-user-1", AccountScopeID: "account-1", UserID: "user-1", Status: "active", CreatedAt: now, UpdatedAt: now}
	selection := pebblestore.CurrentSelectionRecord{UserID: "user-1", TeamID: "team-1", CreatedAt: now, UpdatedAt: now}
	team := pebblestore.TeamRecord{ID: "team-1", AccountScopeID: "account-1", Name: "Test Team", Default: true, CreatedAt: now, UpdatedAt: now}
	membership := pebblestore.TeamMembershipRecord{TeamID: "team-1", UserID: "user-1", Role: "owner", CreatedAt: now, UpdatedAt: now}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1", AccountScopeSource: identity.AccountScopeSourceSession, User: user, AccountScope: account, AccountUser: accountUser, TokenExpires: now.Add(time.Hour)}
	return identity.ActorContext{Principal: principal, UserID: "user-1", AccountScopeID: "account-1", TeamID: "team-1", User: user, AccountScope: account, AccountUser: accountUser, Team: team, Membership: membership, Selection: selection, TokenExpires: now.Add(time.Hour)}
}

func withTestPrincipal(req *http.Request) *http.Request {
	actor := testActorContext()
	ctx := context.WithValue(req.Context(), productActorRequestContextKey, actor)
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, actor.Principal)
	ctx = identity.ContextWithPrincipal(ctx, actor.Principal)
	return req.WithContext(ctx)
}

func newTestWorkspaceService(t *testing.T, path string) (*workspaceruntime.Service, string) {
	t.Helper()
	if err := ensureTestWorkspaceDir(path); err != nil {
		t.Fatalf("create committed workspace: %v", err)
	}
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceSvc.AddForPrincipal(testPrincipal(), path, "repo", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return workspaceSvc, path
}

// Requirement: handleWorktrees must propagate inventory failure, never advertise
// failed Git prerequisites as a usable workspace. The handler fixture isolates
// that failure after valid admission and asserts config/selection remain intact.
func TestHandleWorktreesRejectsInventoryFailure(t *testing.T) {
	workspaceSvc, workspacePath := newTestWorkspaceService(t, t.TempDir())
	s := &Server{workspace: workspaceSvc, worktrees: &fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true},
		managedErr: fmt.Errorf("resolve git common dir: git rev-parse --git-common-dir: fatal: not a git repository"),
	}}
	req := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/worktrees?workspace_path="+workspacePath, nil))
	rr := httptest.NewRecorder()

	s.handleWorktrees(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["ok"] == true || got["worktrees"] != nil || !strings.Contains(rr.Body.String(), "not a git repository") {
		t.Fatalf("inventory failure appeared usable: %+v", got)
	}
	if !s.worktrees.(*fakeWorktreeService).config.Enabled {
		t.Fatal("failed inventory changed config")
	}
	current, ok, err := workspaceSvc.CurrentBindingForPrincipal(testPrincipal())
	if err != nil || !ok || current.ResolvedPath != workspacePath {
		t.Fatalf("failed inventory changed selection: %+v %v", current, err)
	}
}

func TestHandleWorktreesIncludesManagedInventory(t *testing.T) {
	workspaceSvc, workspacePath := newTestWorkspaceService(t, t.TempDir())
	s := &Server{workspace: workspaceSvc, worktrees: &fakeWorktreeService{
		config:  worktreeruntime.Config{Enabled: true},
		managed: []worktreeruntime.ManagedWorktree{{Path: "/tmp/swarmd/worktrees/ws_abc123", WorkspaceID: "ws_abc123", Exists: true, Managed: true}},
	}}
	req := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/worktrees?workspace_path="+workspacePath, nil))
	rr := httptest.NewRecorder()

	s.handleWorktrees(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		OK      bool                              `json:"ok"`
		Managed []worktreeruntime.ManagedWorktree `json:"managed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || len(got.Managed) != 1 || got.Managed[0].WorkspaceID != "ws_abc123" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleWorktreesDeletePrunesManagedOnly(t *testing.T) {
	fake := &fakeWorktreeService{prune: worktreeruntime.PruneResult{Root: "/tmp/swarmd/worktrees", Removed: []string{"/tmp/swarmd/worktrees/ws_missing"}}}
	workspaceSvc, workspacePath := newTestWorkspaceService(t, t.TempDir())
	s := &Server{workspace: workspaceSvc, worktrees: fake}
	req := withTestPrincipal(httptest.NewRequest(http.MethodDelete, "/v1/worktrees?workspace_path="+workspacePath, bytes.NewReader(nil)))
	rr := httptest.NewRecorder()

	s.handleWorktrees(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ws_missing") {
		t.Fatalf("delete response missing pruned path: %s", rr.Body.String())
	}
}
