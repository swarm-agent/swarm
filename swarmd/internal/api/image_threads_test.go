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
	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("STATE_DIRECTORY", dataRoot)

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "image-threads.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	workspacePath := t.TempDir()
	legacyPath := filepath.Join(workspacePath, ".swarm", "tools", "image", "sessions", "legacy")
	externalFolder := filepath.Join(t.TempDir(), "user-images")
	server := &Server{}
	server.SetImageThreadStore(pebblestore.NewImageThreadStore(db))

	payload := map[string]any{
		"title":          "Gallery",
		"workspace_path": workspacePath,
		"workspace_name": "Workspace",
		"image_folders":  []string{legacyPath, externalFolder},
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
	assertPrivateToolStoragePath(t, workspacePath, dataRoot, wantStoragePath, "image", thread.ID)
	if len(thread.ImageFolders) != 2 || thread.ImageFolders[0] != wantStoragePath || thread.ImageFolders[1] != externalFolder {
		t.Fatalf("image_folders = %q, want managed path then external folder", thread.ImageFolders)
	}
	if strings.Contains(strings.Join(thread.ImageFolders, "\n"), filepath.Join(".swarm", "tools")) {
		t.Fatalf("image_folders must not include legacy workspace tool storage: %q", thread.ImageFolders)
	}
	if got, ok := thread.Metadata["tool_storage_path"].(string); !ok || got != wantStoragePath {
		t.Fatalf("metadata.tool_storage_path = %#v, want %q", thread.Metadata["tool_storage_path"], wantStoragePath)
	}
	assertImageThreadUpdateKeepsManagedToolStorage(t, server, thread, wantStoragePath, legacyPath, externalFolder)
	if info, err := os.Stat(wantStoragePath); err != nil {
		t.Fatalf("stat storage path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("storage path is not a directory: %s", wantStoragePath)
	} else if mode := info.Mode().Perm(); mode != appstorage.PrivateDirPerm {
		t.Fatalf("storage path mode = %v, want %v", mode, appstorage.PrivateDirPerm)
	}
}

func TestHandleWorkspaceVideoThreadsCreatesPrivateWorkspaceToolStorage(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("STATE_DIRECTORY", dataRoot)

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-threads.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	workspacePath := t.TempDir()
	legacyPath := filepath.Join(workspacePath, ".swarm", "tools", "video", "sessions", "legacy")
	externalFolder := filepath.Join(t.TempDir(), "user-videos")
	server := &Server{}
	server.SetVideoThreadStore(pebblestore.NewVideoThreadStore(db))

	payload := map[string]any{
		"title":          "Storyboard",
		"workspace_path": workspacePath,
		"workspace_name": "Workspace",
		"video_folders":  []string{legacyPath, externalFolder},
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
	assertPrivateToolStoragePath(t, workspacePath, dataRoot, wantStoragePath, "video", thread.ID)
	if len(thread.VideoFolders) != 2 || thread.VideoFolders[0] != wantStoragePath || thread.VideoFolders[1] != externalFolder {
		t.Fatalf("video_folders = %q, want managed path then external folder", thread.VideoFolders)
	}
	if strings.Contains(strings.Join(thread.VideoFolders, "\n"), filepath.Join(".swarm", "tools")) {
		t.Fatalf("video_folders must not include legacy workspace tool storage: %q", thread.VideoFolders)
	}
	if got, ok := thread.Metadata["tool_storage_path"].(string); !ok || got != wantStoragePath {
		t.Fatalf("metadata.tool_storage_path = %#v, want %q", thread.Metadata["tool_storage_path"], wantStoragePath)
	}
	assertVideoThreadUpdateKeepsManagedToolStorage(t, server, thread, wantStoragePath, legacyPath, externalFolder)
	if info, err := os.Stat(wantStoragePath); err != nil {
		t.Fatalf("stat storage path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("storage path is not a directory: %s", wantStoragePath)
	} else if mode := info.Mode().Perm(); mode != appstorage.PrivateDirPerm {
		t.Fatalf("storage path mode = %v, want %v", mode, appstorage.PrivateDirPerm)
	}
}

func assertImageThreadUpdateKeepsManagedToolStorage(t *testing.T, server *Server, thread pebblestore.ImageThreadSnapshot, storagePath, legacyPath, externalFolder string) {
	t.Helper()
	payload := map[string]any{
		"image_folders": []string{legacyPath, externalFolder},
		"metadata": map[string]any{
			"tool_storage_path": legacyPath,
			"updated":           true,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/workspace/image/threads/"+thread.ID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleWorkspaceImageThread(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Thread pebblestore.ImageThreadSnapshot `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if len(response.Thread.ImageFolders) != 2 || response.Thread.ImageFolders[0] != storagePath || response.Thread.ImageFolders[1] != externalFolder {
		t.Fatalf("updated image_folders = %q, want managed path then external folder", response.Thread.ImageFolders)
	}
	if got, ok := response.Thread.Metadata["tool_storage_path"].(string); !ok || got != storagePath {
		t.Fatalf("updated metadata.tool_storage_path = %#v, want %q", response.Thread.Metadata["tool_storage_path"], storagePath)
	}
}

func assertVideoThreadUpdateKeepsManagedToolStorage(t *testing.T, server *Server, thread pebblestore.VideoThreadSnapshot, storagePath, legacyPath, externalFolder string) {
	t.Helper()
	payload := map[string]any{
		"video_folders": []string{legacyPath, externalFolder},
		"metadata": map[string]any{
			"tool_storage_path": legacyPath,
			"updated":           true,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/workspace/video/threads/"+thread.ID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleWorkspaceVideoThread(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Thread pebblestore.VideoThreadSnapshot `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if len(response.Thread.VideoFolders) != 2 || response.Thread.VideoFolders[0] != storagePath || response.Thread.VideoFolders[1] != externalFolder {
		t.Fatalf("updated video_folders = %q, want managed path then external folder", response.Thread.VideoFolders)
	}
	if got, ok := response.Thread.Metadata["tool_storage_path"].(string); !ok || got != storagePath {
		t.Fatalf("updated metadata.tool_storage_path = %#v, want %q", response.Thread.Metadata["tool_storage_path"], storagePath)
	}
}

func assertPrivateToolStoragePath(t *testing.T, workspacePath, dataRoot, storagePath, toolKind, threadID string) {
	t.Helper()
	bucket, err := appstorage.WorkspaceBucketName(workspacePath)
	if err != nil {
		t.Fatalf("WorkspaceBucketName: %v", err)
	}
	wantPrefix := filepath.Join(dataRoot, appstorage.WorkspacesDir, bucket)
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
