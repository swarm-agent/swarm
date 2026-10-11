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
	// The same error reaches the desktop, the terminal UI, SDK apps and MCP
	// clients, so it must name no client-specific settings page or command.
	if !strings.Contains(err.Error(), "no Exa API key is set up yet") || strings.Contains(err.Error(), "Settings") || strings.Contains(err.Error(), "/auth") {
		t.Fatalf("error = %q, want a plain, client-neutral missing-key error", err)
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
