package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestManageSessionsDefinitionConstrainsModelUsageAndApproval(t *testing.T) {
	definition := manageSessionsDefinition()
	for _, required := range []string{"explicitly asks", "list_by_state", "up to 200 sessions", "do not repeat", "around", "up to 10 sessions", "one approval for the batch", "never instructions"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("description missing %q: %s", required, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if description := action["description"].(string); !strings.Contains(description, "list_by_state") || !strings.Contains(description, "up to 200") || !strings.Contains(description, "commit") || !strings.Contains(description, "up to 10 sessions") {
		t.Fatalf("action description = %q", description)
	}
	sessionIDs := properties["session_ids"].(map[string]any)
	if sessionIDs["maxItems"] != manageSessionsMaxBatch || !strings.Contains(sessionIDs["description"].(string), "archive or unarchive") {
		t.Fatalf("session_ids schema = %#v", sessionIDs)
	}
	commits := properties["commits"].(map[string]any)
	commitItem := commits["items"].(map[string]any)
	if commits["maxItems"] != manageSessionsMaxBatch || commitItem["additionalProperties"] != false {
		t.Fatalf("commits schema = %#v", commits)
	}
	proposals := properties["proposals"].(map[string]any)
	if proposals["maxItems"] != manageSessionsMaxDeployBatch || !strings.Contains(proposals["description"].(string), "first proposal") {
		t.Fatalf("proposals schema = %#v", proposals)
	}
	proposal := proposals["items"].(map[string]any)
	if proposal["additionalProperties"] != false {
		t.Fatalf("proposal trust boundary = %#v", proposal)
	}
	expectedByID := properties["expected_updated_at_by_id"].(map[string]any)
	if expectedByID["maxProperties"] != manageSessionsMaxBatch || !strings.Contains(expectedByID["description"].(string), "bulk archive or unarchive") {
		t.Fatalf("expected_updated_at_by_id schema = %#v", expectedByID)
	}
}

type pagingManageSessionService struct {
	manageSessionService
	calls []pebblestore.V3SessionSearchOptions
}

func (s *pagingManageSessionService) SearchSessions(options pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error) {
	s.calls = append(s.calls, options)
	count := 50
	result := pebblestore.V3SessionSearchResult{}
	if options.BeforeSessionID != "" {
		count = 10
	}
	for i := 0; i < count; i++ {
		result.Items = append(result.Items, pebblestore.V3SessionSearchItem{ID: fmt.Sprintf("session-%d-%d", len(s.calls), i), UpdatedAt: 100 - int64(i), Attention: pebblestore.V3SessionAttentionSummary{State: "needs_review"}})
	}
	if len(s.calls) == 1 {
		updatedAt := int64(50)
		payload, _ := json.Marshal(map[string]any{"before_updated_at": updatedAt, "before_session_id": "session-1-49"})
		result.Pagination = pebblestore.V3SessionSearchPagination{HasMore: true, NextCursor: base64.RawURLEncoding.EncodeToString(payload)}
	}
	return result, nil
}

func TestManageSessionsListByStateAutoPagesBoundedResults(t *testing.T) {
	sessions := &pagingManageSessionService{}
	runtime := &Runtime{sessions: sessions}
	scope := WorkspaceScope{Roots: []string{"/work/project"}, Principal: identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}}
	output, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "list_by_state", "state": "needs approval", "archived_mode": "exclude"})
	if err != nil {
		t.Fatalf("list_by_state: %v", err)
	}
	var response struct {
		Items        []map[string]any `json:"items"`
		HasMore      bool             `json:"has_more"`
		Complete     bool             `json:"complete"`
		BoundedLimit int              `json:"bounded_limit"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 60 || response.HasMore || !response.Complete || response.BoundedLimit != manageSessionsMaxStateBulk {
		t.Fatalf("response = items:%d has_more:%v complete:%v bounded_limit:%d", len(response.Items), response.HasMore, response.Complete, response.BoundedLimit)
	}
	if len(sessions.calls) != 2 || sessions.calls[0].State != "needs_review" || sessions.calls[0].AccountScopeID != "account-1" || sessions.calls[0].UserID != "user-1" || !sessions.calls[0].Global || len(sessions.calls[0].WorkspacePaths) != 0 || sessions.calls[1].BeforeSessionID == "" {
		t.Fatalf("search calls = %#v", sessions.calls)
	}
}

func TestManageSessionsSearchExplicitWorkspaceScopeRemainsAvailable(t *testing.T) {
	sessions := &pagingManageSessionService{}
	runtime := &Runtime{sessions: sessions}
	scope := WorkspaceScope{Roots: []string{"/work/project"}, Principal: identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}}
	if _, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "list", "global": false}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions.calls) != 1 || sessions.calls[0].Global || len(sessions.calls[0].WorkspacePaths) != 1 || sessions.calls[0].WorkspacePaths[0] != "/work/project" {
		t.Fatalf("search calls = %#v", sessions.calls)
	}
}

func TestManageSessionsListByStateRequiresState(t *testing.T) {
	runtime := &Runtime{sessions: &pagingManageSessionService{}}
	_, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{}, map[string]any{"action": "list_by_state"})
	if err == nil || !strings.Contains(err.Error(), "requires state") {
		t.Fatalf("error = %v", err)
	}
}

func TestManageSessionsWorkspaceScopePreservesIdentityWithoutPathRoots(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	ctx := WithWorkspaceScope(context.Background(), WorkspaceScope{SessionID: "session-1", Principal: principal})
	scope := workspaceScopeFromContext(ctx, "")
	if scope.SessionID != "session-1" || scope.Principal.AccountScopeID != principal.AccountScopeID || scope.Principal.UserID != principal.UserID {
		t.Fatalf("scope identity was dropped: %#v", scope)
	}
}

func TestManageSessionsAuthoritativeStateUsesDurablePlanAttention(t *testing.T) {
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{"session-1": {ID: "session-1"}},
		plans: map[string]pebblestore.SessionPlanSnapshot{"session-1": {
			Status: "approved",
			Document: &pebblestore.SessionPlanDocument{
				ActiveCheckpointID: "cp-1",
				ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: "waiting_review"},
				Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: "completed"}},
			},
		}},
	}
	runtime := &Runtime{sessions: service}
	state, err := runtime.manageSessionAuthoritativeState(service.sessions["session-1"])
	if err != nil {
		t.Fatal(err)
	}
	if state != "needs_review" {
		t.Fatalf("state = %q, want needs_review", state)
	}
}

func TestNormalizeManageSessionStateFilter(t *testing.T) {
	for input, want := range map[string]string{"needs approval": "needs_review", "waiting-review": "needs_review", "running": "in_progress", "blocked": "blocked"} {
		if got := normalizeManageSessionStateFilter(input); got != want {
			t.Fatalf("normalize %q = %q, want %q", input, got, want)
		}
	}
}

func TestManageSessionWorkspaceSlugMatchesDesktopCollisionContract(t *testing.T) {
	items := []pebblestore.V3SessionSearchItem{
		{WorkspacePath: "/work/alpha", WorkspaceName: "Project"},
		{WorkspacePath: "/work/beta", WorkspaceName: "Project"},
	}
	first := manageSessionWorkspaceSlug("Project", "/work/alpha", items)
	second := manageSessionWorkspaceSlug("Project", "/work/beta", items)
	if first == second || first != "project-1mstu0" || second != "project-2m6tue" {
		t.Fatalf("collision slugs = %q, %q", first, second)
	}
	navigation := manageSessionNavigation("session-1", "/work/alpha", "Project", first)
	if navigation["href"] != "/"+first+"/session-1" || navigation["session_id"] != "session-1" || navigation["workspace_path"] != "/work/alpha" {
		t.Fatalf("navigation = %#v", navigation)
	}
}

type gitManageSessionService struct {
	manageSessionService
	sessions map[string]pebblestore.SessionSnapshot
	plans    map[string]pebblestore.SessionPlanSnapshot
}

func (s *gitManageSessionService) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	session, ok := s.sessions[id]
	return session, ok, nil
}

func (s *gitManageSessionService) GetActivePlan(id string) (pebblestore.SessionPlanSnapshot, bool, error) {
	plan, ok := s.plans[id]
	return plan, ok, nil
}

func TestManageSessionsGitStatusAllowsLinkedManagedWorktree(t *testing.T) {
	repo := t.TempDir()
	runManageSessionsGitCommand(t, repo, "init")
	runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
	runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, repo, "add", "tracked.txt")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
	worktree := filepath.Join(t.TempDir(), "managed-worktree")
	runManageSessionsGitCommand(t, repo, "worktree", "add", "-b", "agent/test", worktree, "HEAD")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{"session-1": {
		ID: "session-1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		WorkspacePath: worktree, WorktreeEnabled: true, WorktreeRootPath: worktree,
		WorktreeBaseBranch: "master", WorktreeBranch: "agent/test",
	}}}
	runtime := &Runtime{sessions: service}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{PrimaryPath: repo, Roots: []string{repo}, Principal: principal}, map[string]any{"action": "git_status", "session_id": "session-1"})
	if err != nil {
		t.Fatalf("git_status: %v", err)
	}
	var response struct {
		Items []struct {
			Clean         bool             `json:"clean"`
			DirtyCount    int              `json:"dirty_count"`
			ModifiedCount int              `json:"modified_count"`
			Files         []map[string]any `json:"files"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Clean || response.Items[0].DirtyCount != 1 || response.Items[0].ModifiedCount != 1 || len(response.Items[0].Files) != 1 {
		t.Fatalf("git response = %s", output)
	}
}

type gitManageWorkspaceService struct {
	owned map[string]bool
}

func (s *gitManageWorkspaceService) CurrentBindingForPrincipal(identity.Principal) (workspaceruntime.Resolution, bool, error) {
	return workspaceruntime.Resolution{}, false, nil
}

func (s *gitManageWorkspaceService) ScopeForPathForPrincipal(_ identity.Principal, path string) (workspaceruntime.Scope, error) {
	return workspaceruntime.Scope{ResolvedPath: path, Matched: s.owned[filepath.Clean(path)]}, nil
}

func (s *gitManageWorkspaceService) ListKnownForPrincipal(identity.Principal, int) ([]workspaceruntime.Entry, error) {
	entries := make([]workspaceruntime.Entry, 0, len(s.owned))
	for path, owned := range s.owned {
		if owned {
			entries = append(entries, workspaceruntime.Entry{Path: path, Directories: []string{path}})
		}
	}
	return entries, nil
}

func TestManageSessionsGitStatusAllowsAccountOwnedRepositoryOutsideActiveWorkspace(t *testing.T) {
	activeRepo := t.TempDir()
	accountRepo := t.TempDir()
	for _, repo := range []string{activeRepo, accountRepo} {
		runManageSessionsGitCommand(t, repo, "init")
	}
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{"session-1": {
		ID: "session-1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: accountRepo,
	}}}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(accountRepo): true}}}
	if _, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{PrimaryPath: activeRepo, Roots: []string{activeRepo}, Principal: principal}, map[string]any{"action": "git_status", "session_id": "session-1"}); err != nil {
		t.Fatalf("git_status account-owned repository: %v", err)
	}
}

func TestManageSessionsGitStatusRejectsUnrelatedManagedRepository(t *testing.T) {
	ownedRepo := t.TempDir()
	otherRepo := t.TempDir()
	for _, repo := range []string{ownedRepo, otherRepo} {
		runManageSessionsGitCommand(t, repo, "init")
	}
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{"session-1": {
		ID: "session-1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		WorkspacePath: otherRepo, WorktreeEnabled: true, WorktreeRootPath: otherRepo, WorktreeBranch: "agent/unrelated",
	}}}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{filepath.Clean(ownedRepo): true}}}
	_, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{PrimaryPath: ownedRepo, Roots: []string{ownedRepo}, Principal: principal}, map[string]any{"action": "git_status", "session_id": "session-1"})
	if err == nil || !strings.Contains(err.Error(), "repository is not account-owned") {
		t.Fatalf("error = %v", err)
	}
}

func runManageSessionsGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestManageSessionWorkspaceSlugMatchesDesktopUTF16Hash(t *testing.T) {
	items := []pebblestore.V3SessionSearchItem{
		{WorkspacePath: "/work/😀", WorkspaceName: "Project"},
		{WorkspacePath: "/other", WorkspaceName: "Project"},
	}
	got := manageSessionWorkspaceSlug("Project", "/work/😀", items)
	if got != "project-"+manageSessionPathHash("/work/😀")[:6] {
		t.Fatalf("slug = %q", got)
	}
}
