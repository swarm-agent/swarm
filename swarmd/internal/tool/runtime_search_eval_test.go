package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fff"
)

type searchEvalCase struct {
	name       string
	searchRoot string
	include    string
	maxResults int
	toolArgs   map[string]any
	query      string
	patterns   []string
}

func TestSearchBeforeAfterCompactEval(t *testing.T) {
	repoRoot := searchEvalRepoRoot(t)
	cases := []searchEvalCase{
		{
			name:       "single_definition_executeSearchContentQuery",
			searchRoot: filepath.Join(repoRoot, "swarmd", "internal", "tool"),
			include:    "*.go",
			maxResults: 8,
			toolArgs: map[string]any{
				"query":       "executeSearchContentQuery",
				"path":        filepath.Join(repoRoot, "swarmd", "internal", "tool"),
				"include":     "*.go",
				"max_results": 8,
				"timeout_ms":  4000,
			},
			query: "executeSearchContentQuery",
		},
		{
			name:       "multi_identifier_runtime_helpers",
			searchRoot: filepath.Join(repoRoot, "swarmd", "internal", "tool"),
			include:    "*.go",
			maxResults: 12,
			toolArgs: map[string]any{
				"queries":     []string{"executeSearchContentQuery", "searchBatchFlags", "compactSearchQueries"},
				"path":        filepath.Join(repoRoot, "swarmd", "internal", "tool"),
				"include":     "*.go",
				"max_results": 12,
				"timeout_ms":  4000,
			},
			patterns: []string{"executeSearchContentQuery", "searchBatchFlags", "compactSearchQueries"},
		},
		{
			name:       "topic_ui_search_mode",
			searchRoot: filepath.Join(repoRoot, "internal", "ui"),
			include:    "*.go",
			maxResults: 10,
			toolArgs: map[string]any{
				"query":       "search_mode",
				"path":        filepath.Join(repoRoot, "internal", "ui"),
				"include":     "*.go",
				"max_results": 10,
				"timeout_ms":  4000,
			},
			query: "search_mode",
		},
	}

	scope := WorkspaceScope{PrimaryPath: repoRoot, Roots: []string{repoRoot}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := searchEvalBeforeOutput(t, scope, tc.toolArgs)
			after := searchEvalCompactOutput(t, tc.searchRoot, tc.include, tc.maxResults, tc.query, tc.patterns)

			beforeBytes := len(before)
			afterBytes := len(after)
			if beforeBytes <= 0 {
				t.Fatalf("before output is empty")
			}
			if afterBytes <= 0 {
				t.Fatalf("after output is empty")
			}
			if afterBytes >= beforeBytes {
				t.Fatalf("after output not smaller: before=%d after=%d\nbefore:\n%s\n\nafter:\n%s", beforeBytes, afterBytes, before, after)
			}

			beforePath := searchEvalFirstPath(t, before)
			if beforePath != "" && !strings.Contains(after, beforePath) {
				t.Fatalf("after output missing top before path %q\nafter:\n%s", beforePath, after)
			}
			if !strings.Contains(after, "→ Read ") {
				t.Fatalf("after output missing read suggestion\nafter:\n%s", after)
			}

			reduction := 100 * float64(beforeBytes-afterBytes) / float64(beforeBytes)
			t.Logf("case=%s before_bytes=%d after_bytes=%d reduction=%.1f%% top_path=%s", tc.name, beforeBytes, afterBytes, reduction, beforePath)
			t.Logf("before_preview=%q", searchEvalPreview(before, 220))
			t.Logf("after_preview=%q", searchEvalPreview(after, 220))
		})
	}
}

func searchEvalRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve repo root %q: %v", root, err)
	}
	return root
}

func searchEvalBeforeOutput(t *testing.T, scope WorkspaceScope, args map[string]any) string {
	t.Helper()
	legacyArgs := make(map[string]any, len(args)+1)
	for key, value := range args {
		legacyArgs[key] = value
	}
	legacyArgs["_search_payload_style"] = "legacy"
	legacyEncoded, err := json.Marshal(legacyArgs)
	if err != nil {
		t.Fatalf("marshal legacy tool args: %v", err)
	}
	output, err := ExecuteForWorkspaceScope(context.Background(), scope, Call{
		Name:      "search",
		Arguments: string(legacyEncoded),
	})
	if err != nil {
		t.Fatalf("execute search tool: %v\noutput:\n%s", err, output)
	}
	return output
}

func searchEvalCompactOutput(t *testing.T, searchRoot, include string, maxResults int, query string, patterns []string) string {
	t.Helper()
	inst, _, err := fff.Create(searchRoot, false)
	if err != nil {
		t.Fatalf("create FFF instance: %v", err)
	}
	defer inst.Destroy()

	completed, _, err := inst.WaitForScan(15 * time.Second)
	if err != nil {
		t.Fatalf("wait for scan: %v", err)
	}
	if !completed {
		t.Fatalf("scan did not complete for %s", searchRoot)
	}

	pageLimit := uint32(maxResults + 8)
	if len(patterns) > 0 {
		matches, metrics, err := inst.MultiGrepWithOptions(patterns, include, pageLimit, 0, 0, 0, 5, true)
		if err != nil {
			t.Fatalf("multi grep: %v", err)
		}
		return formatCompactSearchEval(searchRoot, include, matches, int(metrics.TotalMatched), maxResults, patterns)
	}

	matches, metrics, err := inst.GrepWithConfig(buildFFFGrepQuery(include, query), fff.GrepOptions{
		PageLimit:           pageLimit,
		AfterContext:        5,
		ClassifyDefinitions: true,
	})
	if err != nil {
		t.Fatalf("grep with config: %v", err)
	}
	return formatCompactSearchEval(searchRoot, include, matches, int(metrics.TotalMatched), maxResults, nil)
}

func formatCompactSearchEval(searchRoot, include string, matches []fff.GrepMatch, totalMatched, maxResults int, patterns []string) string {
	filtered := make([]fff.GrepMatch, 0, len(matches))
	for _, match := range matches {
		pathValue := filepath.Clean(match.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, match.RelativePath)
		if !matchesIncludeGlob(include, relPath) {
			continue
		}
		match.Path = pathValue
		match.RelativePath = relPath
		filtered = append(filtered, match)
	}
	if len(filtered) == 0 {
		return "0 matches"
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		left := filtered[i]
		right := filtered[j]
		if left.IsDefinition != right.IsDefinition {
			return left.IsDefinition
		}
		if left.RelativePath != right.RelativePath {
			return left.RelativePath < right.RelativePath
		}
		return left.LineNumber < right.LineNumber
	})

	shown := filtered
	if len(shown) > maxResults {
		shown = shown[:maxResults]
	}

	suggestPath, suggestDef := searchEvalSuggestPath(shown)
	lines := make([]string, 0, len(shown)*3+4)
	if len(patterns) > 0 {
		lines = append(lines, "patterns: "+strings.Join(patterns, " | "))
	}
	if suggestPath != "" {
		suffix := ""
		if suggestDef {
			suffix = " [def]"
		}
		lines = append(lines, fmt.Sprintf("→ Read %s%s", suggestPath, suffix))
	}
	if totalMatched > len(shown) {
		lines = append(lines, fmt.Sprintf("%d/%d matches shown", len(shown), totalMatched))
	}

	currentPath := ""
	expanded := make(map[string]struct{})
	for _, match := range shown {
		if match.RelativePath != currentPath {
			currentPath = match.RelativePath
			lines = append(lines, currentPath)
		}
		text := searchEvalCompactLine(match.LineContent, match.MatchRanges, 140)
		lines = append(lines, fmt.Sprintf(" %d: %s", match.LineNumber, text))
		if match.IsDefinition {
			if _, ok := expanded[match.RelativePath]; !ok {
				expanded[match.RelativePath] = struct{}{}
				for idx, ctx := range match.ContextAfter {
					if idx >= 5 {
						break
					}
					ctx = strings.TrimSpace(sanitizeForToolOutput(ctx))
					if ctx == "" {
						break
					}
					lines = append(lines, fmt.Sprintf("  %d| %s", match.LineNumber+uint64(idx)+1, searchEvalCompactLine(ctx, nil, 120)))
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func searchEvalSuggestPath(matches []fff.GrepMatch) (string, bool) {
	firstPath := ""
	for _, match := range matches {
		if firstPath == "" {
			firstPath = match.RelativePath
		}
		if match.IsDefinition {
			return match.RelativePath, true
		}
	}
	return firstPath, false
}

func searchEvalCompactLine(line string, ranges []fff.MatchRange, maxRunes int) string {
	line = strings.TrimSpace(sanitizeForToolOutput(line))
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= maxRunes || maxRunes <= 0 {
		return line
	}
	if len(ranges) == 0 {
		return string(runes[:maxRunes-1]) + "…"
	}
	start := int(ranges[0].Start)
	end := int(ranges[0].End)
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start > len(runes) {
		start = len(runes)
	}
	matchLen := end - start
	if matchLen >= maxRunes {
		return string(runes[start:end])
	}
	budget := maxRunes - matchLen
	before := budget / 3
	after := budget - before
	windowStart := start - before
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := end + after
	if windowEnd > len(runes) {
		windowEnd = len(runes)
	}
	fragment := string(runes[windowStart:windowEnd])
	if windowStart > 0 {
		fragment = "…" + fragment
	}
	if windowEnd < len(runes) {
		fragment += "…"
	}
	return fragment
}

func searchEvalFirstPath(t *testing.T, raw string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode before payload: %v\nraw:\n%s", err, raw)
	}
	for _, key := range []string{"matches", "files"} {
		items, ok := payload[key].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		first, ok := items[0].(map[string]any)
		if !ok {
			continue
		}
		if rel := strings.TrimSpace(fmt.Sprint(first["relative_path"])); rel != "" && rel != "<nil>" {
			return rel
		}
		if path := strings.TrimSpace(fmt.Sprint(first["path"])); path != "" && path != "<nil>" {
			return path
		}
	}
	return ""
}

func searchEvalPreview(raw string, maxRunes int) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\n", " | "))
	runes := []rune(raw)
	if len(runes) <= maxRunes {
		return raw
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}
