package api

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: automation control frames are admitted only for their account.
// This codec/admission test is the narrowest negative proof of the transport
// boundary; the delivery test below exercises durable reconnect replay.
func TestAutomationRealtimeAdmission(t *testing.T) {
	p := testPrincipal()
	r := sessionruntime.RealtimeOutboxRecord{AccountScopeID: p.AccountScopeID, UserID: "desktop", Event: pebblestore.V3SessionEvent{EventType: pebblestore.AutomationChangedEventType}}
	if !v3RealtimeRecordVisibleToPrincipal(p, r) { t.Fatal("own account denied") }
	for _, account := range []string{"foreign", ""} {
		r.AccountScopeID = account
		if v3RealtimeRecordVisibleToPrincipal(p, r) { t.Fatal("foreign/unscoped event admitted") }
	}
	frame := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAutomationChanged, EndpointCursor: "opaque"}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil { t.Fatal(err) }
	frame.EndpointCursor = ""
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err == nil { t.Fatal("missing cursor accepted") }
}

// Requirement: non-HTTP automation writes wake an open scoped socket and replay
// after reconnect without polling or subscribing to the synthetic session.
func TestAutomationRealtimeDelivery(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureAutomationRealtime(store)
	initial, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil { t.Fatal(err) }
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer func() { conn.Close() }()
	resume := func() {
		writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, initial), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	}
	resume()
	_, _, err = store.ApplyAutomationMutation(pebblestore.AutomationMutation{SubjectID: "writer", Actor: "user", WrittenAt: 100, MutationID: "create", Record: pebblestore.AutomationRecord{
		Scope: pebblestore.AutomationScope{AccountID: testPrincipal().AccountScopeID, WorkspaceID: "workspace"}, AutomationID: "auto", ID: "auto", Kind: "definition",
		Definition: &pebblestore.AutomationDefinition{Name: "Private name", Plans: []pebblestore.AutomationPlanBinding{{ID: "primary", Plan: pebblestore.AutomationPlanReference{SessionID: "source", PlanID: "plan", Revision: 1}}}, Schedule: pebblestore.AutomationSchedulePolicy{Kind: "manual", MissedPolicy: "skip", OverlapPolicy: "independent"}, Authorization: pebblestore.AutomationAuthorizationPolicy{Mode: "approval_required"}},
	}})
	if err != nil { t.Fatal(err) }
	readSignal := func() {
		t.Helper()
		for i := 0; i < 12; i++ {
			frame := readV3RealtimeFrame(t, conn)
			if frame.Kind == V3RealtimeKindAutomationChanged {
				if frame.EndpointCursor == "" || frame.Session != nil || frame.Event != nil { t.Fatalf("content leaked: %+v", frame) }
				return
			}
		}
		t.Fatal("automation signal missing")
	}
	readSignal()
	conn.Close()
	conn = dialV3RealtimeStream(t, httpServer.URL)
	resume()
	readSignal()
}
