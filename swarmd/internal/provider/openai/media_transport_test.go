package openai

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestValidateOpenAIMediaSurfaceRejectsCodexAndAllowsTextOnly(t *testing.T) {
	if err := validateOpenAIMediaSurface(provideriface.SessionMediaContract{}); err != nil {
		t.Fatalf("text-only contract rejected: %v", err)
	}
	valid := provideriface.SessionMediaContract{ProviderID: "openai", ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", AdapterID: "openai-responses-v1", Hash: "hash"}
	if err := validateOpenAIMediaSurface(valid); err != nil {
		t.Fatalf("valid OpenAI media contract rejected: %v", err)
	}
	crossSurface := valid
	crossSurface.ProviderID = "codex"
	crossSurface.ProviderSurface = "chatgpt_codex"
	crossSurface.CredentialSurface = "codex_oauth"
	crossSurface.AdapterID = "codex-chatgpt-v1"
	if err := validateOpenAIMediaSurface(crossSurface); err == nil {
		t.Fatal("Codex media contract accepted by OpenAI runner")
	}
}
