package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartRoutedSessionV3UsesOneExplicitIdempotencyIdentity(t *testing.T) {
	var request RoutedSessionV3StartRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != routedSessionV3Path {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "route-1" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(validRoutedSessionV3Response("session-1"))
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("token")
	response, err := api.StartRoutedSessionV3(context.Background(), RoutedSessionV3StartRequest{
		Input: "  build it  ", ClientRequestID: " route-1 ", ManagedWorktreeRequested: true, PlanModeRequested: true,
		WorkspacePath: "/source", HostWorkspacePath: "/source", RuntimeWorkspacePath: "/source",
		WorkspaceBindingID: "binding-1", SwarmID: "swarm-1", TargetKind: "host", TargetRelationship: "self",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Input != "build it" || request.ClientRequestID != "route-1" || request.IdempotencyKey != "route-1" || !request.ManagedWorktreeRequested || !request.PlanModeRequested || request.WorkspacePath != "/source" || request.WorkspaceBindingID != "binding-1" || request.SwarmID != "swarm-1" || request.TargetKind != "host" || request.TargetRelationship != "self" {
		t.Fatalf("request = %#v", request)
	}
	if response.SessionID != "session-1" || response.Session.SessionAPI != "v3" {
		t.Fatalf("response = %#v", response)
	}
	if cursor := response.Hydrated().SnapshotEndpointCursor; cursor != "" {
		t.Fatalf("routed hydration exposed storage cursor %q as a signed realtime cursor", cursor)
	}
}

func TestStartRoutedSessionV3RejectsConflictingOrInconsistentAuthority(t *testing.T) {
	api := New("http://unused")
	authority := RoutedSessionV3StartRequest{Input: "x", ClientRequestID: "one", WorkspacePath: "/source", WorkspaceBindingID: "binding-1", SwarmID: "swarm-1", TargetKind: "host", TargetRelationship: "self"}
	conflict := authority
	conflict.IdempotencyKey = "two"
	_, err := api.StartRoutedSessionV3(context.Background(), conflict)
	if err == nil || !strings.Contains(err.Error(), "one stable") {
		t.Fatalf("idempotency error = %v", err)
	}

	payload := validRoutedSessionV3Response("session-1")
	payload["mutation"].(map[string]any)["session_id"] = "other"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(payload) }))
	defer server.Close()
	api = New(server.URL)
	api.SetToken("token")
	_, err = api.StartRoutedSessionV3(context.Background(), authority)
	if err == nil || !strings.Contains(err.Error(), "mutation projection") {
		t.Fatalf("authority error = %v", err)
	}
}

func validRoutedSessionV3Response(sessionID string) map[string]any {
	session := map[string]any{"id": sessionID, "title": "Routed title", "mode": "plan", "workspace_path": "/runtime", "workspace_name": "repo", "worktree_enabled": true, "worktree_root_path": "/runtime"}
	message := map[string]any{"id": "message-1", "session_id": sessionID, "global_seq": 1, "role": "user", "content": "build it"}
	projection := map[string]any{"session_id": sessionID, "last_event_seq": 1, "projection_high_watermark_seq": 1}
	run := map[string]any{"session_id": sessionID, "run_id": "run-1", "status": "pending_executor", "event_seq": 1}
	outbox := map[string]any{"endpoint_seq": 1, "endpoint_cursor": "cursor-1", "session_id": sessionID, "projection": projection, "event": map[string]any{"id": "event-1", "session_id": sessionID, "seq": 1, "event_type": "session.created"}}
	return map[string]any{
		"ok": true, "session_id": sessionID, "title": "Routed title", "starting_mode": "plan", "replayed": false,
		"session": session,
		"session_view": map[string]any{
			"identity":         map[string]any{"session_id": sessionID, "title": "Routed title", "workspace_binding_id": "binding-1", "source_workspace_name": "repo", "source_workspace_path": "/source", "runtime_workspace_path": "/runtime", "runtime_swarm_id": "swarm-1", "authority_host_swarm_id": "swarm-1", "worktree_enabled": true, "worktree_root_path": "/runtime"},
			"agentic_settings": map[string]any{"mode": "plan", "agent_name": "swarm", "resolved_agent_name": "swarm", "effective_preference": map[string]any{"provider": "codex", "model": "gpt"}, "agent_model_policy": map[string]any{}, "context_window": 100},
			"media_capability": map[string]any{"status": "unavailable", "capabilities": []any{}}, "pending_permissions": []any{},
		},
		"first_message": message, "projection": projection,
		"mutation": map[string]any{
			"session_id": sessionID, "primary_seq": 1, "first_seq": 1, "last_seq": 1,
			"session": session, "message": message, "run_intent": run, "projection": projection, "realtime_outbox": outbox,
			"event": map[string]any{"id": "event-1", "session_id": sessionID, "seq": 1, "event_type": "session.created"},
		},
	}
}
