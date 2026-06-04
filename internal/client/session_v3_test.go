package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionV3ClientUsesPrimaryRoutes(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if _, ok := body["swarm_id"]; ok {
				t.Fatalf("v3 create included swarm_id: %#v", body)
			}
			if _, ok := body["workspace_binding_id"]; ok {
				t.Fatalf("v3 create included workspace_binding_id: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": body["workspace_path"], "workspace_name": body["workspace_name"], "title": body["title"], "mode": body["mode"]},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 1, "projection_high_watermark_seq": 1},
				"messages":   []any{},
				"events":     []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-v3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"messages":   []map[string]any{{"id": "msg-1", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hi"}},
				"events":     []any{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode message: %v", err)
			}
			if body["role"] != "user" || body["content"] != "hi" {
				t.Fatalf("message body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 3, "projection_high_watermark_seq": 3},
				"message":    map[string]any{"id": "msg-2", "session_id": "session-v3", "global_seq": 3, "role": "user", "content": "hi"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 3},
				"messages":   []any{},
				"events":     []any{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/run/stop":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode stop: %v", err)
			}
			if body["type"] != "run.stop" || body["run_id"] != "run-1" {
				t.Fatalf("stop body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-v3", "run_id": "run-1", "status": "failed"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	created, err := api.CreateSessionV3WithOptions(context.Background(), SessionCreateOptions{Title: "V3", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: "auto"})
	if err != nil {
		t.Fatalf("CreateSessionV3WithOptions() error = %v", err)
	}
	if created.Session.SessionAPI != "v3" || created.Session.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("created session = %#v", created.Session)
	}
	if _, err := api.GetSessionV3(context.Background(), "session-v3"); err != nil {
		t.Fatalf("GetSessionV3() error = %v", err)
	}
	msg, err := api.SendSessionV3Message(context.Background(), "session-v3", SessionV3MessageOptions{Content: "hi"})
	if err != nil {
		t.Fatalf("SendSessionV3Message() error = %v", err)
	}
	if msg.RunIntent.Status != "pending_executor" || msg.Message.ID != "msg-2" {
		t.Fatalf("message result = %#v", msg)
	}
	if err := api.StopSessionRun(context.Background(), "session-v3", "run-1"); err != nil {
		t.Fatalf("StopSessionRun(v3) error = %v", err)
	}
	want := []string{"POST /v3/sessions", "GET /v3/sessions/session-v3", "POST /v3/sessions/session-v3/messages", "POST /v3/sessions/session-v3/run/stop"}
	if len(seen) != len(want) {
		t.Fatalf("requests = %#v, want %#v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("request[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}
