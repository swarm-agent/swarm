package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchRuntimeHandlesWhitespaceSeparatedMultiPath(t *testing.T) {
	t.Setenv("SWARM_FFF_SEARCH_HELPER", searchEvalHelperPath(t))
	repoRoot := searchEvalRepoRoot(t)
	scope := WorkspaceScope{PrimaryPath: repoRoot, Roots: []string{repoRoot}}
	args, err := json.Marshal(map[string]any{
		"queries":     []string{"FlowRunStatusRunning", "NormalizeCadence"},
		"path":        "swarmd/internal/store/pebble swarmd/internal/flow",
		"include":     "*.go",
		"max_results": 12,
		"timeout_ms":  4000,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	output, err := ExecuteForWorkspaceScope(context.Background(), scope, Call{Name: "search", Arguments: string(args)})
	if err != nil {
		t.Fatalf("execute search tool: %v\noutput:\n%s", err, output)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\noutput:\n%s", err, output)
	}
	if got := mapString(decoded, "search_mode"); got != "content" {
		t.Fatalf("search_mode mismatch: got %q", got)
	}
	results, ok := decoded["results"].([]any)
	if !ok || len(results) < 2 {
		t.Fatalf("expected grouped results from both roots, got %T %#v\noutput=%s", decoded["results"], decoded["results"], output)
	}
	paths := searchDecodedResultPaths(results)
	if !searchPathContains(paths, "swarmd/internal/store/pebble/flow_store.go") {
		t.Fatalf("expected pebble flow_store result, paths=%v output=%s", paths, output)
	}
	if !searchPathContains(paths, "swarmd/internal/flow/schedule.go") {
		t.Fatalf("expected flow schedule result, paths=%v output=%s", paths, output)
	}
	queryResults, ok := decoded["query_results"].([]any)
	if !ok || len(queryResults) < 2 {
		t.Fatalf("expected per-query summaries, got %T %#v", decoded["query_results"], decoded["query_results"])
	}
}

func TestSearchRuntimeFallsBackToFileSearchPerQuery(t *testing.T) {
	t.Setenv("SWARM_FFF_SEARCH_HELPER", searchEvalHelperPath(t))
	repoRoot := searchEvalRepoRoot(t)
	scope := WorkspaceScope{PrimaryPath: repoRoot, Roots: []string{repoRoot}}
	args, err := json.Marshal(map[string]any{
		"queries":     []string{"DefinitelyMissingContentNeedle", "flow_store.go"},
		"path":        "swarmd/internal/store/pebble",
		"include":     "*.go",
		"max_results": 8,
		"timeout_ms":  4000,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	output, err := ExecuteForWorkspaceScope(context.Background(), scope, Call{Name: "search", Arguments: string(args)})
	if err != nil {
		t.Fatalf("execute search tool: %v\noutput:\n%s", err, output)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\noutput:\n%s", err, output)
	}
	results, ok := decoded["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("expected fallback results, got %T %#v\noutput=%s", decoded["results"], decoded["results"], output)
	}
	paths := searchDecodedResultPaths(results)
	if !searchPathContains(paths, "flow_store.go") {
		t.Fatalf("expected file-name fallback result for flow_store, paths=%v output=%s", paths, output)
	}
}

func TestSearchRuntimeUsesCompactDefinitionAwarePayload(t *testing.T) {
	t.Setenv("SWARM_FFF_SEARCH_HELPER", searchEvalHelperPath(t))
	repoRoot := searchEvalRepoRoot(t)
	scope := WorkspaceScope{PrimaryPath: repoRoot, Roots: []string{repoRoot}}
	args, err := json.Marshal(map[string]any{
		"query":       "executeSearchContentQuery",
		"path":        repoRoot + "/swarmd/internal/tool",
		"include":     "*.go",
		"max_results": 8,
		"timeout_ms":  4000,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	output, err := ExecuteForWorkspaceScope(context.Background(), scope, Call{
		Name:      "search",
		Arguments: string(args),
	})
	if err != nil {
		t.Fatalf("execute search tool: %v\noutput:\n%s", err, output)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\noutput:\n%s", err, output)
	}
	if got := mapString(decoded, "path_id"); got != "tool.search.v1" {
		t.Fatalf("path_id mismatch: got %q", got)
	}
	if got := mapString(decoded, "search_mode"); got != "content" {
		t.Fatalf("search_mode mismatch: got %q", got)
	}
	if got := asInt(decoded["count"], 0); got <= 0 {
		t.Fatalf("expected count > 0, got %d", got)
	}
	if got := asInt(decoded["total_matched"], 0); got <= 0 {
		t.Fatalf("expected total_matched > 0, got %d", got)
	}
	if _, exists := decoded["pattern"]; exists {
		t.Fatalf("compact payload should not expose legacy pattern field: %v", decoded["pattern"])
	}
	if _, exists := decoded["query"]; exists {
		t.Fatalf("compact payload should not expose legacy query field: %v", decoded["query"])
	}
	if _, exists := decoded["queries"]; exists {
		t.Fatalf("compact payload should not expose legacy queries field: %v", decoded["queries"])
	}
	if _, exists := decoded["query_count"]; exists {
		t.Fatalf("compact payload should not expose legacy query_count field: %v", decoded["query_count"])
	}

	results, ok := decoded["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("expected grouped results, got %T %#v", decoded["results"], decoded["results"])
	}
	firstGroup, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first result group object, got %T", results[0])
	}
	if got := mapString(firstGroup, "path"); got == "" {
		t.Fatal("expected path in first grouped result")
	}
	items, ok := firstGroup["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected grouped items, got %T %#v", firstGroup["items"], firstGroup["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first grouped item object, got %T", items[0])
	}
	if got := asInt(first["line"], 0); got <= 0 {
		t.Fatalf("expected positive line number, got %d", got)
	}
	if got := mapString(first, "text"); got == "" {
		t.Fatal("expected text in first grouped item")
	}
	if _, exists := first["context_after"]; exists {
		t.Fatalf("compact payload should not expose raw context_after in grouped item: %v", first)
	}
	if _, exists := first["match_ranges"]; exists {
		t.Fatalf("compact payload should not expose raw match_ranges in grouped item: %v", first)
	}

	summary := strings.TrimSpace(mapString(decoded, "summary"))
	if !strings.Contains(strings.ToLower(summary), "search") {
		t.Fatalf("expected search summary, got %q", summary)
	}

	safety, ok := decoded["safety"].(map[string]any)
	if !ok {
		t.Fatalf("expected safety object, got %T", decoded["safety"])
	}
	if _, ok := safety["prompt_injection_detected"]; !ok {
		t.Fatalf("expected safety metadata, got %v", safety)
	}
}

func searchDecodedResultPaths(results []any) []string {
	paths := make([]string, 0, len(results))
	for _, raw := range results {
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := strings.TrimSpace(mapString(group, "path"))
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func searchPathContains(paths []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, path := range paths {
		if strings.Contains(path, want) {
			return true
		}
	}
	return false
}

func searchEvalHelperPath(t *testing.T) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("SWARM_FFF_SEARCH_HELPER")); configured != "" {
		return configured
	}
	repoRoot := searchEvalRepoRoot(t)
	candidate := filepath.Join(repoRoot, ".bin", "main", "swarm-fff-search")
	candidateInfo, candidateErr := os.Stat(candidate)
	helperSource := filepath.Join(repoRoot, "swarmd", "cmd", "swarm-fff-search", "main.go")
	if candidateErr == nil && !candidateInfo.IsDir() && candidateInfo.Mode()&0o111 != 0 {
		if sourceInfo, sourceErr := os.Stat(helperSource); sourceErr != nil || !sourceInfo.ModTime().After(candidateInfo.ModTime()) {
			return candidate
		}
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("create helper output dir: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", candidate, "./cmd/swarm-fff-search")
	cmd.Dir = filepath.Join(repoRoot, "swarmd")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build swarm-fff-search helper: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return candidate
}
