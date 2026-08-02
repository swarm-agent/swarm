package api

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAuthResourceRoundTripsThroughCanonicalRealtimeContract(t *testing.T) {
	payload := sessionsV3AuthResourcePayload{
		AccountScopeID: "account-1",
		EventType:      "auth.credential.activated",
		Provider:       "openai",
		RecordedAt:     10,
		EventSequence:  42,
	}
	message := V3RealtimeMessage{
		Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion,
		Kind: V3RealtimeKindAuthResource, EndpointCursor: "opaque-cursor", Auth: &payload,
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded V3RealtimeMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateV3RealtimeOutboundServerMessage(decoded); err != nil {
		t.Fatalf("auth resource rejected by canonical realtime contract: %v", err)
	}
	if decoded.Auth == nil || decoded.Auth.EventSequence != 42 || decoded.Auth.Provider != "openai" {
		t.Fatalf("replayed auth payload=%#v", decoded.Auth)
	}
}

func TestAuthResourceIsAccountScopedAndRequiresExplicitWorksetResource(t *testing.T) {
	if !v3RealtimeWorksetResourceAllowed("auth") {
		t.Fatal("auth must be a V3 realtime workset resource")
	}
	payload := sessionsV3AuthResourcePayload{AccountScopeID: "account-1", EventType: "auth.credential.upserted", EventSequence: 7}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	record := sessionruntime.RealtimeOutboxRecord{
		AccountScopeID: "account-1",
		Event:          pebblestore.V3SessionEvent{EventType: v3AuthResourceEventType, Payload: raw, Seq: 1},
	}
	if !v3RealtimeWorksetIncludesRecordResource(v3RealtimeWorksetSubscription{Resources: []string{"auth"}}, record) {
		t.Fatal("auth resource was not routed to an auth subscriber")
	}
	if v3RealtimeWorksetIncludesRecordResource(v3RealtimeWorksetSubscription{Resources: []string{"sessions"}}, record) {
		t.Fatal("auth resource leaked to a workset that did not request auth")
	}
}
