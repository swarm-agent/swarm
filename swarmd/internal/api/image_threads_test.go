package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHandleWorkspaceImageThreadsCreatesPrivateWorkspaceToolStorage(t *testing.T) {
	xdgDataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", xdgDataHome)

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "image-threads.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	workspacePath := t.TempDir()
	server := &Server{}
	server.SetImageThreadStore(pebblestore.NewImageThreadStore(db))

	payload := map[string]any{
		"title":          "Gallery",
		"workspace_path": workspacePath,
		"workspace_name": "Workspace",
		"metadata": map[string]any{
			"tool_kind":              "image",
			"session_schema_version": 1,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/workspace/image/threads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleWorkspaceImageThreads(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var response struct {
		Thread pebblestore.ImageThreadSnapshot `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	thread := response.Thread
	if thread.ID == "" {
		t.Fatal("thread id is empty")
	}
	wantStoragePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", thread.ID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	assertPrivateToolStoragePath(t, workspacePath, xdgDataHome, wantStoragePath, "image", thread.ID)
	if len(thread.ImageFolders) == 0 || thread.ImageFolders[0] != wantStoragePath {
		t.Fatalf("image_folders[0] = %q, want %q", thread.ImageFolders, wantStoragePath)
	}
	if got, ok := thread.Metadata["tool_storage_path"].(string); !ok || got != wantStoragePath {
		t.Fatalf("metadata.tool_storage_path = %#v, want %q", thread.Metadata["tool_storage_path"], wantStoragePath)
	}
	if info, err := os.Stat(wantStoragePath); err != nil {
		t.Fatalf("stat storage path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("storage path is not a directory: %s", wantStoragePath)
	} else if mode := info.Mode().Perm(); mode != appstorage.PrivateDirPerm {
		t.Fatalf("storage path mode = %v, want %v", mode, appstorage.PrivateDirPerm)
	}
}

func TestHandleWorkspaceVideoThreadsCreatesPrivateWorkspaceToolStorage(t *testing.T) {
	xdgDataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", xdgDataHome)

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-threads.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	workspacePath := t.TempDir()
	server := &Server{}
	server.SetVideoThreadStore(pebblestore.NewVideoThreadStore(db))

	payload := map[string]any{
		"title":          "Storyboard",
		"workspace_path": workspacePath,
		"workspace_name": "Workspace",
		"metadata": map[string]any{
			"tool_kind":              "video",
			"session_schema_version": 1,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/workspace/video/threads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleWorkspaceVideoThreads(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var response struct {
		Thread pebblestore.VideoThreadSnapshot `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	thread := response.Thread
	if thread.ID == "" {
		t.Fatal("thread id is empty")
	}
	wantStoragePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "video", "sessions", thread.ID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	assertPrivateToolStoragePath(t, workspacePath, xdgDataHome, wantStoragePath, "video", thread.ID)
	if len(thread.VideoFolders) == 0 || thread.VideoFolders[0] != wantStoragePath {
		t.Fatalf("video_folders[0] = %q, want %q", thread.VideoFolders, wantStoragePath)
	}
	if got, ok := thread.Metadata["tool_storage_path"].(string); !ok || got != wantStoragePath {
		t.Fatalf("metadata.tool_storage_path = %#v, want %q", thread.Metadata["tool_storage_path"], wantStoragePath)
	}
	if info, err := os.Stat(wantStoragePath); err != nil {
		t.Fatalf("stat storage path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("storage path is not a directory: %s", wantStoragePath)
	} else if mode := info.Mode().Perm(); mode != appstorage.PrivateDirPerm {
		t.Fatalf("storage path mode = %v, want %v", mode, appstorage.PrivateDirPerm)
	}
}

func assertPrivateToolStoragePath(t *testing.T, workspacePath, xdgDataHome, storagePath, toolKind, threadID string) {
	t.Helper()
	bucket, err := appstorage.WorkspaceBucketName(workspacePath)
	if err != nil {
		t.Fatalf("WorkspaceBucketName: %v", err)
	}
	wantPrefix := filepath.Join(xdgDataHome, appstorage.AppDirName, appstorage.WorkspacesDir, bucket)
	wantPath := filepath.Join(wantPrefix, "tools", toolKind, "sessions", threadID)
	if storagePath != wantPath {
		t.Fatalf("storage path = %q, want %q", storagePath, wantPath)
	}
	if !pathUnder(storagePath, wantPrefix) {
		t.Fatalf("storage path %q is not under private app bucket %q", storagePath, wantPrefix)
	}
	if pathUnder(storagePath, workspacePath) {
		t.Fatalf("storage path %q must not be inside workspace %q", storagePath, workspacePath)
	}
	if strings.Contains(storagePath, string(filepath.Separator)+".swarm"+string(filepath.Separator)) {
		t.Fatalf("storage path %q must not use workspace .swarm storage", storagePath)
	}
}

func pathUnder(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
