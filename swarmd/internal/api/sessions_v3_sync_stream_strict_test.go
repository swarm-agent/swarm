package api

import (
	"bytes"
	"encoding/json"
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
		EndpointCursor   string `json:"endpoint_cursor"`
		HighWatermarkSeq uint64 `json:"high_watermark_seq"`
		Events           []struct {
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

func TestSessionsV3SyncStreamRejectsAheadAndTooOldCursors(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
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
	server.v3RealtimeRetentionBoundary = func() (uint64, error) { return retentionBoundary, nil }
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
