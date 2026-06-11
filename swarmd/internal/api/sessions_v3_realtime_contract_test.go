package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestV3RealtimeContractRouteIsFrozen(t *testing.T) {
	if V3RealtimeStreamPath != "/v3/realtime/stream" {
		t.Fatalf("V3 realtime route = %q, want /v3/realtime/stream", V3RealtimeStreamPath)
	}
	body, err := os.ReadFile("server_routes.go")
	if err != nil {
		t.Fatalf("read server_routes.go: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "mux.HandleFunc(V3RealtimeStreamPath, s.handleV3RealtimeStream)") {
		t.Fatalf("server routes must register the frozen V3 realtime route constant")
	}
	for _, forbidden := range []string{"mux.HandleFunc(\"/ws\", s.handleV3RealtimeStream)", "mux.HandleFunc(\"/v3/sessions/{id}/stream\", s.handleV3RealtimeStream)", "mux.HandleFunc(\"/v3/sessions/\", s.handleV3RealtimeStream)"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("server routes contain forbidden V3 realtime route binding %q", forbidden)
		}
	}
}

func TestV3RealtimeEndpointExistsAndIsNotLegacyStream(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	legacyWS := httptest.NewRecorder()
	server.Handler().ServeHTTP(legacyWS, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/ws", nil)))
	if legacyWS.Code == http.StatusUpgradeRequired {
		t.Fatalf("old /ws unexpectedly accepted as V3 realtime endpoint")
	}

	legacySession := httptest.NewRecorder()
	server.Handler().ServeHTTP(legacySession, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/example/stream", nil)))
	if legacySession.Code == http.StatusUpgradeRequired {
		t.Fatalf("old per-session V3 stream unexpectedly accepted as native V3 realtime endpoint")
	}

	native := httptest.NewRecorder()
	server.Handler().ServeHTTP(native, withTestPrincipal(httptest.NewRequest(http.MethodGet, V3RealtimeStreamPath, nil)))
	if native.Code != http.StatusUpgradeRequired {
		t.Fatalf("native V3 realtime route status = %d, want websocket upgrade required", native.Code)
	}
}

func TestV3RealtimeContractRoundTripsEveryMessageType(t *testing.T) {
	messages := []V3RealtimeMessage{
		validV3RealtimeEventMessage(t),
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayStart, SessionID: "session-a", AfterSeq: 7, HighWatermarkSeq: 10},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayDone, SessionID: "session-a", LastSeq: 10, NextSeq: 11, HighWatermarkSeq: 10},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindCursorError, SessionID: "session-a", LastSeq: 7, HighWatermarkSeq: 12, ErrorCode: "cursor_gap", Error: "refetch required"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindKeepalive, SessionID: "session-a", LastSeq: 10, EndpointCursor: "cursor-10"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHighWater, SessionID: "session-a", HighWatermarkSeq: 12, EndpointCursor: "cursor-12"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a", AfterSeq: 10},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindUnsubscribe, SessionID: "session-a", SubscriptionID: "sub-a"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, AfterRev: 12},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, SessionID: "session-b", ErrorCode: "auth_denied", Error: "not authorized"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, SessionID: "session-a", NextSeq: 13, ErrorCode: "slow_consumer", Reason: "reconnect required"},
	}
	for _, msg := range messages {
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal %s: %v", msg.Kind, err)
		}
		var decoded V3RealtimeMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", msg.Kind, err)
		}
		if err := ValidateV3RealtimeMessage(decoded); err != nil {
			t.Fatalf("validate %s after round trip: %v\njson=%s", msg.Kind, err, string(raw))
		}
	}
}

func TestV3RealtimeContractRejectsInvalidMessages(t *testing.T) {
	valid := validV3RealtimeEventMessage(t)
	tests := map[string]func(V3RealtimeMessage) V3RealtimeMessage{
		"missing protocol":             func(m V3RealtimeMessage) V3RealtimeMessage { m.Protocol = ""; return m },
		"wrong protocol version":       func(m V3RealtimeMessage) V3RealtimeMessage { m.ProtocolVersion = 2; return m },
		"missing session id":           func(m V3RealtimeMessage) V3RealtimeMessage { m.SessionID = ""; return m },
		"missing rev":                  func(m V3RealtimeMessage) V3RealtimeMessage { m.Rev = 0; return m },
		"non-continuous rev prevRev":   func(m V3RealtimeMessage) V3RealtimeMessage { m.PrevRev = 1; return m },
		"missing event seq":            func(m V3RealtimeMessage) V3RealtimeMessage { m.Event.Seq = 0; return m },
		"conflicting session identity": func(m V3RealtimeMessage) V3RealtimeMessage { m.Event.SessionID = "session-b"; return m },
		"unsupported kind":             func(m V3RealtimeMessage) V3RealtimeMessage { m.Kind = "sessionV3StreamFrame"; return m },
		"missing tool identity": func(m V3RealtimeMessage) V3RealtimeMessage {
			m.Event.EventType = "session.tool.delta"
			m.EventType = "session.tool.delta"
			m.Event.Payload = json.RawMessage(`{"run_id":"run-1","call_id":"call-1","delta":"chunk"}`)
			return m
		},
	}
	for name, mutate := range tests {
		msg := mutate(cloneV3RealtimeMessage(t, valid))
		if err := ValidateV3RealtimeMessage(msg); err == nil {
			t.Fatalf("%s: validation succeeded unexpectedly: %+v", name, msg)
		}
	}
}

func TestV3RealtimeSourceGuardRejectsOldTransportDependencies(t *testing.T) {
	for _, file := range []string{"sessions_v3_realtime_contract.go", "sessions_v3_realtime_ws.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(body)
		if file == "sessions_v3_realtime_contract.go" {
			for _, required := range []string{"V3RealtimeProtocol", "V3RealtimeProtocolVersion"} {
				if !strings.Contains(source, required) {
					t.Fatalf("%s missing required V3 realtime contract symbol %q", file, required)
				}
			}
		}
		for _, forbidden := range []string{"EventEnvelope", "sessionV3StreamFrame", "SessionV3StreamFrame", "runStreamManager", "handleRunStream", "handleSessionV3PrimaryStream", "streamSessionV3PrimaryEvents"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden old realtime dependency %q", file, forbidden)
			}
		}
	}
}

func validV3RealtimeEventMessage(t *testing.T) V3RealtimeMessage {
	t.Helper()
	payload := json.RawMessage(`{"kind":"message","run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1"}`)
	return V3RealtimeMessage{
		Protocol:         V3RealtimeProtocol,
		ProtocolVersion:  V3RealtimeProtocolVersion,
		Kind:             V3RealtimeKindEvent,
		SessionID:        "session-a",
		LastSeq:          3,
		HighWatermarkSeq: 3,
		EndpointCursor:   "cursor-3",
		Rev:              3,
		PrevRev:          2,
		EventType:        "session.message.appended",
		Event:            &sessionruntime.SessionEvent{ID: "event-3", SessionID: "session-a", Seq: 3, EventType: "session.message.appended", Payload: payload, TsUnixMs: 1234},
	}
}

func cloneV3RealtimeMessage(t *testing.T, msg V3RealtimeMessage) V3RealtimeMessage {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal clone: %v", err)
	}
	var cloned V3RealtimeMessage
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatalf("unmarshal clone: %v", err)
	}
	return cloned
}
