package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchUsesHostedExaMCPWhenNoAPIKey(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Fatalf("unexpected x-api-key header %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      received["id"],
			"result": map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": "# Example Page\nURL: https://example.com/page\nPublished: 2026-01-01\nAuthor: Example Author\n\nExample markdown body.",
				}},
			},
		})
	}))
	defer server.Close()

	runtime := NewRuntime(1)
	runtime.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{
			Enabled: true,
			Source:  "mcp",
			MCPURL:  server.URL,
		}, nil
	})

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: t.TempDir()}, Call{
		Name:      "webfetch",
		Arguments: `{"url":"https://example.com/page","text":{"max_characters":1234}}`,
	})
	if err != nil {
		t.Fatalf("webfetch failed: %v\noutput: %s", err, output)
	}

	params, ok := received["params"].(map[string]any)
	if !ok {
		t.Fatalf("missing params in request: %#v", received)
	}
	if got := params["name"]; got != "web_fetch_exa" {
		t.Fatalf("tool name = %v, want web_fetch_exa", got)
	}
	arguments, ok := params["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("missing arguments in request: %#v", params)
	}
	if got := int(arguments["maxCharacters"].(float64)); got != 1234 {
		t.Fatalf("maxCharacters = %d, want 1234", got)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if got := decoded["exa_source"]; got != "mcp" {
		t.Fatalf("exa_source = %v, want mcp", got)
	}
	if got := int(decoded["success_count"].(float64)); got != 1 {
		t.Fatalf("success_count = %d, want 1", got)
	}
	results := decoded["results"].([]any)
	first := results[0].(map[string]any)
	if got := strings.TrimSpace(first["text"].(string)); got != "Example markdown body." {
		t.Fatalf("text = %q", got)
	}
}

func TestMCPExaContentParserHandlesFetchErrors(t *testing.T) {
	results, statuses := parseMCPExaContentResults("Error fetching https://example.invalid: blocked", nil)
	if len(results) != 0 {
		t.Fatalf("results = %#v, want none", results)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses len = %d, want 1", len(statuses))
	}
	if statuses[0].ID != "https://example.invalid" || statuses[0].Status != "error" || statuses[0].Error == nil || statuses[0].Error.Tag != "blocked" {
		t.Fatalf("unexpected status: %#v", statuses[0])
	}
}
