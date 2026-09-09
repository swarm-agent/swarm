package api

import (
	"encoding/json"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: catalog signals reach any view in the owning account, never a
// foreign account, and remain opaque cursor-only control frames. The realtime
// admission/codec layer is the narrowest proof of this transport boundary.
func TestWorkspaceCatalogRealtimeAdmission(t *testing.T) {
	principal := testPrincipal()
	record := sessionruntime.RealtimeOutboxRecord{AccountScopeID: principal.AccountScopeID, UserID: "desktop", Event: pebblestore.V3SessionEvent{Seq: 1, EventType: pebblestore.WorkspaceCatalogEventType}}
	if !v3RealtimeRecordVisibleToPrincipal(principal, record) {
		t.Fatal("own account catalog denied")
	}
	record.AccountScopeID = "foreign"
	if v3RealtimeRecordVisibleToPrincipal(principal, record) {
		t.Fatal("foreign catalog admitted")
	}
	record.AccountScopeID = ""
	if v3RealtimeRecordVisibleToPrincipal(principal, record) {
		t.Fatal("unscoped catalog admitted")
	}
	frame := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindWorkspaceCatalog, EndpointCursor: "opaque"}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var decoded V3RealtimeMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.EndpointCursor != "opaque" || decoded.Session != nil || decoded.Event != nil {
		t.Fatalf("invalid control frame: %+v", decoded)
	}
	frame.EndpointCursor = ""
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err == nil {
		t.Fatal("missing cursor admitted")
	}
}

// Requirement: a committed workspace tool/service mutation wakes an already open
// socket without a session subscription, and the same event survives reconnect.
// This hermetic HTTP/WebSocket test checks the actual server publisher + replay.
func TestWorkspaceCatalogRealtimeDelivery(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	initial, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, initial), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	workspacePath := t.TempDir()
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatal(err)
	}
	if _, err := server.workspace.CreateCatalogEntryForPrincipal(testPrincipal(), workspacePath, "Catalog live", ""); err != nil {
		t.Fatal(err)
	}
	readCatalog := func() V3RealtimeMessage {
		t.Helper()
		for i := 0; i < 12; i++ {
			frame := readV3RealtimeFrame(t, conn)
			if frame.Kind == V3RealtimeKindWorkspaceCatalog {
				return frame
			}
		}
		t.Fatal("catalog signal not delivered")
		return V3RealtimeMessage{}
	}
	frame := readCatalog()
	if frame.EndpointCursor == "" {
		t.Fatal("missing replay cursor")
	}
	conn.Close()
	conn = dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, initial), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	if replay := readCatalog(); replay.Kind != frame.Kind {
		t.Fatal("catalog replay missing")
	}
}
