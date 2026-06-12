package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3TUIDirectoryCreateOpenAndWorkset(t *testing.T) {
	t.Setenv("SWARM_V3_DIAGNOSTICS", "0")
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV3PrimaryAuthority(t, server, "/workspace/known")
	cwd := filepath.Join(t.TempDir(), "cwd-only")

	created := postSessionsV3TUIDirectoryCreate(t, server, "tui-dir-create", cwd, "TUI Directory")
	if created.Session.WorkspacePath != cwd || created.Session.WorkspaceName != filepath.Base(cwd) || created.Session.Title != "TUI Directory" {
		t.Fatalf("created session = %+v", created.Session)
	}
	if created.Session.AccountScopeID != testPrincipal().AccountScopeID || created.Session.UserID != testPrincipal().UserID {
		t.Fatalf("session principal = %+v", created.Session)
	}
	if created.Session.Metadata["purpose"] != "cp3" || created.Session.Metadata["swarm_v3_tui_directory_session"] != true || created.Session.Metadata["swarm_v3_tui_cwd_path"] != cwd {
		t.Fatalf("tui cwd metadata = %+v", created.Session.Metadata)
	}
	if created.Session.Metadata["swarm_v3_workspace_binding_id"] != nil || created.Session.Metadata["local_workspace_binding_id"] != nil || created.Session.Metadata["swarm_v3_source_workspace_path"] != nil {
		t.Fatalf("directory session spoofed workspace binding metadata: %+v", created.Session.Metadata)
	}
	if created.Projection.LastEventSeq != 1 || created.Mutation.Event.EventType != "session.created" {
		t.Fatalf("projection/mutation = %+v %+v", created.Projection, created.Mutation)
	}
	if _, ok, err := sessionSvc.Store().GetSessionExecutionV2(created.Session.ID); err != nil || ok {
		t.Fatalf("v2 execution ok=%t err=%v, want none", ok, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}

	openReq := httptest.NewRequest(http.MethodGet, "/v3/tui/sessions/"+created.Session.ID+"?cwd_path="+cwd, nil)
	openRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(openRec, withTestPrincipal(openReq))
	if openRec.Code != http.StatusOK {
		t.Fatalf("open status = %d, want %d, body=%s", openRec.Code, http.StatusOK, openRec.Body.String())
	}
	var opened struct {
		OK      bool                          `json:"ok"`
		Session pebblestore.SessionSnapshot   `json:"session"`
		Events  []sessionruntime.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal(openRec.Body.Bytes(), &opened); err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	if !opened.OK || opened.Session.ID != created.Session.ID || len(opened.Events) != 0 {
		t.Fatalf("opened = %+v", opened)
	}

	wrongOpenReq := httptest.NewRequest(http.MethodGet, "/v3/tui/sessions/"+created.Session.ID+"?cwd_path="+filepath.Join(t.TempDir(), "other"), nil)
	wrongOpenRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongOpenRec, withTestPrincipal(wrongOpenReq))
	if wrongOpenRec.Code != http.StatusNotFound {
		t.Fatalf("wrong cwd open status = %d, want %d, body=%s", wrongOpenRec.Code, http.StatusNotFound, wrongOpenRec.Body.String())
	}

	workset := postSessionsV3TUIWorksetForTest(t, server, http.StatusOK, map[string]any{
		"scope":  map[string]any{"cwd_path": cwd},
		"recent": map[string]any{"limit": 10},
	})
	assertSessionsV3WorksetIDs(t, workset, created.Session.ID)
}

func TestSessionsV3TUIDirectoryRebindsToWorkspaceIntentionally(t *testing.T) {
	t.Setenv("SWARM_V3_DIAGNOSTICS", "0")
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV3PrimaryAuthority(t, server, "/workspace/known")
	cwd := filepath.Join(t.TempDir(), "later-workspace")
	created := postSessionsV3TUIDirectoryCreate(t, server, "tui-dir-rebind-create", cwd, "TUI Rebind")

	bindingID := seedSessionsV3PrimaryAuthority(t, server, cwd)
	rebindBody := fmt.Sprintf(`{"client_request_id":"tui-dir-rebind","cwd_path":%q,"workspace_path":%q,"swarm_id":"host-swarm-id","workspace_binding_id":%q}`, cwd, cwd, bindingID)
	rebindReq := httptest.NewRequest(http.MethodPost, "/v3/tui/sessions/"+created.Session.ID+"/rebind", bytes.NewBufferString(rebindBody))
	rebindReq.Header.Set("Content-Type", "application/json")
	rebindRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rebindRec, withTestPrincipal(rebindReq))
	if rebindRec.Code != http.StatusOK {
		t.Fatalf("rebind status = %d, want %d, body=%s", rebindRec.Code, http.StatusOK, rebindRec.Body.String())
	}
	var rebound struct {
		OK         bool                                 `json:"ok"`
		Session    pebblestore.SessionSnapshot          `json:"session"`
		Projection sessionruntime.SessionProjection     `json:"projection"`
		Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(rebindRec.Body.Bytes(), &rebound); err != nil {
		t.Fatalf("decode rebind response: %v", err)
	}
	if !rebound.OK || rebound.Session.ID != created.Session.ID || rebound.Mutation.Event.EventType != "session.tui.rebound" || rebound.Projection.LastEventSeq != 2 {
		t.Fatalf("rebound payload = %+v", rebound)
	}
	metadata := rebound.Session.Metadata
	if metadata["swarm_v3_tui_directory_session"] != false || metadata["swarm_v3_tui_original_cwd_path"] != cwd {
		t.Fatalf("rebound tui metadata = %+v", metadata)
	}
	if _, ok := metadata["swarm_v3_tui_cwd_path"]; ok {
		t.Fatalf("rebound metadata retained cwd marker: %+v", metadata)
	}
	if metadata["swarm_v3_workspace_binding_id"] != bindingID || metadata["local_workspace_binding_id"] != bindingID || metadata["swarm_v3_source_workspace_path"] != cwd {
		t.Fatalf("rebound authority metadata = %+v", metadata)
	}

	openReq := httptest.NewRequest(http.MethodGet, "/v3/tui/sessions/"+created.Session.ID+"?workspace_path="+cwd, nil)
	openRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(openRec, withTestPrincipal(openReq))
	if openRec.Code != http.StatusOK {
		t.Fatalf("workspace open status = %d, want %d, body=%s", openRec.Code, http.StatusOK, openRec.Body.String())
	}
}

func TestSessionsV3TUIDirectoryCreateRejectsProtectedMetadataAndNonCanonicalPath(t *testing.T) {
	t.Setenv("SWARM_V3_DIAGNOSTICS", "0")
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV3PrimaryAuthority(t, server, "/workspace/known")

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "protected tui cwd", body: `{"client_request_id":"bad-protected","cwd_path":"/tmp/cwd","agent_name":"swarm","metadata":{"swarm_v3_tui_cwd_path":"/tmp/other"}}`, want: "reserved"},
		{name: "non canonical cwd", body: `{"client_request_id":"bad-path","cwd_path":"/tmp/../tmp/cwd","agent_name":"swarm"}`, want: "canonical"},
		{name: "missing cwd", body: `{"client_request_id":"bad-missing","agent_name":"swarm"}`, want: "cwd_path is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v3/tui/sessions", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.want)
			}
		})
	}
	if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %+v err=%v, want none", sessions, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
}

type sessionsV3TUIDirectoryCreatePayload struct {
	OK         bool                                 `json:"ok"`
	Session    pebblestore.SessionSnapshot          `json:"session"`
	Projection sessionruntime.SessionProjection     `json:"projection"`
	Messages   []pebblestore.MessageSnapshot        `json:"messages"`
	Events     []sessionruntime.SessionEvent        `json:"events"`
	Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
}

func postSessionsV3TUIDirectoryCreate(t *testing.T, server *Server, clientRequestID, cwd, title string) sessionsV3TUIDirectoryCreatePayload {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"cwd_path":%q,"title":%q,"mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"cp3"}}`, clientRequestID, cwd, title)
	req := httptest.NewRequest(http.MethodPost, "/v3/tui/sessions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload sessionsV3TUIDirectoryCreatePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !payload.OK || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("create payload = %+v", payload)
	}
	return payload
}
