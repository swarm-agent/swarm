package run

import (
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPreparePreSessionMediaBindingsCanonicalPlan(t *testing.T) {
	contract := testPreSessionMediaContract()
	staged := testPreSessionStagedMedia("stage-1", strings.Repeat("a", 64), 512)

	plan, err := PreparePreSessionMediaBindings(PreSessionMediaBindingInput{
		AccountScopeID: "account", SessionID: "session", WorkspaceScope: "/workspace",
		Contract: contract, Staged: []PreSessionMediaStagedMetadata{staged},
	})
	if err != nil {
		t.Fatalf("prepare bindings: %v", err)
	}
	if plan.ProviderID != contract.ProviderID || plan.Model != contract.Model || plan.ContractHash != contract.Hash || plan.TotalBytes != staged.Size {
		t.Fatalf("binding plan lost resolved facts: %+v", plan)
	}
	if len(plan.Bindings) != 1 {
		t.Fatalf("bindings=%d want 1", len(plan.Bindings))
	}
	binding := plan.Bindings[0]
	wantAssetID := preSessionMediaAssetID(staged.DigestSHA256, contract.Hash)
	if binding.StagingID != staged.StagingID || binding.AssetID != wantAssetID || binding.Reference.AssetID != wantAssetID {
		t.Fatalf("binding identity is not canonical: %+v", binding)
	}
	if binding.Reference.ContractHash != contract.Hash || binding.Reference.MIMEType != "image/png" || binding.Reference.Size != staged.Size || binding.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || binding.Metadata.DetectedMIMEType != "image/png" {
		t.Fatalf("binding reference lost immutable facts: %+v", binding)
	}
	if plan.CredentialFingerprint != contract.CredentialFingerprint || plan.SnapshotSource != contract.SnapshotSource || plan.ExecutionMode != contract.ExecutionMode || plan.ContractVersion != contract.Version || len(binding.Provenance) != 1 {
		t.Fatalf("binding plan lost provider/model/contract lineage: %+v", plan)
	}
}

func TestPreparePreSessionMediaBindingsFailsClosedOnContractAuthority(t *testing.T) {
	baseStaged := []PreSessionMediaStagedMetadata{testPreSessionStagedMedia("stage-1", strings.Repeat("a", 64), 512)}
	tests := []struct {
		name   string
		mutate func(*provideriface.SessionMediaContract)
	}{
		{name: "unsupported version", mutate: func(c *provideriface.SessionMediaContract) { c.Version++ }},
		{name: "empty contract", mutate: func(c *provideriface.SessionMediaContract) { c.Hash = "" }},
		{name: "stale hash", mutate: func(c *provideriface.SessionMediaContract) { c.Model = "switched-without-refresh" }},
		{name: "unreviewed provider", mutate: func(c *provideriface.SessionMediaContract) { c.ProviderID = "unknown"; rehashPreSessionContract(c) }},
		{name: "forged surface", mutate: func(c *provideriface.SessionMediaContract) { c.AdapterID = "forged"; rehashPreSessionContract(c) }},
		{name: "missing model", mutate: func(c *provideriface.SessionMediaContract) { c.Model = ""; rehashPreSessionContract(c) }},
		{name: "missing snapshot", mutate: func(c *provideriface.SessionMediaContract) { c.SnapshotID = ""; rehashPreSessionContract(c) }},
		{name: "missing credential", mutate: func(c *provideriface.SessionMediaContract) { c.CredentialFingerprint = ""; rehashPreSessionContract(c) }},
		{name: "denied execution mode", mutate: func(c *provideriface.SessionMediaContract) { c.ExecutionMode = "read"; rehashPreSessionContract(c) }},
		{name: "denial reasons", mutate: func(c *provideriface.SessionMediaContract) { c.DenialReasons = []string{"denied"}; rehashPreSessionContract(c) }},
		{name: "session mismatch", mutate: func(c *provideriface.SessionMediaContract) { c.SessionScope = "another-session"; rehashPreSessionContract(c) }},
		{name: "workspace mismatch", mutate: func(c *provideriface.SessionMediaContract) { c.WorkspaceScope = "/other"; rehashPreSessionContract(c) }},
		{name: "no allowed capabilities", mutate: func(c *provideriface.SessionMediaContract) { c.Capabilities[0].State = provideriface.MediaCapabilityStateDenied; rehashPreSessionContract(c) }},
		{name: "incomplete allowed capability", mutate: func(c *provideriface.SessionMediaContract) { c.Capabilities[0].ContentTypes = nil; rehashPreSessionContract(c) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := testPreSessionMediaContract()
			test.mutate(&contract)
			if _, err := PreparePreSessionMediaBindings(PreSessionMediaBindingInput{
				AccountScopeID: "account", SessionID: "session", WorkspaceScope: "/workspace",
				Contract: contract, Staged: baseStaged,
			}); err == nil {
				t.Fatal("invalid resolved contract was admitted")
			}
		})
	}
}

func TestPreparePreSessionMediaBindingsRejectsStagedMetadataAndLimits(t *testing.T) {
	baseContract := testPreSessionMediaContract()
	base := testPreSessionStagedMedia("stage-1", strings.Repeat("a", 64), 512)
	tests := []struct {
		name    string
		contract func() provideriface.SessionMediaContract
		staged  []PreSessionMediaStagedMetadata
	}{
		{name: "account ownership mismatch", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.AccountScopeID = "other"; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "declared detected mismatch", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.DeclaredMIMEType = "image/jpeg"; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "unsupported MIME", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.DeclaredMIMEType = "image/jpeg"; item.DetectedMIMEType = "image/jpeg"; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "invalid digest", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.DigestSHA256 = "not-a-digest"; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "empty size", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.Size = 0; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "per item bytes", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: func() []PreSessionMediaStagedMetadata { item := base; item.Size = 1025; return []PreSessionMediaStagedMetadata{item} }()},
		{name: "duplicate staging id", contract: func() provideriface.SessionMediaContract { return baseContract }, staged: []PreSessionMediaStagedMetadata{base, base}},
		{name: "per modality count", contract: func() provideriface.SessionMediaContract { c := baseContract; c.Capabilities[0].MaxCount = 1; rehashPreSessionContract(&c); return c }, staged: []PreSessionMediaStagedMetadata{base, testPreSessionStagedMedia("stage-2", strings.Repeat("b", 64), 512)}},
		{name: "denied semantics", contract: func() provideriface.SessionMediaContract { c := baseContract; c.Capabilities[0].Semantics = pebblestore.ModelCatalogMediaSemanticsClientProcessed; rehashPreSessionContract(&c); return c }, staged: []PreSessionMediaStagedMetadata{base}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PreparePreSessionMediaBindings(PreSessionMediaBindingInput{
				AccountScopeID: "account", SessionID: "session", WorkspaceScope: "/workspace",
				Contract: test.contract(), Staged: test.staged,
			}); err == nil {
				t.Fatal("invalid staged media was admitted")
			}
		})
	}
}

func TestPreparePreSessionMediaBindingsEnforcesAggregateBounds(t *testing.T) {
	contract := testPreSessionMediaContract()
	contract.Capabilities[0].MaxBytes = pebblestore.SessionMediaDefaultMaxBytes
	contract.Capabilities[0].MaxCount = 8
	rehashPreSessionContract(&contract)
	staged := make([]PreSessionMediaStagedMetadata, 4)
	for i := range staged {
		staged[i] = testPreSessionStagedMedia("stage-"+string(rune('a'+i)), strings.Repeat(string(rune('a'+i)), 64), pebblestore.SessionMediaDefaultMaxBytes)
	}
	if _, err := PreparePreSessionMediaBindings(PreSessionMediaBindingInput{
		AccountScopeID: "account", SessionID: "session", WorkspaceScope: "/workspace",
		Contract: contract, Staged: staged,
	}); err == nil {
		t.Fatal("aggregate staged byte quota was not enforced")
	}

	tooMany := make([]PreSessionMediaStagedMetadata, pebblestore.SessionMediaDefaultMaxCount+1)
	for i := range tooMany {
		tooMany[i] = testPreSessionStagedMedia("many-"+string(rune('a'+i)), strings.Repeat(string(rune('a'+i)), 64), 1)
	}
	if _, err := PreparePreSessionMediaBindings(PreSessionMediaBindingInput{
		AccountScopeID: "account", SessionID: "session", WorkspaceScope: "/workspace",
		Contract: contract, Staged: tooMany,
	}); err == nil {
		t.Fatal("aggregate staged count limit was not enforced")
	}
}

func testPreSessionMediaContract() provideriface.SessionMediaContract {
	contract := provideriface.SessionMediaContract{
		Version: SessionMediaContractVersion, ProviderID: "openai", Model: "gpt-vision",
		ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses,
		CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey,
		CredentialFingerprint: "credential-fingerprint", AdapterID: provideriface.MediaAdapterIDOpenAIResponsesV1,
		SnapshotID: "snapshot", SnapshotVersion: "v1", SnapshotSource: "live",
		ExecutionMode: "auto", WorkspaceScope: "/workspace", SessionScope: "session",
		Capabilities: []provideriface.MediaContractCapability{{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"},
			MaxBytes: 1024, MaxCount: 2, Provenance: []string{"adapter:openai-responses-v1"},
		}},
	}
	rehashPreSessionContract(&contract)
	return contract
}

func testPreSessionStagedMedia(id, digest string, size int64) PreSessionMediaStagedMetadata {
	return PreSessionMediaStagedMetadata{
		StagingID: id, AccountScopeID: "account", Modality: "image",
		DeclaredMIMEType: "image/png; charset=binary", DetectedMIMEType: "image/png",
		FileType: ".png", Size: size, DigestSHA256: digest,
	}
}

func rehashPreSessionContract(contract *provideriface.SessionMediaContract) {
	normalizeSessionMediaContract(contract)
	contract.Hash = hashSessionMediaContract(*contract)
}
