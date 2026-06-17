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
	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-canonical","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/other"},"history":{"mode":"none"}}`
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

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/stream-canonical","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/other"},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
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
