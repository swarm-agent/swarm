package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSessionConnectionContractSchemaFreezesRequiredDefinitions(t *testing.T) {
	var schema map[string]any
	decodeSessionConnectionContractSchema(t, &schema)
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema is missing $defs object")
	}
	for _, name := range []string{
		"SessionConnectRequest",
		"SessionConnectResponse",
		"SessionSnapshot",
		"SessionStreamFrame",
		"RunPhase",
		"SessionConnectionError",
	} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema is missing required definition %s", name)
		}
	}
}

func TestSessionConnectionContractRunPhasesAreExact(t *testing.T) {
	var schema struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	decodeSessionConnectionContractSchema(t, &schema)
	got := schema.Defs["RunPhase"].Enum
	want := []string{
		"accepted",
		"pending_executor",
		"executor_started",
		"provider_request_started",
		"provider_first_event",
		"output_streaming",
		"waiting_permission",
		"completed",
		"failed",
		"cancelled",
		"interrupted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunPhase enum mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestSessionConnectionContractStreamFramesAreDiscriminated(t *testing.T) {
	var schema map[string]any
	decodeSessionConnectionContractSchema(t, &schema)
	defs := schema["$defs"].(map[string]any)
	for _, name := range []string{"SessionReadyFrame", "SessionEventFrame", "RunPhaseFrame", "SessionReconnectRequiredFrame"} {
		def, ok := defs[name].(map[string]any)
		if !ok {
			t.Fatalf("definition %s is missing", name)
		}
		required, ok := def["required"].([]any)
		if !ok || !schemaContainsString(required, "type") {
			t.Fatalf("%s must require a type discriminator", name)
		}
		props, ok := def["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s is missing properties", name)
		}
		typeProp, ok := props["type"].(map[string]any)
		if !ok || strings.TrimSpace(schemaAsString(typeProp["const"])) == "" {
			t.Fatalf("%s type property must be a const discriminator", name)
		}
	}
}

func TestSessionConnectResponseGoldenJSON(t *testing.T) {
	activePlan := json.RawMessage(`{"id":"plan-1"}`)
	usage := json.RawMessage(`{"input_tokens":12}`)
	response := SessionConnectResponse{
		Ok:              true,
		ContractVersion: SessionConnectionContractVersion,
		SessionId:       "session-123",
		Snapshot: SessionSnapshot{
			EventSeq:           841,
			Session:            json.RawMessage(`{"id":"session-123","title":"Demo"}`),
			Messages:           []json.RawMessage{json.RawMessage(`{"id":"msg-1","role":"user","content":"hello"}`)},
			CurrentRun:         &SessionCurrentRun{RunId: "run-789", Phase: RunPhaseAccepted},
			PendingPermissions: []json.RawMessage{json.RawMessage(`{"id":"perm-1"}`)},
			ActivePlan:         &activePlan,
			Usage:              &usage,
		},
		Connection: SessionConnectionInfo{
			ConnectionId:   "conn-456",
			Transport:      "websocket",
			Protocol:       SessionConnectionProtocol,
			StreamUrl:      "/v3/session-connections/conn-456/stream?token=redacted",
			ResumeToken:    "resume-redacted",
			ReadyTimeoutMs: SessionConnectionDefaultReadyTimeoutMS,
		},
	}
	assertGoldenJSON(t, response, "session_connect_response.golden.json")
}

func TestSessionStreamFrameGoldenJSON(t *testing.T) {
	frames := []any{
		SessionReadyFrame{Type: "session.ready", ConnectionId: "conn-456", SessionId: "session-123", EventSeq: 846, ResumeToken: "resume-redacted-2"},
		SessionEventFrame{Type: "session.event", SessionId: "session-123", EventSeq: 847, Event: json.RawMessage(`{"id":"evt-847","event_type":"message.created"}`), ResumeToken: "resume-redacted-3"},
		RunPhaseFrame{Type: "run.phase", SessionId: "session-123", RunId: "run-789", Phase: RunPhaseProviderRequestStarted, EventSeq: 848},
		SessionReconnectRequiredFrame{Type: "session.reconnect_required", SessionId: "session-123", Reason: string(SessionReconnectRequiredReasonResumeTokenExpired), Action: SessionErrorAction{Method: "POST", Path: "/v3/sessions/session-123:connect"}},
	}
	assertGoldenJSON(t, frames, "session_stream_frames.golden.json")
}

func decodeSessionConnectionContractSchema(t *testing.T, out any) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "contracts", "session-connection.v1.schema.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
}

func assertGoldenJSON(t *testing.T, value any, name string) {
	t.Helper()
	gotBytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	got := string(gotBytes) + "\n"
	path := filepath.Join("testdata", name)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	want := string(wantBytes)
	if got != want {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

func schemaContainsString(values []any, want string) bool {
	for _, value := range values {
		if schemaAsString(value) == want {
			return true
		}
	}
	return false
}

func schemaAsString(value any) string {
	text, _ := value.(string)
	return text
}
