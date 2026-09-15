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
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestManageSessionsDefinitionConstrainsModelUsageAndApproval(t *testing.T) {
	definition := manageSessionsDefinition()
	for _, required := range []string{"explicitly asks", "list_by_state", "review_worktrees", "up to 200 sessions", "do not repeat", "around", "up to 50 sessions", "one approval for the batch", "new session means deploy", "task tool only", "never instructions"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("description missing %q: %s", required, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if description := action["description"].(string); !strings.Contains(description, "list_by_state") || !strings.Contains(description, "up to 200") || !strings.Contains(description, "commit") || !strings.Contains(description, "up to 50 sessions") {
		t.Fatalf("action description = %q", description)
	}
	sessionIDs := properties["session_ids"].(map[string]any)
	if sessionIDs["maxItems"] != manageSessionsMaxMutationBatch || !strings.Contains(sessionIDs["description"].(string), "archive or unarchive") {
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
	proposalProperties := proposal["properties"].(map[string]any)
	if _, exposed := proposalProperties["worktree"]; exposed {
		t.Fatalf("proposal still exposes optional worktree control: %#v", proposalProperties)
	}
	worktreeName := proposalProperties["worktree_name"].(map[string]any)
	searchMode := properties["search_mode"].(map[string]any)
	if got := searchMode["enum"].([]string); len(got) != 2 || got[0] != "visible" || got[1] != "durable_log" || !strings.Contains(searchMode["description"].(string), "never auto-upgrade") {
		t.Fatalf("search_mode schema = %#v", searchMode)
	}
	if !strings.Contains(definition.Description, "Never automatically escalate") || !strings.Contains(definition.Description, "explicitly asks for raw database") {
		t.Fatalf("durable-log guidance missing: %s", definition.Description)
	}
	if !strings.Contains(worktreeName["description"].(string), "Swarm-authored") || !strings.Contains(worktreeName["description"].(string), "server") {
		t.Fatalf("proposal worktree schema = %#v", proposalProperties)
	}
	expectedByID := properties["expected_updated_at_by_id"].(map[string]any)
	if expectedByID["maxProperties"] != manageSessionsMaxMutationBatch || !strings.Contains(expectedByID["description"].(string), "bulk archive or unarchive") {
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

func TestManageSessionsArchiveAndUnarchiveRejectOnlyAboveFifty(t *testing.T) {
	ids := make([]any, 0, manageSessionsMaxMutationBatch+1)
	for i := 0; i <= manageSessionsMaxMutationBatch; i++ {
		ids = append(ids, fmt.Sprintf("session-%d", i))
	}
	runtime := &Runtime{sessions: &gitManageSessionService{}}
	for _, action := range []string{"archive", "unarchive"} {
		_, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{}, map[string]any{"action": action, "session_ids": ids})
		if err == nil || !strings.Contains(err.Error(), "at most 50 sessions") {
			t.Fatalf("%s error = %v", action, err)
		}
	}
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
	sessions    map[string]pebblestore.SessionSnapshot
	tombstones  map[string]pebblestore.V3SessionTombstone
	plans       map[string]pebblestore.SessionPlanSnapshot
	runStates   map[string]pebblestore.V3SessionRunState
	usages      map[string]pebblestore.SessionUsageSummary
	permissions map[string][]pebblestore.PermissionRecord
	messages    map[string][]pebblestore.MessageSnapshot
	searchItems []pebblestore.V3SessionSearchItem
	events      []pebblestore.V3SessionEvent
	searchCalls int
}

func (s *gitManageSessionService) GetSessionTombstone(id string) (pebblestore.V3SessionTombstone, bool, error) {
	if s.tombstones == nil {
		return pebblestore.V3SessionTombstone{}, false, nil
	}
	t, ok := s.tombstones[id]
	return t, ok, nil
}

func (s *gitManageSessionService) GetSessionRunState(id string) (pebblestore.V3SessionRunState, bool, error) {
	if s.runStates == nil {
		return pebblestore.V3SessionRunState{}, false, nil
	}
	st, ok := s.runStates[id]
	return st, ok, nil
}

func (s *gitManageSessionService) GetUsageSummary(id string) (pebblestore.SessionUsageSummary, bool, error) {
	if s.usages == nil {
		return pebblestore.SessionUsageSummary{}, false, nil
	}
	u, ok := s.usages[id]
	return u, ok, nil
}

func (s *gitManageSessionService) ListPermissions(id string, limit int) ([]pebblestore.PermissionRecord, error) {
	if s.permissions == nil {
		return nil, nil
	}
	p := s.permissions[id]
	if len(p) > limit {
		p = p[:limit]
	}
	return p, nil
}

func (s *gitManageSessionService) ListSessionMessages(id string, afterSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s.messages == nil {
		return nil, nil
	}
	var out []pebblestore.MessageSnapshot
	for _, m := range s.messages[id] {
		if m.GlobalSeq > afterSeq {
			out = append(out, m)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *gitManageSessionService) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	if s.sessions == nil {
		s.sessions = make(map[string]pebblestore.SessionSnapshot)
	}
	if s.messages == nil {
		s.messages = make(map[string][]pebblestore.MessageSnapshot)
	}
	if s.runStates == nil {
		s.runStates = make(map[string]pebblestore.V3SessionRunState)
	}
	switch input.Kind {
	case pebblestore.V3SessionMutationCreateSession:
		if input.Session != nil {
			s.sessions[input.SessionID] = *input.Session
		}
		return pebblestore.V3SessionMutationResult{SessionID: input.SessionID, Session: input.Session}, nil
	case pebblestore.V3SessionMutationAppendMessage:
		if input.Message != nil {
			s.messages[input.SessionID] = append(s.messages[input.SessionID], *input.Message)
		}
		if input.RunIntent != nil {
			s.runStates[input.SessionID] = pebblestore.V3SessionRunState{
				Active: input.RunIntent.Status == pebblestore.V3RunIntentRunning || input.RunIntent.Status == pebblestore.V3RunIntentPendingExecutor,
				RunID:  input.RunIntent.RunID,
				Status: input.RunIntent.Status,
			}
		}
		return pebblestore.V3SessionMutationResult{SessionID: input.SessionID, Message: input.Message, RunIntent: input.RunIntent}, nil
	case pebblestore.V3SessionMutationRecordRunIntent:
		if input.RunIntent != nil {
			s.runStates[input.SessionID] = pebblestore.V3SessionRunState{
				Active: input.RunIntent.Status == pebblestore.V3RunIntentRunning || input.RunIntent.Status == pebblestore.V3RunIntentPendingExecutor,
				RunID:  input.RunIntent.RunID,
				Status: input.RunIntent.Status,
			}
		}
		return pebblestore.V3SessionMutationResult{SessionID: input.SessionID, RunIntent: input.RunIntent}, nil
	}
	return pebblestore.V3SessionMutationResult{SessionID: input.SessionID}, nil
}

type mockSessionController struct {
	cancelCalls   []string
	enqueueCalls  []string
	compactCalls  []string
	cancelResult  bool
	compactResult map[string]any
}

func (m *mockSessionController) CancelSessionRun(principal identity.Principal, sessionID, runID, reason string) (bool, error) {
	m.cancelCalls = append(m.cancelCalls, sessionID+":"+runID+":"+reason)
	return m.cancelResult, nil
}

func (m *mockSessionController) EnqueueSessionRun(principal identity.Principal, sessionID, runID, parentSessionID string) bool {
	m.enqueueCalls = append(m.enqueueCalls, sessionID+":"+runID)
	return true
}

func (m *mockSessionController) CompactSession(ctx context.Context, principal identity.Principal, sessionID, note string) (map[string]any, error) {
	m.compactCalls = append(m.compactCalls, sessionID+":"+note)
	if m.compactResult != nil {
		return m.compactResult, nil
	}
	return map[string]any{"compacted": true, "session_id": sessionID}, nil
}

func (s *gitManageSessionService) ListSessionMessagesBefore(id string, beforeSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s.messages == nil {
		return nil, nil
	}
	var out []pebblestore.MessageSnapshot
	msgs := s.messages[id]
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if beforeSeq == 0 || m.GlobalSeq < beforeSeq {
			out = append(out, m)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *gitManageSessionService) ListSessionMessageTail(id string, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s.messages == nil {
		return nil, nil
	}
	msgs := s.messages[id]
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

func (s *gitManageSessionService) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	session, ok := s.sessions[id]
	return session, ok, nil
}

func (s *gitManageSessionService) GetActivePlan(id string) (pebblestore.SessionPlanSnapshot, bool, error) {
	plan, ok := s.plans[id]
	return plan, ok, nil
}

func (s *gitManageSessionService) SearchSessions(options pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error) {
	s.searchCalls++
	return pebblestore.V3SessionSearchResult{Items: append([]pebblestore.V3SessionSearchItem(nil), s.searchItems...)}, nil
}

func (s *gitManageSessionService) ListSessionEventsBefore(id string, beforeSeq uint64, limit int) ([]pebblestore.V3SessionEvent, error) {
	out := make([]pebblestore.V3SessionEvent, 0, limit)
	for _, event := range s.events {
		if beforeSeq == 0 || event.Seq < beforeSeq {
			out = append(out, event)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func TestManageSessionsGetIncludesDurableVideoStudioContext(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{
		"video-session": {
			ID: "video-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
			WorkspacePath: "/work/video", WorkspaceName: "video", Title: "Video continuation",
			Metadata: map[string]any{
				"creative_mode": "video", "experience": "video_studio",
				"video_project_id": "destination-project", "video_revision_id": "destination-revision",
				"source_session_id": "source-session", "source_video_project_id": "source-project", "source_video_revision_id": "source-revision",
			},
		},
	}}
	output, err := (&Runtime{sessions: service}).executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{"action": "get", "session_id": "video-session"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, want := range []string{`"video_context"`, `"attached":true`, `"durable":true`, `"destination_project_id":"destination-project"`, `"destination_revision_id":"destination-revision"`, `"source_session_id":"source-session"`, `"source_project_id":"source-project"`, `"source_revision_id":"source-revision"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("get output missing %s: %s", want, output)
		}
	}
}

func TestManageSessionsGetOmitsIncompleteVideoContext(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{
		"video-session": {
			ID: "video-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
			Metadata: map[string]any{"creative_mode": "video", "video_project_id": "project-without-revision"},
		},
	}}
	output, err := (&Runtime{sessions: service}).executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{"action": "get", "session_id": "video-session"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(output, `"video_context"`) {
		t.Fatalf("incomplete context must fail closed: %s", output)
	}
}

func TestManageSessionsSearchDefaultsToVisibleAuthority(t *testing.T) {
	service := &gitManageSessionService{searchItems: []pebblestore.V3SessionSearchItem{{ID: "visible-1", Title: "Visible"}}}
	output, err := (&Runtime{sessions: service}).executeManageSessions(context.Background(), WorkspaceScope{}, map[string]any{"action": "search", "query": "visible"})
	if err != nil || service.searchCalls != 1 || !strings.Contains(output, `"search_mode":"visible"`) || strings.Contains(output, "durable_v3_session_events") {
		t.Fatalf("visible search output=%s calls=%d err=%v", output, service.searchCalls, err)
	}
}

func TestManageSessionsDurableLogSearchChecksOwnershipMatchesAndPaginates(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{"owned": {ID: "owned", Title: "Owned", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}},
		events: []pebblestore.V3SessionEvent{
			{ID: "e3", SessionID: "owned", Seq: 3, EventType: "session.diagnostic.recorded", Payload: json.RawMessage(`{"message":"needle newest"}`)},
			{ID: "e2", SessionID: "owned", Seq: 2, EventType: "session.message.appended", Payload: json.RawMessage(`{"content":"other"}`)},
			{ID: "e1", SessionID: "owned", Seq: 1, EventType: "session.message.appended", Payload: json.RawMessage(`{"content":"needle older"}`)},
		},
	}
	runtime := &Runtime{sessions: service}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{"action": "search", "search_mode": "durable_log", "session_id": "owned", "query": "needle", "limit": 1})
	if err != nil || service.searchCalls != 0 || !strings.Contains(output, `"source":"durable_v3_session_events"`) || !strings.Contains(output, `"seq":3`) || strings.Contains(output, `"seq":1`) || !strings.Contains(output, `"next_before_seq":2`) || !strings.Contains(output, `"result_truncated":true`) {
		t.Fatalf("durable search output=%s calls=%d err=%v", output, service.searchCalls, err)
	}
	_, err = runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: identity.Principal{AccountScopeID: "other", UserID: "other"}}, map[string]any{"action": "search", "search_mode": "durable_log", "session_id": "owned", "query": "needle"})
	if err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("ownership error = %v", err)
	}
}

func TestManageSessionsReviewWorktreesClassifiesIntegratedMissingAndDirtyWork(t *testing.T) {
	repo := t.TempDir()
	runManageSessionsGitCommand(t, repo, "init")
	runManageSessionsGitCommand(t, repo, "config", "user.name", "Test User")
	runManageSessionsGitCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, repo, "add", "base.txt")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runManageSessionsGitOutput(t, repo, "rev-parse", "HEAD"))

	integratedWorktree := filepath.Join(t.TempDir(), "integrated")
	runManageSessionsGitCommand(t, repo, "worktree", "add", "-b", "agent/integrated", integratedWorktree, base)
	if err := os.WriteFile(filepath.Join(integratedWorktree, "integrated.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, integratedWorktree, "add", "integrated.txt")
	runManageSessionsGitCommand(t, integratedWorktree, "commit", "-m", "integrated change")
	integratedCommit := strings.TrimSpace(runManageSessionsGitOutput(t, integratedWorktree, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "master.txt"), []byte("master\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManageSessionsGitCommand(t, repo, "add", "master.txt")
	runManageSessionsGitCommand(t, repo, "commit", "-m", "master progress")
	runManageSessionsGitCommand(t, repo, "cherry-pick", integratedCommit)

	missingWorktree := filepath.Join(t.TempDir(), "missing")
	runManageSessionsGitCommand(t, repo, "worktree", "add", "-b", "agent/missing", missingWorktree, base)
	for i, name := range []string{"missing-one.txt", "missing-two.txt"} {
		if err := os.WriteFile(filepath.Join(missingWorktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runManageSessionsGitCommand(t, missingWorktree, "add", name)
		runManageSessionsGitCommand(t, missingWorktree, "commit", "-m", fmt.Sprintf("missing change %d", i+1))
	}

	dirtyWorktree := filepath.Join(t.TempDir(), "dirty")
	runManageSessionsGitCommand(t, repo, "worktree", "add", "-b", "agent/dirty", dirtyWorktree, base)
	if err := os.WriteFile(filepath.Join(dirtyWorktree, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	sessions := map[string]pebblestore.SessionSnapshot{}
	items := make([]pebblestore.V3SessionSearchItem, 0, 3)
	for index, input := range []struct {
		id, title, path, branch string
	}{
		{"integrated", "Integrated", integratedWorktree, "agent/integrated"},
		{"missing", "Missing", missingWorktree, "agent/missing"},
		{"dirty", "Dirty", dirtyWorktree, "agent/dirty"},
	} {
		sessions[input.id] = pebblestore.SessionSnapshot{ID: input.id, Title: input.title, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: input.path, WorktreeEnabled: true, WorktreeRootPath: input.path, WorktreeBranch: input.branch, WorktreeBaseBranch: "master", UpdatedAt: int64(index + 1)}
		items = append(items, pebblestore.V3SessionSearchItem{ID: input.id, Title: input.title, WorkspacePath: input.path, WorktreeEnabled: true, WorktreeBranch: input.branch, UpdatedAt: int64(index + 1), Attention: pebblestore.V3SessionAttentionSummary{State: "needs_review"}})
	}
	runtime := &Runtime{sessions: &gitManageSessionService{sessions: sessions, searchItems: items}}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{PrimaryPath: repo, Roots: []string{repo}, Principal: principal}, map[string]any{"action": "review_worktrees"})
	if err != nil {
		t.Fatalf("review_worktrees: %v", err)
	}
	var response struct {
		ArchiveCandidates []struct {
			SessionID             string `json:"session_id"`
			EquivalentCommitCount int    `json:"equivalent_commit_count"`
		} `json:"archive_candidates"`
		FollowUpCandidates []struct {
			SessionID          string `json:"session_id"`
			Reason             string `json:"reason"`
			MissingCommitCount int    `json:"missing_commit_count"`
			DirtyCount         int    `json:"dirty_count"`
		} `json:"follow_up_candidates"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ArchiveCandidates) != 1 || response.ArchiveCandidates[0].SessionID != "integrated" || response.ArchiveCandidates[0].EquivalentCommitCount != 1 {
		t.Fatalf("archive candidates = %s", output)
	}
	followUps := map[string]struct {
		reason         string
		missing, dirty int
	}{}
	for _, candidate := range response.FollowUpCandidates {
		followUps[candidate.SessionID] = struct {
			reason         string
			missing, dirty int
		}{candidate.Reason, candidate.MissingCommitCount, candidate.DirtyCount}
	}
	if got := followUps["missing"]; got.reason != "commits_missing_from_current_checkout" || got.missing != 2 {
		t.Fatalf("missing candidate = %#v; output=%s", got, output)
	}
	if got := followUps["dirty"]; got.reason != "uncommitted_work" || got.dirty != 1 {
		t.Fatalf("dirty candidate = %#v; output=%s", got, output)
	}
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
		WorktreeBaseBranch: "master", WorktreeBranch: "agent/test", Metadata: map[string]any{"base_commit": strings.TrimSpace(runManageSessionsGitCommandOutput(t, repo, "rev-parse", "HEAD"))},
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
			BaseCommit    string           `json:"base_commit"`
			HeadOID       string           `json:"head_oid"`
			WorktreePath  string           `json:"worktree_path"`
			Recoverable   bool             `json:"recoverable"`
			Files         []map[string]any `json:"files"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Clean || response.Items[0].DirtyCount != 1 || response.Items[0].ModifiedCount != 1 || response.Items[0].BaseCommit == "" || response.Items[0].HeadOID == "" || response.Items[0].WorktreePath != worktree || !response.Items[0].Recoverable || len(response.Items[0].Files) != 1 {
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

func TestPathWithinScopeCanonicalizesSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if pathWithinScope(link, []string{root}, root) {
		t.Fatal("symlink escape was treated as inside workspace scope")
	}
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

func TestManageSessionsGitStatusRejectsSymlinkEscapeFromActiveWorkspace(t *testing.T) {
	activeRoot := t.TempDir()
	outsideRepo := t.TempDir()
	runManageSessionsGitCommand(t, outsideRepo, "init")
	link := filepath.Join(activeRoot, "linked-repo")
	if err := os.Symlink(outsideRepo, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{"session-1": {
		ID: "session-1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: link,
	}}}
	runtime := &Runtime{sessions: service, workspace: &gitManageWorkspaceService{owned: map[string]bool{}}}
	_, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{PrimaryPath: activeRoot, Roots: []string{activeRoot}, Principal: principal}, map[string]any{"action": "git_status", "session_id": "session-1"})
	if err == nil || !strings.Contains(err.Error(), "repository is not account-owned") {
		t.Fatalf("error = %v", err)
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
	_ = runManageSessionsGitCommandOutput(t, dir, args...)
}

func runManageSessionsGitCommandOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
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

func TestManageSessionsGetArchivedSessionSucceeds(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		tombstones: map[string]pebblestore.V3SessionTombstone{
			"archived-1": {
				SessionID: "archived-1",
				Archived:  true,
				UpdatedAt: 5000,
				Session: pebblestore.SessionSnapshot{
					ID:             "archived-1",
					Title:          "Old Archived Work",
					AccountScopeID: principal.AccountScopeID,
					UserID:         principal.UserID,
					WorkspacePath:  "/work/archived",
					WorkspaceName:  "archived",
					UpdatedAt:      4000,
				},
			},
		},
	}
	runtime := &Runtime{sessions: service}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "get",
		"session_id": "archived-1",
	})
	if err != nil {
		t.Fatalf("get archived session failed: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["id"] != "archived-1" || res["archived"] != true || res["state"] != "archived" {
		t.Fatalf("unexpected response: %s", output)
	}
	if res["title"] != "Old Archived Work" {
		t.Fatalf("expected title 'Old Archived Work', got %v", res["title"])
	}
}

func TestManageSessionsGetEnrichesOverwatchDetails(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"session-overwatch": {
				ID:             "session-overwatch",
				Title:          "Build Feature X",
				Mode:           "auto",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/feature",
				WorkspaceName:  "feature",
				CreatedAt:      1000,
				UpdatedAt:      2000,
				MessageCount:   42,
				LastMessageAt:  1900,
			},
		},
		runStates: map[string]pebblestore.V3SessionRunState{
			"session-overwatch": {
				SessionID:    "session-overwatch",
				RunID:        "run-123",
				Active:       true,
				Status:       "running",
				CheckpointID: "cp-2",
				AttemptID:    "cp-2:attempt-1",
				StartedAt:    1500,
			},
		},
		plans: map[string]pebblestore.SessionPlanSnapshot{
			"session-overwatch": {
				ID:        "plan-1",
				SessionID: "session-overwatch",
				Title:     "Feature Plan",
				Status:    "approved",
				Document: &pebblestore.SessionPlanDocument{
					ActiveCheckpointID: "cp-2",
					ExecutionState: &pebblestore.SessionPlanExecutionState{
						Status: "in_progress",
					},
					Checkpoints: []pebblestore.SessionPlanCheckpoint{
						{
							ID:     "cp-1",
							Title:  "Setup",
							Status: "completed",
							Order:  1,
						},
						{
							ID:        "cp-2",
							Title:     "Implementation",
							Status:    "in_progress",
							Order:     2,
							Objective: "Implement feature logic",
							Subtasks: []pebblestore.SessionPlanSubtask{
								{ID: "sub-1", Title: "Subtask 1", Status: "completed"},
								{ID: "sub-2", Title: "Subtask 2", Status: "in_progress"},
							},
							ActiveSubtaskID: "sub-2",
						},
					},
				},
			},
		},
		usages: map[string]pebblestore.SessionUsageSummary{
			"session-overwatch": {
				SessionID:        "session-overwatch",
				TotalTokens:      15000,
				InputTokens:      10000,
				OutputTokens:     5000,
				EstimatedCostUSD: 0.05,
			},
		},
		permissions: map[string][]pebblestore.PermissionRecord{
			"session-overwatch": {
				{
					ID:          "perm-1",
					ToolName:    "bash",
					Requirement: "run_build",
					Status:      "pending",
					CreatedAt:   1600,
				},
			},
		},
		messages: map[string][]pebblestore.MessageSnapshot{
			"session-overwatch": {
				{
					ID:        "msg-last",
					GlobalSeq: 42,
					Role:      "assistant",
					Content:   "Finished step 1, awaiting permission for build.",
					CreatedAt: 1900,
				},
			},
		},
	}
	runtime := &Runtime{sessions: service, orchestration: manageAgentOrchestrationPolicyStub{}}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "get",
		"session_id": "session-overwatch",
	})
	if err != nil {
		t.Fatalf("get overwatch details: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["is_running"] != true {
		t.Fatalf("expected is_running=true, got %v", res["is_running"])
	}
	runState, ok := res["run_state"].(map[string]any)
	if !ok || runState["active"] != true || runState["run_id"] != "run-123" {
		t.Fatalf("expected active run_state, got %v", res["run_state"])
	}
	activePlan, ok := res["active_plan"].(map[string]any)
	if !ok || activePlan["title"] != "Feature Plan" || activePlan["active_checkpoint_id"] != "cp-2" {
		t.Fatalf("expected active_plan with cp-2, got %v", res["active_plan"])
	}
	activeCp, ok := activePlan["active_checkpoint"].(map[string]any)
	if !ok || activeCp["title"] != "Implementation" || activeCp["current_subtask"] != "Subtask 2" {
		t.Fatalf("expected active_checkpoint with current_subtask 'Subtask 2', got %v", activePlan["active_checkpoint"])
	}
	perms, ok := res["pending_permissions"].([]any)
	if !ok || len(perms) != 1 {
		t.Fatalf("expected 1 pending permission, got %v", res["pending_permissions"])
	}
	usage, ok := res["usage"].(map[string]any)
	if !ok || usage["total_tokens"] != float64(15000) {
		t.Fatalf("expected usage summary, got %v", res["usage"])
	}
	lastMsg, ok := res["last_message"].(map[string]any)
	if !ok || lastMsg["role"] != "assistant" || lastMsg["seq"] != float64(42) {
		t.Fatalf("expected last message, got %v", res["last_message"])
	}
}

func TestManageSessionsReadMessagesModeAfterUsesV3Store(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"v3-session": {
				ID:             "v3-session",
				Title:          "V3 Session",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/v3",
			},
		},
		messages: map[string][]pebblestore.MessageSnapshot{
			"v3-session": {
				{ID: "m1", GlobalSeq: 1, Role: "user", Content: "hello"},
				{ID: "m2", GlobalSeq: 2, Role: "assistant", Content: "world"},
				{ID: "m3", GlobalSeq: 3, Role: "user", Content: "continue"},
				{ID: "m4", GlobalSeq: 4, Role: "assistant", Content: "done"},
			},
		},
	}
	runtime := &Runtime{sessions: service}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "read_messages",
		"session_id": "v3-session",
		"mode":       "after",
		"after_seq":  2,
		"limit":      10,
	})
	if err != nil {
		t.Fatalf("read_messages mode=after failed: %v", err)
	}
	var res struct {
		Messages []struct {
			ID  string `json:"id"`
			Seq uint64 `json:"seq"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Messages) != 2 || res.Messages[0].ID != "m3" || res.Messages[1].ID != "m4" {
		t.Fatalf("expected m3 and m4, got %v", res.Messages)
	}
}

func TestManageSessionsSearchSessionScoped(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"target-session": {
				ID:             "target-session",
				Title:          "Debugging Crash",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/debug",
			},
		},
		messages: map[string][]pebblestore.MessageSnapshot{
			"target-session": {
				{ID: "m1", GlobalSeq: 10, Role: "user", Content: "There is a severe memory leak in pebble store"},
				{ID: "m2", GlobalSeq: 20, Role: "assistant", Content: "I checked the code and identified where memory leak happens"},
				{ID: "m3", GlobalSeq: 30, Role: "tool", Content: "test results: 0 failures"},
			},
		},
	}
	runtime := &Runtime{sessions: service}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "search",
		"session_id": "target-session",
		"query":      "memory leak",
	})
	if err != nil {
		t.Fatalf("session-scoped search failed: %v", err)
	}
	var res struct {
		Action     string `json:"action"`
		SearchMode string `json:"search_mode"`
		SessionID  string `json:"session_id"`
		MatchCount int    `json:"match_count"`
		Matches    []struct {
			ID      string `json:"id"`
			Seq     uint64 `json:"seq"`
			Role    string `json:"role"`
			Snippet string `json:"snippet"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.SearchMode != "session" || res.SessionID != "target-session" {
		t.Fatalf("unexpected search response: %s", output)
	}
	if res.MatchCount != 2 || len(res.Matches) != 2 {
		t.Fatalf("expected 2 matches for 'memory leak', got %d", res.MatchCount)
	}
	if res.Matches[0].Seq != 10 && res.Matches[1].Seq != 20 {
		t.Fatalf("unexpected matches: %v", res.Matches)
	}
}

func TestManageSessionsCreateSessionWithPromptAndNavigation(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	workDir := t.TempDir()
	service := &gitManageSessionService{
		sessions: make(map[string]pebblestore.SessionSnapshot),
		messages: make(map[string][]pebblestore.MessageSnapshot),
	}
	controller := &mockSessionController{}
	runtime := &Runtime{sessions: service, sessionController: controller}
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{
		Principal:   principal,
		PrimaryPath: workDir,
		Roots:       []string{workDir},
	}, map[string]any{
		"action":         "create",
		"title":          "Worker Session",
		"workspace_path": workDir,
		"prompt":         "Analyze repository layout",
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["action"] != "create" || res["status"] != "queued" {
		t.Fatalf("unexpected create response: %v", res)
	}
	sessionID, ok := res["session_id"].(string)
	if !ok || sessionID == "" {
		t.Fatalf("missing session_id in create response: %v", res)
	}
	if res["title"] != "Worker Session" || res["workspace_path"] != workDir {
		t.Fatalf("unexpected metadata in create response: %v", res)
	}
	nav, ok := res["navigation"].(map[string]any)
	if !ok || nav["kind"] != "session" || nav["session_id"] != sessionID {
		t.Fatalf("unexpected navigation in create response: %v", res)
	}
	// Verify session was stored
	stored, exists := service.sessions[sessionID]
	if !exists || stored.Title != "Worker Session" {
		t.Fatalf("session was not saved in store: %+v", stored)
	}
	// Verify prompt message was stored
	msgs := service.messages[sessionID]
	if len(msgs) != 1 || msgs[0].Content != "Analyze repository layout" || msgs[0].Role != "user" {
		t.Fatalf("initial message was not stored: %+v", msgs)
	}
	// Verify run was enqueued
	if len(controller.enqueueCalls) != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", len(controller.enqueueCalls))
	}
}

func TestManageSessionsStopActiveRun(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"target": {
				ID:             "target",
				Title:          "Running Session",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/main",
			},
		},
		runStates: map[string]pebblestore.V3SessionRunState{
			"target": {
				Active: true,
				RunID:  "run-target-1",
				Status: "running",
			},
		},
	}
	controller := &mockSessionController{cancelResult: true}
	runtime := &Runtime{sessions: service, sessionController: controller}
	// Stop via action: "stop"
	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "stop",
		"session_id": "target",
		"reason":     "operator pause",
	})
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["status"] != "cancelled" || res["run_id"] != "run-target-1" {
		t.Fatalf("unexpected stop response: %v", res)
	}
	if len(controller.cancelCalls) != 1 || controller.cancelCalls[0] != "target:run-target-1:operator pause" {
		t.Fatalf("unexpected cancel calls: %v", controller.cancelCalls)
	}

	// Test pause alias
	outputPause, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "pause",
		"session_id": "target",
	})
	if err != nil {
		t.Fatalf("pause alias failed: %v", err)
	}
	var resPause map[string]any
	if err := json.Unmarshal([]byte(outputPause), &resPause); err != nil {
		t.Fatalf("decode pause: %v", err)
	}
	if resPause["status"] != "cancelled" {
		t.Fatalf("unexpected pause response: %v", resPause)
	}

	// Test stopping session without active run
	delete(service.runStates, "target")
	outputInactive, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "stop",
		"session_id": "target",
	})
	if err != nil {
		t.Fatalf("stop inactive failed: %v", err)
	}
	var resInactive map[string]any
	if err := json.Unmarshal([]byte(outputInactive), &resInactive); err != nil {
		t.Fatalf("decode inactive: %v", err)
	}
	if resInactive["status"] != "not_running" {
		t.Fatalf("expected status=not_running, got %v", resInactive)
	}
}

func TestManageSessionsSendMessageAndResponseWait(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"worker-session": {
				ID:             "worker-session",
				Title:          "Worker",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/main",
			},
		},
		messages: make(map[string][]pebblestore.MessageSnapshot),
	}
	controller := &mockSessionController{}
	runtime := &Runtime{sessions: service, sessionController: controller}

	// 1. Immediate send_message (wait_seconds: 0)
	output1, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "send_message",
		"session_id": "worker-session",
		"prompt":     "Hello worker",
	})
	if err != nil {
		t.Fatalf("send_message failed: %v", err)
	}
	var res1 map[string]any
	if err := json.Unmarshal([]byte(output1), &res1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res1["status"] != "queued" || res1["action"] != "send_message" {
		t.Fatalf("unexpected queued response: %v", res1)
	}
	if len(service.messages["worker-session"]) != 1 {
		t.Fatalf("message was not stored: %v", service.messages["worker-session"])
	}

	// 2. Reject sending message while session is already running
	service.runStates = map[string]pebblestore.V3SessionRunState{
		"worker-session": {Active: true, RunID: "run-busy", Status: "running"},
	}
	_, errBusy := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "send_message",
		"session_id": "worker-session",
		"prompt":     "another message",
	})
	if errBusy == nil || !strings.Contains(errBusy.Error(), "currently running") {
		t.Fatalf("expected busy error, got %v", errBusy)
	}

	// 3. Send message with wait_seconds > 0 that completes
	delete(service.runStates, "worker-session")
	// simulate background assistant response
	go func() {
		time.Sleep(100 * time.Millisecond)
		service.messages["worker-session"] = append(service.messages["worker-session"], pebblestore.MessageSnapshot{
			ID:        "msg-assistant-1",
			SessionID: "worker-session",
			Role:      "assistant",
			Content:   "I have completed the task!",
			CreatedAt: time.Now().UnixMilli() + 10,
		})
		service.runStates["worker-session"] = pebblestore.V3SessionRunState{
			Active: false,
			Status: "completed",
		}
	}()

	outputWait, errWait := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":       "send_message",
		"session_id":   "worker-session",
		"prompt":       "Please execute tests",
		"wait_seconds": 2,
	})
	if errWait != nil {
		t.Fatalf("send_message with wait failed: %v", errWait)
	}
	var resWait map[string]any
	if err := json.Unmarshal([]byte(outputWait), &resWait); err != nil {
		t.Fatalf("decode wait: %v", err)
	}
	if resWait["status"] != "completed" || resWait["response"] != "I have completed the task!" {
		t.Fatalf("expected completed response with assistant output, got %v", resWait)
	}
}

func TestManageSessionsCompactSession(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	service := &gitManageSessionService{
		sessions: map[string]pebblestore.SessionSnapshot{
			"compact-session": {
				ID:             "compact-session",
				Title:          "Long Conversation",
				AccountScopeID: principal.AccountScopeID,
				UserID:         principal.UserID,
				WorkspacePath:  "/work/main",
			},
		},
	}
	controller := &mockSessionController{
		compactResult: map[string]any{
			"summary":       "Summarized 50 messages",
			"compact_index": 2,
		},
	}
	runtime := &Runtime{sessions: service, sessionController: controller}

	output, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":          "compact",
		"session_id":      "compact-session",
		"compact_handoff": "Retain key facts and decisions",
	})
	if err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["status"] != "completed" || res["action"] != "compact" {
		t.Fatalf("unexpected compact response: %v", res)
	}
	compaction, ok := res["compaction"].(map[string]any)
	if !ok || compaction["summary"] != "Summarized 50 messages" {
		t.Fatalf("unexpected compaction payload: %v", res["compaction"])
	}
	if len(controller.compactCalls) != 1 || controller.compactCalls[0] != "compact-session:Retain key facts and decisions" {
		t.Fatalf("unexpected compact calls: %v", controller.compactCalls)
	}

	// Reject compact on running session
	service.runStates = map[string]pebblestore.V3SessionRunState{
		"compact-session": {Active: true, RunID: "run-active", Status: "running"},
	}
	_, errRunning := runtime.executeManageSessions(context.Background(), WorkspaceScope{Principal: principal}, map[string]any{
		"action":     "compact",
		"session_id": "compact-session",
	})
	if errRunning == nil || !strings.Contains(errRunning.Error(), "currently running") {
		t.Fatalf("expected running error, got %v", errRunning)
	}
}
