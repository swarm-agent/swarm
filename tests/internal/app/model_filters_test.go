package app

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestModelPresetListForProviderCodexKeepsNewestFirst(t *testing.T) {
	presets := modelPresetListForProvider("codex")
	if len(presets) < 4 {
		t.Fatalf("expected codex presets, got %v", presets)
	}
	want := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex"}
	for i, model := range want {
		if presets[i] != model {
			t.Fatalf("codex preset[%d] = %q, want %q", i, presets[i], model)
		}
	}
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestModelAllowedByProviderPresetCodex(t *testing.T) {
	if !modelAllowedByProviderPreset("codex", "gpt-5.5") {
		t.Fatalf("expected gpt-5.5 to be allowed for codex")
	}
	if !modelAllowedByProviderPreset("codex", "gpt-5.4") {
		t.Fatalf("expected gpt-5.4 to be allowed for codex")
	}
	if !modelAllowedByProviderPreset("codex", "gpt-5.4-mini") {
		t.Fatalf("expected gpt-5.4-mini to be allowed for codex")
	}
	if !modelAllowedByProviderPreset("codex", "gpt-5.3-codex-spark") {
		t.Fatalf("expected gpt-5.3-codex-spark to be allowed for codex")
	}
	if modelAllowedByProviderPreset("codex", "gpt-5-codex") {
		t.Fatalf("expected gpt-5-codex to be filtered out for codex")
	}
	if !modelAllowedByProviderPreset("google", "gemini-2.5-pro") {
		t.Fatalf("expected non-codex providers to remain unfiltered")
	}
}

func TestMapAgentsModalDataFiltersCodexModelOptions(t *testing.T) {
	state := client.AgentState{
		Profiles: []client.AgentProfile{
			{
				Name:     "swarm",
				Mode:     "primary",
				Provider: "codex",
				Model:    "gpt-5-codex",
				Enabled:  true,
			},
			{
				Name:     "memory",
				Mode:     "subagent",
				Provider: "codex",
				Model:    "",
				Enabled:  true,
			},
		},
		ActivePrimary: "swarm",
	}
	resolved := providerModelResolverResult{
		ProviderIDs: []string{"codex"},
		ProviderStatuses: map[string]client.ProviderStatus{
			"codex": {ID: "codex", DefaultModel: "gpt-5.5"},
		},
		ModelsByProvider: map[string][]string{
			"codex": []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.1-codex-mini"},
		},
		ReasoningByKey: map[string]bool{
			"codex/gpt-5.5":            true,
			"codex/gpt-5.4":            true,
			"codex/gpt-5.4-mini":       true,
			"codex/gpt-5.1-codex-mini": true,
		},
	}

	data := mapAgentsModalData(state, resolved, "codex", "gpt-5-codex", "high")

	models := data.ModelsByProvider["codex"]
	if hasString(models, "gpt-5-codex") {
		t.Fatalf("disallowed codex model should not appear in available models: %v", models)
	}
	if !hasString(models, "gpt-5.5") {
		t.Fatalf("expected allowed model in available models: %v", models)
	}
	if !hasString(models, "gpt-5.4") {
		t.Fatalf("expected allowed model in available models: %v", models)
	}
	if !hasString(models, "gpt-5.4-mini") {
		t.Fatalf("expected allowed model in available models: %v", models)
	}
	if !hasString(models, "gpt-5.1-codex-mini") {
		t.Fatalf("expected allowed model in available models: %v", models)
	}
	if !hasString(models, "gpt-5.3-codex-spark") {
		t.Fatalf("expected preset model in available models: %v", models)
	}
	if data.DefaultModel != "gpt-5.5" {
		t.Fatalf("default model = %q, want %q", data.DefaultModel, "gpt-5.5")
	}
}

func TestModelIDLessForProviderGoogleInvertsFallbackOrder(t *testing.T) {
	if !modelIDLessForProvider("google", "gemini-2.5-pro", "gemini-1.5-flash") {
		t.Fatalf("expected google fallback order to place gemini-2.5-pro before gemini-1.5-flash")
	}
	if modelIDLessForProvider("google", "gemini-1.5-flash", "gemini-2.5-pro") {
		t.Fatalf("did not expect gemini-1.5-flash before gemini-2.5-pro for google")
	}
	if modelIDLessForProvider("codex", "gpt-5.3-codex", "gpt-5.4") {
		t.Fatalf("did not expect gpt-5.3-codex before gpt-5.4 for codex")
	}
}

func TestMapAgentsModalDataPlacesProviderDefaultModelFirst(t *testing.T) {
	resolved := providerModelResolverResult{
		ProviderIDs: []string{"codex"},
		ProviderStatuses: map[string]client.ProviderStatus{
			"codex": {ID: "codex", DefaultModel: "gpt-5.5"},
		},
		ModelsByProvider: map[string][]string{
			"codex": {
				"gpt-5.1-codex-mini",
				"gpt-5.4",
				"gpt-5.5",
				"gpt-5.3-codex",
				"gpt-5.2",
			},
		},
	}

	data := mapAgentsModalData(client.AgentState{}, resolved, "", "", "")
	models := data.ModelsByProvider["codex"]
	if len(models) == 0 {
		t.Fatalf("expected codex models to be present")
	}
	if models[0] != "gpt-5.5" {
		t.Fatalf("first codex model = %q, want gpt-5.5", models[0])
	}
}

func TestAuthCredentialUpsertToast(t *testing.T) {
	tests := []struct {
		name          string
		upsert        *ui.AuthModalUpsert
		record        client.AuthCredential
		wantLevel     ui.ToastLevel
		wantContains  []string
		wantNotHaving []string
	}{
		{
			name: "requested active and became active warns about replacement",
			upsert: &ui.AuthModalUpsert{
				Provider: "codex",
				Active:   true,
			},
			record: client.AuthCredential{
				Provider: "codex",
				Active:   true,
			},
			wantLevel:    ui.ToastSuccess,
			wantContains: []string{"set active", "replaced previous active credential"},
		},
		{
			name: "requested inactive keeps old active with explicit next step",
			upsert: &ui.AuthModalUpsert{
				Provider: "codex",
				Active:   false,
			},
			record: client.AuthCredential{
				Provider: "codex",
				Active:   false,
			},
			wantLevel:    ui.ToastWarning,
			wantContains: []string{"not active", "Existing active credential is unchanged", "Press a"},
		},
		{
			name: "requested active but not active warns with fix step",
			upsert: &ui.AuthModalUpsert{
				Provider: "codex",
				Active:   true,
			},
			record: client.AuthCredential{
				Provider: "codex",
				Active:   false,
			},
			wantLevel:    ui.ToastWarning,
			wantContains: []string{"not active", "Press a"},
		},
		{
			name:   "nil upsert keeps active success path",
			upsert: nil,
			record: client.AuthCredential{
				Provider: "codex",
				Active:   true,
			},
			wantLevel:     ui.ToastSuccess,
			wantContains:  []string{"set active"},
			wantNotHaving: []string{"replaced previous active credential"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			level, msg := authCredentialUpsertToast(tc.upsert, tc.record)
			if level != tc.wantLevel {
				t.Fatalf("level=%v want=%v", level, tc.wantLevel)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Fatalf("message %q missing substring %q", msg, want)
				}
			}
			for _, unwanted := range tc.wantNotHaving {
				if strings.Contains(msg, unwanted) {
					t.Fatalf("message %q unexpectedly contains %q", msg, unwanted)
				}
			}
		})
	}
}

func TestChatAvailableModelsIncludesReasoningMetadata(t *testing.T) {
	resolved := providerModelResolverResult{
		ProviderIDs: []string{"codex"},
		ProviderStatuses: map[string]client.ProviderStatus{
			"codex": {ID: "codex", Ready: true, DefaultModel: "gpt-5.5"},
		},
		ModelsByProvider: map[string][]string{
			"codex": {"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"},
		},
		ReasoningByKey: map[string]bool{
			"codex/gpt-5.5":      true,
			"codex/gpt-5.4":      true,
			"codex/gpt-5.4-mini": false,
		},
		CatalogByKey: map[string]client.ModelCatalogRecord{
			"codex/gpt-5.5": {Provider: "codex", Model: "gpt-5.5", ContextMode: "default", Reasoning: true},
		},
	}

	app := &App{}
	got := app.chatAvailableModelsFromResolved(resolved, "codex", "gpt-5.5", "default")
	if len(got) == 0 {
		t.Fatalf("expected models")
	}
	var found55, found55OneM, foundDefault, foundMini bool
	for _, entry := range got {
		switch entry.Model {
		case "gpt-5.5":
			if entry.ContextMode == "1m" {
				found55OneM = true
			} else {
				found55 = true
			}
			if !entry.Reasoning {
				t.Fatalf("expected gpt-5.5 reasoning=true")
			}
		case "gpt-5.4":
			foundDefault = true
			if !entry.Reasoning {
				t.Fatalf("expected gpt-5.4 reasoning=true")
			}
		case "gpt-5.4-mini":
			foundMini = true
			if entry.Reasoning {
				t.Fatalf("expected gpt-5.4-mini reasoning=false")
			}
		}
	}
	if !found55 || !found55OneM || !foundDefault || !foundMini {
		t.Fatalf("missing expected models in %#v", got)
	}
}
