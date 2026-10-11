package remote

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type memoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memoryStore) PutJSON(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = raw
	return nil
}

func (m *memoryStore) GetJSON(key string, out any) (bool, error) {
	m.mu.Lock()
	raw, ok := m.data[key]
	m.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (m *memoryStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

type fakeTokens struct {
	minted  [][]string
	revoked []string
}

func (f *fakeTokens) Mint(_ string, scopes []string) (string, string, error) {
	f.minted = append(f.minted, scopes)
	return "swk_device", "tok_device", nil
}

func (f *fakeTokens) Revoke(id string) error {
	f.revoked = append(f.revoked, id)
	return nil
}

// Requirement: a relay URL is an origin over https (http only on loopback for
// local development) so device credentials and tool traffic are never sent in
// clear text to a remote host. Owner: ValidateRelayURL.
func TestValidateRelayURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://relay.example.workers.dev":  "https://relay.example.workers.dev",
		"https://relay.example.workers.dev/": "https://relay.example.workers.dev",
		"http://localhost:8787":              "http://localhost:8787",
		"http://127.0.0.1:8787":              "http://127.0.0.1:8787",
	} {
		if got, err := ValidateRelayURL(raw); err != nil || got != want {
			t.Fatalf("%s: got %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://relay.example.com", "ftp://relay.example.com", "https://user:pw@relay.example.com", "https://relay.example.com/mcp", "https://relay.example.com?x=1", "relay.example.com", ""} {
		if _, err := ValidateRelayURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

// Requirement: remote access is off by default and owner-controlled. Init
// creates an identity without connecting; the device token carries only the
// API scopes the local ceiling needs; status never exposes the private key or
// token; reset revokes the token and deletes the identity. Owners: Init,
// Status, Reset. Threat: a configured-but-unapproved relay connection or a
// leaked device credential through status output.
func TestInitStatusAndReset(t *testing.T) {
	store := &memoryStore{data: map[string][]byte{}}
	tokens := &fakeTokens{}
	svc := NewService(store, tokens)
	if st, err := svc.Status(); err != nil || st.Configured || st.Enabled {
		t.Fatalf("fresh service not off: %+v %v", st, err)
	}
	if _, err := svc.Init(InitInput{RelayURL: "http://relay.example.com", DeviceName: "box"}); err == nil {
		t.Fatal("insecure relay accepted")
	}
	st, err := svc.Init(InitInput{RelayURL: "https://relay.example.com", DeviceName: "box"})
	if err != nil || !st.Configured || st.Enabled || st.PublicKey == "" || !strings.HasPrefix(st.DeviceID, "dev_") {
		t.Fatalf("init: %+v %v", st, err)
	}
	if len(tokens.minted) != 1 || strings.Join(tokens.minted[0], ",") != "sessions:read,automations:read,agents:read" {
		t.Fatalf("read-only device minted %v", tokens.minted)
	}
	encoded, _ := json.Marshal(st)
	if strings.Contains(string(encoded), "swk_device") || strings.Contains(string(encoded), "private") {
		t.Fatalf("status leaks credentials: %s", encoded)
	}
	if _, err := svc.Init(InitInput{RelayURL: "https://relay.example.com", DeviceName: "box"}); err == nil {
		t.Fatal("second init replaced the identity")
	}
	if st, err := svc.Reset(); err != nil || st.Configured {
		t.Fatalf("reset: %+v %v", st, err)
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != "tok_device" {
		t.Fatalf("device token not revoked: %v", tokens.revoked)
	}
	// Manage is the only ceiling that carries account-wide authority: spend
	// limits and agent role default models.
	if _, err := svc.Init(InitInput{RelayURL: "https://relay.example.com", DeviceName: "box", AllowManage: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tokens.minted[1], ","); got != "sessions:read,automations:read,agents:read,automations:write,usage:write,settings:write" {
		t.Fatalf("manage device minted %s", got)
	}
}

// Requirement: every Swarm Control tool has an explicit scope, identical on
// the device and in the reference relay. toolScope falls back to write for
// unknown names, so a missing entry would expose a manage tool (for example
// agent role defaults) to write-only clients. Owners: toolScopes here and
// TOOL_SCOPES in packages/swarm-relay/src/protocol.js. Comparing the two
// checked-in tables is the narrowest layer; the api package checks that every
// served tool appears in the relay table.
func TestToolScopesMatchRelay(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "swarm-relay", "src", "protocol.js"))
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]string{"SCOPE_READ": ScopeRead, "SCOPE_WRITE": ScopeWrite, "SCOPE_APPROVE": ScopeApprove, "SCOPE_MANAGE": ScopeManage}
	relay := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^\s+(swarm_[a-z_]+):\s+(SCOPE_[A-Z]+),$`).FindAllStringSubmatch(string(source), -1) {
		relay[match[1]] = constants[match[2]]
	}
	relayOnly := map[string]bool{"swarm_list_machines": true, "swarm_pair_machine": true, "swarm_remove_machine": true}
	for name := range relayOnly {
		if relay[name] == "" {
			t.Fatalf("relay-only tool %s has no relay scope", name)
		}
	}
	if len(relay) == 0 || len(relay) != len(toolScopes)+len(relayOnly) {
		t.Fatalf("relay has %d tool scopes, device %d", len(relay), len(toolScopes))
	}
	for name, scope := range toolScopes {
		if relay[name] != scope {
			t.Fatalf("%s: device %q relay %q", name, scope, relay[name])
		}
	}
}

// Requirement: the relay authenticates AI clients, but this machine bounds
// what any of them can do. The device proves itself with an Ed25519 signature
// bound to relay origin, device id and nonce; approvals are clamped to the
// client's request and the local ceiling; tool calls outside the ceiling are
// refused without reaching Swarm; allowed calls run through the Swarm Control
// handler with the device's scoped token. Owners: Service.session, handle,
// DecideConsent. A fake relay over a real WebSocket is the narrowest layer
// that exercises the wire protocol; the live workerd relay test covers the
// Worker itself.
func TestRelaySessionEnforcesCeiling(t *testing.T) {
	store := &memoryStore{data: map[string][]byte{}}
	svc := NewService(store, &fakeTokens{})
	var mu sync.Mutex
	controlCalls := []string{}
	svc.SetControlHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		controlCalls = append(controlCalls, r.Header.Get("Authorization")+" "+body.Method+" "+body.Params.Name)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"swarm_get_session"},{"name":"swarm_send_message"},{"name":"swarm_resolve_permission"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
	}))

	frames := make(chan map[string]any, 16)
	var relayConn *websocket.Conn
	connected := make(chan struct{})
	var publicKey ed25519.PublicKey
	var deviceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/connect" {
			http.NotFound(w, r)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		relayConn = conn
		_ = conn.WriteJSON(map[string]any{"type": "challenge", "nonce": "nonce-1"})
		var auth map[string]any
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		sig, _ := base64.StdEncoding.DecodeString(auth["signature"].(string))
		origin := "http://" + r.Host
		if auth["device_id"] != deviceID || !ed25519.Verify(publicKey, []byte(AuthMessage(origin, deviceID, "nonce-1")), sig) {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4401, "invalid device signature"))
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "ready"})
		close(connected)
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
		}
	}))
	defer server.Close()

	st, err := svc.Init(InitInput{RelayURL: server.URL, DeviceName: "box", AllowWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(st.PublicKey)
	publicKey, deviceID = ed25519.PublicKey(raw), st.DeviceID
	if _, err := svc.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go svc.Run(ctx)
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("device did not authenticate")
	}
	for !func() bool { st, _ := svc.Status(); return st.Connected }() {
		time.Sleep(10 * time.Millisecond)
	}

	// Consent: approval is clamped to request ∩ ceiling (no approve scope).
	_ = relayConn.WriteJSON(map[string]any{"type": "consent.request", "code": "ABCD-EFGH", "client_id": "c1", "client_name": "Claude", "redirect_host": "claude.ai", "scopes": []string{ScopeRead, ScopeWrite, ScopeApprove}, "expires_at": time.Now().Add(time.Minute).UnixMilli()})
	for !func() bool { st, _ := svc.Status(); return len(st.Consents) == 1 }() {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := svc.DecideConsent("abcd-efgh", true, []string{ScopeRead, ScopeWrite, ScopeApprove}); err != nil {
		t.Fatal(err)
	}
	decision := <-frames
	if decision["type"] != "consent.decision" || decision["approve"] != true || strings.Join(toStrings(decision["scopes"]), ",") != "swarm:read,swarm:write" {
		t.Fatalf("decision not clamped: %v", decision)
	}
	if _, err := svc.DecideConsent("ABCD-EFGH", true, nil); err == nil {
		t.Fatal("consent decided twice")
	}

	ask := func(id string, scopes []string, method, tool string) map[string]any {
		t.Helper()
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"name": tool, "arguments": map[string]any{}}}
		_ = relayConn.WriteJSON(map[string]any{"type": "mcp.request", "id": id, "client": map[string]any{"client_id": "c1", "scopes": scopes}, "body": body})
		select {
		case frame := <-frames:
			if frame["type"] != "mcp.response" || frame["id"] != id {
				t.Fatalf("unexpected frame %v", frame)
			}
			return frame["body"].(map[string]any)
		case <-ctx.Done():
			t.Fatal("no response")
		}
		return nil
	}
	all := []string{ScopeRead, ScopeWrite, ScopeApprove}
	if got := ask("r1", []string{ScopeWrite}, "tools/call", "swarm_get_session"); got["error"] == nil {
		t.Fatal("client without read was served")
	}
	if got := ask("r2", all, "tools/call", "swarm_resolve_permission"); got["result"].(map[string]any)["isError"] != true {
		t.Fatal("approve executed beyond device ceiling")
	}
	if got := ask("r3", all, "initialize", ""); got["error"] == nil {
		t.Fatal("non-tool method forwarded")
	}
	if got := ask("r4", all, "tools/call", "swarm_send_message"); got["result"].(map[string]any)["isError"] != false {
		t.Fatalf("allowed call failed: %v", got)
	}
	listed := ask("r5", all, "tools/list", "")
	names := []string{}
	for _, tool := range listed["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "swarm_get_session,swarm_send_message" {
		t.Fatalf("tools/list not filtered by ceiling: %v", names)
	}
	mu.Lock()
	calls := strings.Join(controlCalls, "|")
	mu.Unlock()
	if calls != "Bearer swk_device tools/call swarm_send_message|Bearer swk_device tools/list " {
		t.Fatalf("unexpected control calls: %q", calls)
	}

	if _, err := svc.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for func() bool { st, _ := svc.Status(); return st.Connected }() {
		if time.Now().After(deadline) {
			t.Fatal(errors.New("disable did not disconnect"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := []string{}
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Requirement: a relay that does not trust this device yet can hold it in a
// pairing state. The device sends its public key with a valid signature,
// reports the relay's pairing code to the owner while it waits, and becomes
// connected (code cleared) when the relay sends ready on the same connection.
// Owner: Service.session. A fake relay over a real WebSocket exercises the
// wire protocol; relay-side pairing is covered by the workerd relay check.
func TestRelayPairingThenReady(t *testing.T) {
	svc := NewService(&memoryStore{data: map[string][]byte{}}, &fakeTokens{})
	paired := make(chan struct{})
	gotKey := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"type": "challenge", "nonce": "nonce-2"})
		var auth map[string]any
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		key, _ := auth["public_key"].(string)
		raw, _ := base64.StdEncoding.DecodeString(key)
		sig, _ := base64.StdEncoding.DecodeString(auth["signature"].(string))
		if len(raw) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(raw), []byte(AuthMessage("http://"+r.Host, auth["device_id"].(string), "nonce-2")), sig) {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4401, "invalid device signature"))
			return
		}
		gotKey <- key
		_ = conn.WriteJSON(map[string]any{"type": "pairing", "code": "abcd-ef23", "expires_at": time.Now().Add(time.Minute).UnixMilli()})
		<-paired
		_ = conn.WriteJSON(map[string]any{"type": "ready"})
		var discard map[string]any
		_ = conn.ReadJSON(&discard)
	}))
	defer server.Close()

	st, err := svc.Init(InitInput{RelayURL: server.URL, DeviceName: "new box"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go svc.Run(ctx)
	wait := func(what string, ok func(Status) bool) Status {
		t.Helper()
		for {
			current, _ := svc.Status()
			if ok(current) {
				return current
			}
			if ctx.Err() != nil {
				t.Fatalf("timed out waiting for %s: %+v", what, current)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waiting := wait("pairing code", func(s Status) bool { return s.PairingCode != "" })
	if waiting.PairingCode != "ABCD-EF23" || waiting.Connected || waiting.PairingExpiresAt == 0 {
		t.Fatalf("unexpected pairing status: %+v", waiting)
	}
	if key := <-gotKey; key != st.PublicKey {
		t.Fatalf("auth public_key %q does not match device key %q", key, st.PublicKey)
	}
	close(paired)
	ready := wait("connected", func(s Status) bool { return s.Connected })
	if ready.PairingCode != "" || ready.LastError != "" {
		t.Fatalf("pairing state not cleared on ready: %+v", ready)
	}
}

// Requirement: relay clients get only tools the device token can actually
// run. Owner box-setup tools (define agents, mint client app keys, ChatGPT
// sign-in) need admin authority, which the device token never carries, so
// they are neither listed nor forwarded, even for a manage client; read tools
// that need agents:read (list_agents) are listed and forwarded. Threat: a
// relay client offered tools that always fail, or a future token change
// letting a relay client mint keys. Owners: localOnlyTools, filterTools and
// the tools/call ceiling in Service.handle. Calling handle with a fake Swarm
// Control handler is the narrowest layer that runs both checks.
func TestRelayOmitsOwnerOnlyTools(t *testing.T) {
	svc := NewService(&memoryStore{data: map[string][]byte{}}, &fakeTokens{})
	var forwarded []string
	svc.SetControlHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		forwarded = append(forwarded, body.Method+" "+body.Params.Name)
		w.Header().Set("Content-Type", "application/json")
		if body.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"swarm_list_agents"},{"name":"swarm_set_agent_model"},{"name":"swarm_define_agent"},{"name":"swarm_create_client_key"},{"name":"swarm_connect_chatgpt"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
	}))
	cfg := Config{Token: "swk_device", AllowWrite: true, AllowApprove: true, AllowManage: true}
	client := &frameClient{ClientID: "c", Scopes: []string{ScopeRead, ScopeWrite, ScopeApprove, ScopeManage}}
	call := func(body string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(svc.handle(cfg, frame{Client: client, Body: json.RawMessage(body)}), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	listed := []string{}
	for _, tool := range call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)["result"].(map[string]any)["tools"].([]any) {
		listed = append(listed, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(listed, ",") != "swarm_list_agents,swarm_set_agent_model" {
		t.Fatalf("relay listed %v", listed)
	}
	for name := range localOnlyTools {
		out := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`)
		if result, _ := out["result"].(map[string]any); result == nil || result["isError"] != true {
			t.Fatalf("%s was not refused: %v", name, out)
		}
	}
	if strings.Join(forwarded, ",") != "tools/list " {
		t.Fatalf("refused calls reached Swarm Control: %v", forwarded)
	}
	call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"swarm_list_agents","arguments":{}}}`)
	if strings.Join(forwarded, ",") != "tools/list ,tools/call swarm_list_agents" {
		t.Fatalf("list_agents not forwarded: %v", forwarded)
	}
}
