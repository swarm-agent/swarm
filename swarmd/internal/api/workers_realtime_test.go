package api

import (
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: worker invalidation is account-only, content-free, durable and replayable.
// Threat: worker events leak private definitions or disappear after disconnect.
// Boundary: worker outbox, ConfigureAutomationRealtime, V3 admission/codec and replay.
func TestWorkerRealtimeAdmissionAndReplay(t *testing.T) {
	p := testPrincipal()
	r := sessionruntime.RealtimeOutboxRecord{AccountScopeID: p.AccountScopeID, Event: pebblestore.V3SessionEvent{Seq: 1, EventType: pebblestore.WorkerUpdatedEventType}}
	if !v3RealtimeRecordVisibleToPrincipal(p, r) {
		t.Fatal("own account denied")
	}
	for _, account := range []string{"foreign", ""} {
		r.AccountScopeID = account
		if v3RealtimeRecordVisibleToPrincipal(p, r) {
			t.Fatal("foreign event admitted")
		}
	}
	frame := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindWorkerChanged, EndpointCursor: "opaque"}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil {
		t.Fatal(err)
	}
	frame.EndpointCursor = ""
	if ValidateV3RealtimeOutboundServerMessage(frame) == nil {
		t.Fatal("missing cursor accepted")
	}
	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureAutomationRealtime(db)
	initial, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer func() { conn.Close() }()
	resume := func() {
		writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, initial), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	}
	resume()
	if _, err := pebblestore.NewWorkerStore(db).CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Private worker", Instructions: "Private instructions", IdempotencyKey: "realtime-create"}, nil); err != nil {
		t.Fatal(err)
	}
	readSignal := func() {
		t.Helper()
		for i := 0; i < 12; i++ {
			f := readV3RealtimeFrame(t, conn)
			if f.Kind == V3RealtimeKindWorkerChanged {
				if f.EndpointCursor == "" || f.Session != nil || f.Event != nil {
					t.Fatalf("private content leaked: %+v", f)
				}
				return
			}
		}
		t.Fatal("worker invalidation missing")
	}
	readSignal()
	conn.Close()
	conn = dialV3RealtimeStream(t, httpServer.URL)
	resume()
	readSignal()
}
