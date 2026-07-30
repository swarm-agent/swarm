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
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHello, EndpointCursor: "cursor-7"},
		validV3RealtimeEventMessage(t),
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayStart, SessionID: "session-a", EndpointCursor: "cursor-7", HighWatermarkSeq: 10},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayDone, SessionID: "session-a", LastSeq: 10, NextSeq: 11, HighWatermarkSeq: 10, EndpointCursor: "cursor-10"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindCursorError, SessionID: "session-a", LastSeq: 7, HighWatermarkSeq: 12, EndpointCursor: "cursor-7", ErrorCode: "cursor_gap", Error: "refetch required"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindKeepalive, SessionID: "session-a", LastSeq: 10, EndpointCursor: "cursor-10"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindEndpointWatermark, HighWatermarkSeq: 12, EndpointCursor: "cursor-12", Rev: 12, PrevRev: 11},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHighWater, SessionID: "session-a", HighWatermarkSeq: 12, EndpointCursor: "cursor-12"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a", EndpointCursor: "cursor-10"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindUnsubscribe, SessionID: "session-a", SubscriptionID: "sub-a"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: "cursor-12"},
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

func TestV3RealtimeContractResumeCarriesEndpointCursorSubscriptionsAndWorksets(t *testing.T) {
	raw := []byte(`{"protocol":"v3.realtime","protocol_version":1,"kind":"resume","endpoint_cursor":"cursor-42","subscriptions":[{"session_id":"session-a","subscription_id":"sub-a"}],"worksets":[{"workset_id":"desktop:global","subscription_id":"desktop-client:desktop:global","surface":"desktop","selector":{"kind":"global","global":true,"recent":{"limit":25}},"resources":["sessions","events","run_intents"],"auto_subscribe_sessions":true}]}`)
	var decoded V3RealtimeMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode canonical resume message: %v", err)
	}
	if err := ValidateV3RealtimeMessage(decoded); err != nil {
		t.Fatalf("canonical resume message rejected: %v", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal canonical resume message: %v", err)
	}
	encodedText := string(encoded)
	for _, required := range []string{`"endpoint_cursor":"cursor-42"`, `"subscriptions"`, `"worksets"`, `"workset_id":"desktop:global"`, `"auto_subscribe_sessions":true`} {
		if !strings.Contains(encodedText, required) {
			t.Fatalf("canonical resume must preserve %s, got %s", required, encodedText)
		}
	}
	for _, forbidden := range []string{`"after_seq"`, `"afterRev"`} {
		if strings.Contains(encodedText, forbidden) {
			t.Fatalf("canonical resume encoded forbidden legacy cursor %s in %s", forbidden, encodedText)
		}
	}
}

func TestV3RealtimeContractRejectsLegacySessionResumeCursors(t *testing.T) {
	canonical := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: "cursor-42", Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: "session-a", SubscriptionID: "sub-a"}}}
	if err := ValidateV3RealtimeInboundClientMessage(canonical); err != nil {
		t.Fatalf("canonical endpoint_cursor resume rejected: %v", err)
	}

	tests := map[string]V3RealtimeMessage{
		"subscribe after_seq":                  {Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a", AfterSeq: 10},
		"subscribe endpoint_cursor plus after": {Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a", EndpointCursor: "cursor-10", AfterSeq: 10},
		"resume afterRev":                      {Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, AfterRev: 10},
		"resume endpoint_cursor plus afterRev": {Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: "cursor-10", AfterRev: 10},
	}
	for name, msg := range tests {
		if err := ValidateV3RealtimeMessage(msg); err == nil {
			t.Fatalf("%s: legacy cursor validation succeeded unexpectedly: %+v", name, msg)
		}
	}
}

func TestV3RealtimeContractSeparatesInboundAndOutboundKinds(t *testing.T) {
	inboundAllowed := []V3RealtimeMessage{
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindUnsubscribe, SessionID: "session-a"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: "cursor-42"},
	}
	for _, msg := range inboundAllowed {
		if err := ValidateV3RealtimeInboundClientMessage(msg); err != nil {
			t.Fatalf("inbound %s rejected: %v", msg.Kind, err)
		}
		if err := ValidateV3RealtimeOutboundServerMessage(msg); err == nil {
			t.Fatalf("client-only kind %s passed outbound validation", msg.Kind)
		}
	}

	serverOnly := []V3RealtimeMessage{
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHello, EndpointCursor: "cursor-42"},
		validV3RealtimeEventMessage(t),
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayStart, SessionID: "session-a", EndpointCursor: "cursor-42"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayDone, SessionID: "session-a", EndpointCursor: "cursor-42"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindCursorError, ErrorCode: "bad_cursor"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindKeepalive},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindEndpointWatermark, EndpointCursor: "cursor-42"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHighWater, SessionID: "session-a", EndpointCursor: "cursor-42"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindWorksetSessionDiscovered, WorksetID: "workset-a", WorksetSubscriptionID: "workset-sub-a", SessionID: "session-a", EndpointCursor: "cursor-42", Rev: 42, PrevRev: 41, EventType: "session.created"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindWorksetSessionUpdated, WorksetID: "workset-a", WorksetSubscriptionID: "workset-sub-a", SessionID: "session-a", EndpointCursor: "cursor-42", Rev: 42, PrevRev: 41, EventType: "session.updated"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindWorksetSessionRemoved, WorksetID: "workset-a", WorksetSubscriptionID: "workset-sub-a", SessionID: "session-a", EndpointCursor: "cursor-42", Rev: 42, PrevRev: 41, EventType: "session.deleted"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "auth_denied"},
		{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, ErrorCode: "slow_consumer"},
	}
	for _, msg := range serverOnly {
		if err := ValidateV3RealtimeOutboundServerMessage(msg); err != nil {
			t.Fatalf("outbound %s rejected: %v", msg.Kind, err)
		}
		if err := ValidateV3RealtimeInboundClientMessage(msg); err == nil {
			t.Fatalf("server-only kind %s passed inbound validation", msg.Kind)
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
		"unsupported workset selector": func(m V3RealtimeMessage) V3RealtimeMessage {
			m.Kind = V3RealtimeKindResume
			m.EndpointCursor = "cursor-3"
			m.Event = nil
			m.Worksets = []V3RealtimeWorksetSubscriptionRequest{{WorksetID: "desktop:bad", SubscriptionID: "desktop-client:bad", Selector: V3RealtimeWorksetSelector{Kind: "mystery"}}}
			return m
		},
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

func TestV3RealtimeContractAcceptsTypedClientEffectsOnCompletedToolEvents(t *testing.T) {
	message := validV3RealtimeEventMessage(t)
	message.EventType = "session.tool.completed"
	message.Event.EventType = "session.tool.completed"
	message.Event.Payload = json.RawMessage(`{"run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1","tool_name":"manage_theme","recorded_at":1234,"status":"completed","client_effects":[{"type":"refresh_themes"}]}`)
	if err := ValidateV3RealtimeMessage(message); err != nil {
		t.Fatalf("valid typed client effects rejected: %v", err)
	}
}

func TestV3RealtimeContractRejectsInvalidClientEffects(t *testing.T) {
	tests := map[string]struct {
		eventType string
		payload   string
	}{
		"effect on failed event": {eventType: "session.tool.failed", payload: `{"run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1","tool_name":"manage_theme","recorded_at":1234,"status":"failed","client_effects":[{"type":"refresh_themes"}]}`},
		"empty effects":          {eventType: "session.tool.completed", payload: `{"run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1","tool_name":"manage_theme","recorded_at":1234,"status":"completed","client_effects":[]}`},
		"unknown effect":         {eventType: "session.tool.completed", payload: `{"run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1","tool_name":"manage_theme","recorded_at":1234,"status":"completed","client_effects":[{"type":"reload_everything"}]}`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			message := validV3RealtimeEventMessage(t)
			message.EventType = test.eventType
			message.Event.EventType = test.eventType
			message.Event.Payload = json.RawMessage(test.payload)
			if err := ValidateV3RealtimeMessage(message); err == nil {
				t.Fatalf("invalid client effects passed validation")
			}
		})
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
		for _, forbidden := range []string{"EventEnvelope", "sessionV3StreamFrame", "SessionV3StreamFrame", "runStreamManager", "handleRunStream", "handleSessionV3PrimaryStream", "streamSessionV3PrimaryEvents", "BuildSessionWorkset", "sessionsV3WorksetRequest"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden old realtime dependency %q", file, forbidden)
			}
		}
	}
}

func validV3RealtimeEventMessage(t *testing.T) V3RealtimeMessage {
	t.Helper()
	payload := json.RawMessage(`{"kind":"message","run_id":"run-1","step_id":"step-1","call_id":"call-1","tool_instance_id":"tool-1","tool_name":"read","recorded_at":1234}`)
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

func TestV3RealtimeLivePatchContractRoundTrip(t *testing.T) {
	patch := V3RealtimeLivePatch{
		SessionID:    "session-a",
		RunID:        "run-a",
		StreamID:     "assistant:run-a:step:1",
		StreamKind:   "assistant_text",
		Operation:    "append",
		Step:         1,
		StepID:       "step-1",
		LiveSeqStart: 1,
		LiveSeqEnd:   1,
		OffsetStart:  0,
		OffsetEnd:    5,
		Text:         "hello",
		RecordedAt:   12345,
	}
	message := NewV3RealtimeLiveMessage(patch)
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal live message: %v", err)
	}
	var decoded V3RealtimeLiveMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal live message: %v", err)
	}
	if err := ValidateV3RealtimeLiveMessage(decoded); err != nil {
		t.Fatalf("validate live message: %v json=%s", err, string(raw))
	}
	if decoded.Kind != V3RealtimeKindLivePatch || decoded.SessionID != patch.SessionID || decoded.Live != patch {
		t.Fatalf("decoded live message = %+v, want patch %+v", decoded, patch)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal live keys: %v", err)
	}
	for _, forbidden := range []string{"endpoint_cursor", "rev", "prevRev", "event", "projection", "realtime_outbox"} {
		if _, ok := keys[forbidden]; ok {
			t.Fatalf("live message leaked durable key %q in %s", forbidden, string(raw))
		}
	}
}

func TestV3RealtimeLivePatchContractRejectsProviderToolBypass(t *testing.T) {
	patch := V3RealtimeLivePatch{
		SessionID:    "session-a",
		RunID:        "run-a",
		StreamID:     "provider-tool:run-a:step:1:event:1",
		StreamKind:   "provider_tool_call",
		Operation:    "append",
		Step:         1,
		StepID:       "step-1",
		LiveSeqStart: 1,
		LiveSeqEnd:   1,
		OffsetStart:  0,
		OffsetEnd:    2,
		Text:         "{}",
		RecordedAt:   12345,
	}
	if err := ValidateV3RealtimeLiveMessage(NewV3RealtimeLiveMessage(patch)); err == nil {
		t.Fatalf("provider tool live bypass passed validation")
	}
}

func TestV3RealtimeLivePatchContractRejectsInvalidOffsets(t *testing.T) {
	patch := V3RealtimeLivePatch{
		SessionID:    "session-a",
		RunID:        "run-a",
		StreamID:     "assistant:run-a:step:1",
		StreamKind:   "assistant_text",
		Operation:    "append",
		Step:         1,
		StepID:       "step-1",
		LiveSeqStart: 1,
		LiveSeqEnd:   1,
		OffsetStart:  0,
		OffsetEnd:    1,
		Text:         "é",
		RecordedAt:   12345,
	}
	if err := ValidateV3RealtimeLiveMessage(NewV3RealtimeLiveMessage(patch)); err == nil {
		t.Fatalf("invalid UTF-8 byte offset passed validation")
	}
	patch.OffsetEnd = 2
	if err := ValidateV3RealtimeLiveMessage(NewV3RealtimeLiveMessage(patch)); err != nil {
		t.Fatalf("valid UTF-8 byte offset rejected: %v", err)
	}
}
