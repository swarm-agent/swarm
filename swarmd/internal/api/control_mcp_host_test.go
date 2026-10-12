package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: when headless Swarm cannot see a folder the person named, the
// AI learns the host change that fixes it from add_workspace (a non-error
// result naming request_host_change), may record that request only with a
// key holding host:request, can follow it with list_host_changes, and can
// never approve it: there is no approve or deny tool and the decision routes
// are outside the Swarm Control allowlist. Threats: a plain write key (or a
// relay client) requesting changes to the machine, a request duplicated on
// retry, an AI told to retry a folder the machine can never show it, or any
// tool path reaching the owner-only decision. Owners: controlMCPAddWorkspace
// with controlMCPWorkspaceNotVisible, controlMCPRequestHostChange,
// controlMCPListHostChanges, levelAllows, controlMCPRoutes and
// handleHostChanges over HostChangeStore. The API mux with real temporary
// stores and the fake helper is the narrowest layer that runs the tools, the
// route allowlist, the scope checks and the stored record together; the
// helper is never run here (approval is owner-only), which
// host_changes_test.go covers.
func TestControlMCPHostChangeRequestAndListing(t *testing.T) {
	server, _, db := newControlMCPProjectFixture(t)
	store := pebblestore.NewHostChangeStore(db)
	helper := newFakeHostHelper(nil)
	server.SetHostChangeServices(store, helper)
	p := testPrincipal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read", "sessions:write")
	requester := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read", "sessions:write", ScopeHostRequest)
	reader := controlMCPDirectCall(ctx, p, server.apiMux(), server, "swarm:read", "sessions:read")
	decode := func(text string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("result is not JSON: %s", text)
		}
		return out
	}
	stored := func(status string) []pebblestore.HostChange {
		t.Helper()
		changes, err := store.List(p.AccountScopeID, status)
		if err != nil {
			t.Fatal(err)
		}
		return changes
	}
	registered := func() int {
		t.Helper()
		entries, err := server.workspace.ListKnownForPrincipal(p, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	hidden := filepath.Join(t.TempDir(), "home", "person", "app")
	before := registered()

	// add_workspace: the folder exists for the person but not for Swarm.
	text, isError := controlMCPToolText(t, write, "swarm_add_workspace", map[string]any{"path": hidden})
	if isError {
		t.Fatalf("not_visible reported as an error: %s", text)
	}
	blocked := decode(text)
	fix, _ := blocked["host_change"].(map[string]any)
	next, _ := blocked["next"].(string)
	if blocked["added"] != false || blocked["blocked"] != "not_visible" || blocked["path"] != hidden || blocked["supported"] != true ||
		fix["op"] != pebblestore.HostChangeOpMountWorkspace || fix["path"] != hidden {
		t.Fatalf("not_visible result: %s", text)
	}
	for _, want := range []string{"request_host_change", "mount-workspace", hidden, "approval", "list_host_changes", "add_workspace again"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next lacks %q: %s", want, next)
		}
	}
	if registered() != before {
		t.Fatal("a folder Swarm cannot see was registered")
	}

	// A plain write key cannot request: the tool is refused before any route.
	if text, isError := controlMCPToolText(t, write, "swarm_request_host_change", map[string]any{"op": "mount-workspace", "path": hidden, "reason": "the person's app"}); !isError || !strings.Contains(text, ScopeHostRequest) {
		t.Fatalf("write key requested a host change: %s", text)
	}
	if len(stored("")) != 0 {
		t.Fatal("a refused request was recorded")
	}

	// A key with host:request records exactly one pending request.
	text, isError = controlMCPToolText(t, requester, "swarm_request_host_change", map[string]any{"op": "mount-workspace", "path": hidden, "reason": "the person's app"})
	if isError {
		t.Fatalf("request: %s", text)
	}
	requested := decode(text)
	change, _ := requested["change"].(map[string]any)
	id, _ := change["id"].(string)
	requestNext, _ := requested["next"].(string)
	if id == "" || change["status"] != pebblestore.HostChangeStatusPending || change["op"] != "mount-workspace" || change["path"] != hidden || change["reason"] != "the person's app" {
		t.Fatalf("request result: %s", text)
	}
	if !strings.Contains(requestNext, "approval") || !strings.Contains(requestNext, "cannot approve") || !strings.Contains(requestNext, "list_host_changes") {
		t.Fatalf("request next: %s", requestNext)
	}
	if pending := stored(pebblestore.HostChangeStatusPending); len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("stored: %+v", pending)
	}
	// Retrying returns the same pending request instead of a second one.
	if text, isError := controlMCPToolText(t, requester, "swarm_request_host_change", map[string]any{"op": "mount-workspace", "path": hidden, "reason": "again"}); isError || decode(text)["change"].(map[string]any)["id"] != id {
		t.Fatalf("repeat request: %s", text)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown op", map[string]any{"op": "reboot", "path": hidden, "reason": "r"}, "must be one of"},
		{"relative path", map[string]any{"op": "mount-workspace", "path": "home/person/app", "reason": "r"}, "absolute"},
		{"empty reason", map[string]any{"op": "mount-workspace", "path": hidden, "reason": " "}, "must not be empty"},
		{"undeclared argument", map[string]any{"op": "mount-workspace", "path": hidden, "reason": "r", "approve": true}, "unknown argument"},
	} {
		if text, isError := controlMCPToolText(t, requester, "swarm_request_host_change", tc.args); !isError || !strings.Contains(text, tc.want) {
			t.Fatalf("%s: want %q, got %s", tc.name, tc.want, text)
		}
	}
	if len(stored("")) != 1 {
		t.Fatalf("refused requests were recorded: %+v", stored(""))
	}

	// Listing: a read-level AI key and the requester see the request; a key
	// with neither host:request nor swarm:read does not.
	for _, caller := range []*controlMCPCall{reader, requester} {
		text, isError := controlMCPToolText(t, caller, "swarm_list_host_changes", map[string]any{})
		if isError {
			t.Fatalf("list: %s", text)
		}
		listed := decode(text)
		changes, _ := listed["changes"].([]any)
		if listed["supported"] != true || len(changes) != 1 || changes[0].(map[string]any)["id"] != id || changes[0].(map[string]any)["status"] != "pending" {
			t.Fatalf("list result: %s", text)
		}
	}
	if text, isError := controlMCPToolText(t, reader, "swarm_list_host_changes", map[string]any{"status": "applied"}); isError || !strings.Contains(text, `"changes":[]`) {
		t.Fatalf("status filter: %s", text)
	}
	if text, isError := controlMCPToolText(t, reader, "swarm_list_host_changes", map[string]any{"status": "done"}); !isError || !strings.Contains(text, "must be one of") {
		t.Fatalf("unknown status: %s", text)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_list_host_changes", map[string]any{}); !isError || !strings.Contains(text, "lacks") {
		t.Fatalf("write key without a read level listed host changes: %s", text)
	}

	// No tool decides, and the decision routes are outside the allowlist, so
	// the helper never ran and the request is still pending.
	for _, tool := range controlMCPTools() {
		if strings.Contains(tool.Name, "host") && (strings.Contains(tool.Name, "approve") || strings.Contains(tool.Name, "deny")) {
			t.Fatalf("a host change decision tool exists: %s", tool.Name)
		}
	}
	for _, path := range []string{"/v1/host/changes/" + id + "/approve", "/v1/host/changes/" + id + "/deny"} {
		if controlMCPRouteAllowed("POST", path) {
			t.Fatalf("decision route %s is reachable from Swarm Control", path)
		}
		if _, err := requester.dispatch("POST", path, nil, map[string]any{}); err == nil || !strings.Contains(err.Error(), "route unavailable") {
			t.Fatalf("decision route %s dispatched: %v", path, err)
		}
	}
	if calls := helper.received(); len(calls) != 0 {
		t.Fatalf("helper ran without an owner decision: %v", calls)
	}
	if pending := stored(pebblestore.HostChangeStatusPending); len(pending) != 1 {
		t.Fatalf("request state changed: %+v", stored(""))
	}

	// Without the helper the machine cannot apply any change: add_workspace
	// says so instead of sending the AI to request, and a request is refused.
	server.SetHostChangeServices(store, nil)
	other := filepath.Join(t.TempDir(), "home", "person", "docs")
	text, isError = controlMCPToolText(t, write, "swarm_add_workspace", map[string]any{"path": other})
	if isError {
		t.Fatalf("unsupported not_visible reported as an error: %s", text)
	}
	unsupported := decode(text)
	unsupportedNext, _ := unsupported["next"].(string)
	if unsupported["blocked"] != "not_visible" || unsupported["supported"] != false || !strings.Contains(unsupportedNext, "host-helper") || strings.Contains(unsupportedNext, "request_host_change") {
		t.Fatalf("unsupported result: %s", text)
	}
	if text, isError := controlMCPToolText(t, requester, "swarm_request_host_change", map[string]any{"op": "mount-workspace", "path": other, "reason": "docs"}); !isError || !strings.Contains(text, "host-change helper") {
		t.Fatalf("unsupported request: %s", text)
	}
	if text, isError := controlMCPToolText(t, reader, "swarm_list_host_changes", map[string]any{}); isError || !strings.Contains(text, `"supported":false`) {
		t.Fatalf("unsupported list: %s", text)
	}
	if len(stored("")) != 1 {
		t.Fatalf("an unsupported request was recorded: %+v", stored(""))
	}
}

// Requirement: swarm_request_host_change is offered, and the instruction
// that sends an AI to it, shown, only to keys that hold host:request (an SDK
// token minted with --host-request, or a full AI key whose admin scope covers
// it); write-level and read-level AI keys, and tailnet identities built from
// them, neither see nor may call it. Threat: an instruction naming a tool the
// key cannot call, or a level key requesting machine changes. Owners:
// levelAllows, levelRefusal, visibleTools and controlMCPInstructionLines.
// Pure in-process checks on the call object are the narrowest layer.
func TestControlMCPHostChangeToolVisibility(t *testing.T) {
	ctx := context.Background()
	p := testPrincipal()
	const line = "If add_workspace answers blocked not_visible, request the host change it names with request_host_change, tell the person it waits for their approval in Swarm, and add the folder again once list_host_changes shows it applied."
	visible := func(c *controlMCPCall, name string) bool {
		for _, tool := range c.visibleTools() {
			if tool.Name == name {
				return true
			}
		}
		return false
	}
	fullScopes, err := aiKeyScopes("full")
	if err != nil {
		t.Fatal(err)
	}
	writeScopes, err := aiKeyScopes("write")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		scopes  []string
		offered bool
	}{
		{"sdk token with --host-request", []string{"sessions:read", "sessions:write", ScopeHostRequest}, true},
		{"write AI key with host:request", append(append([]string{}, writeScopes...), ScopeHostRequest), true},
		{"full AI key", fullScopes, true},
		{"write AI key", writeScopes, false},
		{"plain sdk token", []string{"sessions:read", "sessions:write"}, false},
		{"read AI key", []string{"swarm:read", "sessions:read"}, false},
	} {
		c := controlMCPDirectCall(ctx, p, nil, nil, tc.scopes...)
		if visible(c, "swarm_request_host_change") != tc.offered {
			t.Fatalf("%s: request_host_change offered=%v, want %v", tc.name, !tc.offered, tc.offered)
		}
		// Listing needs swarm:read or host:request at the route; every key here
		// is offered the tool (no level, or a read level).
		if !visible(c, "swarm_list_host_changes") {
			t.Fatalf("%s: list_host_changes hidden", tc.name)
		}
		hasAddWorkspace := visible(c, "swarm_add_workspace")
		if got := strings.Contains(c.instructions(), line); got != (tc.offered && hasAddWorkspace) {
			t.Fatalf("%s: instruction shown=%v (offered=%v, add_workspace=%v)", tc.name, got, tc.offered, hasAddWorkspace)
		}
		if tc.offered {
			continue
		}
		params, _ := json.Marshal(map[string]any{"name": "swarm_request_host_change", "arguments": map[string]any{"op": "mount-workspace", "path": "/home/person/app", "reason": "r"}})
		result, rpcErr := c.callTool(params)
		if rpcErr != nil {
			t.Fatal(rpcErr.Message)
		}
		content, _ := result["content"].([]map[string]any)
		text, _ := content[0]["text"].(string)
		if result["isError"] != true || !strings.Contains(text, ScopeHostRequest) || !strings.Contains(text, "--host-request") {
			t.Fatalf("%s: refusal %v %s", tc.name, result["isError"], text)
		}
	}
}
