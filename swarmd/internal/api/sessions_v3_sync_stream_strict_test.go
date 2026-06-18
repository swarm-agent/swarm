package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
