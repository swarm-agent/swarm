package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestSessionsV3SyncHydrateTargetsSessionIDsAndRejectsWrongScopeCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate", "Sync Hydrate")
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
	if payload.SnapshotEndpointCursor == "" || payload.SnapshotEndpointCursor == wrongScopeCursor {
		t.Fatalf("hydrate did not return new scoped cursor: got=%q wrong=%q", payload.SnapshotEndpointCursor, wrongScopeCursor)
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
