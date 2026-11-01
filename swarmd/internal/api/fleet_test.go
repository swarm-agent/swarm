package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
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

	// Keys, secret values, the vault, AI keys and B's own permission rules
	// never cross the link; listing secret names does.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/auth/credentials", `{"provider":"openai","type":"api","api_key":"x"}`},
		{http.MethodGet, "/v1/auth/credentials", ""},
		{http.MethodPost, "/v1/auth/credentials/verify", `{}`},
		{http.MethodPost, "/v1/auth/codex/oauth/complete", `{}`},
		{http.MethodPost, "/v1/onboarding/provider/credential", `{}`},
		{http.MethodPost, "/v1/vault/export", `{}`},
		{http.MethodPut, "/v1/secrets/demo/value", `{"value":"x"}`},
		{http.MethodGet, "/v1/secrets/demo/value", ""},
		{http.MethodPost, "/v1/secrets", `{"name":"demo","value":"x"}`},
		{http.MethodPost, "/v3/auth/tokens", `{}`},
		{http.MethodPatch, "/v3/permissions", `{}`},
	} {
		if code, out := a.call(t, c.method, "/v3/fleet/machines/b/api"+c.path, c.body, false); code != http.StatusForbidden {
			t.Fatalf("%s %s through the link: HTTP %d %v", c.method, c.path, code, out)
		}
	}
	if code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b/api/v1/secrets", "", false); code == http.StatusForbidden {
		t.Fatalf("listing secret names through the link: HTTP %d %v", code, out)
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

// Purpose: a linked machine that has gone away must not hold the home. The
// per-machine proxy answers 502 with the reason within the link's bounds and
// records the failure; unlink gives up telling the machine after its own
// bound and unlinks here anyway, with machine_told false. Threat: a
// blackholed address holding both for the kernel's SYN retries (minutes).
// Boundary: Server.handleFleetForward, Server.handleFleetUnlink,
// fleet.NewClient. A real link redirected to a TCP listener that accepts and
// never answers is the narrowest hermetic stand-in for a gone machine.
func TestFleetGoneMachineFailsFast(t *testing.T) {
	a := newFleetTestSwarm(t, "home-a")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "box-b")
	defer b.cleanup()
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: HTTP %d %v", code, out)
	}

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var held []net.Conn // accepted, never answered
		for {
			conn, err := silent.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			held = append(held, conn)
		}
	}()
	defer func() {
		silent.Close()
		<-done
	}()
	machine, _, _ := a.s.fleet.GetMachine("b")
	machine.Address, machine.LastError = silent.Addr().String(), ""
	if err := a.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b/api/v3/fleet", "", false)
	if code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out), "could not reach b") {
		t.Fatalf("proxy to a gone machine: HTTP %d %v", code, out)
	}
	if elapsed := time.Since(start); elapsed > fleet.TLSHandshakeTimeout+3*time.Second {
		t.Fatalf("proxy held %v", elapsed)
	}
	if noted, _, _ := a.s.fleet.GetMachine("b"); noted.LastError == "" {
		t.Fatal("proxy failure not recorded")
	}

	previous := fleetUnlinkTellTimeout
	fleetUnlinkTellTimeout = 200 * time.Millisecond
	defer func() { fleetUnlinkTellTimeout = previous }()
	start = time.Now()
	code, out = a.call(t, http.MethodDelete, "/v3/fleet/machines/b", "", false)
	if code != http.StatusOK || out["unlinked"] != true || out["machine_told"] != false || out["detail"] == nil {
		t.Fatalf("unlink a gone machine: HTTP %d %v", code, out)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("unlink held %v", elapsed)
	}
	if _, found, _ := a.s.fleet.GetMachine("b"); found {
		t.Fatal("machine kept after unlink")
	}
	if _, found, _ := a.s.fleetTokens.GetFleetLinkToken(a.account, "b"); found {
		t.Fatal("session kept after unlink")
	}
}

// Purpose: an ordinary call through the home's per-machine proxy to a machine
// that completes the pinned TLS handshake but never answers (or a pooled
// connection to one that has gone away) fails at the link's header bound with
// a 502 and a recorded last_error, rather than holding the caller until it
// gives up. Threat: the proxy lifting the header bound, so only new
// connections are bounded (dial and TLS) and the console's per-machine reads
// hang for its own 60 s timeout or forever. Boundary:
// Server.handleFleetForward, fleetForwardHeaderBound, fleet.Client.Do. A real
// TLS server holding b's node key, which accepts every request and never
// answers, is the narrowest hermetic stand-in; the test takes about 30 s.
func TestFleetProxySilentMachineFailsAtHeaderBound(t *testing.T) {
	a := newFleetTestSwarm(t, "home-a")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "box-b")
	defer b.cleanup()
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: HTTP %d %v", code, out)
	}

	cert, _, err := b.s.fleetCertificate()
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var reached sync.WaitGroup
	reached.Add(1)
	var once sync.Once
	silent := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(reached.Done) // TLS done, request read: the machine is up
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	silent.TLS = fleet.ServerTLS(cert)
	silent.StartTLS()
	defer silent.Close()
	defer close(release)
	machine, _, _ := a.s.fleet.GetMachine("b")
	machine.Address, machine.LastError = strings.TrimPrefix(silent.URL, "https://"), ""
	if err := a.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b/api/v3/fleet", "", false)
	elapsed := time.Since(start)
	reached.Wait()
	if code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out), "could not reach b") || !strings.Contains(fmt.Sprint(out), "no answer within") {
		t.Fatalf("proxy to a silent machine: HTTP %d %v after %v", code, out, elapsed)
	}
	if elapsed < fleet.ResponseHeaderTimeout-time.Second || elapsed > fleet.ResponseHeaderTimeout+3*time.Second {
		t.Fatalf("proxy bounded at %v, want about %v", elapsed, fleet.ResponseHeaderTimeout)
	}
	noted, _, _ := a.s.fleet.GetMachine("b")
	if !strings.Contains(noted.LastError, "no answer within") {
		t.Fatalf("last_error not recorded: %q", noted.LastError)
	}
}

// Purpose: a long-poll forwarded through the home's per-machine proxy that
// names its wait as `wait` (/v3/signals, /v3/sessions:agent-events; 30 s
// maximum) gets the held-wait bound, so a 30 s poll with nothing new answers
// through the link and leaves the machine's last_error empty. Threat: the
// proxy reading only wait_seconds, so such a poll gets the plain 30 s header
// bound, which starts before the dial while the machine's wait starts after
// parsing; every idle poll then fails with 502 "no answer within 30s" and
// noteFleetContact marks a healthy machine as failing (the console shows it
// offline). Boundary: Server.handleFleetForward, fleetForwardHeaderBound,
// fleet.Client.Do. A real TLS server holding b's node key that answers just
// after 30 s, as a machine whose wait started late does, is the narrowest
// hermetic stand-in; the test takes about 31 s.
func TestFleetProxyWaitLongPollOutlastsHeaderBound(t *testing.T) {
	a := newFleetTestSwarm(t, "home-a")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "box-b")
	defer b.cleanup()
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: HTTP %d %v", code, out)
	}

	cert, _, err := b.s.fleetCertificate()
	if err != nil {
		t.Fatal(err)
	}
	hold := fleet.ResponseHeaderTimeout + 500*time.Millisecond
	polling := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wait") != "30" {
			http.Error(w, "wait not forwarded", http.StatusBadRequest)
			return
		}
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"signals":[]}`))
	}))
	polling.TLS = fleet.ServerTLS(cert)
	polling.StartTLS()
	defer polling.Close()
	machine, _, _ := a.s.fleet.GetMachine("b")
	machine.Address, machine.LastError = strings.TrimPrefix(polling.URL, "https://"), ""
	if err := a.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code, out := a.call(t, http.MethodGet, "/v3/fleet/machines/b/api/v3/signals?wait=30", "", false)
	elapsed := time.Since(start)
	if code != http.StatusOK || out["signals"] == nil {
		t.Fatalf("proxied wait long-poll: HTTP %d %v after %v", code, out, elapsed)
	}
	if elapsed < hold {
		t.Fatalf("answered after %v, before the machine's %v hold", elapsed, hold)
	}
	if noted, _, _ := a.s.fleet.GetMachine("b"); noted.LastError != "" {
		t.Fatalf("healthy machine recorded last_error %q", noted.LastError)
	}
}

// Purpose: a linked catalog call (manage_jobs, manage_setup, media and
// designs, manage_workers) that forwards wait_seconds gets the held-wait
// header bound, so a linked get_job asked to wait longer than 30 s receives
// the machine's held answer and leaves the machine's last_error empty.
// Threat: callFleetCatalog sending the call with the plain 30 s header bound
// while the machine holds its answer for wait_seconds; the call fails with
// "no answer within 30s", noteFleetContact marks a healthy machine as failing
// (the console shows it offline), and a give_job that already started there
// reports an error, so the caller may retry and start it twice. Boundary:
// Server.callFleetJobs, Server.callFleetCatalog, fleet.WithHeldWait,
// fleet.Client.Do. A real TLS server holding b's node key that answers
// /v3/jobs/get_job just after 30 s is the narrowest hermetic stand-in; the
// test takes about 31 s.
func TestFleetLinkedJobsWaitOutlastsHeaderBound(t *testing.T) {
	a := newFleetTestSwarm(t, "home-a")
	defer a.cleanup()
	b := newFleetTestSwarm(t, "box-b")
	defer b.cleanup()
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: HTTP %d %v", code, out)
	}

	cert, _, err := b.s.fleetCertificate()
	if err != nil {
		t.Fatal(err)
	}
	hold := fleet.ResponseHeaderTimeout + 500*time.Millisecond
	holding := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if r.Method != http.MethodPost || r.URL.Path != JobsPath+"/get_job" || json.NewDecoder(r.Body).Decode(&got) != nil || got["wait_seconds"] != float64(31) {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"j1","status":"done"}`))
	}))
	holding.TLS = fleet.ServerTLS(cert)
	holding.StartTLS()
	defer holding.Close()
	machine, _, _ := a.s.fleet.GetMachine("b")
	machine.Address, machine.LastError = strings.TrimPrefix(holding.URL, "https://"), ""
	if err := a.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(`{"machine":"b","action":"get_job","job_id":"j1","wait_seconds":31}`), &args); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: a.user, AccountScopeID: a.account}
	start := time.Now()
	result, err := a.s.callFleetJobs(context.Background(), principal, "b", "get_job", args)
	elapsed := time.Since(start)
	if err != nil || !strings.Contains(string(result), `"status":"done"`) {
		t.Fatalf("linked get_job with wait_seconds 31: %s, %v after %v", result, err, elapsed)
	}
	if elapsed < hold {
		t.Fatalf("answered after %v, before the machine's %v hold", elapsed, hold)
	}
	if noted, _, _ := a.s.fleet.GetMachine("b"); noted.LastError != "" {
		t.Fatalf("healthy machine recorded last_error %q", noted.LastError)
	}
}
