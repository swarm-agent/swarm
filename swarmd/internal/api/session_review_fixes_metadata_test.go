package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: worker_review_fixes is daemon-only authority. Only the worker
// execution service pins it on a reviewer session that a job automation step
// opted into fixes. The sandbox registration bypass, the per-call identity
// check, the workspace scope and repository authorization all read it.
// A client can neither set it when creating a session nor change it through
// the metadata route. An ordinary metadata update on a fixing reviewer's
// session keeps it, so later runs and recovery do not fail on a dropped pin.
// Threat: a client granting itself the fixing reviewer's writable identity,
// or an unrelated metadata write silently erasing the pin.
// Boundary: isProtectedSessionsV3MetadataKey via
// validateSessionsV3CreateMetadata (handleSessionsV3PrimaryCreate) and
// handleSessionV3PrimaryMetadata / mergeSessionsV3MetadataUpdate.
// Narrowest layer: the create and metadata handlers over a real store.
func TestSessionsV3WorkerReviewFixesIsDaemonOnlyMetadata(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := filepath.Join(t.TempDir(), "scratch")
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	principal := testPrincipal()
	create := func(requestID string, metadata map[string]any) (int, map[string]any) {
		t.Helper()
		payload := map[string]any{"client_request_id": requestID, "workspace_path": workspace, "workspace_name": "scratch", "swarm_id": "host-swarm-id",
			"workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self", "title": "Review", "mode": "auto", "agent_name": "swarm"}
		if metadata != nil {
			payload["metadata"] = metadata
		}
		raw, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		server.handleSessionsV3PrimaryCreate(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))), principal)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Create refuses it and stores nothing.
	if code, out := create("fixes-refused", map[string]any{"worker_review_fixes": "security"}); code != http.StatusBadRequest {
		t.Fatalf("create with worker_review_fixes: %d %v, want 400", code, out)
	}
	if _, found, _ := sessions.GetSession(stableSessionsV3PrimarySessionID(principal, "fixes-refused")); found {
		t.Fatal("a refused create stored a session")
	}

	code, created := create("fixes-session", nil)
	if code != http.StatusOK {
		t.Fatalf("plain create: %d %v", code, created)
	}
	sessionID, _ := created["session_id"].(string)
	metadata := func(body string) (int, string) {
		w := httptest.NewRecorder()
		server.handleSessionV3PrimaryMetadata(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/metadata", strings.NewReader(body))), principal, sessionID)
		return w.Code, w.Body.String()
	}
	pinned := func() any {
		t.Helper()
		snapshot, ok, err := sessions.GetSession(sessionID)
		if err != nil || !ok {
			t.Fatalf("session: %v %v", ok, err)
		}
		return snapshot.Metadata["worker_review_fixes"]
	}

	// A client cannot set it on an existing session.
	if code, body := metadata(`{"client_request_id":"meta-grant","metadata":{"worker_review_fixes":"security"}}`); code != http.StatusBadRequest || !strings.Contains(body, "reserved") {
		t.Fatalf("metadata grant of worker_review_fixes: %d %s, want 400", code, body)
	}
	if got := pinned(); got != nil {
		t.Fatalf("a refused metadata update set worker_review_fixes = %v", got)
	}

	// The daemon pins it (as startPlan does). An ordinary update keeps it,
	// and a client can neither change nor clear it.
	snapshot, _, err := sessions.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	daemon := map[string]any{}
	for k, v := range snapshot.Metadata {
		daemon[k] = v
	}
	daemon["worker_review_fixes"] = "security"
	if _, _, err := sessions.UpdateMetadata(sessionID, daemon); err != nil {
		t.Fatal(err)
	}
	if got := pinned(); got != "security" {
		t.Fatalf("daemon pin = %v", got)
	}
	if code, body := metadata(`{"client_request_id":"meta-note","metadata":{"note":"x"}}`); code != http.StatusOK {
		t.Fatalf("unrelated metadata change: %d %s", code, body)
	}
	if got := pinned(); got != "security" {
		t.Fatalf("an ordinary metadata update dropped worker_review_fixes: %v", got)
	}
	if code, _ := metadata(`{"client_request_id":"meta-clear","metadata":{"worker_review_fixes":""}}`); code != http.StatusBadRequest {
		t.Fatalf("metadata clear of worker_review_fixes: %d, want 400", code)
	}
	if got := pinned(); got != "security" {
		t.Fatalf("a refused clear changed worker_review_fixes: %v", got)
	}
	if merged := mergeSessionsV3MetadataUpdate(map[string]any{"worker_review_fixes": "security", "note": "old"}, map[string]any{"worker_review_fixes": "", "note": "new"}); merged["worker_review_fixes"] != "security" || merged["note"] != "new" {
		t.Fatalf("merge = %+v", merged)
	}
}
