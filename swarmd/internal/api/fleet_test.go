package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fleetTestSwarm struct {
	s       *Server
	account string
	user    string
	fp      string
	cleanup func()
}

// newFleetTestSwarm is one Swarm with an owner, a node key and its fleet.
func newFleetTestSwarm(t *testing.T, name string) *fleetTestSwarm {
	t.Helper()
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "fleet"))
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	node, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{
		SwarmID: "swarm-" + name, Name: name, Role: "master",
		PublicKey: base64.StdEncoding.EncodeToString(public), PrivateKey: base64.StdEncoding.EncodeToString(private),
		Fingerprint: fleet.Fingerprint(public),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetSwarmStore(swarmStore)
	s.SetFleet(pebblestore.NewFleetStore(store), pebblestore.NewAuthStore(store))
	return &fleetTestSwarm{s: s, account: actor.AccountScopeID, user: actor.UserID, fp: node.Fingerprint, cleanup: func() {
		s.CloseFleetListener()
		_ = store.Close()
		cleanup()
	}}
}

// call is the person on this Swarm calling a fleet route (onSocket: over
// the owner socket, as swarmctl does).
func (f *fleetTestSwarm) call(t *testing.T, method, path, body string, onSocket bool) (int, map[string]any) {
	t.Helper()
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(method, path, strings.NewReader(body)), f.user, f.account)
	if onSocket {
		req = markLocalTransportRequest(req)
	}
	w := httptest.NewRecorder()
	f.s.handleFleet(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (f *fleetTestSwarm) invite(t *testing.T) string {
	t.Helper()
	code, out := f.call(t, http.MethodPost, "/v3/fleet/invites", `{"address":"127.0.0.1:0"}`, true)
	if code != http.StatusCreated {
		t.Fatalf("invite: HTTP %d %v", code, out)
	}
	return out["code"].(string)
}

// Purpose: a link goes one way. B invites A with a one-time code made only
// on B; A redeems it and from then on uses B's own API as its owner, over
// TLS pinned to both keys, with the session B gave it. B gets nothing that
// reaches A, decides alone who controls it, and ends A's control at once.
// Threat: a leaked client session making codes; a code used twice; another
// key with A's session; A with a revoked session; A changing who controls
// B. Boundary: Server.handleFleet, Server.fleetLinkHandler, fleet.Client.
func TestFleetLinkGoesOneWay(t *testing.T) {
	a := newFleetTestSwarm(t, "home-a")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "box-b")
	defer b.cleanup()
	c := newFleetTestSwarm(t, "other-c")
	defer c.cleanup()

	// A code is made only on the machine.
	if code, _ := b.call(t, http.MethodPost, "/v3/fleet/invites", `{"address":"127.0.0.1:0"}`, false); code != http.StatusForbidden {
		t.Fatalf("code off the owner socket: HTTP %d", code)
	}
	if code, _ := b.call(t, http.MethodPost, "/v3/fleet/invites", `{"address":"8.8.8.8"}`, true); code != http.StatusBadRequest {
		t.Fatalf("public address: HTTP %d", code)
	}
	linkCode := b.invite(t)

	// A links with it.
	body, _ := json.Marshal(map[string]string{"code": linkCode, "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: HTTP %d %v", code, out)
	}
	if code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b", "", false); code != http.StatusOK || out["reached"] != true {
		t.Fatalf("check: HTTP %d %v", code, out)
	}

	// A uses B's own API as its owner: B's fleet answer names A as its home.
	code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b/api/v3/fleet", "", false)
	homes, _ := out["homes"].([]any)
	if code != http.StatusOK || out["name"] != "box-b" || len(homes) != 1 || homes[0].(map[string]any)["fingerprint"] != a.fp {
		t.Fatalf("B's API through the link: HTTP %d %v", code, out)
	}
	// B's client sessions list A's session by name, as the person sees it.
	clients, err := b.s.identitySessions.ListClientSessions()
	if err != nil || len(clients) != 1 || clients[0].Name != "home home-a" {
		t.Fatalf("B's client sessions: %+v %v", clients, err)
	}

	// A may not decide who controls B.
	for _, path := range []string{"/v3/fleet/machines/b/api/v1/auth/clients", "/v3/fleet/machines/b/api/v3/fleet/homes/" + a.fp} {
		method := http.MethodGet
		if strings.Contains(path, "/homes/") {
			method = http.MethodDelete
		}
		if code, out := a.call(t, method, path, "", false); code != http.StatusForbidden {
			t.Fatalf("%s %s through the link: HTTP %d %v", method, path, code, out)
		}
	}

	// The code worked once.
	body, _ = json.Marshal(map[string]string{"code": linkCode, "name": "b"})
	if code, _ := c.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusBadGateway {
		t.Fatalf("code used twice: HTTP %d", code)
	}

	// One way: B has no machine and holds nothing for A.
	if code, out := b.call(t, http.MethodGet, "/v3/fleet", "", false); code != http.StatusOK || len(out["machines"].([]any)) != 0 {
		t.Fatalf("B's machines: %v", out)
	}
	if _, found, _ := b.s.fleetTokens.GetFleetLinkToken(b.account, "home-a"); found {
		t.Fatal("B holds a session for A")
	}

	// A's session with another key, and A's key without its session, are refused.
	machine, _, _ := a.s.fleet.GetMachine("b")
	token, _, err := a.s.fleetTokens.GetFleetLinkToken(a.account, "b")
	if err != nil || token == "" {
		t.Fatalf("A's sealed session: %v", err)
	}
	cCert, _, _ := c.s.fleetCertificate()
	if err := fleet.NewClient(cCert, machine.Address, b.fp, token).JSON(t.Context(), http.MethodGet, "/v3/fleet", nil, nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("A's session with C's key: %v", err)
	}
	aCert, _, _ := a.s.fleetCertificate()
	if err := fleet.NewClient(aCert, machine.Address, b.fp, "").JSON(t.Context(), http.MethodGet, "/v3/fleet", nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("A's key without its session: %v", err)
	}

	// B ends A's control: A is out at once.
	if code, out := b.call(t, http.MethodDelete, "/v3/fleet/homes/"+a.fp, "", false); code != http.StatusOK {
		t.Fatalf("drop: HTTP %d %v", code, out)
	}
	if code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b", "", false); out["reached"] != false {
		t.Fatalf("check after drop: HTTP %d %v", code, out)
	}
	// With no code open and no home, B stops listening.
	if b.s.fleetListenAddress() != "" {
		t.Fatal("B still listens with nothing to answer")
	}

	// Linked again, A unlinks: B is told and forgets A.
	if code, _ := a.call(t, http.MethodDelete, "/v3/fleet/machines/b", "", false); code != http.StatusOK {
		t.Fatalf("forget the dropped machine: HTTP %d", code)
	}
	body, _ = json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link again: HTTP %d %v", code, out)
	}
	if code, out := a.call(t, http.MethodDelete, "/v3/fleet/machines/b", "", false); code != http.StatusOK || out["machine_told"] != true {
		t.Fatalf("unlink: HTTP %d %v", code, out)
	}
	if homes, _ := b.s.fleet.ListHomes(); len(homes) != 0 {
		t.Fatalf("B still has homes: %+v", homes)
	}
	deadline := time.Now().Add(time.Second)
	for b.s.fleetListenAddress() != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.s.fleetListenAddress() != "" {
		t.Fatal("B still listens after its only home left")
	}
}
