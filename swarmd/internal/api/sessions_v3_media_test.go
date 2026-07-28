package api

import (
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestProjectSessionsV3MediaCapabilityReportsSanitizedAuthorityAndDenials(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		Version: 1, Hash: "contract", ProviderID: "openai", Model: "gpt-test",
		ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", AdapterID: "openai-responses-v1",
		SnapshotID: "snapshot", SnapshotVersion: "v1", SnapshotSource: "live",
		DenialReasons: []string{"effective agent tool contract denies media inspection"},
		Capabilities:  []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateDenied, Reason: "run media contract prerequisites are denied"}},
	}
	projection := projectSessionsV3MediaCapability(contract)
	if projection.Status != "unavailable" || projection.ContractToken != "" || len(projection.Capabilities) != 0 {
		t.Fatalf("denied projection exposed capability: %+v", projection)
	}
	if projection.ProviderSurface != contract.ProviderSurface || projection.CredentialSurface != contract.CredentialSurface || projection.AdapterID != contract.AdapterID {
		t.Fatalf("projection lost adapter authority: %+v", projection)
	}
	if !reflect.DeepEqual(projection.DenialReasons, contract.DenialReasons) || projection.SnapshotSource != "live" {
		t.Fatalf("projection lost provenance or denial reasons: %+v", projection)
	}
}

func TestSessionMediaAllowedCapabilityAcceptsImageExtensionWhenContractUsesMIMEOnly(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		Hash: "contract",
		Capabilities: []provideriface.MediaContractCapability{{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			MIMETypes: []string{"image/png"}, MaxBytes: 1024, MaxCount: 1,
		}},
	}
	if _, ok := sessionMediaAllowedCapability(contract, "image", "image/png", "png"); !ok {
		t.Fatal("MIME-authoritative image capability rejected the uploaded filename extension")
	}
}

func TestProjectSessionsV3MediaCapabilityFailsClosedForUnreviewedProviders(t *testing.T) {
	for _, providerID := range []string{"exa", "copilot", "gemini", "ollama", "bedrock"} {
		projection := projectSessionsV3MediaCapability(provideriface.SessionMediaContract{
			Version: 1, ProviderID: providerID, Hash: "forged",
			Capabilities:  []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateDenied}},
			DenialReasons: []string{"provider has no reviewed conversational media surface"},
		})
		if projection.Status != "unavailable" || projection.ContractToken != "" || len(projection.Capabilities) != 0 {
			t.Fatalf("unreviewed provider %q projected media: %+v", providerID, projection)
		}
	}
}
