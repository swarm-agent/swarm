package api

import (
	"path/filepath"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the V3 executor must carry its final resolved tool list into media
// instructions, never advertising manage_media after it is filtered out.
// composeSessionV3ResolvedInstructions owns this boundary; a stored session and
// real tool resolver exercise request composition without a provider/network call.
// Media admission is supplied as an already-resolved fact, not tested here.
func TestSessionV3ResolvedMediaInstructions(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "media-instructions"), nil)
	defer f.close()
	id := f.createSession("Media instructions")
	session, found, err := f.sessions.GetSession(id)
	if err != nil || !found {
		t.Fatalf("session: found=%v error=%v", found, err)
	}
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, tool.NewRuntime(1), f.permissions, f.server.agents, nil, nil)
	executor := f.server.v3SessionExecutor
	for _, enabled := range []bool{true, false} {
		name := "inspection-only"
		if enabled {
			name = "manage-media"
		}
		t.Run(name, func(t *testing.T) {
			profile := pebblestore.AgentProfile{Name: "media-test", Prompt: "Inspect requested media.", ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
				"media_inspect": {Enabled: pebblestore.BoolPtr(true)},
				"manage_media":  {Enabled: pebblestore.BoolPtr(enabled)},
			}}}
			tools, err := executor.resolveSessionV3ProviderTools(session.AccountScopeID, profile)
			if err != nil {
				t.Fatal(err)
			}
			contract := provideriface.SessionMediaContract{Hash: "media-test-contract", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: "native", MIMETypes: []string{"image/png"}, MaxBytes: 1024, MaxCount: 1}}}
			tools = runruntime.MaterializeSessionMediaTool(tools, contract)
			hasManage := false
			for _, definition := range tools {
				if definition.Name == "manage_media" {
					hasManage = true
				}
			}
			if hasManage != enabled {
				t.Fatalf("resolved manage_media = %v, want %v", hasManage, enabled)
			}
			resolved := sessionV3ResolvedRuntime{Session: session, AgentProfile: profile, Tools: tools, MediaContract: contract}
			instructions, err := executor.composeSessionV3ResolvedInstructions(sessionV3ExecutorJob{SessionID: id, RunID: "media-instructions-test"}, resolved, tools)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(instructions, "Current run media_inspect input contract") {
				t.Fatal("inspection contract missing")
			}
			const guidance = "- Generating, editing, saving and transcribing media are done with manage_media using its own models and options, not with media_inspect."
			want := 0
			if enabled {
				want = 1
			}
			if got := strings.Count(instructions, guidance); got != want {
				t.Fatalf("guidance count = %d, want %d", got, want)
			}
		})
	}
}
