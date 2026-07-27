package tool

import (
	"context"
	"strings"
	"testing"
)

func TestWebFetchRequiresActiveExaAPIKeyBeforeRequest(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{
			Enabled:     false,
			SearchURL:   "https://api.exa.ai/search",
			ContentsURL: "https://api.exa.ai/contents",
		}, nil
	})

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: t.TempDir()}, Call{
		Name:      "webfetch",
		Arguments: `{"url":"https://example.com/page"}`,
	})
	if err == nil {
		t.Fatalf("webfetch succeeded without an Exa API key: %s", output)
	}
	if !strings.Contains(err.Error(), "active API key") || !strings.Contains(err.Error(), "/auth key exa <api_key>") {
		t.Fatalf("error = %q, want actionable Exa API-key setup guidance", err)
	}
}

func TestResolveExaConfigNormalizesToAPIKeySource(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{Enabled: true, Source: "mcp", APIKey: " test-key "}, nil
	})

	config, err := runtime.resolveExaConfig(context.Background())
	if err != nil {
		t.Fatalf("resolveExaConfig: %v", err)
	}
	if config.Source != "api_key" {
		t.Fatalf("source = %q, want api_key", config.Source)
	}
	if config.APIKey != "test-key" {
		t.Fatalf("api key was not trimmed")
	}
}
