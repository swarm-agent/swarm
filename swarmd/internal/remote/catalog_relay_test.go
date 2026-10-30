package remote

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Purpose: the relay holds a client to its own levels on catalog tools. A
// catalog tool's actions have different levels, so the relay cannot gate it
// by name (manage_projects would default to write): it forwards the call
// with the client's effective levels (ClientLevelsHeader, its scopes within
// the device ceiling) for Swarm Control to check per action, and lists the
// catalog tool as Swarm Control filtered it. swarm_* tools keep the name
// gate. Threat: a write client reaching approve or manage actions, or a
// read client shut out of read actions, through the relay. Boundary:
// Service.handle and filterTools with a recording Swarm Control handler.
func TestRelayForwardsClientLevelsForCatalogTools(t *testing.T) {
	var got []string
	s := &Service{control: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(ClientLevelsHeader))
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	})}
	cfg := Config{AllowWrite: true, Token: "device"}
	call := func(name string, scopes ...string) string {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"action": "list"}}})
		return string(s.handle(cfg, frame{Client: &frameClient{Scopes: scopes}, Body: body}))
	}
	// A read-only client reaches manage_projects (its read actions are
	// checked there) with its levels.
	if out := call("manage_projects", ScopeRead); strings.Contains(out, "does not allow") || len(got) != 1 || got[0] != ScopeRead {
		t.Fatalf("read client: %s %v", out, got)
	}
	// A client asking for approve and manage gets only what the ceiling
	// allows forwarded.
	if out := call("manage_projects", ScopeRead, ScopeWrite, ScopeApprove, ScopeManage); strings.Contains(out, "does not allow") || got[1] != ScopeRead+","+ScopeWrite {
		t.Fatalf("ceiling not applied: %s %v", out, got)
	}
	// swarm_* tools keep the name gate.
	if out := call("swarm_stop_run", ScopeRead); !strings.Contains(out, "does not allow") || len(got) != 2 {
		t.Fatalf("name gate: %s %v", out, got)
	}
	listed := string(filterTools([]byte(`{"result":{"tools":[{"name":"manage_projects"},{"name":"swarm_stop_run"}]}}`), []string{ScopeRead}))
	if !strings.Contains(listed, "manage_projects") || strings.Contains(listed, "swarm_stop_run") {
		t.Fatalf("relay listing: %s", listed)
	}
}
