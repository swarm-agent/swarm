package fff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := filepath.Clean(cwd)
	for {
		if fileExists(filepath.Join(dir, "swarmd", "go.mod")) && fileExists(filepath.Join(dir, "internal", "fff", "fff.go")) {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatalf("repo root not found from %s", cwd)
		}
		dir = next
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func TestCreateWithOptionsAndSearch(t *testing.T) {
	repoRoot := testRepoRoot(t)
	historyDir := t.TempDir()

	inst, metrics, err := CreateWithOptions(repoRoot, CreateOptions{
		HistoryDBPath:   filepath.Join(historyDir, "history.mdb"),
		WarmupMmapCache: false,
	})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	defer inst.Destroy()

	if metrics.CreateDuration < 0 {
		t.Fatalf("unexpected create duration: %v", metrics.CreateDuration)
	}

	completed, _, err := inst.WaitForScan(2 * time.Minute)
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if !completed {
		t.Fatal("scan did not complete")
	}

	items, searchMetrics, err := inst.Search("runtime", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if searchMetrics.TotalFiles == 0 {
		t.Fatal("expected indexed files")
	}
	if len(items) == 0 {
		t.Fatal("expected search results")
	}
}

func TestGrepWithConfigSupportsDefinitionsAndContext(t *testing.T) {
	repoRoot := testRepoRoot(t)
	inst, _, err := Create(repoRoot, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer inst.Destroy()

	completed, _, err := inst.WaitForScan(2 * time.Minute)
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if !completed {
		t.Fatal("scan did not complete")
	}

	matches, metrics, err := inst.GrepWithConfig("func executeSearchContentQuery", GrepOptions{
		PageLimit:           10,
		BeforeContext:       1,
		AfterContext:        3,
		ClassifyDefinitions: true,
	})
	if err != nil {
		t.Fatalf("GrepWithConfig: %v", err)
	}
	if metrics.TotalFilesSearched == 0 {
		t.Fatal("expected searched files")
	}
	if len(matches) == 0 {
		t.Fatal("expected grep matches")
	}

	foundDefinition := false
	foundContext := false
	for _, match := range matches {
		if strings.Contains(match.RelativePath, filepath.ToSlash(filepath.Join("internal", "tool", "runtime.go"))) && match.IsDefinition {
			foundDefinition = true
			if len(match.ContextAfter) > 0 {
				foundContext = true
			}
		}
	}
	if !foundDefinition {
		t.Fatal("expected at least one definition match in runtime.go")
	}
	if !foundContext {
		t.Fatal("expected context lines for definition match")
	}
}

func TestMultiGrepAndHistory(t *testing.T) {
	repoRoot := testRepoRoot(t)
	historyDir := t.TempDir()
	inst, _, err := CreateWithOptions(repoRoot, CreateOptions{
		HistoryDBPath:   filepath.Join(historyDir, "history.mdb"),
		WarmupMmapCache: false,
	})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	defer inst.Destroy()

	completed, _, err := inst.WaitForScan(2 * time.Minute)
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if !completed {
		t.Fatal("scan did not complete")
	}

	patterns := []string{"executeSearchContentQuery", "executeSearchFileQuery"}
	matches, _, err := inst.MultiGrepWithOptions(patterns, "*.go", 20, 0, 0, 0, 0, true)
	if err != nil {
		t.Fatalf("MultiGrepWithOptions: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected multi-grep matches")
	}

	tracked, err := inst.TrackQuery("executeSearchContentQuery", filepath.Join(repoRoot, "swarmd", "internal", "tool", "runtime.go"))
	if err != nil {
		t.Fatalf("TrackQuery: %v", err)
	}
	if !tracked {
		t.Fatal("expected TrackQuery success")
	}

	value, ok, err := inst.GetHistoricalQuery(0)
	if err != nil {
		t.Fatalf("GetHistoricalQuery: %v", err)
	}
	if !ok {
		t.Fatal("expected historical query entry")
	}
	if strings.TrimSpace(value) == "" {
		t.Fatal("expected non-empty historical query")
	}
}
