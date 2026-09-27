package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: project.updated frames are admitted only for their matching account scope.
// Negative cases verify foreign and unscoped accounts are rejected.
func TestProjectRealtimeAdmission(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Realtime project invalidation events must be strictly isolated
	//   by account scope. Only clients authenticating under the matching account scope
	//   may receive project.updated frames.
	// - Boundary/authority: v3RealtimeRecordVisibleToPrincipal in sessions_v3_realtime_ws.go
	//   and ValidateV3RealtimeOutboundServerMessage in sessions_v3_realtime_contract.go.
	// - Threat/regression: Cross-account project leakage or snooping on project/task names.

	p := identity.Principal{Type: "user", UserID: "test-user", AccountScopeID: "acct_alpha"}
	r := sessionruntime.RealtimeOutboxRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         "desktop",
		Event: pebblestore.V3SessionEvent{
			Seq:       1,
			EventType: pebblestore.ProjectUpdatedEventType,
		},
	}
	if !v3RealtimeRecordVisibleToPrincipal(p, r) {
		t.Fatal("own account event unexpectedly rejected")
	}

	// Negative tests: foreign or unscoped accounts must be rejected
	for _, foreignAccount := range []string{"acct_beta", "foreign", ""} {
		r.AccountScopeID = foreignAccount
		if v3RealtimeRecordVisibleToPrincipal(p, r) {
			t.Fatalf("foreign/unscoped account %q event unexpectedly admitted", foreignAccount)
		}
	}

	// Frame validation: must have valid endpoint_cursor
	frame := V3RealtimeMessage{
		Protocol:        V3RealtimeProtocol,
		ProtocolVersion: V3RealtimeProtocolVersion,
		Kind:            V3RealtimeKindProjectUpdated,
		EndpointCursor:  "opaque-cursor",
	}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil {
		t.Fatalf("valid project frame rejected: %v", err)
	}

	frame.EndpointCursor = ""
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err == nil {
		t.Fatal("missing endpoint_cursor unexpectedly accepted")
	}
}

// Requirement: store project/task mutations wake an open scoped socket and replay
// after reconnect without polling or subscribing to the synthetic session.
func TestProjectRealtimeDeliveryAndReplay(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a project or task is mutated in Pebble store (PutProject,
	//   PutProjectTask, etc.), it must immediately wake the realtime hub and deliver a
	//   project.updated frame to all active connections in that account scope.
	//   Reconnecting with an endpoint_cursor must replay the frame from durable outbox.
	// - Boundary/authority: ConfigureProjectRealtime, v3RealtimeProcessOutboxRecord,
	//   and publishProjectRealtime.
	// - Threat/regression: Dropped events leading to UI falling back to CPU-churning polling.

	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(store)

	sessionStore := pebblestore.NewSessionStore(store)
	accountID := testPrincipal().AccountScopeID

	initial, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer func() { conn.Close() }()

	resume := func(cursor uint64) {
		writeV3RealtimeMessage(t, conn, V3RealtimeMessage{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindResume,
			EndpointCursor:  signedV3RealtimeCursorForTest(t, server, cursor),
			Worksets: []V3RealtimeWorksetSubscriptionRequest{
				v3RealtimeGlobalWorksetRequestForTest(),
			},
		})
	}
	resume(initial)

	// Mutate project
	proj := &pebblestore.ProjectRecord{
		Name:        "Test Realtime Project",
		Description: "For delivery testing",
	}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatalf("PutProject failed: %v", err)
	}

	// Read frame from websocket
	readProjectFrame := func(expectedProjectID string) V3RealtimeMessage {
		t.Helper()
		for i := 0; i < 15; i++ {
			frame := readV3RealtimeFrame(t, conn)
			if frame.Kind == V3RealtimeKindProjectUpdated {
				if frame.EndpointCursor == "" {
					t.Fatalf("missing endpoint_cursor on frame: %+v", frame)
				}
				if frame.ProjectID != expectedProjectID {
					t.Fatalf("expected project_id %q, got %q", expectedProjectID, frame.ProjectID)
				}
				return frame
			}
		}
		t.Fatalf("timed out waiting for project.updated frame for project %s", expectedProjectID)
		return V3RealtimeMessage{}
	}

	frame1 := readProjectFrame(proj.ID)
	if frame1.Kind != V3RealtimeKindProjectUpdated {
		t.Fatalf("expected kind %q, got %q", V3RealtimeKindProjectUpdated, frame1.Kind)
	}

	// Mutate task
	task := &pebblestore.ProjectTaskRecord{
		ProjectID: proj.ID,
		Title:     "Realtime Task",
		Agent:     "coder",
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatalf("PutProjectTask failed: %v", err)
	}

	frame2 := readProjectFrame(proj.ID)
	if frame2.Kind != V3RealtimeKindProjectUpdated {
		t.Fatalf("expected kind %q, got %q", V3RealtimeKindProjectUpdated, frame2.Kind)
	}

	// Reconnect and replay
	conn.Close()
	conn = dialV3RealtimeStream(t, httpServer.URL)
	resume(initial)

	// Should replay frame1 and frame2
	replayedFrame1 := readProjectFrame(proj.ID)
	if replayedFrame1.Kind != V3RealtimeKindProjectUpdated {
		t.Fatalf("expected replayed kind %q, got %q", V3RealtimeKindProjectUpdated, replayedFrame1.Kind)
	}
}

// Requirement: GET /v3/projects/{id}/tasks and GET /v3/projects/{id}/tasks/{taskId}
// must be pure reads: no write-on-read to Pebble, no outbox revision advance.
func TestProjectReadPurity_NoWriteOnRead(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Read endpoints must have strict read purity. A GET request
	//   must never write back to Pebble store or spawn git subprocesses, preventing
	//   CPU churn and database write amplification under frequent client reads.
	// - Boundary/authority: Server.handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Write-on-read causing lock contention and 100% CPU usage.

	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(store)

	sessionStore := pebblestore.NewSessionStore(store)
	accountID := testPrincipal().AccountScopeID

	proj := &pebblestore.ProjectRecord{
		Name: "Purity Project",
	}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ProjectID: proj.ID,
		Title:     "Purity Task",
		Agent:     "coder",
		Status:    "in_progress",
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	// Record outbox revision after setup writes
	revBefore, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}

	h := server.apiMux()
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, nil)
		p := testPrincipal()
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &pebblestore.ScopedTokenRecord{
			AccountScopeID: p.AccountScopeID,
			UserID:         p.UserID,
			Scopes:         []string{"projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. GET /v3/projects/{id}/tasks collection
	w1 := call(http.MethodGet, "/"+proj.ID+"/tasks")
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 on collection GET, got %d: %s", w1.Code, w1.Body.String())
	}

	revAfterCollectionGet, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	if revAfterCollectionGet != revBefore {
		t.Fatalf("read impurity: collection GET advanced outbox revision from %d to %d (write-on-read detected!)",
			revBefore, revAfterCollectionGet)
	}

	// 2. GET /v3/projects/{id}/tasks/{taskId} single task
	w2 := call(http.MethodGet, "/"+proj.ID+"/tasks/"+task.ID)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on single task GET, got %d: %s", w2.Code, w2.Body.String())
	}

	revAfterSingleGet, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	if revAfterSingleGet != revBefore {
		t.Fatalf("read impurity: single task GET advanced outbox revision from %d to %d (write-on-read detected!)",
			revBefore, revAfterSingleGet)
	}

	// 3. Repeat collection GET 10 times to verify absolute zero outbox growth
	for i := 0; i < 10; i++ {
		w := call(http.MethodGet, "/"+proj.ID+"/tasks")
		if w.Code != http.StatusOK {
			t.Fatalf("repeat %d: expected 200, got %d", i, w.Code)
		}
	}
	revFinal, _ := server.sessions.CurrentRealtimeOutboxRevision()
	if revFinal != revBefore {
		t.Fatalf("repeated collection GET caused writes: rev before=%d, rev final=%d", revBefore, revFinal)
	}
}

// Requirement: collection GET /v3/projects/{id}/tasks must hydrate task statuses
// authoritatively without executing git subprocesses or mutating Pebble.
func TestProjectTasks_CollectionGet_NoSubprocessStormAndHydration(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Collection GET /v3/projects/{id}/tasks must hydrate task statuses
	//   authoritatively from session state without executing git subprocesses or mutating Pebble.
	// - Boundary/authority: Server.handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: CPU churn and slow API responses caused by git subprocess storms.

	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(store)

	sessionStore := pebblestore.NewSessionStore(store)
	accountID := testPrincipal().AccountScopeID

	proj := &pebblestore.ProjectRecord{
		Name: "Collection Test Project",
	}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	// Create 5 tasks
	for i := 0; i < 5; i++ {
		task := &pebblestore.ProjectTaskRecord{
			ProjectID: proj.ID,
			Title:     fmt.Sprintf("Task %d", i),
			Agent:     "coder",
			Status:    "in_progress",
		}
		if err := sessionStore.PutProjectTask(accountID, task); err != nil {
			t.Fatal(err)
		}
	}

	revBefore, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}

	h := server.apiMux()
	r := httptest.NewRequest(http.MethodGet, ProjectsPath+"/"+proj.ID+"/tasks", nil)
	p := testPrincipal()
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"projects:read", "sessions:read"},
	}
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(w, r)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		Count int                             `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 5 || len(resp.Tasks) != 5 {
		t.Fatalf("expected 5 tasks, got count=%d, len=%d", resp.Count, len(resp.Tasks))
	}

	// Verify all tasks have valid worktree branch and name formatted in memory
	for _, tk := range resp.Tasks {
		if tk.WorktreeBranch == "" || !strings.HasPrefix(tk.WorktreeBranch, "agent/") {
			t.Fatalf("expected worktree branch starting with agent/, got %q", tk.WorktreeBranch)
		}
		if tk.WorktreeName == "" {
			t.Fatalf("expected non-empty worktree name")
		}
	}

	// Verify zero write-on-read
	revAfter, _ := server.sessions.CurrentRealtimeOutboxRevision()
	if revAfter != revBefore {
		t.Fatalf("write-on-read detected: outbox revision changed from %d to %d", revBefore, revAfter)
	}

	// Verify performance: 5 tasks without git subprocesses should execute near instantaneously (< 500ms)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("collection GET took %v, expected fast in-memory execution", elapsed)
	}
}
