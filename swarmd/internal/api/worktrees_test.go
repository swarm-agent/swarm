package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type fakeWorktreeService struct {
	config  worktreeruntime.Config
	managed []worktreeruntime.ManagedWorktree
	prune   worktreeruntime.PruneResult
}

func (f *fakeWorktreeService) GetConfig(workspacePath string) (worktreeruntime.Config, error) {
	cfg := f.config
	cfg.WorkspacePath = workspacePath
	return cfg, nil
}

func (f *fakeWorktreeService) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	f.config = worktreeruntime.Config{WorkspacePath: workspacePath, Enabled: enabled, UseCurrentBranch: useCurrentBranch, BaseBranch: baseBranch, BranchName: branchName}
	return f.config, nil, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{}, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{}, nil
}

func (f *fakeWorktreeService) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	return "agent/test", nil
}

func (f *fakeWorktreeService) ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return f.managed, nil
}

func (f *fakeWorktreeService) PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error) {
	return f.prune, nil
}

func newTestWorkspaceService(t *testing.T, path string) (*workspaceruntime.Service, string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceSvc.Add(path, "repo", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return workspaceSvc, path
}

func TestHandleWorktreesIncludesManagedInventory(t *testing.T) {
	workspaceSvc, workspacePath := newTestWorkspaceService(t, t.TempDir())
	s := &Server{workspace: workspaceSvc, worktrees: &fakeWorktreeService{
		config:  worktreeruntime.Config{Enabled: true},
		managed: []worktreeruntime.ManagedWorktree{{Path: "/tmp/swarmd/worktrees/ws_abc123", WorkspaceID: "ws_abc123", Exists: true, Managed: true}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/worktrees?workspace_path="+workspacePath, nil)
	rr := httptest.NewRecorder()

	s.handleWorktrees(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		OK      bool                              `json:"ok"`
		Managed []worktreeruntime.ManagedWorktree `json:"managed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || len(got.Managed) != 1 || got.Managed[0].WorkspaceID != "ws_abc123" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleWorktreesDeletePrunesManagedOnly(t *testing.T) {
	fake := &fakeWorktreeService{prune: worktreeruntime.PruneResult{Root: "/tmp/swarmd/worktrees", Removed: []string{"/tmp/swarmd/worktrees/ws_missing"}}}
	workspaceSvc, workspacePath := newTestWorkspaceService(t, t.TempDir())
	s := &Server{workspace: workspaceSvc, worktrees: fake}
	req := httptest.NewRequest(http.MethodDelete, "/v1/worktrees?workspace_path="+workspacePath, bytes.NewReader(nil))
	rr := httptest.NewRecorder()

	s.handleWorktrees(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ws_missing") {
		t.Fatalf("delete response missing pruned path: %s", rr.Body.String())
	}
}
