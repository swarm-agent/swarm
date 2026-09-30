package api

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

// Purpose: scoped realtime extraction admits committed usage projections only,
// with principal/account isolation and an opaque cursor. Owners:
// usageScopesFromRealtimeRecord, v3RealtimeRecordVisibleToPrincipal and outbound
// frame validation. This boundary test does not claim socket replay execution.
func TestUsageScopeRealtimeExtractionAndAdmission(t *testing.T) {
	total := pebblestore.UsageScopeTotal{Kind: "task", ProjectID: "project", ID: "task", TotalTokens: 100}
	payload, err := json.Marshal(map[string]any{"turn_usage": pebblestore.SessionTurnUsageSnapshot{ScopeTotals: []pebblestore.UsageScopeTotal{total}}})
	if err != nil { t.Fatal(err) }
	r := sessionruntime.RealtimeOutboxRecord{UserID: "user", AccountScopeID: "account", Event: pebblestore.V3SessionEvent{Seq: 1, EventType: "run.usage.updated", Payload: payload}}
	p := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	if !v3RealtimeRecordVisibleToPrincipal(p, r) { t.Fatal("own receipt rejected") }
	scopes := usageScopesFromRealtimeRecord(r)
	if len(scopes) != 1 || scopes[0] != total { t.Fatalf("scope extraction: %+v", scopes) }
	r.AccountScopeID = "foreign"
	if v3RealtimeRecordVisibleToPrincipal(p, r) { t.Fatal("foreign receipt admitted") }
	r.AccountScopeID = "account"
	r.UserID = "foreign-user"
	if v3RealtimeRecordVisibleToPrincipal(p, r) { t.Fatal("foreign principal admitted") }
	r.Event.EventType = "session.metadata.updated"
	if len(usageScopesFromRealtimeRecord(r)) != 0 { t.Fatal("metadata impersonated usage") }
	frame := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindUsageScopeUpdated, EndpointCursor: "opaque", Event: &pebblestore.V3SessionEvent{EventType: V3RealtimeKindUsageScopeUpdated}}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil { t.Fatal(err) }
	frame.EndpointCursor = ""
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err == nil { t.Fatal("cursorless invalidation accepted") }
}
