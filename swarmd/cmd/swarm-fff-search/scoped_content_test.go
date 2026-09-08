package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/tool/searchipc"
)

// Purpose: worker.handle/runContentSearches must preserve literal content and
// prefilter exact-file/include scopes before native pagination. Bare filenames
// must not become grep text when FFF AI mode is disabled. These native fixture
// tests assert lines, scope, scanned-file counts, misses and worker reuse rather
// than accepting a filename fallback as proof of successful content search.
func TestScopedContentNativeFilters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "src", "nested")
	file := filepath.Join(dir, "example.go")
	writeTestFile(t, file, "before\nUniqueNeedle\nafter\n*.go\n!excluded\n/src/\nstatus:modified\na  b\nUniqueNeedle\n")
	writeTestFile(t, filepath.Join(dir, "sibling.go"), "UniqueNeedle\n")
	writeTestFile(t, filepath.Join(root, "outside", "example.go"), "UniqueNeedle\n")
	w := &worker{}
	defer w.close()
	for _, tc := range []struct{ name, target, include, query, mode string }{
		{"exact file", file, "example.go", "UniqueNeedle", "literal"},
		{"exact include", dir, "example.go", "UniqueNeedle", "literal"},
		{"wildcard include", dir, "example*.go", "UniqueNeedle", "literal"},
		{"path include", root, "src/nested/example.go", "UniqueNeedle", "literal"},
		{"relative path include", filepath.Join(root, "src"), "nested/example.go", "UniqueNeedle", "literal"},
		{"regex file", file, "example.go", "Unique[N]eedle", "regex"},
		{"fuzzy file", file, "example.go", "UniqueNeedle", "fuzzy"},
		{"literal glob", file, "example.go", "*.go", "literal"},
		{"literal exclusion", file, "example.go", "!excluded", "literal"},
		{"literal path", file, "example.go", "/src/", "literal"},
		{"literal status", file, "example.go", "status:modified", "literal"},
		{"literal spacing", file, "example.go", "a  b", "literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := searchipc.Request{IndexRoot: root, TargetPath: tc.target, Operation: "content", Queries: []string{tc.query}, Include: tc.include, ContentMode: tc.mode, MaxResults: 10, PageLimit: 10, MaxMatchesPerFile: 1, BeforeContext: 1, AfterContext: 1, TimeoutMillis: 10000}
			resp := w.handle(req, time.Now())
			if !resp.Completed || resp.HelperError != "" || len(resp.ContentResults) != 1 {
				t.Fatalf("bad response: %+v", resp)
			}
			result := resp.ContentResults[0]
			if result.Error != "" || len(result.Matches) != 1 || len(resp.FileResults) != 0 {
				t.Fatalf("not one content match: %+v", resp)
			}
			match := result.Matches[0]
			wantPath := "example.go"
			if tc.target == root {
				wantPath = "src/nested/example.go"
			} else if tc.target == filepath.Join(root, "src") {
				wantPath = "nested/example.go"
			}
			if match.RelativePath != wantPath || match.LineNumber == 0 {
				t.Fatalf("wrong match: %+v", match)
			}
			if tc.mode == "literal" && !strings.Contains(match.LineContent, tc.query) {
				t.Fatalf("literal text changed: %+v", match)
			}
			if result.Metrics.TotalFilesSearched != 1 || result.Metrics.FilteredFileCount != 1 {
				t.Fatalf("scope not pushed into native filter: %+v", result.Metrics)
			}
			// The native fuzzy matcher does not return context; literal/regex do.
			if tc.mode != "fuzzy" && (len(match.ContextBefore) != 1 || len(match.ContextAfter) != 1) {
				t.Fatalf("missing context: %+v", match)
			}
			if resp.Diagnostics.ColdStartCount != 1 {
				t.Fatalf("worker not reused: %+v", resp.Diagnostics)
			}
		})
	}
	// A missing include must not widen into filename fallback or scan siblings.
	resp := w.handle(searchipc.Request{IndexRoot: root, TargetPath: dir, Operation: "content", Queries: []string{"UniqueNeedle"}, Include: "missing.go", MaxResults: 10, TimeoutMillis: 10000}, time.Now())
	if !resp.Completed || len(resp.ContentResults) != 1 || len(resp.ContentResults[0].Matches) != 0 || resp.ContentResults[0].Metrics.TotalFilesSearched != 0 || len(resp.FileResults) != 0 {
		t.Fatalf("miss broadened scope: %+v", resp)
	}
}

// Purpose: native prefiltering must be anchored and escape literal path syntax;
// an identical nested suffix must not consume the exact target's result page.
func TestScopedContentAnchorsAndPagination(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"example.go", "123", "odd[1].go", "a,b.go"} {
		writeTestFile(t, filepath.Join(root, name), "UniqueNeedle\n")
		writeTestFile(t, filepath.Join(root, "nested", name), "UniqueNeedle\n")
	}
	w := &worker{}
	defer w.close()
	for _, name := range []string{"example.go", "123", "odd[1].go", "a,b.go"} {
		resp := w.handle(searchipc.Request{IndexRoot: root, TargetPath: filepath.Join(root, name), Operation: "content", Queries: []string{"UniqueNeedle"}, MaxResults: 1, PageLimit: 1, TimeoutMillis: 10000}, time.Now())
		if !resp.Completed || len(resp.ContentResults) != 1 {
			t.Fatalf("bad response: %+v", resp)
		}
		r := resp.ContentResults[0]
		if r.Error != "" || len(r.Matches) != 1 || r.Matches[0].RelativePath != name || r.Metrics.TotalFilesSearched != 1 || r.Metrics.FilteredFileCount != 1 || r.Metrics.NextFileOffset != 0 {
			t.Fatalf("unanchored %q: %+v", name, resp)
		}
	}
	// Three scoped files, one match per page, no duplicates or outside rows.
	target := filepath.Join(root, "pages")
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		writeTestFile(t, filepath.Join(target, name), "UniqueNeedle\n")
	}
	// Use a new worker so this test does not depend on watcher timing.
	pages := &worker{}
	defer pages.close()
	seen := map[string]bool{}
	var offset uint32
	for page := 0; page < 4; page++ {
		resp := pages.handle(searchipc.Request{IndexRoot: root, TargetPath: target, Operation: "content", Queries: []string{"UniqueNeedle"}, Include: "*.go", MaxResults: 1, PageLimit: 1, FileOffset: offset, TimeoutMillis: 10000}, time.Now())
		if !resp.Completed || len(resp.ContentResults) != 1 {
			t.Fatalf("bad page: %+v", resp)
		}
		r := resp.ContentResults[0]
		for _, m := range r.Matches {
			if seen[m.RelativePath] {
				t.Fatalf("duplicate page: %+v", resp)
			}
			seen[m.RelativePath] = true
		}
		if r.Metrics.NextFileOffset == 0 {
			break
		}
		if r.Metrics.NextFileOffset <= offset {
			t.Fatalf("nonadvancing page: %+v", resp)
		}
		offset = r.Metrics.NextFileOffset
	}
	if len(seen) != 3 {
		t.Fatalf("incomplete pages: %v", seen)
	}
}

// Purpose: unsupported native constraint tokens must fail explicitly, not scan
// a broader scope. Spaces in the workspace root itself remain supported because
// constraints are root-relative; only unrepresentable filter tokens are rejected.
func TestScopedContentRejectsUnrepresentableFilter(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "with space")
	writeTestFile(t, filepath.Join(target, "a.go"), "needle\n")
	w := &worker{}
	defer w.close()
	resp := w.handle(searchipc.Request{IndexRoot: root, TargetPath: target, Operation: "content", Queries: []string{"needle"}, MaxResults: 10, TimeoutMillis: 10000}, time.Now())
	if len(resp.ContentResults) != 1 || !strings.Contains(resp.ContentResults[0].Error, "whitespace") || resp.ContentResults[0].Metrics.TotalFilesSearched != 0 || len(resp.FileResults) != 0 {
		t.Fatalf("ambiguous filter accepted: %+v", resp)
	}
}

// Purpose: the newline-delimited C multi-pattern ABI must never reinterpret one
// user query as OR alternatives, nor truncate it at NUL. Reject before grep.
func TestScopedContentRejectsPatternSeparators(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "first\nsecond\n")
	w := &worker{}
	defer w.close()
	for _, query := range []string{"first\nsecond", "first\rsecond", "first\x00second"} {
		resp := w.handle(searchipc.Request{IndexRoot: root, TargetPath: root, Operation: "content", Queries: []string{query}, MaxResults: 10, TimeoutMillis: 10000}, time.Now())
		if len(resp.ContentResults) != 1 || resp.ContentResults[0].Error == "" || len(resp.ContentResults[0].Matches) != 0 || resp.ContentResults[0].Metrics.TotalFilesSearched != 0 {
			t.Fatalf("pattern reinterpreted: %+v", resp)
		}
	}
}
