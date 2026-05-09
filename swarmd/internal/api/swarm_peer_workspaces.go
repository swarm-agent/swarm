package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const (
	peerWorkspaceDiscoverPath     = "/v1/swarm/peer/workspaces/discover"
	peerWorkspaceImportBundlePath = "/v1/swarm/peer/workspaces/import-bundle"
	peerWorkspaceCreatePath       = "/v1/swarm/peer/workspaces/create"
	peerWorkspaceTransferPrefix   = "/v1/swarm/peer/workspaces/transfer/"
	peerWorkspaceDefaultRoot      = "workspaces"
)

type peerWorkspaceInfo struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	GitWorkspace  bool   `json:"git_workspace"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type peerWorkspaceDiscoverResponse struct {
	OK         bool                `json:"ok"`
	Workspaces []peerWorkspaceInfo `json:"workspaces"`
}

type peerWorkspaceCreateRequest struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

type peerWorkspaceCreateResponse struct {
	OK        bool              `json:"ok"`
	Workspace peerWorkspaceInfo `json:"workspace"`
}

type peerWorkspaceImportBundleResponse struct {
	OK            bool   `json:"ok"`
	WorkspacePath string `json:"workspace_path"`
	WorkspaceName string `json:"workspace_name"`
}

func (s *Server) handlePeerWorkspaceDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service is not configured"))
		return
	}
	entries, err := s.workspace.ListKnown(100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	workspaces := make([]peerWorkspaceInfo, 0, len(entries))
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			continue
		}
		gitWorkspace, _ := s.workspace.IsGitWorkspace(path)
		workspaces = append(workspaces, peerWorkspaceInfo{
			Path:          path,
			Name:          firstNonEmpty(strings.TrimSpace(entry.WorkspaceName), defaultReplicatedWorkspaceName(path)),
			GitWorkspace:  gitWorkspace,
			DefaultBranch: detectGitDefaultBranch(context.Background(), path),
		})
	}
	writeJSON(w, http.StatusOK, peerWorkspaceDiscoverResponse{OK: true, Workspaces: workspaces})
}

func (s *Server) handlePeerWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service is not configured"))
		return
	}
	var req peerWorkspaceCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := sanitizeReplicationMountName(req.Name)
	if name == "" {
		name = "workspace"
	}
	targetPath := strings.TrimSpace(req.Path)
	if targetPath == "" {
		root, err := peerWorkspaceImportRoot()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		targetPath = uniquePeerWorkspacePath(root, name)
	}
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.workspace.Add(targetPath, name, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	gitWorkspace, _ := s.workspace.IsGitWorkspace(targetPath)
	writeJSON(w, http.StatusOK, peerWorkspaceCreateResponse{OK: true, Workspace: peerWorkspaceInfo{Path: targetPath, Name: name, GitWorkspace: gitWorkspace}})
}

func (s *Server) handlePeerWorkspaceImportBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service is not configured"))
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := sanitizeReplicationMountName(r.FormValue("workspace_name"))
	if name == "" {
		name = "workspace"
	}
	mode := workspace.NormalizeReplicationMode(r.FormValue("replication_mode"))
	if mode != "" && mode != workspace.ReplicationModeBundle {
		writeError(w, http.StatusBadRequest, errors.New("managed workspace import currently supports git bundle mode only"))
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bundle file is required: %w", err))
		return
	}
	defer file.Close()

	root, err := peerWorkspaceImportRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bundlePath := filepath.Join(root, fmt.Sprintf(".%s-%d.bundle", name, time.Now().UnixNano()))
	if err := writeUploadedBundle(bundlePath, file); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(bundlePath)

	targetPath := uniquePeerWorkspacePath(root, name)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := cloneGitBundle(ctx, bundlePath, targetPath); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.workspace.Add(targetPath, name, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerWorkspaceImportBundleResponse{OK: true, WorkspacePath: targetPath, WorkspaceName: name})
}

func (s *Server) handlePeerWorkspaceTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": "not_found"})
}

func (s *Server) requirePeerAuth(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return false
	}
	peerID := strings.TrimSpace(r.Header.Get(peerAuthSwarmIDHeader))
	peerToken := strings.TrimSpace(r.Header.Get(peerAuthTokenHeader))
	if peerID == "" || peerToken == "" {
		writeError(w, http.StatusUnauthorized, errors.New("peer auth is required"))
		return false
	}
	ok, err := s.swarm.ValidateIncomingPeerAuth(peerID, peerToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("invalid peer auth"))
		return false
	}
	return true
}

func peerWorkspaceImportRoot() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, peerWorkspaceDefaultRoot), nil
}

func uniquePeerWorkspacePath(root, name string) string {
	name = sanitizeReplicationMountName(name)
	if name == "" {
		name = "workspace"
	}
	candidate := filepath.Join(root, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = filepath.Join(root, fmt.Sprintf("%s-%d", name, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func writeUploadedBundle(path string, file multipart.File) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	return err
}

func cloneGitBundle(ctx context.Context, bundlePath, targetPath string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", bundlePath, targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(targetPath)
		return fmt.Errorf("import git bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func detectGitDefaultBranch(ctx context.Context, workspacePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", workspacePath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(output))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

func linkIDForRemoteReplication(targetSwarmID, sourceWorkspacePath string) string {
	return "remote_" + sanitizeReplicationMountName(strings.TrimSpace(targetSwarmID)+"_"+strings.TrimSpace(sourceWorkspacePath))
}

func addWorkspaceReplicationResponse(response *swarmReplicateResponse, source workspace.NormalizedReplicationWorkspace, catalog replicateWorkspaceCatalogEntry, link pebblestore.WorkspaceReplicationLink) {
	if response == nil {
		return
	}
	response.Workspaces = append(response.Workspaces, swarmReplicateWorkspaceResponse{
		SourceWorkspacePath: source.SourceWorkspacePath,
		SourceWorkspaceName: firstNonEmpty(strings.TrimSpace(catalog.Name), defaultReplicatedWorkspaceName(source.SourceWorkspacePath)),
		Link:                link,
	})
}
