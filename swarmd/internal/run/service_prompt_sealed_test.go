package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a sealed agent (custom preset, only client tools) runs on its own
// definition only. Whatever workspace its session names — a gateway may pass
// workspace_path on create — that workspace's rule files (AGENTS.md) never
// reach its prompt, while an ordinary agent in the same workspace still gets
// them.
// Threat: a gateway token pointing a public chat at a workspace whose
// AGENTS.md then becomes part of the bot's prompt (and extractable by visitors).
// Boundary: Service.composeInstructionsForScopeWithDiscoveryRoots,
// agent.Service.SealedAgentError.
// Narrowest layer: the run service's prompt composition with a real agent store
// and real discovery over a temporary workspace; no provider runs.
func TestSealedAgentPromptLoadsNoWorkspaceRules(t *testing.T) {
	const marker = "WORKSPACE-RULE-MARKER-7f3a"
	// Discovery also reads the daemon's managed config dir: keep it temporary.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	t.Setenv("SWARM_CONFIG", "")
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "sealed-prompt.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	const account = "account-sealed-prompt"
	if _, err := agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Look up an order.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"order_id": map[string]any{"type": "string"}}}}); err != nil {
		t.Fatal(err)
	}
	upsert := func(name string, tools map[string]pebblestore.AgentToolConfig) pebblestore.AgentProfile {
		t.Helper()
		profile, _, _, err := agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: name, Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "Help visitors.",
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: tools}})
		if err != nil {
			t.Fatal(err)
		}
		return profile
	}
	sealed := upsert("frontdesk", map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}})
	plain := upsert("helper", map[string]pebblestore.AgentToolConfig{})
	if err := agents.SealedAgentError(account, sealed.Name); err != nil {
		t.Fatalf("frontdesk is not sealed: %v", err)
	}
	if err := agents.SealedAgentError(account, plain.Name); err == nil {
		t.Fatal("helper (no tools) counted as sealed")
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Rules\n\n"+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, agents, discovery.NewServiceWithUserHome(home), nil)
	scope := tool.WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace}, Principal: identity.Principal{AccountScopeID: account, UserID: "user-1"}, SessionID: "session-1"}

	if prompt := svc.composeInstructionsForScope(scope, sealed, ""); strings.Contains(prompt, marker) || !strings.Contains(prompt, "Help visitors.") {
		t.Fatalf("sealed agent prompt: has marker=%v, has own prompt=%v", strings.Contains(prompt, marker), strings.Contains(prompt, "Help visitors."))
	}
	if prompt := svc.composeInstructionsForScope(scope, plain, ""); !strings.Contains(prompt, marker) {
		t.Fatal("an ordinary agent no longer gets the workspace AGENTS.md")
	}
}
