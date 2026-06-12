package client

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestSessionV2LifecyclePathEscapesID(t *testing.T) {
	if got, want := sessionV2LifecyclePath("session/one", "messages"), "/v2/sessions/session%2Fone/messages"; got != want {
		t.Fatalf("sessionV2LifecyclePath() = %q, want %q", got, want)
	}
	if got, want := sessionV2LifecyclePath(" session-1 ", "/run/stream"), "/v2/sessions/session-1/run/stream"; got != want {
		t.Fatalf("sessionV2LifecyclePath() = %q, want %q", got, want)
	}
}

func TestPrimaryLifecycleMethodsUseV2SessionPaths(t *testing.T) {
	ctx := context.Background()
	sessionID := "session-1"
	planID := "plan-1"
	permissionID := "permission-1"

	tests := []struct {
		name   string
		method string
		path   string
		call   func(*API) error
	}{
		{"get session", http.MethodGet, "/v2/sessions/session-1", func(api *API) error { _, err := api.GetSession(ctx, sessionID); return err }},
		{"list messages", http.MethodGet, "/v2/sessions/session-1/messages", func(api *API) error { _, err := api.ListSessionMessages(ctx, sessionID, 7, 25); return err }},
		{"usage", http.MethodGet, "/v2/sessions/session-1/usage", func(api *API) error { _, _, _, err := api.GetSessionUsage(ctx, sessionID, 5); return err }},
		{"get mode", http.MethodGet, "/v2/sessions/session-1/mode", func(api *API) error { _, err := api.GetSessionMode(ctx, sessionID); return err }},
		{"set mode", http.MethodPost, "/v2/sessions/session-1/mode", func(api *API) error { _, err := api.SetSessionMode(ctx, sessionID, "auto"); return err }},
		{"metadata", http.MethodPost, "/v2/sessions/session-1/metadata", func(api *API) error {
			_, err := api.UpdateSessionMetadata(ctx, sessionID, map[string]any{"k": "v"})
			return err
		}},
		{"get codex", http.MethodGet, "/v2/sessions/session-1/codex", func(api *API) error { _, err := api.GetSessionCodexConfig(ctx, sessionID); return err }},
		{"update codex", http.MethodPost, "/v2/sessions/session-1/codex", func(api *API) error {
			_, err := api.UpdateSessionCodexConfig(ctx, sessionID, SessionCodexConfigUpdateRequest{})
			return err
		}},
		{"get preference", http.MethodGet, "/v2/sessions/session-1/preference", func(api *API) error { _, err := api.GetSessionPreference(ctx, sessionID); return err }},
		{"set preference", http.MethodPost, "/v2/sessions/session-1/preference", func(api *API) error {
			_, err := api.SetSessionPreference(ctx, sessionID, map[string]any{"provider": "anthropic"})
			return err
		}},
		{"list plans", http.MethodGet, "/v2/sessions/session-1/plans", func(api *API) error { _, _, err := api.ListSessionPlans(ctx, sessionID, 10); return err }},
		{"get plan", http.MethodGet, "/v2/sessions/session-1/plans/plan-1", func(api *API) error { _, err := api.GetSessionPlan(ctx, sessionID, planID); return err }},
		{"get active plan", http.MethodGet, "/v2/sessions/session-1/plans/active", func(api *API) error { _, _, err := api.GetActiveSessionPlan(ctx, sessionID); return err }},
		{"save plan", http.MethodPost, "/v2/sessions/session-1/plans", func(api *API) error {
			_, err := api.SaveSessionPlan(ctx, sessionID, SessionPlanUpsertRequest{Title: "Plan"})
			return err
		}},
		{"set active plan", http.MethodPost, "/v2/sessions/session-1/plans/active", func(api *API) error { _, err := api.SetActiveSessionPlan(ctx, sessionID, planID); return err }},
		{"list pending permissions", http.MethodGet, "/v3/sessions/session-1/permissions", func(api *API) error { _, err := api.ListPendingPermissions(ctx, sessionID, 20); return err }},
		{"list permissions", http.MethodGet, "/v3/sessions/session-1/permissions", func(api *API) error { _, err := api.ListPermissions(ctx, sessionID, 20); return err }},
		{"resolve permission", http.MethodPost, "/v3/sessions/session-1/permissions/permission-1/resolve", func(api *API) error {
			_, err := api.ResolvePermissionWithArguments(ctx, sessionID, permissionID, "approve_once", "ok", `{"cmd":"true"}`)
			return err
		}},
		{"resolve all permissions", http.MethodPost, "/v3/sessions/session-1/permissions/resolve_all", func(api *API) error {
			_, err := api.ResolveAllPermissions(ctx, sessionID, "deny_once", "no")
			return err
		}},
		{"run", http.MethodPost, "/v2/sessions/session-1/run", func(api *API) error {
			_, err := api.RunSessionWithOptions(ctx, sessionID, "prompt", "swarm", "", RunSessionOptions{})
			return err
		}},
		{"stop primary run", http.MethodPost, "/v2/sessions/session-1/run/stop/primary", func(api *API) error { return api.StopPrimarySessionRun(ctx, sessionID, "run-1", "primary-swarm") }},
		{"stop local-container run", http.MethodPost, "/v2/sessions/session-1/run/stop/local-container", func(api *API) error { return api.StopLocalContainerSessionRun(ctx, sessionID, "run-1") }},
		{"background run", http.MethodPost, "/v2/sessions/session-1/run/stream", func(api *API) error {
			_, err := api.StartBackgroundSessionRun(ctx, sessionID, "prompt", "swarm", "", RunSessionOptions{})
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				if strings.HasPrefix(gotPath, "/v1/") {
					t.Fatalf("unexpected v1 path: %s", gotPath)
				}
				if r.URL.Path == "/v2/sessions/session-1/run/stop/primary" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatalf("decode stop body: %v", err)
					}
					if body["target_swarm_id"] != "primary-swarm" || body["run_id"] != "run-1" {
						t.Fatalf("stop body = %+v", body)
					}
				}
				writeLifecycleTestResponse(t, w, r, sessionID, planID, permissionID)
			}))
			defer server.Close()

			api := New(server.URL)
			api.SetToken("test-token")
			if err := tc.call(api); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if gotMethod != tc.method {
				t.Fatalf("method = %q, want %q", gotMethod, tc.method)
			}
			if gotPath != tc.path {
				t.Fatalf("path = %q, want %q", gotPath, tc.path)
			}
		})
	}
}

func TestStopSessionRunChoosesEndpointFromSessionExecution(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name           string
		executionClass string
		runtimeSwarmID string
		wantPath       string
	}{
		{name: "primary", executionClass: "primary", runtimeSwarmID: "primary-swarm", wantPath: "/v2/sessions/session-1/run/stop/primary"},
		{name: "local-container", executionClass: "local_container", runtimeSwarmID: "container-swarm", wantPath: "/v2/sessions/session-1/run/stop/local-container"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/v2/sessions/session-1":
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session": map[string]any{"id": "session-1", "session_execution": map[string]any{"execution_class": tc.executionClass, "runtime_swarm_id": tc.runtimeSwarmID}}})
				case tc.wantPath:
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "run_id": "run-1", "status": "stop_requested"})
				default:
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()

			api := New(server.URL)
			api.SetToken("test-token")
			if err := api.StopSessionRun(ctx, "session-1", "run-1"); err != nil {
				t.Fatalf("StopSessionRun() error = %v", err)
			}
			if len(paths) != 2 || paths[1] != tc.wantPath {
				t.Fatalf("paths = %v, want second path %s", paths, tc.wantPath)
			}
		})
	}
}

func TestRunSessionV2RequestSanitizesAuthorityOverrides(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sessions/session-1/run" {
			t.Fatalf("path = %q, want /v2/sessions/session-1/run", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"session_id": "session-1"}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	_, err := api.RunSessionWithOptions(context.Background(), "session-1", "prompt", "swarm", "", RunSessionOptions{
		Compact:          true,
		TargetKind:       "workspace",
		TargetName:       "override",
		ToolScope:        &RunToolScope{Preset: "danger"},
		ExecutionContext: &RunExecutionContext{CWD: "elsewhere"},
	})
	if err != nil {
		t.Fatalf("RunSessionWithOptions() error = %v", err)
	}
	assertNoAuthorityOverrides(t, body)
	if got, _ := body["compact"].(bool); !got {
		t.Fatalf("compact = %#v, want true", body["compact"])
	}
}

func TestStartBackgroundSessionRunV2RequestKeepsBackgroundAndSanitizesOverrides(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sessions/session-1/run/stream" {
			t.Fatalf("path = %q, want /v2/sessions/session-1/run/stream", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "run_id": "run-1", "status": "running", "background": true})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	_, err := api.StartBackgroundSessionRun(context.Background(), "session-1", "prompt", "swarm", "", RunSessionOptions{
		TargetKind:       "workspace",
		TargetName:       "override",
		ToolScope:        &RunToolScope{Preset: "danger"},
		ExecutionContext: &RunExecutionContext{CWD: "elsewhere"},
	})
	if err != nil {
		t.Fatalf("StartBackgroundSessionRun() error = %v", err)
	}
	assertNoAuthorityOverrides(t, body)
	if got, _ := body["background"].(bool); !got {
		t.Fatalf("background = %#v, want true", body["background"])
	}
}

func TestRunSessionStreamUsesV2WebsocketPathAndSanitizedPayload(t *testing.T) {
	var handshakePath string
	var startPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakePath = r.URL.Path
		if r.URL.Path != "/v2/sessions/session-1/run/stream" {
			t.Fatalf("websocket path = %q, want /v2/sessions/session-1/run/stream", r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		opcode, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read start frame: %v", err)
		}
		if opcode != wsOpcodeText {
			t.Fatalf("opcode = %d, want text", opcode)
		}
		if err := json.Unmarshal(payload, &startPayload); err != nil {
			t.Fatalf("decode start payload: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "turn.completed", "session_id": "session-1", "result": map[string]any{"session_id": "session-1", "assistant_message": map[string]any{"id": "msg-1", "role": "assistant", "content": "ok"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	result, err := api.RunSessionStreamWithOptions(context.Background(), "session-1", "prompt", "swarm", "", RunSessionOptions{
		TargetKind:       "workspace",
		TargetName:       "override",
		ToolScope:        &RunToolScope{Preset: "danger"},
		ExecutionContext: &RunExecutionContext{CWD: "elsewhere"},
	}, nil)
	if err != nil {
		t.Fatalf("RunSessionStreamWithOptions() error = %v", err)
	}
	if result.SessionID != "session-1" {
		t.Fatalf("result session = %q, want session-1", result.SessionID)
	}
	if handshakePath != "/v2/sessions/session-1/run/stream" {
		t.Fatalf("handshake path = %q", handshakePath)
	}
	assertNoAuthorityOverrides(t, startPayload)
	if got, _ := startPayload["type"].(string); got != "run.start" {
		t.Fatalf("type = %q, want run.start", got)
	}
}

func TestPersistRunStreamClientErrorUsesV2MessagesPath(t *testing.T) {
	var gotPath string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	api.persistRunStreamClientError("session-1", "decode", io.ErrUnexpectedEOF)
	if gotPath != "/v2/sessions/session-1/messages" {
		t.Fatalf("path = %q, want /v2/sessions/session-1/messages", gotPath)
	}
	if strings.Contains(gotPath, "/v1/") {
		t.Fatalf("unexpected v1 path: %s", gotPath)
	}
	if !strings.Contains(body, streamClientErrorPathID+"/decode") {
		t.Fatalf("persisted body missing path id: %s", body)
	}
}

func assertNoAuthorityOverrides(t *testing.T, body map[string]any) {
	t.Helper()
	for _, key := range []string{"target_kind", "target_name", "tool_scope", "execution_context"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s present in v2 payload: %#v", key, body)
		}
	}
}

func writeLifecycleTestResponse(t *testing.T, w http.ResponseWriter, r *http.Request, sessionID, planID, permissionID string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v2/sessions/" + sessionID:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session": map[string]any{"id": sessionID}})
	case "/v2/sessions/" + sessionID + "/messages":
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "messages": []any{}})
	case "/v2/sessions/" + sessionID + "/usage":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "has_usage_summary": false, "turn_usage_records": []any{}})
	case "/v2/sessions/" + sessionID + "/mode":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "mode": "auto"})
	case "/v2/sessions/" + sessionID + "/metadata":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session": map[string]any{"id": sessionID}})
	case "/v2/sessions/" + sessionID + "/codex":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "service_tier": "auto", "context_mode": "auto", "effective_context_window": 1})
	case "/v2/sessions/" + sessionID + "/preference":
		_ = json.NewEncoder(w).Encode(ModelResolved{Preference: ModelPreference{Provider: "anthropic", Model: "claude"}})
	case "/v2/sessions/" + sessionID + "/plans":
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "plan": map[string]any{"id": planID, "session_id": sessionID, "title": "Plan"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "plans": []any{}, "active_plan_id": ""})
	case "/v2/sessions/" + sessionID + "/plans/" + planID:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "plan": map[string]any{"id": planID, "session_id": sessionID, "title": "Plan"}})
	case "/v2/sessions/" + sessionID + "/plans/active":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "has_active": true, "active_plan": map[string]any{"id": planID, "session_id": sessionID, "title": "Plan", "active": true}})
	case "/v2/sessions/" + sessionID + "/permissions", "/v3/sessions/" + sessionID + "/permissions":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "count": 0, "permissions": []any{}})
	case "/v2/sessions/" + sessionID + "/permissions/" + permissionID + "/resolve", "/v3/sessions/" + sessionID + "/permissions/" + permissionID + "/resolve":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "permission": map[string]any{"id": permissionID, "session_id": sessionID}})
	case "/v2/sessions/" + sessionID + "/permissions/resolve_all", "/v3/sessions/" + sessionID + "/permissions/resolve_all":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "count": 0, "resolved": []any{}})
	case "/v2/sessions/" + sessionID + "/run":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"session_id": sessionID}})
	case "/v2/sessions/" + sessionID + "/run/stop/primary":
		if r.Method != http.MethodPost {
			t.Fatalf("primary stop method = %s, want POST", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "run_id": "run-1", "status": "stop_requested"})
	case "/v2/sessions/" + sessionID + "/run/stop/local-container":
		if r.Method != http.MethodPost {
			t.Fatalf("local-container stop method = %s, want POST", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "run_id": "run-1", "status": "stop_requested"})
	case "/v2/sessions/" + sessionID + "/run/stream":
		if r.Method != http.MethodPost {
			t.Fatalf("run stream method = %s, want POST", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": sessionID, "run_id": "run-1", "status": "running", "background": true})
	default:
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}
}

func hijackLifecycleTestWebsocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	accept := wsAcceptForKey(r.Header.Get("Sec-WebSocket-Key"))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

func readClientLifecycleTestFrame(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	payloadLength := int(head[1] & 0x7F)
	if payloadLength == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		payloadLength = int(ext[0])<<8 | int(ext[1])
	} else if payloadLength == 127 {
		return 0, nil, http.ErrNotSupported
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeServerLifecycleTestFrame(t *testing.T, conn io.Writer, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal server frame: %v", err)
	}
	header := []byte{0x80 | wsOpcodeText}
	if len(raw) <= 125 {
		header = append(header, byte(len(raw)))
	} else if len(raw) <= 65535 {
		header = append(header, 126, byte(len(raw)>>8), byte(len(raw)))
	} else {
		t.Fatalf("test frame too large")
	}
	if _, err := conn.Write(append(header, raw...)); err != nil {
		t.Fatalf("write server frame: %v", err)
	}
}
