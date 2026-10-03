package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
// layer proving both response handling and durable postconditions, including retry,
// denial and conflicting replies that must not overwrite a terminal decision.
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
	denied, err := permissions.CreatePending(permission.CreateInput{SessionID: session.ID, RunID: active.RunID, CallID: "approval", ToolName: "task", ToolArguments: `{}`, Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if w := resolve(denied.ID, "deny", "Do not delegate"); w.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", w.Code, w.Body.String())
	}
	// Retried or conflicting responses must never change the durable denial.
	for _, action := range []string{"deny", "allow"} {
		resolve(denied.ID, action, "late response")
		records, err := permissions.ListPermissions(session.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, record := range records {
			if record.ID == denied.ID {
				found = true
				if record.Status != pebblestore.PermissionStatusDenied || record.Reason != "Do not delegate" {
					t.Fatalf("terminal denial overwritten: %+v", record)
				}
			}
		}
		if !found {
			t.Fatal("denial disappeared")
		}
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
	foundAnswer := false
	for _, record := range all {
		if record.ID == pending.ID {
			foundAnswer = true
			if record.Status != pebblestore.PermissionStatusApproved || record.Reason != "A custom response outside the options" {
				t.Fatalf("custom response lost: %+v", record)
			}
		}
	}
	if !foundAnswer {
		t.Fatal("accepted custom response disappeared")
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

// Purpose: the real project route, createSessionsV3Primary and
// acceptSessionsV3Message must preserve server-owned project membership while
// admitting the composer's messages. The old composer sent metadata.project_id,
// which reproduces the release-blocking reserved-key error even after successful
// creation. HTTP handlers backed by Pebble are the narrowest deterministic layer
// proving creation/listing, exact rejection with no partial message/run, and
// admission without that redundant authority field. This is not live UI/provider
// verification.
func TestProjectConversationComposerReservedProjectMetadata(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	projectPath := ProjectsPath + "/project/sessions"
	var ids []string
	for _, key := range []string{"composer-one", "composer-two"} {
		w := projectConversationRequest(t, s, p, http.MethodPost, projectPath, map[string]any{"client_request_id": key})
		var created struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusOK || created.SessionID == "" {
			t.Fatalf("create: %d %s (%v)", w.Code, w.Body.String(), err)
		}
		ids = append(ids, created.SessionID)
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct creation requests reused one conversation")
	}
	post := func(id, key string, metadata map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"client_request_id": key, "role": "user", "content": key, "metadata": metadata})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+id+"/messages", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, withTestPrincipal(r))
		return w
	}
	for _, id := range ids {
		before, found, err := sessions.GetSession(id)
		if err != nil || !found {
			t.Fatalf("created session missing: %v", err)
		}
		for _, projectID := range []string{"project", "foreign-project"} {
			w := post(id, "rejected-"+projectID, map[string]any{"orchestrate_view": true, "project_id": projectID})
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusBadRequest || failure.Error != `metadata key "project_id" is reserved for primary authority state` {
				t.Fatalf("exact release-blocker rejection: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			messages, err := sessions.ListSessionMessages(id, 0, 10)
			if err != nil || len(messages) != 0 {
				t.Fatalf("rejected metadata appended messages: %+v %v", messages, err)
			}
			if _, found, err := sessions.GetSessionActiveRunIntent(id); err != nil || found {
				t.Fatalf("rejected metadata admitted a run: found=%v err=%v", found, err)
			}
			after, found, err := sessions.GetSession(id)
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected metadata mutated session: %+v %v", after, err)
			}
		}
		// Match the corrected composer: project membership is read from the
		// canonical session, not supplied as generic message metadata.
		metadata := map[string]any{"orchestrate_view": true}
		if id == ids[1] {
			metadata["selected_project_id"] = "project"
			metadata["selected_task_id"] = "discussion-context"
		}
		for retry := 0; retry < 2; retry++ {
			if w := post(id, "accepted-"+id, metadata); w.Code != http.StatusOK {
				t.Fatalf("composer admission: %d %s", w.Code, w.Body.String())
			}
		}
		messages, err := sessions.ListSessionMessages(id, 0, 10)
		if err != nil || len(messages) != 1 || messages[0].SessionID != id || messages[0].Content != "accepted-"+id || messages[0].Metadata["project_id"] != nil {
			t.Fatalf("persisted message ownership/idempotency: %+v %v", messages, err)
		}
		active, found, err := sessions.GetSessionActiveRunIntent(id)
		if err != nil || !found || active.SessionID != id {
			t.Fatalf("run admission: %+v %v", active, err)
		}
	}
	w := projectConversationRequest(t, s, p, http.MethodGet, projectPath, nil)
	var listed struct {
		Sessions []struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != http.StatusOK || len(listed.Sessions) != 2 {
		t.Fatalf("durable API listing: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	seen := map[string]bool{}
	for _, row := range listed.Sessions {
		item := row.Session
		seen[item.ID] = true
		if item.Metadata["project_id"] != "project" || item.Metadata["agent_name"] != "system-orchestrator" || item.WorkspacePath != "" || item.WorktreeEnabled {
			t.Fatalf("listed project/runtime identity changed: %+v", item)
		}
		stored, found, err := sessions.GetSession(item.ID)
		if err != nil || !found {
			t.Fatalf("listed session is not durable: %v", err)
		}
		if err := sessions.Store().ValidateProjectConversation(stored, p.AccountScopeID, p.UserID); err != nil {
			t.Fatalf("project/runtime/filesystem authority changed: %v", err)
		}
	}
	if !seen[ids[0]] || !seen[ids[1]] {
		t.Fatalf("listing lost conversations: %+v", seen)
	}
}

// Purpose: canonical project ownership must survive the bounded V3 sync shell
// used by sidebar hydration and reconnect. The real project creation and sync
// HTTP handlers backed by Pebble are the narrowest layer reproducing the live
// disappearance; reads must neither expose agent prompts nor mutate authority.
func TestProjectConversationSyncPreservesOwnership(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	created := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "sync-conversation"})
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || created.Code != http.StatusOK || result.SessionID == "" {
		t.Fatalf("create failed: %d %s", created.Code, created.Body.String())
	}
	before, _, err := sessions.GetSession(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{V3SyncHydratePath, V3SyncBootstrapPath} {
		body := map[string]any{"surface": "desktop", "history": map[string]any{"mode": "none"}}
		if path == V3SyncHydratePath {
			body["session_ids"] = []string{result.SessionID}
		} else {
			body["selector"] = map[string]any{"kind": "session_ids", "session_ids": []string{result.SessionID}}
		}
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, withTestPrincipal(req))
		var payload struct {
			Sessions map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != http.StatusOK {
			t.Fatalf("sync failed: %d %s", w.Code, w.Body.String())
		}
		got := payload.Sessions[result.SessionID]
		for _, key := range []string{"project_id", "swarm_v3_project_id", "role", "agent_name"} {
			if got.Metadata[key] != before.Metadata[key] || got.Metadata[key] == nil {
				t.Fatalf("%s lost canonical %s: got %v want %v", path, key, got.Metadata[key], before.Metadata[key])
			}
		}
		if got.WorkspacePath != "" || got.WorktreeEnabled || got.Metadata["agent_profile"] != nil {
			t.Fatal("sync leaked filesystem authority or private agent profile")
		}
	}
	after, _, err := sessions.GetSession(result.SessionID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("sync reads changed durable authority")
	}
}
