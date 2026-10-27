package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: exercise handleFleetForward and fleet.Client.Upgrade through real
// in-process home and pinned-TLS machine handlers, the narrowest layer proving
// realtime bytes travel both ways without substituting the app's credentials.
// Refused upgrades and lost contact must remain HTTP errors, never false 101s.
func TestFleetWebSocketRealtime(t *testing.T) {
	a := newFleetTestSwarm(t, "home")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "machine")
	defer b.cleanup()
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	app, err := a.s.identitySessions.IssueClientSession("test app")
	if err != nil {
		t.Fatal(err)
	}
	linkToken, found, err := a.s.fleetTokens.GetFleetLinkToken(a.account, "b")
	if err != nil || !found || linkToken == app.Token {
		t.Fatal("missing distinct link session")
	}
	home := httptest.NewServer(a.s.withDesktopBoundary(a.s.Handler()))
	defer home.Close()
	headers := http.Header{"X-Swarm-Token": {app.Token}, "Cookie": {"app=private"}, "Authorization": {"Bearer app-private"}}
	url := "ws" + strings.TrimPrefix(home.URL, "http") + "/v3/fleet/machines/b/api"
	dialer := gorillaws.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, resp, err := dialer.Dial(url+V3RealtimeStreamPath, headers)
	if err != nil {
		t.Fatalf("upgrade: %v response=%v", err, resp)
	}
	defer conn.Close()
	hello := readV3RealtimeFrameIncludingHello(t, conn)
	if hello.Kind != V3RealtimeKindHello || hello.EndpointCursor == "" {
		t.Fatalf("hello: %+v", hello)
	}
	// The machine authenticates only its link session paired with the home's key;
	// receiving hello proves the app token (unknown there) was not forwarded.
	snapshot := pebblestore.SessionSnapshot{ID: "fleet-live", UserID: b.user, AccountScopeID: b.account, Title: "before"}
	mutate := func(kind string, id string) sessionruntime.SessionMutationResult {
		result, err := b.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: kind, SessionID: snapshot.ID, UserID: b.user, AccountScopeID: b.account, Session: &snapshot, ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, NowUnixMs: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	mutate(sessionruntime.SessionMutationCreateSession, "create")
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: snapshot.ID, SubscriptionID: "fleet-sub"})
	for i := 0; i < 10; i++ {
		if frame := readV3RealtimeFrame(t, conn); frame.Kind == V3RealtimeKindReplayDone {
			break
		} else if i == 9 {
			t.Fatal("no replay completion")
		}
	}
	snapshot.Title = "after"
	changed := mutate(sessionruntime.SessionMutationUpdateMetadata, "update")
	for i := 0; i < 10; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Event != nil && frame.Event.Seq == changed.Event.Seq {
			if frame.SessionID != snapshot.ID {
				t.Fatal("wrong machine session")
			}
			break
		}
		if i == 9 {
			t.Fatal("no live event")
		}
	}
	conn.Close()
	scoped, _, err := a.s.security.CreateScopedTokenUnder(nil, "reader", []string{"sessions:read"}, a.account, a.user, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := a.s.fleet.GetMachine("b")
	cDenied, denied, deniedErr := dialer.Dial(url+V3RealtimeStreamPath, http.Header{"X-Swarm-Token": {scoped}})
	if cDenied != nil {
		cDenied.Close()
	}
	if deniedErr == nil || denied == nil || denied.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner: %v %v", deniedErr, denied)
	}
	denied.Body.Close()
	after, _, _ := a.s.fleet.GetMachine("b")
	if before != after {
		t.Fatal("non-owner contacted machine")
	}
	for _, tc := range []struct {
		path   string
		header http.Header
		status int
	}{
		{V3RealtimeStreamPath, nil, http.StatusUnauthorized},
		{V3RealtimeStreamPath, http.Header{"X-Swarm-Token": {app.Token}, "Origin": {"https://untrusted.example"}}, http.StatusForbidden},
		{"/v3/fleet", headers, http.StatusBadGateway},
	} {
		c, response, err := dialer.Dial(url+tc.path, tc.header)
		if c != nil {
			c.Close()
		}
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Fatalf("refusal: %v %v", err, response)
		}
		response.Body.Close()
	}
	machine, _, _ := a.s.fleet.GetMachine("b")
	if machine.LastError == "" {
		t.Fatal("non-101 not recorded")
	}
	b.s.CloseFleetListener()
	c, response, err := dialer.Dial(url+V3RealtimeStreamPath, headers)
	if c != nil {
		c.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("offline: %v %v", err, response)
	}
	response.Body.Close()
	machine, _, _ = a.s.fleet.GetMachine("b")
	if machine.LastError == "" {
		t.Fatal("contact failure not recorded")
	}
}
