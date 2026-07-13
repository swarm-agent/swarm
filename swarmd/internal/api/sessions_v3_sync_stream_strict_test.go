package api

import (
	"bytes"
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

func TestSessionsV3SyncStreamRejectsMissingLegacyAndUnknownSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing cursor", body: `{"surface":"desktop","selector":{"kind":"global","global":true}}`, want: "endpoint_cursor_required"},
		{name: "legacy cursor", body: `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"cursor-1"}`, want: "endpoint_cursor_legacy_unsupported"},
		{name: "unknown selector", body: `{"surface":"desktop","selector":{"kind":"everything"},"endpoint_cursor":"cursor-1"}`, want: "unsupported sync selector kind everything"},
		{name: "recent without scope", body: `{"surface":"desktop","selector":{"kind":"recent"},"endpoint_cursor":"cursor-1"}`, want: "workset recent selector requires explicit workspace_path, workspace_paths, or global=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("stream status=%d want=%d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("stream error body %q does not contain %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSessionsV3SyncStreamUsesCanonicalBootstrapSelectorScope(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-canonical", "Sync Stream Canonical", "/workspace/stream-canonical")
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-canonical","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/stream-canonical"},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, withTestPrincipal(bootstrapReq))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                 `json:"snapshot_endpoint_cursor"`
		Selector               sessionsV3SyncSelector `json:"selector"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.Selector.WorkspacePath != "/workspace/stream-canonical" || bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap selector/cursor invalid: %+v", bootstrap)
	}
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "stream canonical replay")

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-canonical","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/stream-canonical"},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID {
			return
		}
	}
	t.Fatalf("stream using canonical selector missed appended event for %s: %+v", created.ID, stream.Events)
}

func TestSessionsV3SyncStreamResponseUsesStablePublicDTO(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-public-dto", "Sync Stream Public DTO", "/workspace/stream-public-dto")
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-public-dto","recent":{"limit":10}},"history":{"mode":"none"}}`
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
		t.Fatalf("bootstrap returned empty cursor")
	}
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "public dto replay")

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-public-dto","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(streamRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, forbidden := range []string{"after_endpoint_seq", "high_watermark_seq", "endpoint_seq"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("stream response leaked internal top-level field %q: %s", forbidden, streamRec.Body.String())
		}
	}
	endpointCursor, _ := payload["endpoint_cursor"].(string)
	if payload["ok"] != true || strings.TrimSpace(endpointCursor) == "" || strings.HasPrefix(endpointCursor, "cursor-") || !strings.HasPrefix(endpointCursor, "v3c1.") {
		t.Fatalf("stream response missing signed opaque public cursor fields: %s", streamRec.Body.String())
	}
	events, ok := payload["events"].([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("stream response missing public events: %s", streamRec.Body.String())
	}
	found := false
	for _, raw := range events {
		event, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("stream event is not object: %#v", raw)
		}
		for _, forbidden := range []string{"endpoint_seq", "endpoint_cursor", "user_id", "account_scope_id", "created_at"} {
			if _, ok := event[forbidden]; ok {
				t.Fatalf("stream event leaked raw outbox field %q: %#v", forbidden, event)
			}
		}
		if event["session_id"] == created.ID {
			found = true
			if strings.TrimSpace(event["event_type"].(string)) == "" || event["event"] == nil || event["projection"] == nil {
				t.Fatalf("stream event missing public event/projection fields: %#v", event)
			}
		}
	}
	if !found {
		t.Fatalf("stream response missed replay event for %s: %s", created.ID, streamRec.Body.String())
	}
}

func TestSessionsV3SyncStreamRecentWorkspaceSelectorFiltersReplay(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspaceA := "/workspace/stream-recent-a"
	workspaceB := "/workspace/stream-recent-b"
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-recent-a", "Sync Stream Recent A", workspaceA)
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-recent-b", "Sync Stream Recent B", workspaceB)

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"recent","workspace_path":"` + workspaceA + `","recent":{"limit":50}},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, withTestPrincipal(bootstrapReq))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                 `json:"snapshot_endpoint_cursor"`
		Selector               sessionsV3SyncSelector `json:"selector"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.SnapshotEndpointCursor == "" || bootstrap.Selector.Kind != "recent" || bootstrap.Selector.WorkspacePath != workspaceA {
		t.Fatalf("bootstrap selector/cursor invalid: %+v", bootstrap)
	}

	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdA.ID, "recent workspace A replay")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "recent workspace B replay")

	streamBody := `{"surface":"desktop","selector":{"kind":"recent","workspace_path":"` + workspaceA + `","recent":{"limit":50}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	foundA := false
	for _, event := range stream.Events {
		switch event.SessionID {
		case createdA.ID:
			foundA = true
		case createdB.ID:
			t.Fatalf("recent workspace stream leaked event for workspace B session %s: %+v", createdB.ID, stream.Events)
		}
	}
	if !foundA {
		t.Fatalf("recent workspace stream missed workspace A session %s: %+v", createdA.ID, stream.Events)
	}
}

func TestSessionsV3SyncStreamWorkspaceCursorAdvancesAcrossAThenBExactlyOnce(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := "/workspace/stream-ab-exact"
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-ab-a", "Sync Stream AB A", workspace)
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-ab-b", "Sync Stream AB B", workspace)

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"history":{"mode":"none"}}`
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

	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdA.ID, "A after bootstrap")
	streamBodyA := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReqA := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBodyA))
	streamReqA.Header.Set("Content-Type", "application/json")
	streamRecA := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRecA, withTestPrincipal(streamReqA))
	if streamRecA.Code != http.StatusOK {
		t.Fatalf("stream A status=%d body=%s", streamRecA.Code, streamRecA.Body.String())
	}
	var streamA struct {
		EndpointCursor string `json:"endpoint_cursor"`
		Events         []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRecA.Body.Bytes(), &streamA); err != nil {
		t.Fatalf("decode stream A: %v", err)
	}
	if countSessionV3StreamEvents(streamA.Events, createdA.ID) != 1 || countSessionV3StreamEvents(streamA.Events, createdB.ID) != 0 {
		t.Fatalf("stream A events mismatch: %+v body=%s", streamA.Events, streamRecA.Body.String())
	}

	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "B after A cursor")
	streamBodyB := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"endpoint_cursor":"` + streamA.EndpointCursor + `"}`
	streamReqB := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBodyB))
	streamReqB.Header.Set("Content-Type", "application/json")
	streamRecB := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRecB, withTestPrincipal(streamReqB))
	if streamRecB.Code != http.StatusOK {
		t.Fatalf("stream B status=%d body=%s", streamRecB.Code, streamRecB.Body.String())
	}
	var streamB struct {
		Events []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRecB.Body.Bytes(), &streamB); err != nil {
		t.Fatalf("decode stream B: %v", err)
	}
	if countSessionV3StreamEvents(streamB.Events, createdB.ID) != 1 || countSessionV3StreamEvents(streamB.Events, createdA.ID) != 0 {
		t.Fatalf("stream B events mismatch: %+v body=%s", streamB.Events, streamRecB.Body.String())
	}
}

func TestSessionsV3PrimaryDeleteRouteStreamsWorkspaceTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := "/workspace/stream-delete-tombstone"
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-delete-tombstone", "Sync Stream Delete Tombstone", workspace)
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"history":{"mode":"none"}}`
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

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+created.ID, nil)
	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, withTestPrincipal(deleteReq))
	if deleteRec.Code != http.StatusOK || !strings.Contains(deleteRec.Body.String(), `"ok":true`) || !strings.Contains(deleteRec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete response status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok, err := server.sessions.GetSession(created.ID); err != nil || ok {
		t.Fatalf("deleted session still visible ok=%v err=%v", ok, err)
	}

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID && event.EventType == "session.deleted" {
			return
		}
	}
	t.Fatalf("workspace stream missed delete tombstone event for %s: %+v body=%s", created.ID, stream.Events, streamRec.Body.String())
}

func TestSessionsV3PrimaryDeleteRouteRejectsOtherPrincipal(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-delete-other", "Sync Stream Delete Other", "/workspace/stream-delete-other")
	other := testPrincipal()
	other.UserID = "delete-route-other-user"
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+created.ID, nil)
	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, requestWithTestPrincipalForAccount(deleteReq, other.UserID, other.AccountScopeID))
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("other principal delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok, err := server.sessions.GetSession(created.ID); err != nil || !ok {
		t.Fatalf("other principal delete removed session ok=%v err=%v", ok, err)
	}
}

func TestSessionsV3PrimaryArchiveRouteStreamsWorkspaceTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := "/workspace/stream-archive-tombstone"
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-archive-tombstone", "Sync Stream Archive Tombstone", workspace)
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"history":{"mode":"none"}}`
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

	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/archive", nil)
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK || !strings.Contains(archiveRec.Body.String(), `"ok":true`) || !strings.Contains(archiveRec.Body.String(), `"archived":true`) {
		t.Fatalf("archive response status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}
	if strings.Contains(archiveRec.Body.String(), `"deleted":true`) {
		t.Fatalf("archive response used delete tombstone body=%s", archiveRec.Body.String())
	}
	if _, ok, err := server.sessions.GetSession(created.ID); err != nil || ok {
		t.Fatalf("archived session still visible ok=%v err=%v", ok, err)
	}
	if sessions, err := server.sessions.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("archived session still listed sessions=%+v err=%v", sessions, err)
	}
	tombstones, err := server.sessions.ListSessionTombstonesForAccount(testPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	var archived bool
	for _, tombstone := range tombstones {
		if tombstone.SessionID == created.ID {
			archived = tombstone.Kind == "archived" && tombstone.Archived && !tombstone.Deleted
		}
	}
	if !archived {
		t.Fatalf("archive tombstone missing or not archived: %+v", tombstones)
	}

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID && event.EventType == "session.archived" {
			return
		}
	}
	t.Fatalf("workspace stream missed archive tombstone event for %s: %+v body=%s", created.ID, stream.Events, streamRec.Body.String())
}

func TestSessionsV3PrimaryBatchArchiveRouteArchivesMultipleSessions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := "/workspace/stream-batch-archive"
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-batch-archive-a", "Batch Archive A", workspace)
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-batch-archive-b", "Batch Archive B", workspace)

	body := fmt.Sprintf(`{"session_ids":[%q,%q,%q]}`, createdA.ID, createdB.ID, createdA.ID)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch archive status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Archived bool `json:"archived"`
		Results  []struct {
			SessionID string                         `json:"session_id"`
			Archived  bool                           `json:"archived"`
			Tombstone pebblestore.V3SessionTombstone `json:"tombstone"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode batch archive response: %v", err)
	}
	if !response.Archived || len(response.Results) != 2 || response.Results[0].SessionID != createdA.ID || response.Results[1].SessionID != createdB.ID {
		t.Fatalf("batch archive response = %+v", response)
	}
	for _, result := range response.Results {
		if !result.Archived || result.Tombstone.Kind != "archived" || !result.Tombstone.Archived {
			t.Fatalf("batch archive result = %+v", result)
		}
	}
	if sessions, err := server.sessions.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("active sessions after batch archive = %+v err=%v", sessions, err)
	}
	tombstones, err := server.sessions.ListSessionTombstonesForAccount(testPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	if len(tombstones) != 2 {
		t.Fatalf("batch archive tombstones = %+v", tombstones)
	}
}

func TestSessionsV3SyncStreamWorkspaceUsesDurableMembershipForRunIntentRecord(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	workspace := "/workspace/stream-run-intent-membership"
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-run-intent-membership", "Sync Stream Run Intent Membership", workspace)
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"history":{"mode":"none"}}`
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
	now := time.Now().UnixMilli()
	_, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "run-intent-membership",
		IdempotencyKey:  "run-intent-membership",
		PayloadHash:     "hash-run-intent-membership",
		RequestHash:     "hash-run-intent-membership",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:       &pebblestore.V3SessionRunIntent{SessionID: created.ID, RunID: "run-intent-membership", Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now},
		NowUnixMs:       now,
	})
	if err != nil {
		t.Fatalf("record run intent: %v", err)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspace + `","recent":{"limit":20}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID && event.EventType == "session.run_intent.recorded" {
			return
		}
	}
	t.Fatalf("workspace stream missed run_intent record for %s: %+v", created.ID, stream.Events)
}

func TestSessionsV3SyncStreamWorkspaceFailsClosedWhenMembershipUnavailable(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-missing-membership", "Sync Stream Missing Membership", "/workspace/stream-missing-membership")
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-missing-membership","recent":{"limit":20}},"history":{"mode":"none"}}`
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
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "membership unavailable")
	previousListOutbox := v3SyncStreamListRealtimeOutboxAfter
	v3SyncStreamListRealtimeOutboxAfter = func(_ *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
		records, err := sessionSvc.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
		if err != nil || len(records) == 0 {
			return records, err
		}
		records[0].Membership = nil
		records[0].Event.Payload = []byte(`{"session_id":"` + records[0].SessionID + `","seq":2,"kind":"message.append"}`)
		return records, nil
	}
	t.Cleanup(func() { v3SyncStreamListRealtimeOutboxAfter = previousListOutbox })

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-missing-membership","recent":{"limit":20}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusGone || !strings.Contains(streamRec.Body.String(), "endpoint_membership_unavailable") || !strings.Contains(streamRec.Body.String(), `"ok":false`) {
		t.Fatalf("missing membership response status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
}

func TestSessionsV3SyncStreamReplaysPastOtherUserRowsFromGlobalOutbox(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	userA := testPrincipal()
	userB := testPrincipal()
	userB.UserID = "sync-stream-other-user"
	workspaceA := "/workspace/stream-global-outbox-a"
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-global-outbox-a", "Sync Stream Global Outbox A", workspaceA)
	createdB := createSessionForPrincipalWithWorkspace(t, sessionSvc, userB, "sync-stream-global-outbox-b", "/workspace/stream-global-outbox-b")

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspaceA + `","recent":{"limit":10}},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, requestWithTestPrincipalForAccount(bootstrapReq, userA.UserID, userA.AccountScopeID))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string `json:"snapshot_endpoint_cursor"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}

	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, userB, createdB.ID, "other user global gap")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdA.ID, "visible after global gap")

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspaceA + `","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `","limit":1}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, requestWithTestPrincipalForAccount(streamReq, userA.UserID, userA.AccountScopeID))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var first struct {
		EndpointCursor string `json:"endpoint_cursor"`
		Events         []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first stream: %v", err)
	}
	if len(first.Events) != 0 || !first.HasMore || first.EndpointCursor == bootstrap.SnapshotEndpointCursor {
		t.Fatalf("first stream page should advance across invisible global row without leaking: %+v", first)
	}

	secondBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"` + workspaceA + `","recent":{"limit":10}},"endpoint_cursor":"` + first.EndpointCursor + `","limit":10}`
	secondReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(secondBody))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(secondRec, requestWithTestPrincipalForAccount(secondReq, userA.UserID, userA.AccountScopeID))
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second stream status=%d body=%s", secondRec.Code, secondRec.Body.String())
	}
	var second struct {
		Events []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second stream: %v", err)
	}
	for _, event := range second.Events {
		switch event.SessionID {
		case createdA.ID:
			return
		case createdB.ID:
			t.Fatalf("global outbox stream leaked other user event: %+v", second.Events)
		}
	}
	t.Fatalf("global outbox stream missed visible event after invisible row: %+v", second.Events)
}

func TestSessionsV3SyncStreamRejectsTamperedCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
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
		t.Fatalf("bootstrap returned empty cursor")
	}
	tampered := bootstrap.SnapshotEndpointCursor[:len(bootstrap.SnapshotEndpointCursor)-1]
	if strings.HasSuffix(bootstrap.SnapshotEndpointCursor, "A") {
		tampered += "B"
	} else {
		tampered += "A"
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + tampered + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusBadRequest || !strings.Contains(streamRec.Body.String(), "endpoint_cursor_tampered") {
		t.Fatalf("tampered cursor response status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
}

func TestSessionsV3SyncStreamRejectsGlobalOutboxGap(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-gap", "Sync Stream Gap", "/workspace/stream-gap")
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
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

	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, testPrincipal(), created.ID, "gap first hidden")
	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, testPrincipal(), created.ID, "gap second visible")
	previousListOutbox := v3SyncStreamListRealtimeOutboxAfter
	v3SyncStreamListRealtimeOutboxAfter = func(_ *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
		records, err := sessionSvc.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
		if err != nil || len(records) == 0 {
			return records, err
		}
		return records[1:], nil
	}
	t.Cleanup(func() { v3SyncStreamListRealtimeOutboxAfter = previousListOutbox })

	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusGone || !strings.Contains(streamRec.Body.String(), "endpoint_cursor_gap") || !strings.Contains(streamRec.Body.String(), `"ok":false`) {
		t.Fatalf("gap response status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
}

func TestSessionsV3SyncStreamRejectsMissingTailFromGlobalOutbox(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-tail-gap", "Sync Stream Tail Gap", "/workspace/stream-tail-gap")
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
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

	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, testPrincipal(), created.ID, "tail first")
	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, testPrincipal(), created.ID, "tail second missing")
	appendSessionsV3PrimaryMessageForPrincipalTest(t, sessionSvc, testPrincipal(), created.ID, "tail third missing")
	previousListOutbox := v3SyncStreamListRealtimeOutboxAfter
	v3SyncStreamListRealtimeOutboxAfter = func(_ *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
		records, err := sessionSvc.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
		if err != nil || len(records) <= 1 {
			return records, err
		}
		return records[:1], nil
	}
	t.Cleanup(func() { v3SyncStreamListRealtimeOutboxAfter = previousListOutbox })

	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `","limit":500}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusGone || !strings.Contains(streamRec.Body.String(), "endpoint_cursor_gap") || !strings.Contains(streamRec.Body.String(), `"missing_endpoint_seq"`) {
		t.Fatalf("tail gap response status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
}

func TestSessionsV3SyncStreamRejectsAheadAndTooOldCursors(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-stream-retention", "Sync Stream Retention", "/workspace/stream-retention")
	principal := testPrincipal()
	selector := sessionsV3SyncSelector{Kind: "global", Global: true}
	resources := sessionsV3SyncResourceSet(sessionsV3WorksetResources{}, sessionsV3WorksetHistory{Mode: "none"}, false)
	scope := v3SyncCursorScopeForSnapshot(principal, "desktop", "v3.sync.snapshot", selector, resources)
	currentHead, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatalf("current head: %v", err)
	}
	aheadCursor, err := server.signV3SyncEndpointCursor(scope, currentHead+10)
	if err != nil {
		t.Fatalf("sign ahead cursor: %v", err)
	}

	aheadBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + aheadCursor + `"}`
	aheadReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(aheadBody))
	aheadReq.Header.Set("Content-Type", "application/json")
	aheadRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(aheadRec, withTestPrincipal(aheadReq))
	if aheadRec.Code != http.StatusGone || !strings.Contains(aheadRec.Body.String(), "endpoint_cursor_ahead") || !strings.Contains(aheadRec.Body.String(), "bootstrap_required") {
		t.Fatalf("ahead cursor response status=%d body=%s", aheadRec.Code, aheadRec.Body.String())
	}

	retentionBoundary := currentHead + 1
	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{OldestRetainedRealtimeEndpointSeq: retentionBoundary, RealtimePrunedThroughEndpointSeq: retentionBoundary - 1, UpdatedAtUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	boundaryCursor, err := server.signV3SyncEndpointCursor(scope, currentHead)
	if err != nil {
		t.Fatalf("sign boundary cursor: %v", err)
	}
	boundaryBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + boundaryCursor + `"}`
	boundaryReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(boundaryBody))
	boundaryReq.Header.Set("Content-Type", "application/json")
	boundaryRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(boundaryRec, withTestPrincipal(boundaryReq))
	if boundaryRec.Code != http.StatusOK {
		t.Fatalf("boundary cursor response status=%d body=%s, want replayable cursor oldest_available-1", boundaryRec.Code, boundaryRec.Body.String())
	}
	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{OldestRetainedRealtimeEndpointSeq: currentHead + 2, RealtimePrunedThroughEndpointSeq: currentHead + 1, UpdatedAtUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	tooOldCursor, err := server.signV3SyncEndpointCursor(scope, currentHead)
	if err != nil {
		t.Fatalf("sign too-old cursor: %v", err)
	}
	tooOldBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + tooOldCursor + `"}`
	tooOldReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(tooOldBody))
	tooOldReq.Header.Set("Content-Type", "application/json")
	tooOldRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(tooOldRec, withTestPrincipal(tooOldReq))
	if tooOldRec.Code != http.StatusGone || !strings.Contains(tooOldRec.Body.String(), "endpoint_cursor_too_old") || !strings.Contains(tooOldRec.Body.String(), "oldest_available") {
		t.Fatalf("too-old cursor response status=%d body=%s", tooOldRec.Code, tooOldRec.Body.String())
	}
}

func countSessionV3StreamEvents(events []struct {
	SessionID string `json:"session_id"`
}, sessionID string) int {
	count := 0
	for _, event := range events {
		if event.SessionID == sessionID {
			count++
		}
	}
	return count
}

func createSessionForPrincipalWithWorkspace(t *testing.T, sessionSvc *sessionruntime.Service, principal identity.Principal, sessionID, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	now := time.Now().UnixMilli()
	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "create-" + sessionID,
		IdempotencyKey:  "create-" + sessionID,
		PayloadHash:     "hash-create-" + sessionID,
		RequestHash:     "hash-create-" + sessionID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			WorkspacePath:  workspacePath,
			WorkspaceName:  strings.Trim(workspacePath, "/"),
			Title:          sessionID,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		NowUnixMs: now,
	})
	if err != nil {
		t.Fatalf("create session %s: %v", sessionID, err)
	}
	if result.Session == nil {
		t.Fatalf("create session %s returned nil session", sessionID)
	}
	return *result.Session
}

func appendSessionsV3PrimaryMessageForPrincipalTest(t *testing.T, sessionSvc *sessionruntime.Service, principal identity.Principal, sessionID, content string) {
	t.Helper()
	now := time.Now().UnixMilli()
	messageID := sessionID + "-message-" + strings.ReplaceAll(content, " ", "-")
	message := pebblestore.MessageSnapshot{ID: messageID, SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Role: "user", Content: content, CreatedAt: now}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: messageID,
		IdempotencyKey:  messageID,
		PayloadHash:     "hash-" + messageID,
		RequestHash:     "hash-" + messageID,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("append message for principal %s session %s: %v", principal.UserID, sessionID, err)
	}
}
