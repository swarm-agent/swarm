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

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHandleWorkspaceImageThreadsCreatesWorkspaceToolStorage(t *testing.T) {
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
	wantStoragePath := filepath.Join(workspacePath, ".swarm", "tools", "image", "sessions", thread.ID)
	if len(thread.ImageFolders) == 0 || thread.ImageFolders[0] != wantStoragePath {
		t.Fatalf("image_folders[0] = %q, want %q", thread.ImageFolders, wantStoragePath)
	}
	if got, ok := thread.Metadata["tool_storage_path"].(string); !ok || got != wantStoragePath {
		t.Fatalf("metadata.tool_storage_path = %#v, want %q", thread.Metadata["tool_storage_path"], wantStoragePath)
	}
	if !strings.Contains(wantStoragePath, string(filepath.Separator)+".swarm"+string(filepath.Separator)+"tools"+string(filepath.Separator)+"image") {
		t.Fatalf("storage path does not use workspace .swarm tools area: %s", wantStoragePath)
	}
	if info, err := os.Stat(wantStoragePath); err != nil {
		t.Fatalf("stat storage path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("storage path is not a directory: %s", wantStoragePath)
	}
}
