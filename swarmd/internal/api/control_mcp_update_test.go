package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
)

// Requirement: swarm_status advertises the installed updater only when configured.
// Threat: an AI cannot discover updates, or receives a guessed command on an
// unmanaged machine. Owners: controlMCPStatus and handleBoxSummary. The real
// internal route on a temporary store proves the serialized MCP status contract
// without a daemon, network, sudo or an actual update.
func TestControlMCPStatusUpdateCommand(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	scopes, _ := aiKeyScopes("full")
	_, token, err := s.security.CreateScopedTokenUnder(nil, "update-test", scopes, actor.AccountScopeID, actor.UserID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "update.json")
	if err := os.WriteFile(state, []byte(`{"state":"idle","running":"installed","channel":"stable","auto":"off"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_UPDATE_STATE", state)
	for _, installer := range []string{"", "/var/lib/swarm-headless/install.sh"} {
		t.Run(installer, func(t *testing.T) {
			t.Setenv("SWARM_INSTALLER", installer)
			req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), actor.UserID, actor.AccountScopeID), &token)
			c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
			result, rpcErr := c.callTool(json.RawMessage(`{"name":"swarm_status","arguments":{}}`))
			if rpcErr != nil || result["isError"] == true {
				t.Fatalf("status: %v %v", result, rpcErr)
			}
			content := result["content"].([]map[string]any)
			var out map[string]any
			if err := json.Unmarshal([]byte(content[0]["text"].(string)), &out); err != nil {
				t.Fatal(err)
			}
			update := out["machine"].(map[string]any)["update"].(map[string]any)
			if update["state"] != "idle" || update["running"] != "installed" || update["channel"] != "stable" || update["auto"] != "off" {
				t.Fatalf("lost update state: %v", update)
			}
			if installer == "" {
				if _, ok := update["command"]; ok {
					t.Fatal("command present without installer")
				}
				if _, ok := update["how"]; ok {
					t.Fatal("how present without installer")
				}
			} else {
				if update["command"] != "sudo bash "+installer+" update" {
					t.Fatalf("machine.update.command = %v; want %q", update["command"], "sudo bash "+installer+" update")
				}
				how, _ := update["how"].(string)
				for _, word := range []string{"person", "sudo", "machine", "no job", "restarts"} {
					if !strings.Contains(how, word) {
						t.Fatalf("how lacks %q: %s", word, how)
					}
				}
			}
		})
	}
}

// Requirement: initialize directs an AI to the update command. Threat: status
// gains an undiscoverable field. Owners: controlMCPCall.instructions and
// controlMCPInitializeResult; their composed result is the narrow pure boundary.
func TestControlMCPInitializeUpdateInstructions(t *testing.T) {
	c := controlMCPProviderTestCall(t, nil, "sessions:read", "swarm:read", "signals:read")
	result := controlMCPInitializeResult(nil, c.instructions())
	text, _ := result["instructions"].(string)
	if !strings.Contains(text, "machine.update.command") || !strings.Contains(text, "swarm_status") {
		t.Fatalf("missing update discovery: %s", text)
	}
}

// Requirement: a client reads a machine's drain from its summary with times
// in Unix ms, like the summary's other times, and no run identifiers.
// Owner: boxDrainSummary.
func TestBoxDrainSummaryTimes(t *testing.T) {
	idle := boxDrainSummary(executioncapacity.DrainStatus{})
	if idle["draining"] != false || idle["since_ms"] != int64(0) || idle["deadline_ms"] != int64(0) {
		t.Fatalf("idle drain: %v", idle)
	}
	since := time.UnixMilli(1_793_000_000_000)
	d := boxDrainSummary(executioncapacity.DrainStatus{Draining: true, Since: since, Deadline: since.Add(time.Hour), Blockers: make([]executioncapacity.DrainBlocker, 2)})
	if d["draining"] != true || d["since_ms"] != since.UnixMilli() || d["deadline_ms"] != since.Add(time.Hour).UnixMilli() || d["blocking_runs"] != 2 {
		t.Fatalf("draining: %v", d)
	}
}
