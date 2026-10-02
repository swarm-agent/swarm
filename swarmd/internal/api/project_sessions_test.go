package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func projectConversationRequest(t *testing.T, s *Server, p identity.Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:read", "sessions:write"},
	})
	w := httptest.NewRecorder()
	s.handleProjects(w, r.WithContext(ctx))
	return w
}

// Purpose: project conversation creation/listing must use V3 durability without
// allocating filesystem authority or replacing history. The HTTP/store layer
// proves idempotency and rejected cross-project/account/spoofed requests have no
// partial sessions; a nil workspace/worktree service makes accidental allocation fail.
func TestProjectConversationsCreateListAndRejectSpoofing(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	project := &pebblestore.ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "retained-primary"}
	if err := sessions.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	path := ProjectsPath + "/project/sessions"
	for _, key := range []string{"first", "second", "first"} {
		w := projectConversationRequest(t, s, p, http.MethodPost, path, map[string]any{"client_request_id": key})
		if w.Code != http.StatusOK {
			t.Fatalf("create %s: %d %s", key, w.Code, w.Body.String())
		}
	}
	assertCount := func() {
		t.Helper()
		items, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 100)
		if err != nil || len(items) != 2 {
			t.Fatalf("conversations = %+v, %v", items, err)
		}
		for _, item := range items {
			if err := sessions.Store().ValidateProjectConversation(item, p.AccountScopeID, p.UserID); err != nil {
				t.Fatal(err)
			}
			if item.WorkspacePath != "" || item.WorktreeEnabled || len(item.WorkspaceGrants) != 0 {
				t.Fatalf("filesystem authority leaked: %+v", item)
			}
		}
		got, found, err := sessions.Store().GetProject(p.AccountScopeID, "project")
		if err != nil || !found || got.PrimarySessionID != "retained-primary" {
			t.Fatalf("primary history changed: %+v %v", got, err)
		}
	}
	assertCount()
	for _, body := range []map[string]any{
		{"client_request_id": "first", "title": "different payload"},
		{"client_request_id": "bad-project", "project_id": "other"},
		{"client_request_id": "bad-path", "workspace_path": "."},
		{"client_request_id": "bad-worktree", "worktree_mode": "on"},
		{"client_request_id": "bad-toggle", "managed_worktree_requested": true},
		{"client_request_id": "bad-agent", "agent_name": "swarm"},
		{"client_request_id": "bad-task", "metadata": map[string]any{"task_id": "task"}},
		{"client_request_id": "bad-provenance", "metadata": map[string]any{"swarm_v3_project_id": "project"}},
		{"client_request_id": "bad-membership", "metadata": map[string]any{"project_id": "project"}},
	} {
		w := projectConversationRequest(t, s, p, http.MethodPost, path, body)
		if w.Code < 400 {
			t.Fatalf("accepted forged request %+v: %s", body, w.Body.String())
		}
		assertCount()
	}
	other := p
	other.AccountScopeID = "other-account"
	if w := projectConversationRequest(t, s, other, http.MethodPost, path, map[string]any{"client_request_id": "foreign"}); w.Code != http.StatusNotFound {
		t.Fatalf("cross-account create = %d %s", w.Code, w.Body.String())
	}
	other = p
	other.UserID = "other-user"
	w := projectConversationRequest(t, s, other, http.MethodGet, path, nil)
	var listed struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != http.StatusOK || len(listed.Sessions) != 0 {
		t.Fatalf("cross-user list = %d %s (%v)", w.Code, w.Body.String(), err)
	}
	items, _ := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 100)
	if _, found, err := s.requireSessionV3Access(other, items[0].ID); err != nil || found {
		t.Fatalf("cross-user session access: found=%v err=%v", found, err)
	}
	if w := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/clear-context", nil); w.Code != http.StatusGone {
		t.Fatalf("legacy reset = %d %s", w.Code, w.Body.String())
	}
	assertCount()
}

// Purpose: pending project permissions survive reconnect and stale/cross-session
// replies cannot change them. The API plus real permission store is the narrowest
// layer proving both response handling and durable postconditions, including retry.
func TestProjectConversationPermissionReplayAndStaleRejection(t *testing.T) {
	s, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	w := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "permission-conversation", "mode": "plan"})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	items, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions: %+v %v", items, err)
	}
	session := items[0]
	postSessionsV3PrimaryTestMessage(t, s, session.ID, "message", "Discuss the project")
	active, found, err := sessions.GetSessionActiveRunIntent(session.ID)
	if err != nil || !found {
		t.Fatalf("active run: %+v %v", active, err)
	}
	pending, err := permissions.CreatePending(permission.CreateInput{SessionID: session.ID, RunID: active.RunID, CallID: "ask", ToolName: "ask_user", ToolArguments: `{"question":"Choose","options":["A","B"]}`, Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := permissions.CreatePending(permission.CreateInput{SessionID: session.ID, RunID: "old-run", CallID: "old-ask", ToolName: "ask_user", ToolArguments: `{"question":"Choose","options":["A","B"]}`, Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(id, action, reason string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"action": action, "reason": reason})
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.handleSessionV3PrimaryPermissionResolve(w, r, p, session.ID, id)
		return w
	}
	otherResponse := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "other-permission-conversation"})
	var otherCreated struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(otherResponse.Body.Bytes(), &otherCreated); err != nil || otherResponse.Code != http.StatusOK {
		t.Fatalf("other conversation: %s %v", otherResponse.Body.String(), err)
	}
	foreign, err := permissions.CreatePending(permission.CreateInput{SessionID: otherCreated.SessionID, RunID: active.RunID, CallID: "foreign", ToolName: "ask_user", ToolArguments: `{}`, Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if w := resolve(foreign.ID, "allow", "foreign response"); w.Code < 400 {
		t.Fatal("accepted cross-session permission")
	}
	foreignPending, err := permissions.ListPending(otherCreated.SessionID, 10)
	if err != nil || len(foreignPending) != 1 || foreignPending[0].ID != foreign.ID {
		t.Fatalf("foreign pending changed: %+v %v", foreignPending, err)
	}
	if w := resolve(stale.ID, "allow", "old response"); w.Code < 400 {
		t.Fatalf("accepted stale permission: %s", w.Body.String())
	}
	if w := resolve(pending.ID, "allow_always", "persistent"); w.Code < 400 {
		t.Fatal("accepted persistent project policy")
	}
	for i := 0; i < 2; i++ {
		if w := resolve(pending.ID, "allow", "A custom response outside the options"); w.Code != http.StatusOK {
			t.Fatalf("reply/retry %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	records, err := permissions.ListPending(session.ID, 100)
	if err != nil || len(records) != 1 || records[0].ID != stale.ID {
		t.Fatalf("pending reconnect authority = %+v %v", records, err)
	}
	all, err := permissions.ListPermissions(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range all {
		if record.ID == pending.ID && (record.Status != pebblestore.PermissionStatusApproved || record.Reason != "A custom response outside the options") {
			t.Fatalf("custom response lost: %+v", record)
		}
	}
}

// Purpose: project titles use the account-configured Router and canonical title
// mutation, never allocate a worktree or become a permanent generic label. A
// recording provider at the API boundary proves routing and durable title events
// without a live provider; this is a deterministic contract test, not a benchmark.
func TestProjectConversationRouterTitlePersistsWithoutWorktree(t *testing.T) {
	router := &sessionRouterRecordingRunner{id: "router", response: provideriface.Response{Text: `{"title":"Project discussion","worktree_name":"unused-project-title"}`}}
	s, sessions, p := newRoutedSessionAtomicityServer(t, router, false, false)
	s.worktrees = nil
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	w := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "title"})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	items, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions: %+v %v", items, err)
	}
	session := items[0]
	message := pebblestore.MessageSnapshot{ID: "first-message", SessionID: session.ID, Role: "user", Content: "Discuss the project architecture"}
	if _, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		ClientRequestID: "message", IdempotencyKey: "message", PayloadHash: "message", RequestHash: "message",
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &message,
	}); err != nil {
		t.Fatal(err)
	}
	executor := &sessionV3Executor{server: s}
	job := sessionV3ExecutorJob{SessionID: session.ID, Principal: p, RunID: "title-run"}
	executor.generateAndApplySessionV3Title(job)
	executor.generateAndApplySessionV3Title(job)
	got, found, err := sessions.GetSession(session.ID)
	if err != nil || !found || got.Title != "Project discussion" || got.Metadata["title_source"] != routedSessionTitleSourceRouter {
		t.Fatalf("Router title = %+v %v", got, err)
	}
	if router.createCalls != 1 || router.streamingCalls != 0 || got.WorktreeEnabled || got.WorkspacePath != "" {
		t.Fatalf("unexpected Router/allocation behavior: calls=%d/%d session=%+v", router.createCalls, router.streamingCalls, got)
	}
	events, err := sessions.ListSessionEvents(session.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.EventType == "session.title.updated" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("title events=%d", count)
	}
}
