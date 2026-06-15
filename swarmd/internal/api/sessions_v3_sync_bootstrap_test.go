package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

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
