package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Fake credential: never a real key. The bridge must keep it out of every
// output stream and file.
const mcpTestSecret = "swk_fake-bridge-key-0000"

// mcpTestDaemon is a Unix-socket fixture standing in for the daemon's owner
// socket: it issues fake AI keys, deletes them, and answers /mcp only for a
// live key, echoing the method so forwarding can be checked exactly.
type mcpTestDaemon struct {
	t      *testing.T
	socket string

	mu        sync.Mutex
	now       time.Time
	issued    []string
	live      map[string]bool
	purged    []string
	mintBody  map[string]any
	mcpCalls  []string
	mintCode  int
	mcpCode   int
	requests  int
	authOnKey []string
}

func newMCPTestDaemon(t *testing.T) *mcpTestDaemon {
	d := &mcpTestDaemon{t: t, now: time.Now(), live: map[string]bool{}}
	d.socket = setupFixture(t, d.serve)
	return d
}

func (d *mcpTestDaemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v3/auth/tokens":
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Swarm-Token") != "" {
			d.t.Error("key issuance must ride on socket ownership, not a credential")
		}
		if d.mintCode != 0 {
			w.WriteHeader(d.mintCode)
			_, _ = w.Write([]byte(`{"error":"trusted principal required"}`))
			return
		}
		d.mintBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&d.mintBody)
		id := "key-" + string(rune('1'+len(d.issued)))
		d.issued = append(d.issued, id)
		d.live[id] = true
		secret := mcpTestSecret
		if len(d.issued) > 1 {
			secret = strings.TrimSuffix(mcpTestSecret, "0000") + id
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "token": secret, "record": map[string]any{"id": id, "expires_at": d.now.Add(30 * time.Minute).UnixMilli()}})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v3/auth/tokens/"):
		id := strings.TrimPrefix(r.URL.Path, "/v3/auth/tokens/")
		if r.URL.Query().Get("purge") != "true" || !d.live[id] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(d.live, id)
		d.purged = append(d.purged, id)
		_, _ = w.Write([]byte(`{"ok":true,"deleted":true}`))
	case r.Method == http.MethodPost && r.URL.Path == "/mcp":
		auth := r.Header.Get("Authorization")
		d.authOnKey = append(d.authOnKey, auth)
		if r.Header.Get("Content-Type") != "application/json" {
			d.t.Error("MCP message forwarded without application/json")
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			d.t.Errorf("forwarded message is not the client's JSON: %v", err)
		}
		d.mcpCalls = append(d.mcpCalls, msg.Method)
		if d.mcpCode != 0 {
			w.WriteHeader(d.mcpCode)
			_, _ = w.Write([]byte(`{"error":"Swarm Control requires a scoped bearer token"}`))
			return
		}
		if !strings.HasPrefix(auth, "Bearer swk_") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// Indented on purpose: the bridge must re-emit it as one line.
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"method": msg.Method, "text": "line one\nline two"}})
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// ownedBy reports the real file but as owned by uid, so owner checks do not
// depend on which user runs the tests.
func ownedBy(uid uint32) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		return fakeOwnerInfo{FileInfo: info, uid: uid}, nil
	}
}

type fakeOwnerInfo struct {
	os.FileInfo
	uid  uint32
	mode os.FileMode
}

func (f fakeOwnerInfo) Mode() os.FileMode {
	if f.mode != 0 {
		return f.mode
	}
	return f.FileInfo.Mode()
}
func (f fakeOwnerInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }

func runBridgeForTest(t *testing.T, d *mcpTestDaemon, input string, extra ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--socket", d.socket}, extra...)
	err := runMCPBridge(ctx, args, strings.NewReader(input), &stdout, &stderr, 1000, ownedBy(1000))
	return stdout.String(), stderr.String(), err
}

// Requirement: a local AI client speaks MCP stdio (newline-delimited
// JSON-RPC) to the bridge, and every message reaches the daemon's Swarm
// Control endpoint unchanged with a full AI key the bridge got over the owner
// socket. Threats: a lost or merged reply, a reply split over lines, a
// malformed line killing the session, a notification answered, the key
// leaking to output or disk, or a key left behind after exit. Owner:
// runMCPBridge / mcpBridge.serve / forward / shutdown. A Unix fixture of the
// owner socket is the narrowest layer that observes the exact HTTP the bridge
// sends; the daemon side of the contract is TestControlMCPLocalBridgeKey.
func TestMCPBridgeForwardsStdioToSwarmControl(t *testing.T) {
	d := newMCPTestDaemon(t)
	home, tmp := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		``,
		`{not json`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":9,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"swarm_list_sessions"}}`,
	}, "\n") // The last message has no trailing newline: EOF ends it.
	stdout, stderr, err := runBridgeForTest(t, d, input)
	if err != nil {
		t.Fatalf("bridge failed: %v (stderr %q)", err, stderr)
	}
	if d.mintBody["ai_access"] != "full" || d.mintBody["expires_in_seconds"] != float64(1800) || d.mintBody["name"] != mcpBridgeKeyName {
		t.Fatalf("key request: %v", d.mintBody)
	}
	// Requests run concurrently, so arrival order is not fixed.
	sort.Strings(d.mcpCalls)
	if got := strings.Join(d.mcpCalls, ","); got != "initialize,notifications/initialized,tools/call,tools/list" {
		t.Fatalf("forwarded %q; the parse error and the client response must not be forwarded", got)
	}
	for _, auth := range d.authOnKey {
		if auth != "Bearer "+mcpTestSecret {
			t.Fatal("a message was forwarded without the bridge's AI key")
		}
	}
	replies := map[string]map[string]any{}
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		var reply map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatalf("stdout line is not one JSON-RPC message: %q", scanner.Text())
		}
		id, _ := json.Marshal(reply["id"])
		replies[string(id)] = reply
	}
	if len(replies) != 4 {
		t.Fatalf("want replies for 1, \"two\", 3 and the parse error; got %q", stdout)
	}
	for id, method := range map[string]string{`1`: "initialize", `"two"`: "tools/list", `3`: "tools/call"} {
		result, _ := replies[id]["result"].(map[string]any)
		if result["method"] != method || result["text"] != "line one\nline two" {
			t.Fatalf("reply %s: %v", id, replies[id])
		}
	}
	if parse, _ := replies["null"]["error"].(map[string]any); parse["code"] != float64(-32700) {
		t.Fatalf("malformed line must get a JSON-RPC parse error: %v", replies["null"])
	}
	if len(d.issued) != 1 || len(d.purged) != 1 || d.purged[0] != d.issued[0] || len(d.live) != 0 {
		t.Fatalf("the bridge's key must be deleted at exit: issued %v purged %v", d.issued, d.purged)
	}
	if strings.Contains(stdout, mcpTestSecret) || strings.Contains(stderr, mcpTestSecret) {
		t.Fatal("the AI key reached an output stream")
	}
	for _, dir := range []string{home, tmp} {
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() {
				if data, _ := os.ReadFile(path); bytes.Contains(data, []byte(mcpTestSecret)) {
					t.Errorf("the AI key was written to %s", path)
				}
			}
			return nil
		})
	}
}

// Requirement: --access picks the AI key level; anything else is refused
// before the daemon is asked. Threat: a typo silently minting a full key.
func TestMCPBridgeAccessLevel(t *testing.T) {
	d := newMCPTestDaemon(t)
	if _, _, err := runBridgeForTest(t, d, "", "--access", "read"); err != nil || d.mintBody["ai_access"] != "read" {
		t.Fatalf("read level: %v %v", err, d.mintBody)
	}
	before := d.requests
	if _, _, err := runBridgeForTest(t, d, "", "--access", "admin"); err == nil || d.requests != before {
		t.Fatal("an unknown level must be refused without contacting Swarm")
	}
}

// Requirement: only the non-root OS user that owns Swarm's owner socket may
// run the bridge; a stopped Swarm and a wrong user get clear errors on stderr
// and nothing on stdout. Threats: root or another user driving the owner's
// Swarm, a symlink or plain file posing as the socket, a key minted for a
// refused caller. Owner: checkMCPOwnerSocket, then runMCPBridge's ordering
// (refuse before any request). The kernel's 0600 socket mode remains the
// enforcing boundary; this check makes refusal explicit and early.
func TestMCPBridgeRefusesNonOwner(t *testing.T) {
	d := newMCPTestDaemon(t)
	info, err := os.Lstat(d.socket)
	if err != nil {
		t.Fatal(err)
	}
	stat := func(info os.FileInfo, err error) func(string) (os.FileInfo, error) {
		return func(string) (os.FileInfo, error) { return info, err }
	}
	cases := []struct {
		name  string
		euid  int
		lstat func(string) (os.FileInfo, error)
		want  string
	}{
		{"root", 0, stat(fakeOwnerInfo{FileInfo: info, uid: 0}, nil), "as root"},
		{"other user", 1001, stat(fakeOwnerInfo{FileInfo: info, uid: 1000}, nil), "not Swarm's owner"},
		{"plain file", 1000, stat(fakeOwnerInfo{FileInfo: info, uid: 1000, mode: 0o600}, nil), "not Swarm's owner socket"},
		{"symlink", 1000, stat(fakeOwnerInfo{FileInfo: info, uid: 1000, mode: os.ModeSymlink | 0o777}, nil), "not Swarm's owner socket"},
		{"no access", 1001, stat(nil, &fs.PathError{Op: "lstat", Path: d.socket, Err: fs.ErrPermission}), "may not use"},
		{"not running", 1000, stat(nil, &fs.PathError{Op: "lstat", Path: d.socket, Err: fs.ErrNotExist}), "not running"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		err := runMCPBridge(context.Background(), []string{"--socket", d.socket}, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &stdout, &stderr, tc.euid, tc.lstat)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", tc.name, err, tc.want)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%s: stdout must carry only MCP, got %q", tc.name, stdout.String())
		}
	}
	if d.requests != 0 || len(d.issued) != 0 {
		t.Fatal("a refused caller reached Swarm")
	}
	if err := checkMCPOwnerSocket(d.socket, 1000, ownedBy(1000)); err != nil {
		t.Fatalf("the owner must be admitted: %v", err)
	}
}

// Requirement: when Swarm refuses the caller or the key, the bridge says so
// and stops instead of minting around the refusal; when Swarm is down it
// says how to start it. Threats: a revoked key silently replaced, a refusal
// surfacing as a hang. Owner: mint, forward (401 → errMCPBridgeRefused),
// handle/serve, unreachable.
func TestMCPBridgeReportsRefusalAndStoppedSwarm(t *testing.T) {
	refused := newMCPTestDaemon(t)
	refused.mintCode = http.StatusUnauthorized
	stdout, _, err := runBridgeForTest(t, refused, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if err == nil || !strings.Contains(err.Error(), "refused to give this user an AI key") || stdout != "" || len(refused.mcpCalls) != 0 {
		t.Fatalf("issuance refusal: %v %q", err, stdout)
	}

	revoked := newMCPTestDaemon(t)
	revoked.mcpCode = http.StatusUnauthorized
	stdout, _, err = runBridgeForTest(t, revoked, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n")
	if !errors.Is(err, errMCPBridgeRefused) {
		t.Fatalf("a refused key must stop the bridge: %v", err)
	}
	var reply struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(stdout), &reply) != nil || reply.ID != 7 || reply.Error.Code != -32603 || !strings.Contains(reply.Error.Message, "revoked or expired") {
		t.Fatalf("the pending request must get the refusal: %q", stdout)
	}
	if len(revoked.issued) != 1 || len(revoked.live) != 0 {
		t.Fatalf("no replacement key may be minted and the refused one is deleted: issued %v live %v", revoked.issued, revoked.live)
	}

	// A socket file nothing listens on: Swarm stopped without cleanup.
	dir, err := os.MkdirTemp("", "mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	stale := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	var out, errOut bytes.Buffer
	err = runMCPBridge(context.Background(), []string{"--socket", stale}, strings.NewReader(""), &out, &errOut, 1000, ownedBy(1000))
	if err == nil || !strings.Contains(err.Error(), "Swarm is not running") || out.Len() != 0 {
		t.Fatalf("stopped Swarm: %v %q", err, out.String())
	}
}

// Requirement: an oversized line is answered with a JSON-RPC error and the
// session continues. Threat: one huge message ending the session or being
// buffered without bound. Owner: readMCPLine / serve.
func TestMCPBridgeOversizedLine(t *testing.T) {
	d := newMCPTestDaemon(t)
	huge := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", mcpBridgeMaxMessage) + `"}}`
	stdout, _, err := runBridgeForTest(t, d, huge+"\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.Contains(stdout, `"code":-32600`) || !strings.Contains(stdout, `"id":2`) || len(d.mcpCalls) != 1 {
		t.Fatalf("oversized line: %q calls %v", stdout, d.mcpCalls)
	}
}

// Requirement: the bridge renews its key before expiry while keeping the
// replaced key valid long enough for a provider key link (10 minutes) the AI
// handed out, deletes replaced keys once expired and idle, and deletes all of
// them at exit. Threats: a session dying at the 30-minute mark, a key link
// broken by renewal, expired keys piling up. Owner: mcpBridge.acquire /
// release / shutdown with an injected clock.
func TestMCPBridgeRenewsAndDeletesKeys(t *testing.T) {
	d := newMCPTestDaemon(t)
	clock := d.now
	bridge := &mcpBridge{client: mcpBridgeClient(d.socket), socket: d.socket, access: "full", now: func() time.Time { return clock }, log: &bytes.Buffer{}, out: &bytes.Buffer{}}
	ctx := context.Background()
	use := func() *mcpBridgeKey {
		t.Helper()
		key, err := bridge.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		bridge.release(key)
		return key
	}
	first := use()
	clock = clock.Add(14 * time.Minute)
	if use() != first || len(d.issued) != 1 {
		t.Fatal("renewed too early")
	}
	clock = clock.Add(time.Minute) // 15 minutes left: renew.
	d.mu.Lock()
	d.now = clock
	d.mu.Unlock()
	second := use()
	if second == first || len(d.issued) != 2 || len(d.purged) != 0 {
		t.Fatalf("renewal must mint a new key and keep the old one valid: issued %v purged %v", d.issued, d.purged)
	}
	if left := first.expires.Sub(clock); left < 10*time.Minute {
		t.Fatalf("the replaced key must outlive a provider key link, has %s", left)
	}
	// Past the first key's expiry the second key reaches its own renewal
	// point too: the first is deleted, the second stays valid and retired.
	clock = first.expires.Add(time.Second)
	d.mu.Lock()
	d.now = clock
	d.mu.Unlock()
	if third := use(); third == second || len(d.issued) != 3 || len(d.purged) != 1 || d.purged[0] != "key-1" {
		t.Fatalf("an expired idle key must be deleted and only it: issued %v purged %v", d.issued, d.purged)
	}
	bridge.shutdown()
	if len(d.live) != 0 {
		t.Fatalf("keys left after exit: %v", d.live)
	}
}
