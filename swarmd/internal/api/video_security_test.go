package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/videotranscription"
	"swarm/packages/swarmd/internal/workspace"
)

func TestVideoThreadHandlersRequireProductPrincipal(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-auth.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	server := &Server{}
	server.SetVideoThreadStore(pebblestore.NewVideoThreadStore(db))

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/workspace/video/threads?workspace_path=/workspace", nil),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/scan", bytes.NewReader([]byte(`{"workspace_path":"/workspace","folder_path":"/workspace"}`))),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/transcribe", bytes.NewReader([]byte(`{"workspace_path":"/workspace","video_ref":"videosrc_test"}`))),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/transcribe/status", bytes.NewReader([]byte(`{"workspace_path":"/workspace","session_id":"session","job_ref":"trjob_test"}`))),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/transcribe/read", bytes.NewReader([]byte(`{"workspace_path":"/workspace","session_id":"session","transcript_ref":"transcript_test"}`))),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/transcribe/cancel", bytes.NewReader([]byte(`{"workspace_path":"/workspace","session_id":"session","job_ref":"trjob_test"}`))),
		httptest.NewRequest(http.MethodPost, "/v1/workspace/video/storage/reveal?thread_id=thread", nil),
	} {
		recorder := httptest.NewRecorder()
		switch request.URL.Path {
		case "/v1/workspace/video/threads":
			server.handleWorkspaceVideoThreads(recorder, request)
		case "/v1/workspace/video/scan":
			server.handleWorkspaceVideoScan(recorder, request)
		case "/v1/workspace/video/transcribe":
			server.handleWorkspaceVideoTranscribe(recorder, request)
		case "/v1/workspace/video/transcribe/status":
			server.handleWorkspaceVideoTranscribeStatus(recorder, request)
		case "/v1/workspace/video/transcribe/read":
			server.handleWorkspaceVideoTranscribeRead(recorder, request)
		case "/v1/workspace/video/transcribe/cancel":
			server.handleWorkspaceVideoTranscribeCancel(recorder, request)
		default:
			server.handleVideoStorageReveal(recorder, request)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d body = %s", request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestDirectVideoTranscriptionRejectsOversizedRequestBody(t *testing.T) {
	principal := accountTestPrincipal()
	workspacePath := t.TempDir()
	server, _ := newVideoWorkspaceSecurityServer(t, principal, workspacePath)
	payload := `{"workspace_path":"` + workspacePath + `","video_ref":"videosrc_` + strings.Repeat("a", 64) + `","focus_notes":"` + strings.Repeat("x", directVideoRequestMaxBytes) + `"}`
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/video/transcribe", strings.NewReader(payload)))
	recorder := httptest.NewRecorder()
	server.handleWorkspaceVideoTranscribe(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "request body exceeds") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSafeDirectVideoTranscriptBoundsOutputAndOmitsProviderIdentity(t *testing.T) {
	segments := make([]pebblestore.NormalizedTranscriptSegment, directVideoTranscriptMaxSegments+1)
	for index := range segments {
		segments[index] = pebblestore.NormalizedTranscriptSegment{StartMs: int64(index), EndMs: int64(index + 1), Visual: "frame", Text: "frame"}
	}
	transcript := pebblestore.NormalizedTranscript{
		Ref: "transcript_ref", JobRef: "trjob_ref", SchemaVersion: pebblestore.NormalizedTranscriptSchemaVersion,
		Text: strings.Repeat("é", directVideoTranscriptMaxBytes), Segments: segments,
		Metadata: pebblestore.NormalizedTranscriptMetadata{ProviderID: "google", Model: "secret-model", ModelSnapshot: "private-snapshot", MediaSettingsHash: "private-hash"},
	}
	encoded, err := json.Marshal(safeDirectVideoTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"details_truncated":true`) || !strings.Contains(body, `"segments_truncated":true`) || !strings.Contains(body, `"text_truncated":true`) {
		t.Fatalf("missing truncation markers: %s", body)
	}
	for _, private := range []string{"google", "secret-model", "private-snapshot", "private-hash"} {
		if strings.Contains(body, private) {
			t.Fatalf("response exposed private provider identity %q: %s", private, body)
		}
	}
}

func TestResolveDirectVideoSessionRejectsOtherUserSession(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "direct-video-session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := accountTestPrincipal()
	workspacePath := t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(db))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}
	sessions := pebblestore.NewSessionStore(db)
	sessionService := session.NewService(sessions, nil)
	server := NewServer(nil, nil, nil, nil, sessionService, workspaceService, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	server.SetVideoTranscriptionService(&videotranscription.Service{})
	now := time.Now().UnixMilli()
	foreign := pebblestore.SessionSnapshot{
		ID: "foreign-direct-session", UserID: "other-user", AccountScopeID: principal.AccountScopeID,
		WorkspacePath: workspacePath, Metadata: map[string]any{"workspace_id": "wrong", "system_session": true, "navigation_hidden": true, "settings_locked": true}, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: foreign.ID, UserID: foreign.UserID, AccountScopeID: foreign.AccountScopeID,
		ClientRequestID: "create-foreign", Kind: pebblestore.V3SessionMutationCreateSession, Session: &foreign,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveDirectVideoSession(principal, workspacePath, foreign.ID); err == nil {
		t.Fatal("expected cross-user direct transcription session rejection")
	}
}

func TestVideoThreadStoreSeparatesAccountsAndRejectsLegacyRecords(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-account.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	store := pebblestore.NewVideoThreadStore(db)
	for _, account := range []string{"account-one", "account-two"} {
		if _, err := store.CreateForAccount(account, "user", pebblestore.VideoThreadSnapshot{ID: "shared", WorkspacePath: "/workspace", Title: account}); err != nil {
			t.Fatalf("create %s: %v", account, err)
		}
	}
	one, ok, err := store.GetForAccount("account-one", "shared")
	if err != nil || !ok || one.Title != "account-one" {
		t.Fatalf("account one thread = %+v ok=%v err=%v", one, ok, err)
	}
	if err := db.PutJSON(pebblestore.KeyVideoThread("legacy"), pebblestore.VideoThreadSnapshot{ID: "legacy", WorkspacePath: "/workspace"}); err != nil {
		t.Fatalf("put legacy: %v", err)
	}
	if _, ok, err := store.GetForAccount("account-one", "legacy"); err != nil || ok {
		t.Fatalf("legacy account lookup ok=%v err=%v", ok, err)
	}
}

func TestOpenManagedVideoClipRejectsEscapeAndSymlink(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	workspacePath := t.TempDir()
	root, err := ensureWorkspaceToolStorage(workspacePath, "video", "thread")
	if err != nil {
		t.Fatalf("ensure storage: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	thread := pebblestore.VideoThreadSnapshot{ID: "thread", WorkspacePath: workspacePath, Metadata: map[string]any{"tool_storage_path": root}}
	if _, _, err := openManagedVideoClip(thread, outside); err == nil {
		t.Fatal("expected outside clip rejection")
	}
	link := filepath.Join(root, "link.mp4")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, _, err := openManagedVideoClip(thread, link); err == nil {
		t.Fatal("expected symlink clip rejection")
	}
}

func newVideoWorkspaceSecurityServer(t *testing.T, principal identity.Principal, workspacePath string) (*Server, *pebblestore.VideoThreadStore) {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-workspace.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(db))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	server := NewServer(nil, nil, nil, nil, nil, workspaceService, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	videoStore := pebblestore.NewVideoThreadStore(db)
	server.SetVideoThreadStore(videoStore)
	return server, videoStore
}

func TestVideoScanRejectsCrossWorkspaceFolder(t *testing.T) {
	principal := accountTestPrincipal()
	workspacePath := t.TempDir()
	server, _ := newVideoWorkspaceSecurityServer(t, principal, workspacePath)
	payload, _ := json.Marshal(map[string]any{"workspace_path": workspacePath, "folder_path": t.TempDir()})
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/video/scan", bytes.NewReader(payload)))
	recorder := httptest.NewRecorder()
	server.handleWorkspaceVideoScan(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestVideoThreadCreateRejectsUnownedWorkspace(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	principal := accountTestPrincipal()
	owned := t.TempDir()
	server, _ := newVideoWorkspaceSecurityServer(t, principal, owned)
	payload, _ := json.Marshal(map[string]any{"workspace_path": t.TempDir(), "title": "escape"})
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/video/threads", bytes.NewReader(payload)))
	recorder := httptest.NewRecorder()
	server.handleWorkspaceVideoThreads(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}
