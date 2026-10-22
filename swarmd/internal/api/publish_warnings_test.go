package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: ignored publish policy must reach AI status, the matching workspace,
// and monitoring once at startup, without enabling delivery or leaking commands.
// Owners: LoadWithWarnings, handleBoxSummary, controlMCPListWorkspaces and
// EmitPublishConfigWarnings. Real MCP routes and a temporary signal store are
// the narrowest boundary proving visibility, fresh removal, and no read events.
// Cross-account reads must not disclose folders, and invalid publish policy must
// not disable the independently authorized workspace-list operation.
func TestPublishConfigWarningsStatusAndSignal(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	t.Cleanup(publish.AllowOwnerForTests(os.Getuid()))
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "feed"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	feed := pebblestore.NewSignalStore(db)
	s.SetSignalStore(feed)
	s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(db))
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	scopes, _ := aiKeyScopes("full")
	_, token, err := s.security.CreateScopedTokenUnder(nil, "publish-warning", scopes, actor.AccountScopeID, actor.UserID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), actor.UserID, actor.AccountScopeID), &token)
	principal, _ := PrincipalFromRequest(req)
	folder, other := t.TempDir(), t.TempDir()
	for _, path := range []string{folder, other} {
		outboxPinGit(t, path, "init", "-q")
		outboxPinGit(t, path, "commit", "-q", "--allow-empty", "-m", "base")
		if _, err := s.workspace.AddForPrincipal(principal, path, "folder", "", false); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(t.TempDir(), "publish")
	s.SetPublishConfig(config)
	write := func(options string) {
		t.Helper()
		if err := os.WriteFile(config, []byte(folder+"\t/owner/private-command\t"+options+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("auto,land,require-review,require-proof,pipeline,sync")
	s.EmitPublishConfigWarnings(signals.NewEmitter(feed).Emit)
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
	call := func(name string) map[string]any {
		t.Helper()
		result, rpcErr := c.callTool(json.RawMessage(`{"name":"` + name + `","arguments":{}}`))
		if rpcErr != nil || result["isError"] == true {
			t.Fatalf("%s: %v %v", name, result, rpcErr)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(result["content"].([]map[string]any)[0]["text"].(string)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for i := 0; i < 2; i++ {
		status := call("swarm_status")
		encoded, _ := json.Marshal(status["reasons"])
		for _, want := range []string{folder, "auto", "land", "require-review", "require-proof", "pipeline", "deliver to: publish", "swarm_set_workflow"} {
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("status missing %q: %s", want, encoded)
			}
		}
		if status["status"] == "ok" {
			t.Fatal("removed policy reported healthy")
		}
		entries := call("swarm_list_workspaces")["workspaces"].([]any)
		seen := 0
		for _, raw := range entries {
			entry := raw.(map[string]any)
			if entry["path"] == folder {
				seen++
				if len(entry["warnings"].([]any)) != 1 {
					t.Fatalf("warnings: %v", entry)
				}
			} else if entry["warnings"] != nil {
				t.Fatalf("unrelated warning: %v", entry)
			}
		}
		if seen != 1 {
			t.Fatalf("missing workspace: %v", entries)
		}
	}
	page, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: actor.AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Signals) != 1 {
		t.Fatalf("startup/read events: %+v", page.Signals)
	}
	sig := page.Signals[0]
	if sig.Kind != "publish.config.warning" || sig.Severity != "warning" || sig.Refs["workspace_path"] != "" || !strings.Contains(sig.Summary, "deliver to: publish") || strings.Contains(sig.Summary, "private-command") {
		t.Fatalf("signal: %+v", sig)
	}
	readScopes, _ := aiKeyScopes("read")
	_, otherToken, err := s.security.CreateScopedTokenUnder(nil, "other-account", readScopes, "other-account", "other-user", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c.request = requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), "other-user", "other-account"), &otherToken)
	for _, name := range []string{"swarm_status", "swarm_list_workspaces"} {
		encoded, _ := json.Marshal(call(name))
		if strings.Contains(string(encoded), folder) || strings.Contains(string(encoded), "removed publish options") {
			t.Fatalf("cross-account %s disclosure: %s", name, encoded)
		}
	}
	otherPage, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: "other-account"})
	if err != nil || len(otherPage.Signals) != 1 {
		t.Fatalf("machine warning missing: %+v %v", otherPage, err)
	}
	encoded, _ := json.Marshal(otherPage.Signals)
	if strings.Contains(string(encoded), folder) || strings.Contains(string(encoded), "private-command") {
		t.Fatalf("cross-account signal disclosure: %s", encoded)
	}
	c.request = req
	write("sync")
	status, _ := json.Marshal(call("swarm_status"))
	if strings.Contains(string(status), "removed publish options") {
		t.Fatalf("stale warning: %s", status)
	}
	entries, _ := json.Marshal(call("swarm_list_workspaces"))
	if strings.Contains(string(entries), "warnings") {
		t.Fatalf("stale workspace warning: %s", entries)
	}
	s.EmitPublishConfigWarnings(signals.NewEmitter(feed).Emit)
	write("auto,typo")
	invalidEntries := call("swarm_list_workspaces")["workspaces"].([]any)
	if len(invalidEntries) != 2 {
		t.Fatalf("invalid publish config hid workspaces: %v", invalidEntries)
	}
	for _, raw := range invalidEntries {
		entry := raw.(map[string]any)
		if entry["path"] != folder && entry["path"] != other {
			t.Fatalf("unexpected workspace: %v", entry)
		}
	}
	if warnings, err := s.publishWarnings(); err == nil || len(warnings) != 0 {
		t.Fatalf("invalid policy: %v %v", warnings, err)
	}
	s.EmitPublishConfigWarnings(signals.NewEmitter(feed).Emit)
	page, err = feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: actor.AccountScopeID})
	if err != nil || len(page.Signals) != 1 {
		t.Fatalf("clean/invalid config emitted: %+v %v", page, err)
	}
}
