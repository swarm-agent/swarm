package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3SyncBootstrapReturnsCanonicalSnapshotScopeAndReplayInstructions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap", "Sync Bootstrap", "/workspace/cp5")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "hello sync")

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5","recent":{"limit":10}},"history":{"mode":"tail","max_messages_per_session":10},"resources":{"messages":true,"run_intents":true,"active_plan":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                     bool                                        `json:"ok"`
		Rev                    uint64                                      `json:"rev"`
		SnapshotEndpointCursor string                                      `json:"snapshot_endpoint_cursor"`
		SyncScope              map[string]string                           `json:"sync_scope"`
		SessionsByID           map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
		ProjectionsBySession   map[string]pebblestore.V3SessionProjection  `json:"projections_by_session"`
		MessagesBySession      map[string][]pebblestore.MessageSnapshot    `json:"messages_by_session"`
		RunIntentsBySession    map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
		SessionOrder           []string                                    `json:"session_order"`
		ReplayInstructions     map[string]any                              `json:"replay_instructions"`
		TombstonesBySession    map[string]any                              `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if !payload.OK || payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("bootstrap sessions_by_id missing created session: %+v", payload.SessionsByID)
	}
	if payload.ProjectionsBySession[created.ID].SessionID != created.ID {
		t.Fatalf("bootstrap projection missing: %+v", payload.ProjectionsBySession)
	}
	if len(payload.MessagesBySession[created.ID]) != 1 {
		t.Fatalf("bootstrap messages_by_session = %+v", payload.MessagesBySession)
	}
	if payload.Rev == 0 || !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
		t.Fatalf("bootstrap cursor/rev invalid: rev=%d cursor=%q", payload.Rev, payload.SnapshotEndpointCursor)
	}
	if payload.SyncScope["surface"] != "desktop" || payload.SyncScope["stream_kind"] != "v3.sync.snapshot" || payload.SyncScope["selector_filter_hash"] == "" {
		t.Fatalf("bootstrap sync_scope invalid: %+v", payload.SyncScope)
	}
	if payload.ReplayInstructions["stream_path"] != V3SyncStreamPath || payload.ReplayInstructions["after_endpoint_cursor"] != payload.SnapshotEndpointCursor {
		t.Fatalf("bootstrap replay instructions invalid: %+v", payload.ReplayInstructions)
	}
	if payload.TombstonesBySession == nil {
		t.Fatalf("bootstrap must include tombstones_by_session map")
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != created.ID {
		t.Fatalf("session_order = %+v", payload.SessionOrder)
	}
}

func TestSessionsV3SyncBootstrapGlobalSelectorWithoutRecentUsesNativeAccountSnapshot(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-a", "Sync Global A", "/workspace/global-a")
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-b", "Sync Global B", "/workspace/global-b")

	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("global bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
		Selector               sessionsV3SyncSelector                 `json:"selector"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode global bootstrap response: %v", err)
	}
	if payload.Selector.Kind != "global" || !payload.Selector.Global {
		t.Fatalf("global bootstrap selector = %+v", payload.Selector)
	}
	if !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
		t.Fatalf("global bootstrap missing signed cursor: %+v", payload)
	}
	if payload.SessionsByID[createdA.ID].ID != createdA.ID || payload.SessionsByID[createdB.ID].ID != createdB.ID {
		t.Fatalf("global bootstrap missing account sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
	}
}

func TestSessionsV3SyncBootstrapGlobalRecentDoesNotSelectAllAccountSessions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-a", "Sync Global Recent A", "/workspace/global-recent-a")
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-b", "Sync Global Recent B", "/workspace/global-recent-b")
	createdC := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-c", "Sync Global Recent C", "/workspace/global-recent-c")

	body := `{"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":1}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("global recent bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder []string                               `json:"session_order"`
		Selector     sessionsV3SyncSelector                 `json:"selector"`
		Pagination   pebblestore.V3SyncSnapshotPagination   `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode global recent bootstrap response: %v", err)
	}
	if payload.Selector.Kind != "recent" || !payload.Selector.Global || payload.Selector.Recent.Limit != 1 {
		t.Fatalf("global recent bootstrap selector = %+v", payload.Selector)
	}
	if len(payload.SessionOrder) != 1 || len(payload.SessionsByID) != 1 {
		t.Fatalf("global recent bootstrap selected all sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
	}
	selectedID := payload.SessionOrder[0]
	if selectedID != createdA.ID && selectedID != createdB.ID && selectedID != createdC.ID {
		t.Fatalf("global recent bootstrap selected unexpected session %q from %+v", selectedID, payload.SessionOrder)
	}
	if !payload.Pagination.HasMore {
		t.Fatalf("global recent bootstrap did not report remaining recent page: %+v", payload.Pagination)
	}
}

func TestSessionsV3SyncBootstrapUsesCanonicalSelectorForSnapshotOptions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	selectorSession := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-selector", "Sync Bootstrap Selector", "/workspace/bootstrap-selector")
	rawWorkspaceSession := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-raw", "Sync Bootstrap Raw", "/workspace/bootstrap-raw")
	expectedSelector := sessionsV3SyncSelector{Kind: "workspace", WorkspacePath: "/workspace/bootstrap-selector", Recent: sessionsV3WorksetRecent{Limit: 10}}
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(expectedSelector))

	body, err := json.Marshal(map[string]any{
		"surface": "desktop",
		"selector": map[string]any{
			"kind":           "workspace",
			"workspace_path": "/workspace/bootstrap-selector",
			"recent":         map[string]any{"limit": 10},
		},
		"workspace": map[string]any{"workspace_path": "/workspace/bootstrap-selector"},
		"recent":    map[string]any{"limit": 10},
		"history":   map[string]any{"mode": "none"},
	})
	if err != nil {
		t.Fatalf("marshal bootstrap body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		Selector               sessionsV3SyncSelector                 `json:"selector"`
		SyncScope              map[string]string                      `json:"sync_scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if payload.Selector.Kind != expectedSelector.Kind || payload.Selector.WorkspacePath != expectedSelector.WorkspacePath || payload.Selector.Recent.Limit != expectedSelector.Recent.Limit {
		t.Fatalf("bootstrap selector = %+v, want %+v", payload.Selector, expectedSelector)
	}
	if payload.SessionsByID[selectorSession.ID].ID != selectorSession.ID {
		t.Fatalf("bootstrap did not read from canonical selector workspace: %+v", payload.SessionsByID)
	}
	if payload.SessionsByID[rawWorkspaceSession.ID].ID != "" {
		t.Fatalf("bootstrap read from conflicting raw workspace field: %+v", payload.SessionsByID[rawWorkspaceSession.ID])
	}
	if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
		t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
	}
	cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
	if err != nil {
		t.Fatalf("verify bootstrap cursor: %v", err)
	}
	if cursorPayload.SelectorFilterHash != expectedSelectorHash {
		t.Fatalf("cursor selector hash = %q, want %q", cursorPayload.SelectorFilterHash, expectedSelectorHash)
	}
}

func TestSessionsV3SyncBootstrapRejectsGlobalSelectorWithConflictingRawWorkspace(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"workspace":{"workspace_path":"/workspace/bootstrap-raw"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "global selector cannot be combined") {
		t.Fatalf("bootstrap error did not report selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsConflictingRawWorkspaceSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/selector","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/raw"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync selector conflicts with top-level workspace") {
		t.Fatalf("bootstrap error did not report workspace selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsUnboundedWorkspaceSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/unbounded"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace selector requires recent.limit") {
		t.Fatalf("bootstrap error did not report bounded workspace requirement: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapUsesNativeSnapshotBuilderNotLegacyWorkset(t *testing.T) {
	source, err := os.ReadFile("sessions_v3_sync_bootstrap.go")
	if err != nil {
		t.Fatalf("read sync bootstrap source: %v", err)
	}
	body := string(source)
	for _, forbidden := range []string{"BuildSessionWorkset", "V3SessionWorksetOptions", "V3SessionWorksetResult", "sessionsV3SyncPlans", "sessionsV3SyncTombstonesBySession", "ListPending(", "GetUsageSummary(", "GetActivePlan(", "ListPlanRevisions(", "ListSessionTombstonesForAccount("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sync bootstrap must not use legacy/out-of-snapshot path %q", forbidden)
		}
	}
}

func TestSessionsV3SyncHydrateTargetsSessionIDs(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate", "Sync Hydrate")
	body := `{"surface":"desktop","session_ids":["` + created.ID + `"]}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                     bool                                   `json:"ok"`
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
		ReplayInstructions     map[string]any                         `json:"replay_instructions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !payload.OK || payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("hydrate missing target session: %+v", payload.SessionsByID)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != created.ID {
		t.Fatalf("hydrate session_order = %+v", payload.SessionOrder)
	}
	if payload.SnapshotEndpointCursor == "" {
		t.Fatalf("hydrate did not return scoped cursor")
	}
	if payload.ReplayInstructions["after_endpoint_cursor"] != payload.SnapshotEndpointCursor {
		t.Fatalf("hydrate replay instructions invalid: %+v", payload.ReplayInstructions)
	}
}

func TestSessionsV3SyncHydrateCanonicalizesSessionIDSelectorForCursorScope(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-canonical-a", "Sync Hydrate Canonical A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-canonical-b", "Sync Hydrate Canonical B")
	expectedIDs := canonicalV3SyncSessionIDs([]string{createdA.ID, createdB.ID})
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: expectedIDs}))

	var firstSelectorHash string
	for _, tc := range []struct {
		name       string
		sessionIDs []string
	}{
		{name: "ordered", sessionIDs: []string{createdA.ID, createdB.ID}},
		{name: "reversed", sessionIDs: []string{createdB.ID, createdA.ID}},
		{name: "duplicate and empty", sessionIDs: []string{createdA.ID, "", createdB.ID, createdA.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"surface": "desktop", "session_ids": tc.sessionIDs})
			if err != nil {
				t.Fatalf("marshal hydrate body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var payload struct {
				SnapshotEndpointCursor string                 `json:"snapshot_endpoint_cursor"`
				Selector               sessionsV3SyncSelector `json:"selector"`
				SyncScope              map[string]string      `json:"sync_scope"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode hydrate response: %v", err)
			}
			if payload.Selector.Kind != "session_ids" || strings.Join(payload.Selector.SessionIDs, ",") != strings.Join(expectedIDs, ",") {
				t.Fatalf("hydrate selector = %+v, want canonical ids %+v", payload.Selector, expectedIDs)
			}
			if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
				t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
			}
			cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
			if err != nil {
				t.Fatalf("verify hydrate cursor: %v", err)
			}
			if cursorPayload.SelectorFilterHash != payload.SyncScope["selector_filter_hash"] {
				t.Fatalf("cursor selector hash = %q, response hash = %q", cursorPayload.SelectorFilterHash, payload.SyncScope["selector_filter_hash"])
			}
			if firstSelectorHash == "" {
				firstSelectorHash = payload.SyncScope["selector_filter_hash"]
			} else if payload.SyncScope["selector_filter_hash"] != firstSelectorHash {
				t.Fatalf("selector hash = %q, want stable hash %q", payload.SyncScope["selector_filter_hash"], firstSelectorHash)
			}
		})
	}
}

func TestSessionsV3SyncHydrateIncludeActiveDoesNotWidenSessionIDs(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-active-a", "Sync Hydrate Active A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-active-b", "Sync Hydrate Active B")
	now := time.Now().UnixMilli()
	runID := "run-sync-hydrate-active-b"
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       createdB.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "sync-hydrate-active-b-running",
		IdempotencyKey:  "sync-hydrate-active-b-running",
		PayloadHash:     "hash-sync-hydrate-active-b-running",
		RequestHash:     "hash-sync-hydrate-active-b-running",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run.queued",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now},
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("mark session B active: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"surface":        "desktop",
		"session_ids":    []string{createdA.ID},
		"include_active": true,
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                      `json:"snapshot_endpoint_cursor"`
		Selector               sessionsV3SyncSelector                      `json:"selector"`
		SyncScope              map[string]string                           `json:"sync_scope"`
		SessionsByID           map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
		SessionOrder           []string                                    `json:"session_order"`
		RunIntentsBySession    map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if payload.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("hydrate missing requested session A: %+v", payload.SessionsByID)
	}
	if _, leaked := payload.SessionsByID[createdB.ID]; leaked {
		t.Fatalf("hydrate include_active widened to active session B: %+v", payload.SessionsByID)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != createdA.ID {
		t.Fatalf("hydrate session_order = %+v, want only %s", payload.SessionOrder, createdA.ID)
	}
	if _, leaked := payload.RunIntentsBySession[createdB.ID]; leaked {
		t.Fatalf("hydrate include_active leaked active run intents for session B: %+v", payload.RunIntentsBySession)
	}
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: []string{createdA.ID}}))
	if payload.Selector.Kind != "session_ids" || len(payload.Selector.SessionIDs) != 1 || payload.Selector.SessionIDs[0] != createdA.ID {
		t.Fatalf("hydrate selector = %+v, want only %s", payload.Selector, createdA.ID)
	}
	if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
		t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
	}
	cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
	if err != nil {
		t.Fatalf("verify hydrate cursor: %v", err)
	}
	if cursorPayload.SelectorFilterHash != expectedSelectorHash {
		t.Fatalf("cursor selector hash = %q, want %q", cursorPayload.SelectorFilterHash, expectedSelectorHash)
	}

	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdA.ID, "hydrate stream handoff A")
	mutatedAt := time.Now().UnixMilli()
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       createdB.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "sync-hydrate-active-b-running-after-cursor",
		IdempotencyKey:  "sync-hydrate-active-b-running-after-cursor",
		PayloadHash:     "hash-sync-hydrate-active-b-running-after-cursor",
		RequestHash:     "hash-sync-hydrate-active-b-running-after-cursor",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, CreatedAt: now, UpdatedAt: mutatedAt},
		NowUnixMs:       mutatedAt,
	}); err != nil {
		t.Fatalf("mutate session B after hydrate: %v", err)
	}

	streamBody, err := json.Marshal(map[string]any{
		"surface":         "desktop",
		"session_ids":     []string{createdA.ID},
		"include_active":  true,
		"endpoint_cursor": payload.SnapshotEndpointCursor,
	})
	if err != nil {
		t.Fatalf("marshal stream body: %v", err)
	}
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewReader(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamRec.Code, http.StatusOK, streamRec.Body.String())
	}
	var streamPayload struct {
		EndpointCursor string `json:"endpoint_cursor"`
		Events         []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &streamPayload); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if !strings.HasPrefix(streamPayload.EndpointCursor, v3SyncCursorPrefix) || strings.HasPrefix(streamPayload.EndpointCursor, "cursor-") {
		t.Fatalf("stream endpoint_cursor is not signed/opaque: %q", streamPayload.EndpointCursor)
	}
	foundA := false
	for _, event := range streamPayload.Events {
		switch event.SessionID {
		case createdA.ID:
			foundA = true
		case createdB.ID:
			t.Fatalf("stream using hydrate cursor leaked active session B mutation: %+v", streamPayload.Events)
		}
	}
	if !foundA {
		t.Fatalf("stream using hydrate cursor missed requested session A mutation: %+v", streamPayload.Events)
	}
}

func TestSessionsV3SyncHydrateRejectsConflictingSelectorFields(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-conflict", "Sync Hydrate Conflict")

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"selector": map[string]any{
			"kind":           "workspace",
			"workspace_path": "/x",
			"recent":         map[string]any{"limit": 50},
		},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync hydrate selector conflicts") {
		t.Fatalf("hydrate error did not report selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapSnapshotCursorCoversConcurrentMutationReplay(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-race-a", "Sync Race A", "/workspace/cp5-race")
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-race","recent":{"limit":10}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if bootstrap.SessionsByID[created.ID].ID != created.ID || bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap invalid: %+v", bootstrap)
	}
	createdAfter := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-race-b", "Sync Race B", "/workspace/cp5-race")
	if bootstrap.SessionsByID[createdAfter.ID].ID != "" {
		t.Fatalf("test setup expected mutation after snapshot cursor, but bootstrap included %s", createdAfter.ID)
	}
	scope := v3SyncCursorScopeForSnapshot(testPrincipal(), "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "workspace", WorkspacePath: "/workspace/cp5-race", Recent: sessionsV3WorksetRecent{Limit: 10}}, []string{"sessions", "projections", "membership", "tombstones"})
	afterSeq, _, err := server.parseV3SyncEndpointCursor(bootstrap.SnapshotEndpointCursor, scope)
	if err != nil {
		t.Fatalf("parse bootstrap cursor: %v", err)
	}
	if createdAfter.ID == "" || createdAfter.UpdatedAt == 0 {
		t.Fatalf("post-bootstrap mutation was not committed: %+v", createdAfter)
	}
	currentHead, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatalf("current outbox head: %v", err)
	}
	if currentHead <= afterSeq {
		t.Fatalf("post-bootstrap mutation not replayable after cursor: head=%d after=%d", currentHead, afterSeq)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-race","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamRec.Code, http.StatusOK, streamRec.Body.String())
	}
	var stream struct {
		EndpointCursor   string `json:"endpoint_cursor"`
		HighWatermarkSeq uint64 `json:"high_watermark_seq"`
		Events           []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if stream.EndpointCursor == "" || stream.HighWatermarkSeq <= afterSeq {
		t.Fatalf("stream did not advance past bootstrap cursor: %+v after=%d", stream, afterSeq)
	}
	found := false
	for _, event := range stream.Events {
		if event.SessionID == createdAfter.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("stream replay after bootstrap cursor missed post-bootstrap session %s: %+v", createdAfter.ID, stream.Events)
	}
}

func TestSessionsV3SyncHydrateRejectsKnownSessionWrongScopeCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-reject", "Sync Hydrate Reject")
	otherPrincipal := testPrincipal()
	otherPrincipal.AccountScopeID = "acct-other"
	wrongScopeCursor, err := server.signV3SyncEndpointCursor(v3SyncCursorScopeForSnapshot(otherPrincipal, "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: []string{created.ID}}, []string{"sessions", "projections", "membership", "tombstones"}), 1)
	if err != nil {
		t.Fatalf("sign wrong scope cursor: %v", err)
	}

	body := `{"surface":"desktop","session_ids":["` + created.ID + `"],"known_sessions":{"` + created.ID + `":{"endpoint_cursor":"` + wrongScopeCursor + `"}}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "endpoint_cursor_scope_mismatch") {
		t.Fatalf("hydrate wrong-scope cursor error missing code: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsKnownSessionLegacyEndpointCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-bootstrap-legacy-known", "Sync Bootstrap Legacy Known")
	body := `{"surface":"desktop","selector":{"kind":"session_ids","session_ids":["` + created.ID + `"]},"known_sessions":{"` + created.ID + `":{"endpoint_cursor":"cursor-1"}}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "endpoint_cursor_legacy_unsupported") {
		t.Fatalf("bootstrap legacy known cursor error missing code: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapIncludesExtraResourcesFromSnapshot(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-extra-resources", "Sync Extra Resources", "/workspace/cp5-extra")
	if err := server.sessions.Store().PutUsageSummary(pebblestore.SessionUsageSummary{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Provider: "test-provider", Model: "test-model", InputTokens: 3, OutputTokens: 4}); err != nil {
		t.Fatalf("put usage summary: %v", err)
	}
	if _, _, err := server.sessions.SavePlan(created.ID, "sync-plan", "Sync Plan", "# Sync Plan", "draft", "draft", true); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	permissionRecord, err := server.perm.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: "run-sync-extra", CallID: "call-sync-extra", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-extra","recent":{"limit":10}},"history":{"mode":"none"},"resources":{"active_plan":true,"plan_revisions":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		PermissionsBySession      map[string][]pebblestore.PermissionRecord    `json:"permissions_by_session"`
		UsageBySession            map[string]pebblestore.SessionUsageSummary   `json:"usage_by_session"`
		PlansBySession            map[string]pebblestore.SessionPlanSnapshot   `json:"plans_by_session"`
		PlanRevisionsBySession    map[string][]pebblestore.SessionPlanSnapshot `json:"plan_revisions_by_session"`
		TombstonesBySession       map[string]pebblestore.V3SessionTombstone    `json:"tombstones_by_session"`
		SnapshotEndpointCursor    string                                       `json:"snapshot_endpoint_cursor"`
		ReplayInstructions        map[string]any                               `json:"replay_instructions"`
		AgentModelPolicyBySession map[string]any                               `json:"agent_model_policy_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if got := payload.PermissionsBySession[created.ID]; len(got) != 1 || got[0].ID != permissionRecord.ID {
		t.Fatalf("permissions_by_session missing pending permission: %+v", got)
	}
	if payload.UsageBySession[created.ID].InputTokens != 3 || payload.UsageBySession[created.ID].OutputTokens != 4 {
		t.Fatalf("usage_by_session missing summary: %+v", payload.UsageBySession[created.ID])
	}
	if payload.PlansBySession[created.ID].ID != "sync-plan" || !payload.PlansBySession[created.ID].Active {
		t.Fatalf("plans_by_session missing active plan: %+v", payload.PlansBySession[created.ID])
	}
	if got := payload.PlanRevisionsBySession[created.ID]; len(got) == 0 || got[0].ID != "sync-plan" {
		t.Fatalf("plan_revisions_by_session missing revisions: %+v", got)
	}
	if payload.TombstonesBySession == nil || payload.SnapshotEndpointCursor == "" || payload.ReplayInstructions["after_endpoint_cursor"] != payload.SnapshotEndpointCursor || payload.AgentModelPolicyBySession == nil {
		t.Fatalf("bootstrap response missing durable sync fields: %+v", payload)
	}
}

func TestSessionsV3SyncHydrateReturnsDeletedRequestedSessionTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-hydrate-tombstone", "Sync Hydrate Tombstone", "/workspace/cp4-hydrate-tombstone")
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"history":     map[string]any{"mode": "none"},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("deleted session still present in sessions_by_id: %+v", payload.SessionsByID[created.ID])
	}
	tombstone := payload.TombstonesBySession[created.ID]
	if !tombstone.Deleted || tombstone.Kind != "deleted" || tombstone.Session.ID != created.ID || tombstone.WorkspacePath != "/workspace/cp4-hydrate-tombstone" {
		t.Fatalf("hydrate deleted tombstone invalid: %+v", tombstone)
	}
}

func TestSessionsV3SyncBootstrapReturnsDeletedSessionTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-tombstone", "Sync Tombstone", "/workspace/cp5-tombstone")
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-tombstone","recent":{"limit":10}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("deleted session still present in sessions_by_id: %+v", payload.SessionsByID[created.ID])
	}
	tombstone := payload.TombstonesBySession[created.ID]
	if !tombstone.Deleted || tombstone.Kind != "deleted" || tombstone.Session.ID != created.ID || tombstone.WorkspacePath != "/workspace/cp5-tombstone" {
		t.Fatalf("deleted tombstone invalid: %+v", tombstone)
	}
}

func TestSessionsV3SyncWorkspaceStreamDoesNotDropDeletedMembershipEvent(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-delete-stream", "Sync Delete Stream", "/workspace/cp5-delete-stream")

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-delete-stream","recent":{"limit":10}},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, withTestPrincipal(bootstrapReq))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string `json:"snapshot_endpoint_cursor"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap cursor missing")
	}
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-delete-stream","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var stream struct {
		Events []struct {
			SessionID string `json:"session_id"`
			Event     struct {
				EventType string `json:"event_type"`
			} `json:"event"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID && event.Event.EventType == "session.deleted" {
			return
		}
	}
	t.Fatalf("workspace stream missed durable delete membership event for %s: %+v", created.ID, stream.Events)
}

func TestSessionsV3SyncStreamWebsocketIsExplicitlyUnsupportedForSnapshotCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + V3SyncStreamPath
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial sync stream websocket: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial sync stream websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set sync stream read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read sync stream unsupported frame: %v", err)
	}
	var frame struct {
		Kind string `json:"kind"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode sync stream unsupported frame %s: %v", string(raw), err)
	}
	if frame.Kind != V3RealtimeKindCursorError || frame.Code != "sync_websocket_unsupported" {
		t.Fatalf("sync websocket frame = %+v raw=%s", frame, string(raw))
	}
}

func TestSessionsV3SyncAuthSeparationForHydrateAndStream(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-auth-separation", "Sync Auth Separation")

	hydrateReq := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(`{"surface":"desktop","session_ids":["`+created.ID+`"]}`))
	hydrateReq.Header.Set("Content-Type", "application/json")
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, requestWithTestPrincipalForAccount(hydrateReq, "other-user", "other-account"))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("cross-account hydrate status=%d body=%s", hydrateRec.Code, hydrateRec.Body.String())
	}
	var hydrate struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrate); err != nil {
		t.Fatalf("decode cross-account hydrate: %v", err)
	}
	if len(hydrate.SessionsByID) != 0 || len(hydrate.SessionOrder) != 0 {
		t.Fatalf("cross-account hydrate leaked session: %+v", hydrate)
	}

	scope := v3SyncCursorScopeForSnapshot(testPrincipal(), "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "global", Global: true}, []string{"sessions", "projections", "membership", "tombstones"})
	cursor, err := server.signV3SyncEndpointCursor(scope, 1)
	if err != nil {
		t.Fatalf("sign cursor: %v", err)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + cursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, requestWithTestPrincipalForAccount(streamReq, "other-user", "other-account"))
	if streamRec.Code != http.StatusBadRequest {
		t.Fatalf("cross-account stream status=%d want=%d body=%s", streamRec.Code, http.StatusBadRequest, streamRec.Body.String())
	}
	if !strings.Contains(streamRec.Body.String(), "endpoint_cursor_scope_mismatch") {
		t.Fatalf("cross-account stream did not fail closed on cursor scope: %s", streamRec.Body.String())
	}
}

func TestSessionsV3SyncCanonicalScopeIsAccountAndUser(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	userA := testPrincipal()
	userB := testPrincipal()
	userB.UserID = "test-user-b"

	createForPrincipal := func(principal identity.Principal, sessionID, workspace string) pebblestore.SessionSnapshot {
		t.Helper()
		result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
			SessionID:       sessionID,
			UserID:          principal.UserID,
			AccountScopeID:  principal.AccountScopeID,
			ClientRequestID: "create-" + sessionID,
			IdempotencyKey:  "create-" + sessionID,
			PayloadHash:     "hash-create-" + sessionID,
			Kind:            sessionruntime.SessionMutationCreateSession,
			Session: &pebblestore.SessionSnapshot{
				ID:             sessionID,
				UserID:         principal.UserID,
				AccountScopeID: principal.AccountScopeID,
				WorkspacePath:  workspace,
				WorkspaceName:  strings.Trim(workspace, "/"),
				Title:          sessionID,
			},
			NowUnixMs: time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatalf("create session %s: %v", sessionID, err)
		}
		if result.Session == nil {
			t.Fatalf("create session %s returned no session", sessionID)
		}
		return *result.Session
	}

	createdA := createForPrincipal(userA, "sync-scope-user-a", "/workspace/sync-scope")
	createdB := createForPrincipal(userB, "sync-scope-user-b", "/workspace/sync-scope")
	legacy := createdA
	legacy.ID = "sync-scope-legacy-empty-user"
	legacy.UserID = ""
	legacy.Title = "legacy empty user"
	legacy.UpdatedAt = time.Now().UnixMilli()
	if err := sessionSvc.Store().CreateSession(legacy); err != nil {
		t.Fatalf("create legacy empty-user session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, requestWithTestPrincipalForAccount(req, userA.UserID, userA.AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("bootstrap missing user A session: %+v", bootstrap.SessionsByID)
	}
	if bootstrap.SessionsByID[createdB.ID].ID != "" || bootstrap.SessionsByID[legacy.ID].ID != "" {
		t.Fatalf("bootstrap leaked other user or empty user session: %+v", bootstrap.SessionsByID)
	}

	hydrateBody := `{"surface":"desktop","session_ids":["` + createdA.ID + `","` + createdB.ID + `","` + legacy.ID + `"]}`
	hydrateReq := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(hydrateBody))
	hydrateReq.Header.Set("Content-Type", "application/json")
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, requestWithTestPrincipalForAccount(hydrateReq, userA.UserID, userA.AccountScopeID))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status=%d body=%s", hydrateRec.Code, hydrateRec.Body.String())
	}
	var hydrate struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrate); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	if hydrate.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("hydrate missing user A session: %+v", hydrate.SessionsByID)
	}
	if hydrate.SessionsByID[createdB.ID].ID != "" || hydrate.SessionsByID[legacy.ID].ID != "" {
		t.Fatalf("hydrate leaked other user or empty user session: %+v", hydrate.SessionsByID)
	}

	if bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap missing cursor")
	}
	message := pebblestore.MessageSnapshot{ID: "sync-scope-user-b-message", SessionID: createdB.ID, UserID: userB.UserID, AccountScopeID: userB.AccountScopeID, Role: "user", Content: "should not leak", CreatedAt: time.Now().UnixMilli()}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: createdB.ID, UserID: userB.UserID, AccountScopeID: userB.AccountScopeID, ClientRequestID: "sync-scope-user-b-message", IdempotencyKey: "sync-scope-user-b-message", PayloadHash: "hash-sync-scope-user-b-message", Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("append user B message: %v", err)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, requestWithTestPrincipalForAccount(streamReq, userA.UserID, userA.AccountScopeID))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var stream struct {
		Events []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == createdB.ID || event.SessionID == legacy.ID {
			t.Fatalf("stream leaked other user or empty user event: %+v", stream.Events)
		}
	}
}

func TestSessionsV3SyncTUIAndPhoneEquivalentBootstrapScopes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	phoneCreated := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "phone-equivalent-create", "Phone Equivalent", "/workspace/cp5-phone")
	inactiveCreated := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "inactive-create", "Inactive Session", "/workspace/cp5-phone")

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "desktop workspace", body: `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-phone","recent":{"limit":10}},"history":{"mode":"none"}}`},
		{name: "tui cwd", body: `{"surface":"tui","selector":{"kind":"tui","cwd_path":"/workspace/cp5-phone","recent":{"limit":10}},"history":{"mode":"none"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
			}
			var payload struct {
				SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
				SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
				SessionOrder           []string                               `json:"session_order"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode bootstrap: %v", err)
			}
			if !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
				t.Fatalf("bootstrap missing signed cursor: %+v", payload)
			}
			if payload.SessionsByID[phoneCreated.ID].ID != phoneCreated.ID || payload.SessionsByID[inactiveCreated.ID].ID != inactiveCreated.ID {
				t.Fatalf("bootstrap missing phone/inactive sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
			}
		})
	}
}
