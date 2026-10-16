package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a sealed agent's model run carries only its own definition, even
// when its session was created in a workspace that has an AGENTS.md (a gateway
// may pass workspace_path on create); an ordinary agent's run in the same kind
// of workspace still carries the file.
// Threat: a gateway token pointing a public chat at a workspace whose rule
// files then reach the bot's prompt, where visitors can extract them.
// Boundary: sessionV3Executor.resolveSessionV3Runtime (sealed branch) and
// run.Service.composeInstructionsForScopeWithDiscoveryRoots (ordinary branch).
// Narrowest layer: the real V3 executor with real discovery and a recording
// provider, since the prompt is assembled across the executor and run service.
func TestSealedAgentRunCarriesNoWorkspaceRules(t *testing.T) {
	const marker = "WORKSPACE-RULE-MARKER-c41d"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	t.Setenv("SWARM_CONFIG", "")
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "sealed-prompt"), nil)
	defer f.close()
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "hello", StopReason: "stop"}, nil
	}
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, tool.NewRuntime(1), f.permissions, f.server.agents, discovery.NewServiceWithUserHome(home), nil)
	if _, _, _, err := f.server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name: "helper", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Enabled: pebblestore.BoolPtr(true), Prompt: "Help out.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{}},
	}); err != nil {
		t.Fatal(err)
	}

	instructionsFor := func(agent string) string {
		t.Helper()
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Rules\n\n"+marker+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bindingID := seedSessionsV3PrimaryAuthority(t, f.server, workspace)
		raw, _ := json.Marshal(map[string]any{
			"client_request_id": "create-" + agent, "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
			"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
			"title": agent, "mode": sessionruntime.ModeAuto, "agent_name": agent,
			"preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"},
		})
		code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
		if code != http.StatusOK {
			t.Fatalf("create %s session: %d %s", agent, code, body)
		}
		var created struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		}
		if err := json.Unmarshal([]byte(body), &created); err != nil {
			t.Fatal(err)
		}
		f.say(created.Session.ID, "hello-"+agent, "hello")
		f.runner.mu.Lock()
		defer f.runner.mu.Unlock()
		return f.runner.lastRequest.Instructions
	}

	if got := instructionsFor("frontdesk"); strings.Contains(got, marker) || !strings.Contains(got, "Take orders.") {
		t.Fatalf("sealed agent run: has marker=%v, has own prompt=%v", strings.Contains(got, marker), strings.Contains(got, "Take orders."))
	}
	if got := instructionsFor("helper"); !strings.Contains(got, marker) {
		t.Fatal("an ordinary agent's run no longer carries the workspace AGENTS.md")
	}
}
