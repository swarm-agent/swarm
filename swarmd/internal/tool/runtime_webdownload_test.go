package tool

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
)

func TestWebDownloadDefaultUsesWorkspaceCacheBucket(t *testing.T) {
	workspaceDir := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"id":    "https://example.com/page",
				"url":   "https://example.com/page",
				"title": "Example Page",
				"text":  "Example body.",
			}},
		})
	}))
	defer server.Close()

	runtime := NewRuntime(1)
	runtime.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{
			Enabled:     true,
			Source:      "api_key",
			APIKey:      "test-key",
			ContentsURL: server.URL,
		}, nil
	})

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: workspaceDir}, Call{
		Name:      "webdownload",
		Arguments: `{"url":"https://example.com/page"}`,
	})
	if err != nil {
		t.Fatalf("webdownload failed: %v\noutput: %s", err, output)
	}

	wantDir, err := appstorage.WorkspaceCacheDir(workspaceDir, "downloads")
	if err != nil {
		t.Fatalf("WorkspaceCacheDir: %v", err)
	}
	wantFile := filepath.Join(wantDir, "001-example-com-page.txt")
	if got, err := os.ReadFile(wantFile); err != nil || strings.TrimSpace(string(got)) != "Example body." {
		t.Fatalf("download file = %q, %v; want Example body.", string(got), err)
	}
	if info, err := os.Stat(wantFile); err != nil || info.Mode().Perm() != appstorage.PrivateFilePerm {
		t.Fatalf("download file perm = %v, %v; want %v", infoModePerm(info), err, appstorage.PrivateFilePerm)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, ".swarm", "downloads")); !os.IsNotExist(err) {
		t.Fatalf("workspace-local downloads directory exists or stat failed unexpectedly: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if got := filepath.Clean(decoded["output_dir"].(string)); got != filepath.Clean(wantDir) {
		t.Fatalf("output_dir = %q, want %q", got, wantDir)
	}
	manifest := decoded["manifest"].([]any)
	first := manifest[0].(map[string]any)
	if got := filepath.Clean(first["file_path"].(string)); got != filepath.Clean(wantFile) {
		t.Fatalf("file_path = %q, want %q", got, wantFile)
	}
}

func TestWebDownloadExplicitOutputDirRemainsWorkspaceRelative(t *testing.T) {
	workspaceDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"id":   "https://example.com/explicit",
				"url":  "https://example.com/explicit",
				"text": "Explicit body.",
			}},
		})
	}))
	defer server.Close()

	runtime := NewRuntime(1)
	runtime.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{
			Enabled:     true,
			Source:      "api_key",
			APIKey:      "test-key",
			ContentsURL: server.URL,
		}, nil
	})

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: workspaceDir}, Call{
		Name:      "webdownload",
		Arguments: `{"url":"https://example.com/explicit","output_dir":"project-downloads"}`,
	})
	if err != nil {
		t.Fatalf("webdownload failed: %v\noutput: %s", err, output)
	}

	wantFile := filepath.Join(workspaceDir, "project-downloads", "001-example-com-explicit.txt")
	if got, err := os.ReadFile(wantFile); err != nil || strings.TrimSpace(string(got)) != "Explicit body." {
		t.Fatalf("download file = %q, %v; want Explicit body.", string(got), err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if got := decoded["output_dir"]; got != "project-downloads" {
		t.Fatalf("output_dir = %v, want project-downloads", got)
	}
	manifest := decoded["manifest"].([]any)
	first := manifest[0].(map[string]any)
	if got := filepath.ToSlash(first["file_path"].(string)); got != "project-downloads/001-example-com-explicit.txt" {
		t.Fatalf("file_path = %q", got)
	}
}

func TestResolveWebDownloadOutputDirRejectsCoderReadOnlyRootBeforeCreation(t *testing.T) {
	workspaceDir := t.TempDir()
	readOnlyDir := t.TempDir()
	scope := normalizeWorkspaceScope(workspaceDir, nil)
	scope.ReadOnlyRoots = []string{readOnlyDir}
	scope.MutationScopes = []string{"."}

	requested := filepath.Join(readOnlyDir, "downloads")
	if _, _, err := resolveWebDownloadOutputDir(scope, requested); err == nil || !strings.Contains(err.Error(), "outside the Coder owned scope") {
		t.Fatalf("read-only output directory error = %v, want owned-scope rejection", err)
	}
	if _, err := os.Stat(requested); !os.IsNotExist(err) {
		t.Fatalf("read-only output directory was created: %v", err)
	}
}

func TestWriteWorkspaceFileRejectsLinksAndSymlinkedDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows hosts")
	}
	workspaceDir := t.TempDir()
	outsideDir := t.TempDir()
	scope := normalizeWorkspaceScope(workspaceDir, nil)

	t.Run("ordinary new output", func(t *testing.T) {
		path := filepath.Join(workspaceDir, "downloads", "new.txt")
		if err := writeWorkspaceFile(scope, path, []byte("downloaded"), 0o644); err != nil {
			t.Fatalf("writeWorkspaceFile: %v", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "downloaded" {
			t.Fatalf("content = %q, %v; want downloaded", got, err)
		}
	})

	t.Run("planted child symlink", func(t *testing.T) {
		outsidePath := filepath.Join(outsideDir, "symlink-target.txt")
		if err := os.WriteFile(outsidePath, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		insidePath := filepath.Join(workspaceDir, "downloads", "symlink.txt")
		if err := os.Symlink(outsidePath, insidePath); err != nil {
			t.Fatal(err)
		}
		if err := writeWorkspaceFile(scope, insidePath, []byte("changed"), 0o644); err == nil {
			t.Fatal("writeWorkspaceFile unexpectedly followed child symlink")
		}
		if got, err := os.ReadFile(outsidePath); err != nil || string(got) != "original" {
			t.Fatalf("outside content = %q, %v; want original", got, err)
		}
	})

	t.Run("hard-linked existing file", func(t *testing.T) {
		outsidePath := filepath.Join(outsideDir, "hardlink-target.txt")
		if err := os.WriteFile(outsidePath, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		insidePath := filepath.Join(workspaceDir, "downloads", "hardlink.txt")
		if err := os.Link(outsidePath, insidePath); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		err := writeWorkspaceFile(scope, insidePath, []byte("changed"), 0o644)
		if err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("error = %v, want hard-link rejection", err)
		}
		if got, readErr := os.ReadFile(outsidePath); readErr != nil || string(got) != "original" {
			t.Fatalf("outside content = %q, %v; want original", got, readErr)
		}
	})

	t.Run("symlinked output directory", func(t *testing.T) {
		insideDir := filepath.Join(workspaceDir, "linked-downloads")
		if err := os.Symlink(outsideDir, insideDir); err != nil {
			t.Fatal(err)
		}
		if err := writeWorkspaceFile(scope, filepath.Join(insideDir, "escaped.txt"), []byte("changed"), 0o644); err == nil {
			t.Fatal("writeWorkspaceFile unexpectedly followed output directory symlink")
		}
		if _, err := os.Stat(filepath.Join(outsideDir, "escaped.txt")); !os.IsNotExist(err) {
			t.Fatalf("outside file exists or stat failed unexpectedly: %v", err)
		}
	})
}

func infoModePerm(info fs.FileInfo) fs.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
