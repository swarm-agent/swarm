package tool

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func infoModePerm(info fs.FileInfo) fs.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
