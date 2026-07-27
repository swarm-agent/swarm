package run

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCompileSessionMediaContractPilotIntersectionAndDenials(t *testing.T) {
	boolFalse := false
	baseCatalog := func(provider, model, surface, credential, semantics string) *pebblestore.ModelCatalogRecord {
		return &pebblestore.ModelCatalogRecord{
			Provider: provider, Model: model, Source: "live", SourceSnapshotID: "snap-1", SourceSnapshotVersion: "v1",
			Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: surface, CredentialSurface: credential,
				Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: semantics, MIMETypes: []string{"image/png", "image/jpeg"}}}},
		}
	}
	baseMeta := &pebblestore.ModelCatalogMeta{SnapshotID: "snap-1", SnapshotVersion: "v1", Source: "live"}
	openAIAdapter := provideriface.MediaAdapterDeclaration{AdapterID: "openai-responses-v1", ProviderID: "openai", ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", CredentialFingerprint: "credential-a", Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/jpeg", "image/png", "image/webp"}, ContentTypes: []string{"input_image"}, MaxBytes: 10, MaxCount: 2}}}
	codexAdapter := provideriface.MediaAdapterDeclaration{AdapterID: "codex-chatgpt-v1", ProviderID: "codex", ProviderSurface: "chatgpt_codex", CredentialSurface: "codex_oauth", CredentialFingerprint: "credential-b", Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, ContentTypes: []string{"input_image"}, MaxBytes: 20, MaxCount: 3}}}
	unsupportedCatalog := baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative)
	unsupportedCatalog.Media.Inputs[0].State = pebblestore.ModelCatalogMediaStateUnsupported
	staleValidCatalog := baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative)
	staleValidCatalog.Source = "live"
	staleValidCatalog.ExpiresAt = 1

	tests := []struct {
		name    string
		input   SessionMediaContractInput
		allowed bool
	}{
		{"supported openai", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, Adapter: openAIAdapter, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, true},
		{"supported codex", SessionMediaContractInput{ProviderID: "codex", Model: "gpt", Catalog: baseCatalog("codex", "gpt", "chatgpt_codex", "codex_oauth", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, Adapter: codexAdapter, AgentAuthorized: true, ExecutionMode: "plan", WorkspaceScope: "/workspace", SessionScope: "session"}, true},
		{"unsupported modality", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: unsupportedCatalog, CatalogMeta: baseMeta, Adapter: openAIAdapter, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"unknown catalog", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Adapter: openAIAdapter, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"stale valid cache", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: staleValidCatalog, CatalogMeta: baseMeta, Adapter: openAIAdapter, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, true},
		{"credential surface mismatch", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, Adapter: codexAdapter, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"agent denial", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, Adapter: openAIAdapter, AgentAuthorized: boolFalse, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"execution mode denial", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, Adapter: openAIAdapter, AgentAuthorized: true, ExecutionMode: "read", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"adapter missing", SessionMediaContractInput{ProviderID: "openai", Model: "gpt", Catalog: baseCatalog("openai", "gpt", "responses_api", "openai_api_key", pebblestore.ModelCatalogMediaSemanticsNative), CatalogMeta: baseMeta, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
		{"non pilot", SessionMediaContractInput{ProviderID: "anthropic", Model: "claude", AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := CompileSessionMediaContract(test.input)
			if got := SessionMediaContractAllows(contract, "image", "image/png", ""); got != test.allowed {
				t.Fatalf("allowed=%v contract=%+v", got, contract)
			}
			if contract.Hash == "" {
				t.Fatalf("contract hash is empty")
			}
		})
	}
}

func TestSessionMediaContractAllowsImageExtensionWhenMIMEIsAuthoritative(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		Capabilities: []provideriface.MediaContractCapability{{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			MIMETypes: []string{"image/png"}, MaxBytes: 1024, MaxCount: 1,
		}},
	}
	if !SessionMediaContractAllows(contract, "image", "image/png", "png") {
		t.Fatal("MIME-authoritative image contract rejected a harmless filename extension")
	}
	if SessionMediaContractAllows(contract, "image", "image/jpeg", "png") {
		t.Fatal("MIME-authoritative image contract accepted an unsupported detected MIME type")
	}
}

func TestCompileSessionMediaContractEveryNonPilotProviderIsTextOnly(t *testing.T) {
	for _, providerID := range []string{"anthropic", "gemini", "openrouter", "ollama", "bedrock"} {
		contract := CompileSessionMediaContract(SessionMediaContractInput{
			ProviderID: providerID, Model: "multimodal", AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session",
			Catalog:     &pebblestore.ModelCatalogRecord{Provider: providerID, Model: "multimodal", Source: "live", SourceSnapshotID: "snapshot", SourceSnapshotVersion: "v1", Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: "forged", CredentialSurface: "forged", Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}}}},
			CatalogMeta: &pebblestore.ModelCatalogMeta{SnapshotID: "snapshot", SnapshotVersion: "v1"},
			Adapter:     provideriface.MediaAdapterDeclaration{AdapterID: "forged", ProviderID: providerID, ProviderSurface: "forged", CredentialSurface: "forged", CredentialFingerprint: "forged", Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}}},
		})
		if SessionMediaContractAllows(contract, "image", "image/png", "") || len(allowedSessionMediaCapabilities(contract)) != 0 {
			t.Fatalf("non-pilot provider %q admitted media: %+v", providerID, contract)
		}
	}
}

func TestCompileSessionMediaContractStableHashAndLineageInputs(t *testing.T) {
	catalog := &pebblestore.ModelCatalogRecord{Provider: "openai", Model: "gpt-a", Source: "live", SourceSnapshotID: "snap-a", SourceSnapshotVersion: "v1", Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png", "image/jpeg"}}}}}
	input := SessionMediaContractInput{ProviderID: "openai", Model: "gpt-a", Catalog: catalog, CatalogMeta: &pebblestore.ModelCatalogMeta{SnapshotID: "snap-a", SnapshotVersion: "v1"}, Adapter: provideriface.MediaAdapterDeclaration{AdapterID: "adapter-a", ProviderID: "openai", ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", CredentialFingerprint: "cred", Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/jpeg", "image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1, MaxCount: 1}}}, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session"}
	first := CompileSessionMediaContract(input)
	input.Adapter.Inputs[0].MIMETypes = []string{"image/png", "image/jpeg"}
	second := CompileSessionMediaContract(input)
	if first.Hash != second.Hash {
		t.Fatalf("equivalent ordering changed hash: %s != %s", first.Hash, second.Hash)
	}
	input.Model = "gpt-b"
	if switched := CompileSessionMediaContract(input); switched.Hash == first.Hash {
		t.Fatalf("model switch did not change contract hash")
	}
	input.Model = "gpt-a"
	input.Catalog.SourceSnapshotID = "snap-b"
	input.CatalogMeta.SnapshotID = "snap-b"
	if refreshed := CompileSessionMediaContract(input); refreshed.Hash == first.Hash {
		t.Fatalf("snapshot switch did not change contract hash")
	}

	input.Catalog.SourceSnapshotID = "snap-a"
	input.CatalogMeta.SnapshotID = "snap-a"
	input.Adapter.CredentialFingerprint = "cred-rotated"
	if rotated := CompileSessionMediaContract(input); rotated.Hash == first.Hash {
		t.Fatalf("credential change did not change contract hash")
	}
	input.Adapter.CredentialFingerprint = "cred"
	input.Adapter.Inputs[0].MIMETypes = []string{"image/png"}
	if narrowed := CompileSessionMediaContract(input); narrowed.Hash == first.Hash {
		t.Fatalf("effective admission change did not change contract hash")
	}
	input.Adapter.Inputs[0].MIMETypes = []string{"image/png", "image/jpeg"}
	input.AgentAuthorized = false
	if denied := CompileSessionMediaContract(input); denied.Hash == first.Hash || SessionMediaContractAllows(denied, "image", "image/png", "") {
		t.Fatalf("agent authorization change did not close admission and change hash: %+v", denied)
	}
}
