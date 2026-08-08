package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionDumpRequiresDevMode(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	dataDir := t.TempDir()
	server.SetDataDir(dataDir)
	writeSessionDumpStartupConfig(t, server.startupConfigPath, false)
	createSessionDumpTestSession(t, server, sessionSvc, "dump-disabled")

	rec := requestSessionDump(t, server, "dump-disabled")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if entries, err := os.ReadDir(dataDir); err != nil || len(entries) != 0 {
		t.Fatalf("disabled request wrote data: entries=%v err=%v", entries, err)
	}
}

func TestSessionDumpWritesReadablePrivateFile(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	dataDir := t.TempDir()
	server.SetDataDir(dataDir)
	writeSessionDumpStartupConfig(t, server.startupConfigPath, true)
	createSessionDumpTestSession(t, server, sessionSvc, "dump-readable")

	rec := requestSessionDump(t, server, "dump-readable")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.SessionID != "dump-readable" {
		t.Fatalf("unexpected response: %+v", response)
	}
	wantDir := filepath.Join(dataDir, sessionDumpDirectoryName)
	if filepath.Dir(response.Path) != wantDir {
		t.Fatalf("dump path = %q, want directory %q", response.Path, wantDir)
	}
	info, err := os.Stat(response.Path)
	if err != nil {
		t.Fatalf("stat dump: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("dump mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("stat dump directory: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dump directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	var dump sessionDumpFile
	payload, err := os.ReadFile(response.Path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if err := json.Unmarshal(payload, &dump); err != nil {
		t.Fatalf("decode dump: %v", err)
	}
	if dump.Session.ID != "dump-readable" || len(dump.Messages) != 1 || dump.Messages[0].Content != "readable message" {
		t.Fatalf("dump missing session history: session=%q messages=%+v", dump.Session.ID, dump.Messages)
	}
	if len(dump.Events) < 2 || dump.Projection.SessionID != "dump-readable" {
		t.Fatalf("dump missing V3 records: projection=%+v events=%d", dump.Projection, len(dump.Events))
	}
}

func TestSessionDumpUnknownSessionIsNotWritten(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	dataDir := t.TempDir()
	server.SetDataDir(dataDir)
	writeSessionDumpStartupConfig(t, server.startupConfigPath, true)

	rec := requestSessionDump(t, server, "missing-session")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, sessionDumpDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("failed request created dump directory: %v", err)
	}
}

func TestSessionDumpCanDumpAnySessionInDevMode(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	dataDir := t.TempDir()
	server.SetDataDir(dataDir)
	writeSessionDumpStartupConfig(t, server.startupConfigPath, true)
	createForeignSessionDumpTestSession(t, server, "dump-foreign")

	rec := requestSessionDump(t, server, "dump-foreign")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	payload, err := os.ReadFile(response.Path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var dump sessionDumpFile
	if err := json.Unmarshal(payload, &dump); err != nil {
		t.Fatalf("decode dump: %v", err)
	}
	if dump.Session.ID != "dump-foreign" {
		t.Fatalf("dump session = %q, want dump-foreign", dump.Session.ID)
	}
}

func requestSessionDump(t *testing.T, server *Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"session_id":"` + sessionID + `"}`)
	req := httptest.NewRequest(http.MethodPost, SessionDumpPath, body)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func writeSessionDumpStartupConfig(t *testing.T, path string, devMode bool) {
	t.Helper()
	cfg := startupconfig.Default(path)
	cfg.DevMode = devMode
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
}

func createSessionDumpTestSession(t *testing.T, server *Server, sessionSvc *sessionruntime.Service, sessionID string) {
	t.Helper()
	principal := testPrincipal()
	now := time.Now().UnixMilli()
	snapshot := pebblestore.SessionSnapshot{
		ID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: "/workspace/session-dump", Title: "Session dump", Mode: sessionruntime.ModeAuto,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: "create-" + sessionID, IdempotencyKey: "create-" + sessionID,
		PayloadHash: "create-" + sessionID, RequestHash: "create-" + sessionID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, NowUnixMs: now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	message := pebblestore.MessageSnapshot{
		ID: "message-" + sessionID, SessionID: sessionID, UserID: principal.UserID,
		AccountScopeID: principal.AccountScopeID, Role: "user", Content: "readable message", CreatedAt: now + 1,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: "message-" + sessionID, IdempotencyKey: "message-" + sessionID,
		PayloadHash: "message-" + sessionID, RequestHash: "message-" + sessionID,
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: now + 1,
	}); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if _, ok, err := sessionSvc.GetSession(sessionID); err != nil || !ok {
		t.Fatalf("get created session: ok=%t err=%v", ok, err)
	}
}

func createForeignSessionDumpTestSession(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	now := time.Now().UnixMilli()
	snapshot := pebblestore.SessionSnapshot{
		ID: sessionID, UserID: "foreign-user", AccountScopeID: "foreign-account",
		WorkspacePath: "/workspace/foreign", Title: "Foreign", Mode: sessionruntime.ModeAuto,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID,
		ClientRequestID: "create-" + sessionID, IdempotencyKey: "create-" + sessionID,
		PayloadHash: "create-" + sessionID, RequestHash: "create-" + sessionID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, NowUnixMs: now,
	}); err != nil {
		t.Fatalf("create foreign session: %v", err)
	}
}
