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
	for _, expected := range []string{`"asset_id"`, `"path"`, `"artifact_reference"`, `"session_id"`, `"collection_id"`, `"variant_id"`, `"event_seq"`, `"oneOf"`} {
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
	for _, expected := range []string{"media_inspect", "complete exact ready managed artifact reference", "session_id", "collection_id", "variant_id", "event_seq", "image/png", "semantics=native", "max_bytes=1024", "All unlisted media kinds"} {
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

func TestProviderToolMediaInputItemsCarriesOnlyAuthorizedPayload(t *testing.T) {
	payload := tool.MediaPayload{AssetID: "asset", Modality: "image", MIMEType: "image/png", Size: 3, Bytes: []byte("png")}
	items := providerToolMediaInputItems([]tool.Result{{Name: mediaInspectToolName, Media: &payload}})
	if len(items) != 1 {
		t.Fatalf("media input items = %#v", items)
	}
	content, ok := items[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["type"] != "session_media" {
		t.Fatalf("media input content = %#v", items[0]["content"])
	}
	got, ok := content[0]["media"].(provideriface.SessionMediaPayload)
	if !ok || got.AssetID != payload.AssetID || string(got.Bytes) != "png" {
		t.Fatalf("media input payload = %#v", content[0]["media"])
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
	if _, err := decodeMediaInspectArguments(`{"path":"secret","artifact_reference":{"session_id":"s","collection_id":"c","variant_id":"v","event_seq":1}}`); err == nil {
		t.Fatal("media invocation accepted both path and artifact_reference")
	}
	if _, err := decodeMediaInspectArguments(`{"artifact_reference":{"session_id":"s","collection_id":"c","variant_id":"v"}}`); err == nil || !strings.Contains(err.Error(), "requires session_id, collection_id, variant_id, and event_seq") {
		t.Fatalf("incomplete artifact reference error = %v", err)
	}
	if args, err := decodeMediaInspectArguments(`{"artifact_reference":{"session_id":" s ","collection_id":" c ","variant_id":" v ","event_seq":7}}`); err != nil || args.ArtifactReference == nil || args.ArtifactReference.SessionID != "s" || args.ArtifactReference.CollectionID != "c" || args.ArtifactReference.VariantID != "v" || args.ArtifactReference.EventSeq != 7 {
		t.Fatalf("exact artifact reference rejected or not normalized: args=%+v err=%v", args, err)
	}
	if args, err := decodeMediaInspectArguments(`{"path":"web/public/pwa-icon-512.png"}`); err != nil || args.Path == "" {
		t.Fatalf("workspace media path rejected: args=%+v err=%v", args, err)
	}
}

func TestDesignerMediaToolIsMaterializedOnlyForAllowedResolvedModelContract(t *testing.T) {
	profile := agentruntime.DesignerAgentProfileForParent(pebblestore.AgentProfile{})
	if !AgentProfileAuthorizesMedia(profile) {
		t.Fatal("compiled Designer profile did not explicitly authorize media")
	}
	base := []provideriface.ToolDefinition{{Type: "function", Name: "read"}, {Type: "function", Name: mediaInspectToolName}}
	catalog := &pebblestore.ModelCatalogRecord{
		Provider: "openai", Model: "designer-vision", Source: "live", SourceSnapshotID: "designer-snapshot", SourceSnapshotVersion: "v1",
		Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses, CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey, Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}}},
	}
	input := SessionMediaContractInput{
		ProviderID: "openai", Model: catalog.Model, Catalog: catalog, CatalogMeta: &pebblestore.ModelCatalogMeta{SnapshotID: catalog.SourceSnapshotID, SnapshotVersion: catalog.SourceSnapshotVersion},
		Adapter:         provideriface.MediaAdapterDeclaration{AdapterID: provideriface.MediaAdapterIDOpenAIResponsesV1, ProviderID: "openai", ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses, CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey, CredentialFingerprint: "designer-credential", Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}}},
		AgentAuthorized: AgentProfileAuthorizesMedia(profile), ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "designer-session",
	}
	allowed := CompileSessionMediaContract(input)
	if tools := MaterializeSessionMediaTool(base, allowed); len(tools) != 2 || tools[1].Name != mediaInspectToolName {
		t.Fatalf("allowed Designer model tools = %#v, want media_inspect", tools)
	}
	input.Model = "designer-text"
	input.Catalog = &pebblestore.ModelCatalogRecord{Provider: "openai", Model: input.Model, Source: "live", SourceSnapshotID: "designer-text-snapshot", SourceSnapshotVersion: "v1"}
	input.CatalogMeta = &pebblestore.ModelCatalogMeta{SnapshotID: input.Catalog.SourceSnapshotID, SnapshotVersion: input.Catalog.SourceSnapshotVersion}
	denied := CompileSessionMediaContract(input)
	if tools := MaterializeSessionMediaTool(base, denied); len(tools) != 1 || tools[0].Name != "read" {
		t.Fatalf("unsupported Designer model tools = %#v, want media_inspect omitted", tools)
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
