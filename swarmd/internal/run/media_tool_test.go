package run

import (
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestListAgentToolDefinitionsCarriesAuthorizationPlaceholder(t *testing.T) {
	svc := &Service{tools: tool.NewRuntime(1)}
	definitions := svc.ListAgentToolDefinitions()
	count := 0
	for _, definition := range definitions {
		if definition.Name == mediaInspectToolName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("media authorization placeholder count = %d, want 1", count)
	}
}

func TestCompileStoredV3SwarmToolContractAcceptsMediaRuntime(t *testing.T) {
	svc := &Service{tools: tool.NewRuntime(1)}
	profile := agentruntime.SwarmAgentProfileForContext(pebblestore.AgentProfile{})
	resolved, _, err := svc.CompileStoredV3AgentToolContract("", profile)
	if err != nil {
		t.Fatalf("compile stored V3 Swarm tool contract: %v", err)
	}
	media, ok := resolved.Tools[mediaInspectToolName]
	if !ok || !media.Enabled {
		t.Fatalf("resolved media tool = %+v, present = %t, want enabled", media, ok)
	}
}

func TestSessionMediaToolSchemaAndInstructionsShareContract(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		Hash: "contract-a",
		Capabilities: []provideriface.MediaContractCapability{
			{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png", "image/jpeg"}, FileTypes: []string{"png", "jpg"}, MaxBytes: 1024, MaxCount: 2},
			{Modality: "audio", State: provideriface.MediaCapabilityStateDenied, MIMETypes: []string{"audio/wav"}},
		},
	}
	base := []provideriface.ToolDefinition{{Type: "function", Name: "read"}, {Type: "function", Name: mediaInspectToolName}}
	tools := MaterializeSessionMediaTool(base, contract)
	if len(tools) != 2 || tools[0].Name != "read" || tools[1].Name != mediaInspectToolName {
		t.Fatalf("materialized tools = %#v", tools)
	}
	raw := mustProviderToolInvokerJSON(t, tools[1].Parameters)
	for _, expected := range []string{`"asset_id"`, `"path"`, `"anyOf"`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("media schema missing %q: %s", expected, raw)
		}
	}
	for _, denied := range []string{"contract-a", "audio/wav", `"audio"`, "digest_sha256", "contract_hash"} {
		if strings.Contains(raw, denied) {
			t.Fatalf("media schema leaked denied value %q: %s", denied, raw)
		}
	}
	instructions := AppendSessionMediaInstructions("base", contract)
	for _, expected := range []string{"media_inspect", "image/png", "semantics=native", "max_bytes=1024", "All unlisted media kinds"} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("media instructions missing %q: %s", expected, instructions)
		}
	}
	if strings.Contains(instructions, "audio/wav") {
		t.Fatalf("media instructions leaked denied type: %s", instructions)
	}
}

func TestSessionMediaToolOmittedForEmptyAndNonPilotContracts(t *testing.T) {
	base := []provideriface.ToolDefinition{{Type: "function", Name: "read"}, {Type: "function", Name: mediaInspectToolName}}
	for _, contract := range []provideriface.SessionMediaContract{
		{},
		{Hash: "denied", ProviderID: "anthropic", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateDenied, MIMETypes: []string{"image/png"}}}},
	} {
		tools := MaterializeSessionMediaTool(base, contract)
		if len(tools) != 1 || tools[0].Name != "read" {
			t.Fatalf("denied contract tools = %#v", tools)
		}
		if got := AppendSessionMediaInstructions("base", contract); got != "base" {
			t.Fatalf("denied contract instructions = %q", got)
		}
	}
}

func TestMediaInspectInvocationRejectsForgedStaleAndDeniedCalls(t *testing.T) {
	contract := provideriface.SessionMediaContract{Hash: "current", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, MIMETypes: []string{"image/png"}, FileTypes: []string{"png"}, MaxBytes: 1024, MaxCount: 1}}}
	if _, err := validateMediaInspectInvocation(contract, "image", "image/png", "png"); err != nil {
		t.Fatalf("valid media invocation rejected: %v", err)
	}
	if _, err := validateMediaInspectInvocation(provideriface.SessionMediaContract{}, "image", "image/png", "png"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("empty media contract error = %v", err)
	}
	if _, err := validateMediaInspectInvocation(contract, "image", "audio/wav", "wav"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("forged media type error = %v", err)
	}
	if _, err := decodeMediaInspectArguments(`{"asset_id":"a","path":"secret"}`); err == nil {
		t.Fatal("media invocation accepted both asset_id and path")
	}
	if args, err := decodeMediaInspectArguments(`{"path":"web/public/pwa-icon-512.png"}`); err != nil || args.Path == "" {
		t.Fatalf("workspace media path rejected: args=%+v err=%v", args, err)
	}
}

func TestMediaAuthorizationIsExplicitForConversationalAgents(t *testing.T) {
	for _, profile := range []pebblestore.AgentProfile{
		agentruntime.SwarmAgentProfileForContext(pebblestore.AgentProfile{}),
		agentruntime.FinderAgentProfileForParent(pebblestore.AgentProfile{}),
		agentruntime.CoderAgentProfileForParent(pebblestore.AgentProfile{}),
		agentruntime.DesignerAgentProfileForParent(pebblestore.AgentProfile{}),
	} {
		if !AgentProfileAuthorizesMedia(profile) {
			t.Fatalf("conversational profile %q did not explicitly authorize media", profile.Name)
		}
	}
	for _, profile := range []pebblestore.AgentProfile{
		agentruntime.CompactAgentProfileForParent(pebblestore.AgentProfile{}),
		agentruntime.AITaskPreparerAgentProfileForParent(pebblestore.AgentProfile{}),
		agentruntime.ReviewCommitAgentProfileForParent(pebblestore.AgentProfile{}),
	} {
		if AgentProfileAuthorizesMedia(profile) {
			t.Fatalf("utility profile %q unexpectedly authorized media", profile.Name)
		}
	}
	customDenied := pebblestore.AgentProfile{ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}}
	if AgentProfileAuthorizesMedia(customDenied) {
		t.Fatal("custom saved agent without media authorization was broadened")
	}
	customAllowed := customDenied
	customAllowed.ToolContract = pebblestore.CloneAgentToolContract(customDenied.ToolContract)
	customAllowed.ToolContract.Tools[mediaInspectToolName] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	if !AgentProfileAuthorizesMedia(customAllowed) {
		t.Fatal("custom saved agent explicit media authorization was ignored")
	}
}
