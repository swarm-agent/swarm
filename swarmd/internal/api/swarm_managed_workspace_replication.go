package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/workspace"
)

const (
	managedWorkspacePreflightPath        = "/v1/swarm/managed-workspaces/preflight"
	managedWorkspaceReplicatePath        = "/v1/swarm/managed-workspaces/replicate"
	managedWorkspaceInventoryPath        = "/v1/swarm/managed-workspaces/inventory"
	peerManagedWorkspacePreflightPath    = "/v1/swarm/peer/managed-workspaces/preflight"
	peerManagedWorkspaceEnsureLinkPath   = "/v1/swarm/peer/managed-workspaces/ensure-link"
	peerManagedWorkspaceLinkExistingPath = "/v1/swarm/peer/managed-workspaces/link-existing"
	peerManagedWorkspaceImportBundlePath = "/v1/swarm/peer/managed-workspaces/import-bundle"
	peerManagedWorkspaceInventoryPath    = "/v1/swarm/peer/managed-workspaces/inventory"

	managedWorkspaceActionImportBundle = "import_bundle"
	managedWorkspaceActionLinkExisting = "link_existing"
	managedWorkspaceActionConflict     = "conflict"
	managedWorkspaceTargetKind         = "managed_host"
)

type managedWorkspaceSelectionRequest struct {
	SourceWorkspacePath string `json:"source_workspace_path"`
	DestinationPath     string `json:"destination_path,omitempty"`
}

type managedWorkspacePreflightRequest struct {
	TargetSwarmID   string                             `json:"target_swarm_id"`
	DestinationRoot string                             `json:"destination_root"`
	Workspaces      []managedWorkspaceSelectionRequest `json:"workspaces"`
}

type managedWorkspaceConfirmedPlan struct {
	SourceWorkspacePath string `json:"source_workspace_path"`
	DestinationPath     string `json:"destination_path"`
	Action              string `json:"action"`
	PlanID              string `json:"plan_id,omitempty"`
}

type managedWorkspaceReplicateRequest struct {
	TargetSwarmID   string                             `json:"target_swarm_id"`
	DestinationRoot string                             `json:"destination_root"`
	Workspaces      []managedWorkspaceSelectionRequest `json:"workspaces"`
	ConfirmedPlans  []managedWorkspaceConfirmedPlan    `json:"confirmed_plans"`
}

type managedWorkspaceTargetResponse struct {
	SwarmID string `json:"swarm_id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
}

type managedWorkspacePlanResponse struct {
	PlanID              string `json:"plan_id"`
	SourceWorkspacePath string `json:"source_workspace_path"`
	SourceWorkspaceName string `json:"source_workspace_name"`
	DestinationRoot     string `json:"destination_root"`
	DestinationPath     string `json:"destination_path"`
	Action              string `json:"action"`
	GitWorkspace        bool   `json:"git_workspace"`
	OK                  bool   `json:"ok"`
	Error               string `json:"error,omitempty"`
}

type managedWorkspacePreflightResponse struct {
	OK              bool                           `json:"ok"`
	Ready           bool                           `json:"ready"`
	Target          managedWorkspaceTargetResponse `json:"target"`
	DestinationRoot string                         `json:"destination_root"`
	Workspaces      []managedWorkspacePlanResponse `json:"workspaces"`
}

type managedWorkspaceResultResponse struct {
	SourceWorkspacePath string                                     `json:"source_workspace_path"`
	SourceWorkspaceName string                                     `json:"source_workspace_name"`
	ManagedHostName     string                                     `json:"managed_host_name"`
	DestinationPath     string                                     `json:"destination_path"`
	Action              string                                     `json:"action"`
	Binding             pebblestore.TopologyWorkspaceBindingRecord `json:"binding"`
}

type managedWorkspaceReplicateResponse struct {
	OK         bool                             `json:"ok"`
	Target     managedWorkspaceTargetResponse   `json:"target"`
	Workspaces []managedWorkspaceResultResponse `json:"workspaces"`
}

type workspaceManagedLinkUpsertRequest struct {
	WorkspacePath   string `json:"workspace_path"`
	TargetSwarmID   string `json:"target_swarm_id"`
	DestinationRoot string `json:"destination_root,omitempty"`
	DestinationPath string `json:"destination_path,omitempty"`
	WorkspaceName   string `json:"workspace_name,omitempty"`
	Provision       *bool  `json:"provision,omitempty"`
}

type workspaceManagedLinkRemoveRequest struct {
	WorkspacePath string `json:"workspace_path"`
	BindingID     string `json:"binding_id"`
}

type workspaceManagedLinkResponse struct {
	OK              bool                                       `json:"ok"`
	Target          managedWorkspaceTargetResponse             `json:"target,omitempty"`
	WorkspacePath   string                                     `json:"workspace_path"`
	DestinationPath string                                     `json:"destination_path,omitempty"`
	Exists          bool                                       `json:"exists,omitempty"`
	Created         bool                                       `json:"created,omitempty"`
	Registered      bool                                       `json:"registered,omitempty"`
	Binding         pebblestore.TopologyWorkspaceBindingRecord `json:"binding,omitempty"`
}

type managedWorkspaceActiveCWDResponse struct {
	Path          string `json:"path"`
	WorkspacePath string `json:"workspace_path"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	SessionTitle  string `json:"session_title,omitempty"`
	Active        bool   `json:"active"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
}

type peerManagedWorkspaceInventoryResponse struct {
	OK                    bool                                `json:"ok"`
	ManagedHome           string                              `json:"managed_home"`
	SavedWorkspaces       []workspace.Entry                   `json:"saved_workspaces"`
	DiscoveredDirectories []workspace.DiscoverEntry           `json:"discovered_directories"`
	ActiveCWDs            []managedWorkspaceActiveCWDResponse `json:"active_cwds"`
}

type managedWorkspaceInventoryResponse struct {
	OK                    bool                                `json:"ok"`
	Target                managedWorkspaceTargetResponse      `json:"target"`
	ManagedHome           string                              `json:"managed_home"`
	SavedWorkspaces       []workspace.Entry                   `json:"saved_workspaces"`
	DiscoveredDirectories []workspace.DiscoverEntry           `json:"discovered_directories"`
	ActiveCWDs            []managedWorkspaceActiveCWDResponse `json:"active_cwds"`
}

type peerManagedWorkspacePreflightRequest struct {
	DestinationRoot string                         `json:"destination_root"`
	Workspaces      []peerManagedWorkspacePlanItem `json:"workspaces"`
}

type peerManagedWorkspacePlanItem struct {
	SourceWorkspacePath    string `json:"source_workspace_path"`
	SourceHomeRelativePath string `json:"source_home_relative_path,omitempty"`
	WorkspaceName          string `json:"workspace_name"`
	DestinationPath        string `json:"destination_path,omitempty"`
	GitWorkspace           bool   `json:"git_workspace"`
}

type peerManagedWorkspacePreflightResponse struct {
	OK              bool                           `json:"ok"`
	Ready           bool                           `json:"ready"`
	DestinationRoot string                         `json:"destination_root"`
	Workspaces      []managedWorkspacePlanResponse `json:"workspaces"`
}

type peerManagedWorkspaceLinkExistingRequest struct {
	DestinationRoot        string `json:"destination_root"`
	DestinationPath        string `json:"destination_path"`
	WorkspaceName          string `json:"workspace_name"`
	SourceWorkspacePath    string `json:"source_workspace_path"`
	SourceWorkspaceName    string `json:"source_workspace_name,omitempty"`
	SourceHomeRelativePath string `json:"source_home_relative_path,omitempty"`
}

type peerManagedWorkspaceEnsureLinkRequest struct {
	DestinationRoot        string `json:"destination_root"`
	DestinationPath        string `json:"destination_path,omitempty"`
	WorkspaceName          string `json:"workspace_name"`
	SourceWorkspacePath    string `json:"source_workspace_path"`
	SourceHomeRelativePath string `json:"source_home_relative_path,omitempty"`
	Provision              bool   `json:"provision"`
}

type peerManagedWorkspaceEnsureLinkResponse struct {
	OK              bool   `json:"ok"`
	DestinationPath string `json:"destination_path"`
	WorkspaceName   string `json:"workspace_name"`
	Exists          bool   `json:"exists"`
	Created         bool   `json:"created"`
	Registered      bool   `json:"registered"`
}

type peerManagedWorkspaceLinkExistingResponse struct {
	OK              bool   `json:"ok"`
	DestinationPath string `json:"destination_path"`
	WorkspaceName   string `json:"workspace_name"`
}

type peerManagedWorkspaceImportBundleResponse struct {
	OK              bool   `json:"ok"`
	DestinationPath string `json:"destination_path"`
	WorkspaceName   string `json:"workspace_name"`
}

func (s *Server) handleManagedWorkspacePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req managedWorkspacePreflightRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.managedWorkspacePreflight(r, req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleManagedWorkspaceReplicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req managedWorkspaceReplicateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.managedWorkspaceReplicate(r, req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleManagedWorkspaceInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	targetSwarmID := strings.TrimSpace(r.URL.Query().Get("target_swarm_id"))
	if targetSwarmID == "" {
		selectedTargetSwarmID, status, err := s.selectedManagedWorkspaceInventoryTargetSwarmID(r)
		if err != nil {
			writeError(w, status, err)
			return
		}
		targetSwarmID = selectedTargetSwarmID
	}
	response, status, err := s.managedWorkspaceInventory(r, targetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) selectedManagedWorkspaceInventoryTargetSwarmID(r *http.Request) (string, int, error) {
	remoteTarget, err := s.currentRemoteSwarmTargetForRequest(r)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	if remoteTarget != nil && strings.TrimSpace(remoteTarget.SwarmID) != "" {
		return strings.TrimSpace(remoteTarget.SwarmID), http.StatusOK, nil
	}
	targets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) && !strings.EqualFold(strings.TrimSpace(target.Kind), "manager") && strings.TrimSpace(target.SwarmID) != "" {
			return strings.TrimSpace(target.SwarmID), http.StatusOK, nil
		}
	}
	return "", http.StatusBadRequest, errors.New("target_swarm_id is required")
}

func (s *Server) handlePeerManagedWorkspacePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	var req peerManagedWorkspacePreflightRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.peerManagedWorkspacePreflight(req, r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePeerManagedWorkspaceInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	response, status, err := s.peerManagedWorkspaceInventory(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleWorkspaceManagedLinkUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req workspaceManagedLinkUpsertRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.workspaceManagedLinkUpsert(r, req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleWorkspaceManagedLinkRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req workspaceManagedLinkRemoveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	bindingID := strings.TrimSpace(req.BindingID)
	if workspacePath == "" || bindingID == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_path and binding_id are required"))
		return
	}
	binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, bindingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok || strings.TrimSpace(binding.SourceWorkspacePath) != workspacePath {
		writeError(w, http.StatusNotFound, errors.New("topology workspace binding not found"))
		return
	}
	if err := s.topology.DeleteWorkspaceBindingForAccount(principal.AccountScopeID, bindingID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceManagedLinkResponse{OK: true, WorkspacePath: workspacePath, Binding: binding})
}

func (s *Server) handlePeerManagedWorkspaceEnsureLink(w http.ResponseWriter, r *http.Request) {
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
	var req peerManagedWorkspaceEnsureLinkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.peerManagedWorkspaceEnsureLink(r, req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePeerManagedWorkspaceLinkExisting(w http.ResponseWriter, r *http.Request) {
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
	var req peerManagedWorkspaceLinkExistingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspaceName := sanitizeReplicationMountName(firstNonEmpty(req.WorkspaceName, req.SourceWorkspaceName, defaultReplicatedWorkspaceName(req.SourceWorkspacePath)))
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	preflight, status, err := s.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: req.DestinationRoot,
		Workspaces: []peerManagedWorkspacePlanItem{{
			SourceWorkspacePath:    strings.TrimSpace(req.SourceWorkspacePath),
			SourceHomeRelativePath: cleanManagedWorkspaceRelativePath(req.SourceHomeRelativePath),
			WorkspaceName:          workspaceName,
			DestinationPath:        strings.TrimSpace(req.DestinationPath),
			GitWorkspace:           true,
		}},
	}, r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if len(preflight.Workspaces) != 1 || !preflight.Workspaces[0].OK || preflight.Workspaces[0].Action != managedWorkspaceActionLinkExisting || filepath.Clean(preflight.Workspaces[0].DestinationPath) != filepath.Clean(req.DestinationPath) {
		writeError(w, http.StatusConflict, errors.New("destination no longer matches a linkable existing managed workspace"))
		return
	}
	principal, ok := s.peerManagedWorkspacePrincipalForRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusForbidden, errors.New("managed workspace peer request requires persisted pairing account binding"))
		return
	}
	if _, err := s.workspace.AddForPrincipal(principal, preflight.Workspaces[0].DestinationPath, workspaceName, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerManagedWorkspaceLinkExistingResponse{OK: true, DestinationPath: preflight.Workspaces[0].DestinationPath, WorkspaceName: workspaceName})
}

func (s *Server) handlePeerManagedWorkspaceImportBundle(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspaceName := sanitizeReplicationMountName(r.FormValue("workspace_name"))
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	destinationRoot := strings.TrimSpace(r.FormValue("destination_root"))
	destinationPath := strings.TrimSpace(r.FormValue("destination_path"))
	if destinationPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("destination_path is required"))
		return
	}
	preflight, status, err := s.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: destinationRoot,
		Workspaces: []peerManagedWorkspacePlanItem{{
			SourceWorkspacePath:    strings.TrimSpace(r.FormValue("source_workspace_path")),
			SourceHomeRelativePath: cleanManagedWorkspaceRelativePath(r.FormValue("source_home_relative_path")),
			WorkspaceName:          workspaceName,
			DestinationPath:        destinationPath,
			GitWorkspace:           true,
		}},
	}, r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if len(preflight.Workspaces) != 1 || !preflight.Workspaces[0].OK || preflight.Workspaces[0].Action != managedWorkspaceActionImportBundle || filepath.Clean(preflight.Workspaces[0].DestinationPath) != filepath.Clean(destinationPath) {
		writeError(w, http.StatusConflict, errors.New("destination no longer matches an importable preflight plan"))
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bundle file is required: %w", err))
		return
	}
	defer file.Close()

	bundlePath, err := writeManagedWorkspaceUploadedBundle(preflight.Workspaces[0].DestinationRoot, workspaceName, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(bundlePath)

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := managedWorkspaceCloneGitBundle(ctx, bundlePath, destinationPath); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	principal, ok := s.peerManagedWorkspacePrincipalForRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusForbidden, errors.New("managed workspace peer request requires persisted pairing account binding"))
		return
	}
	if _, err := s.workspace.AddForPrincipal(principal, destinationPath, workspaceName, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerManagedWorkspaceImportBundleResponse{OK: true, DestinationPath: destinationPath, WorkspaceName: workspaceName})
}

func (s *Server) managedWorkspacePreflight(r *http.Request, req managedWorkspacePreflightRequest) (managedWorkspacePreflightResponse, int, error) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return managedWorkspacePreflightResponse{}, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	target, localSwarmID, peerToken, status, err := s.resolveManagedWorkspaceTarget(r, req.TargetSwarmID)
	if err != nil {
		return managedWorkspacePreflightResponse{}, status, err
	}
	normalized, catalog, status, err := s.normalizeManagedWorkspaceSelectionsForPrincipal(principal, req.Workspaces)
	if err != nil {
		return managedWorkspacePreflightResponse{}, status, err
	}
	peerReq := s.buildPeerManagedWorkspacePreflightRequest(req.DestinationRoot, normalized, catalog, req.Workspaces)
	peerResp, err := postPeerManagedWorkspacePreflight(r.Context(), *target, localSwarmID, peerToken, peerReq)
	if err != nil {
		return managedWorkspacePreflightResponse{}, http.StatusBadGateway, err
	}
	return managedWorkspacePreflightResponse{
		OK:              peerResp.OK,
		Ready:           peerResp.Ready,
		Target:          managedWorkspaceTargetResponse{SwarmID: target.SwarmID, Name: firstNonEmpty(target.Name, target.SwarmID), Online: true},
		DestinationRoot: peerResp.DestinationRoot,
		Workspaces:      peerResp.Workspaces,
	}, http.StatusOK, nil
}

func (s *Server) managedWorkspaceInventory(r *http.Request, targetSwarmID string) (managedWorkspaceInventoryResponse, int, error) {
	target, localSwarmID, peerToken, status, err := s.resolveManagedWorkspaceTarget(r, targetSwarmID)
	if err != nil {
		return managedWorkspaceInventoryResponse{}, status, err
	}
	peerResp, err := getPeerManagedWorkspaceInventory(r.Context(), *target, localSwarmID, peerToken)
	if err != nil {
		return managedWorkspaceInventoryResponse{}, http.StatusBadGateway, err
	}
	return managedWorkspaceInventoryResponse{
		OK:                    peerResp.OK,
		Target:                managedWorkspaceTargetResponse{SwarmID: target.SwarmID, Name: firstNonEmpty(target.Name, target.SwarmID), Online: true},
		ManagedHome:           peerResp.ManagedHome,
		SavedWorkspaces:       peerResp.SavedWorkspaces,
		DiscoveredDirectories: peerResp.DiscoveredDirectories,
		ActiveCWDs:            peerResp.ActiveCWDs,
	}, http.StatusOK, nil
}

func (s *Server) workspaceManagedLinkUpsert(r *http.Request, req workspaceManagedLinkUpsertRequest) (workspaceManagedLinkResponse, int, error) {
	if s == nil || s.workspace == nil {
		return workspaceManagedLinkResponse{}, http.StatusInternalServerError, errors.New("workspace service is not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return workspaceManagedLinkResponse{}, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	if workspacePath == "" {
		return workspaceManagedLinkResponse{}, http.StatusBadRequest, errors.New("workspace_path is required")
	}
	normalized, catalog, status, err := s.normalizeManagedWorkspaceSelectionsForPrincipal(principal, []managedWorkspaceSelectionRequest{{SourceWorkspacePath: workspacePath, DestinationPath: strings.TrimSpace(req.DestinationPath)}})
	if err != nil {
		return workspaceManagedLinkResponse{}, status, err
	}
	if len(normalized) != 1 {
		return workspaceManagedLinkResponse{}, http.StatusBadRequest, errors.New("exactly one workspace is required")
	}
	normalizedWorkspace := normalized[0]
	workspaceName := sanitizeReplicationMountName(firstNonEmpty(strings.TrimSpace(req.WorkspaceName), strings.TrimSpace(catalog[normalizedWorkspace.SourceWorkspacePath].Name), defaultReplicatedWorkspaceName(normalizedWorkspace.SourceWorkspacePath)))
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	target, localSwarmID, peerToken, status, err := s.resolveManagedWorkspaceTarget(r, req.TargetSwarmID)
	if err != nil {
		return workspaceManagedLinkResponse{}, status, err
	}
	provision := true
	if req.Provision != nil {
		provision = *req.Provision
	}
	peerResp, err := ensureManagedWorkspaceLinkOnPeer(r.Context(), *target, localSwarmID, peerToken, peerManagedWorkspaceEnsureLinkRequest{
		DestinationRoot:        strings.TrimSpace(req.DestinationRoot),
		DestinationPath:        strings.TrimSpace(req.DestinationPath),
		WorkspaceName:          workspaceName,
		SourceWorkspacePath:    normalizedWorkspace.SourceWorkspacePath,
		SourceHomeRelativePath: sourceHomeRelativePath(normalizedWorkspace.SourceWorkspacePath),
		Provision:              provision,
	})
	if err != nil {
		return workspaceManagedLinkResponse{}, http.StatusBadGateway, err
	}
	binding, err := s.upsertManagedWorkspaceBindingForPrincipal(principal, *target, normalizedWorkspace.SourceWorkspacePath, workspaceName, peerResp.DestinationPath)
	if err != nil {
		return workspaceManagedLinkResponse{}, http.StatusInternalServerError, err
	}
	return workspaceManagedLinkResponse{
		OK:              true,
		Target:          managedWorkspaceTargetResponse{SwarmID: target.SwarmID, Name: firstNonEmpty(target.Name, target.SwarmID), Online: true},
		WorkspacePath:   normalizedWorkspace.SourceWorkspacePath,
		DestinationPath: peerResp.DestinationPath,
		Exists:          peerResp.Exists,
		Created:         peerResp.Created,
		Registered:      peerResp.Registered,
		Binding:         binding,
	}, http.StatusOK, nil
}

func getPeerManagedWorkspaceInventory(ctx context.Context, target swarmTarget, localSwarmID, peerToken string) (peerManagedWorkspaceInventoryResponse, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedWorkspaceInventoryPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return peerManagedWorkspaceInventoryResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return peerManagedWorkspaceInventoryResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return peerManagedWorkspaceInventoryResponse{}, fmt.Errorf("managed host workspace inventory failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded peerManagedWorkspaceInventoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return peerManagedWorkspaceInventoryResponse{}, err
	}
	return decoded, nil
}

func (s *Server) peerManagedWorkspaceInventory(r *http.Request) (peerManagedWorkspaceInventoryResponse, int, error) {
	if s == nil || s.workspace == nil {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusInternalServerError, errors.New("workspace service is not configured")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusInternalServerError, fmt.Errorf("resolve managed host home: %w", err)
	}
	principal, ok := s.peerManagedWorkspacePrincipalForRequest(r)
	if !ok || !principal.Valid() {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusForbidden, errors.New("managed workspace peer request requires persisted pairing account binding")
	}
	saved, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusInternalServerError, err
	}
	discovered, err := s.workspace.Discover(managedWorkspaceInventoryDiscoverRoots(r), managedWorkspaceInventoryLimit(r, 200))
	if err != nil {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusInternalServerError, err
	}
	activeCWDs, err := s.managedWorkspaceActiveCWDsForPrincipal(principal, managedWorkspaceInventoryLimit(r, 200))
	if err != nil {
		return peerManagedWorkspaceInventoryResponse{}, http.StatusInternalServerError, err
	}
	return peerManagedWorkspaceInventoryResponse{OK: true, ManagedHome: filepath.Clean(home), SavedWorkspaces: saved, DiscoveredDirectories: discovered, ActiveCWDs: activeCWDs}, http.StatusOK, nil
}

func peerManagedWorkspacePrincipal() identity.Principal {
	return identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             "peer-managed-workspace",
		AccountScopeID:     "peer-managed-workspace",
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
}

func (s *Server) peerManagedWorkspacePrincipalForRequest(r *http.Request) (identity.Principal, bool) {
	if r != nil {
		if principal, ok := s.trustedPairingPrincipalForPeerRequest(r); ok && principal.Valid() {
			return principal, true
		}
		if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
			return principal, true
		}
	}
	if s != nil && s.swarmStore != nil {
		if pairing, ok, err := s.swarmStore.GetLocalPairing(); err == nil && ok {
			principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: strings.TrimSpace(pairing.UserID), AccountScopeID: strings.TrimSpace(pairing.AccountScopeID), AccountScopeSource: identity.AccountScopeSourceServerState}
			if principal.Valid() {
				return principal, true
			}
		}
	}
	return peerManagedWorkspacePrincipal(), true
}

func (s *Server) peerManagedWorkspaceEnsureLink(r *http.Request, req peerManagedWorkspaceEnsureLinkRequest) (peerManagedWorkspaceEnsureLinkResponse, int, error) {
	principal, ok := s.peerManagedWorkspacePrincipalForRequest(r)
	if !ok || !principal.Valid() {
		return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusForbidden, errors.New("managed workspace peer request requires persisted pairing account binding")
	}
	workspaceName := sanitizeReplicationMountName(firstNonEmpty(strings.TrimSpace(req.WorkspaceName), defaultReplicatedWorkspaceName(req.SourceWorkspacePath)))
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	root, err := normalizeManagedWorkspaceDestinationRoot(req.DestinationRoot)
	if err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusBadRequest, err
	}
	destination, err := managedWorkspaceDestinationPath(root, workspaceName, cleanManagedWorkspaceRelativePath(req.SourceHomeRelativePath), strings.TrimSpace(req.DestinationPath))
	if err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusBadRequest, err
	}
	created := false
	registered := false
	info, statErr := os.Stat(destination)
	if statErr == nil {
		if !info.IsDir() {
			return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusConflict, errors.New("destination exists and is not a directory")
		}
	} else if os.IsNotExist(statErr) {
		if !req.Provision {
			return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusNotFound, errors.New("destination does not exist")
		}
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusInternalServerError, err
		}
		created = true
	} else {
		return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusInternalServerError, statErr
	}
	if _, err := s.workspace.AddForPrincipal(principal, destination, workspaceName, "", false); err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusInternalServerError, err
	}
	registered = true
	if s.topology != nil {
		localSwarmID := ""
		if cfg, err := s.loadStartupConfig(); err == nil {
			if state, stateErr := s.currentSwarmState(cfg); stateErr == nil {
				localSwarmID = strings.TrimSpace(state.Node.SwarmID)
			}
		}
		if localSwarmID == "" {
			return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusInternalServerError, errors.New("managed host local swarm id is unavailable")
		}
		if _, err := s.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
			BindingID:                 pebblestore.CanonicalTopologyWorkspaceBindingID(localSwarmID, strings.TrimSpace(req.SourceWorkspacePath)),
			UserID:                    principal.UserID,
			AccountScopeID:            principal.AccountScopeID,
			SourceWorkspacePath:       strings.TrimSpace(req.SourceWorkspacePath),
			SourceWorkspaceName:       workspaceName,
			DestinationRuntimeSwarmID: localSwarmID,
			DestinationHostSwarmID:    localSwarmID,
			DestinationWorkspacePath:  destination,
			ReplicationMode:           workspace.ReplicationModeBundle,
			Writable:                  true,
			LegacyTargetKind:          managedWorkspaceTargetKind,
		}); err != nil {
			return peerManagedWorkspaceEnsureLinkResponse{}, http.StatusInternalServerError, err
		}
	}
	return peerManagedWorkspaceEnsureLinkResponse{OK: true, DestinationPath: destination, WorkspaceName: workspaceName, Exists: true, Created: created, Registered: registered}, http.StatusOK, nil
}

func (s *Server) managedWorkspaceActiveCWDsForPrincipal(principal identity.Principal, limit int) ([]managedWorkspaceActiveCWDResponse, error) {
	if s == nil || s.sessions == nil {
		return nil, nil
	}
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, identity.ErrPrincipalRequired
	}
	sessions, err := s.sessions.ListSessionsForAccount(principal.AccountScopeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]managedWorkspaceActiveCWDResponse, 0, len(sessions))
	seen := map[string]struct{}{}
	for _, session := range sessions {
		path := filepath.Clean(strings.TrimSpace(session.WorkspacePath))
		if path == "" || path == "." {
			continue
		}
		key := path + "\x00" + strings.TrimSpace(session.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		active := false
		if session.Lifecycle != nil {
			active = session.Lifecycle.Active
		}
		out = append(out, managedWorkspaceActiveCWDResponse{Path: path, WorkspacePath: path, WorkspaceName: strings.TrimSpace(session.WorkspaceName), SessionID: strings.TrimSpace(session.ID), SessionTitle: strings.TrimSpace(session.Title), Active: active, UpdatedAt: session.UpdatedAt})
	}
	return out, nil
}

func managedWorkspaceInventoryDiscoverRoots(r *http.Request) []string {
	if r == nil {
		return nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("roots"))
	if raw == "" {
		return nil
	}
	roots := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			roots = append(roots, part)
		}
	}
	return roots
}

func managedWorkspaceInventoryLimit(r *http.Request, fallback int) int {
	if r == nil {
		return fallback
	}
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed > 1000 {
		return 1000
	}
	return parsed
}

func (s *Server) managedWorkspaceReplicate(r *http.Request, req managedWorkspaceReplicateRequest) (managedWorkspaceReplicateResponse, int, error) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return managedWorkspaceReplicateResponse{}, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	preflight, status, err := s.managedWorkspacePreflight(r, managedWorkspacePreflightRequest{TargetSwarmID: req.TargetSwarmID, DestinationRoot: req.DestinationRoot, Workspaces: req.Workspaces})
	if err != nil {
		return managedWorkspaceReplicateResponse{}, status, err
	}
	if !preflight.Ready {
		return managedWorkspaceReplicateResponse{}, http.StatusConflict, errors.New("managed workspace preflight is not ready")
	}
	if len(req.ConfirmedPlans) > 0 {
		confirmed := make(map[string]managedWorkspaceConfirmedPlan, len(req.ConfirmedPlans))
		for _, plan := range req.ConfirmedPlans {
			key := filepath.Clean(strings.TrimSpace(plan.SourceWorkspacePath))
			if key == "." || key == "" {
				return managedWorkspaceReplicateResponse{}, http.StatusBadRequest, errors.New("confirmed source_workspace_path is required")
			}
			confirmed[key] = plan
		}
		for _, plan := range preflight.Workspaces {
			confirmedPlan, ok := confirmed[filepath.Clean(plan.SourceWorkspacePath)]
			if !ok {
				return managedWorkspaceReplicateResponse{}, http.StatusBadRequest, fmt.Errorf("missing confirmation for workspace %q", plan.SourceWorkspacePath)
			}
			if filepath.Clean(confirmedPlan.DestinationPath) != filepath.Clean(plan.DestinationPath) || strings.TrimSpace(confirmedPlan.Action) != plan.Action {
				return managedWorkspaceReplicateResponse{}, http.StatusConflict, fmt.Errorf("confirmed plan for workspace %q changed after preflight", plan.SourceWorkspacePath)
			}
		}
	}

	target, localSwarmID, peerToken, status, err := s.resolveManagedWorkspaceTarget(r, req.TargetSwarmID)
	if err != nil {
		return managedWorkspaceReplicateResponse{}, status, err
	}
	if s.workspace == nil {
		return managedWorkspaceReplicateResponse{}, http.StatusInternalServerError, errors.New("workspace service is not configured")
	}
	results := make([]managedWorkspaceResultResponse, 0, len(preflight.Workspaces))
	for _, plan := range preflight.Workspaces {
		switch plan.Action {
		case managedWorkspaceActionImportBundle:
			if err := importManagedWorkspaceBundleToPeer(r.Context(), *target, localSwarmID, peerToken, req.DestinationRoot, plan); err != nil {
				return managedWorkspaceReplicateResponse{}, http.StatusBadGateway, err
			}
		case managedWorkspaceActionLinkExisting:
			if err := linkExistingManagedWorkspaceOnPeer(r.Context(), *target, localSwarmID, peerToken, req.DestinationRoot, plan); err != nil {
				return managedWorkspaceReplicateResponse{}, http.StatusBadGateway, err
			}
		}
		binding, err := s.upsertManagedWorkspaceBindingForPrincipal(principal, *target, plan.SourceWorkspacePath, plan.SourceWorkspaceName, plan.DestinationPath)
		if err != nil {
			return managedWorkspaceReplicateResponse{}, http.StatusInternalServerError, err
		}
		results = append(results, managedWorkspaceResultResponse{
			SourceWorkspacePath: plan.SourceWorkspacePath,
			SourceWorkspaceName: plan.SourceWorkspaceName,
			ManagedHostName:     firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(target.SwarmID)),
			DestinationPath:     plan.DestinationPath,
			Action:              plan.Action,
			Binding:             binding,
		})
	}
	return managedWorkspaceReplicateResponse{OK: true, Target: managedWorkspaceTargetResponse{SwarmID: target.SwarmID, Name: firstNonEmpty(target.Name, target.SwarmID), Online: true}, Workspaces: results}, http.StatusOK, nil
}

func (s *Server) resolveManagedWorkspaceTarget(r *http.Request, targetSwarmID string) (*swarmTarget, string, string, int, error) {
	if s == nil || s.swarm == nil {
		return nil, "", "", http.StatusInternalServerError, errors.New("swarm service is not configured")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	targets, _, err := s.swarmTargetsForRequestWithOptions(requestWithSwarmTargetQuery(r, targetSwarmID), true)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	var target *swarmTarget
	for i := range targets {
		if strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			targetCopy := targets[i]
			target = &targetCopy
			break
		}
	}
	if target == nil {
		return nil, "", "", http.StatusBadRequest, fmt.Errorf("managed host target %q was not found", targetSwarmID)
	}
	if strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManager) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		return nil, "", "", http.StatusBadRequest, errors.New("target must be a managed host")
	}
	if strings.TrimSpace(target.BackendURL) == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host route is missing")
	}
	ctx, cancel := context.WithTimeout(r.Context(), swarmTargetHealthTimeout)
	defer cancel()
	if probeSwarmTargetBackend(ctx, target.BackendURL) {
		target.Online = true
		target.Selectable = true
	} else {
		return nil, "", "", http.StatusBadGateway, errors.New("managed host route is not reachable")
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, *target)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		return nil, "", "", http.StatusInternalServerError, errors.New("local swarm id is not configured")
	}
	return target, localSwarmID, peerToken, http.StatusOK, nil
}

func requestWithSwarmTargetQuery(r *http.Request, targetSwarmID string) *http.Request {
	if r == nil {
		req := httptestRequestWithTarget(targetSwarmID)
		return req
	}
	clone := r.Clone(r.Context())
	values := clone.URL.Query()
	values.Set("swarm_id", strings.TrimSpace(targetSwarmID))
	clone.URL.RawQuery = values.Encode()
	return clone
}

func httptestRequestWithTarget(targetSwarmID string) *http.Request {
	reqURL := &url.URL{Path: managedWorkspacePreflightPath}
	values := url.Values{}
	values.Set("swarm_id", strings.TrimSpace(targetSwarmID))
	reqURL.RawQuery = values.Encode()
	return &http.Request{Method: http.MethodPost, URL: reqURL}
}

func (s *Server) normalizeManagedWorkspaceSelectionsForPrincipal(principal identity.Principal, inputs []managedWorkspaceSelectionRequest) ([]workspace.NormalizedReplicationWorkspace, map[string]replicateWorkspaceCatalogEntry, int, error) {
	if s == nil || s.workspace == nil {
		return nil, nil, http.StatusInternalServerError, errors.New("workspace service is not configured")
	}
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, nil, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	if len(inputs) == 0 {
		return nil, nil, http.StatusBadRequest, errors.New("at least one workspace is required")
	}
	workspaceInputs := make([]workspace.ReplicationWorkspaceInput, 0, len(inputs))
	for _, input := range inputs {
		owned, err := s.resolveAccountOwnedPath(principal, input.SourceWorkspacePath)
		if err != nil {
			return nil, nil, http.StatusBadRequest, err
		}
		workspaceInputs = append(workspaceInputs, workspace.ReplicationWorkspaceInput{SourceWorkspacePath: owned.WorkspacePath, ReplicationMode: workspace.ReplicationModeBundle})
	}
	normalized, err := s.workspace.NormalizeReplicationWorkspaces(workspaceInputs)
	if err != nil {
		return nil, nil, http.StatusBadRequest, err
	}
	for _, item := range normalized {
		if item.ReplicationMode != workspace.ReplicationModeBundle || !item.GitWorkspace {
			return nil, nil, http.StatusBadRequest, fmt.Errorf("managed host replication requires git bundle source workspace %q", item.SourceWorkspacePath)
		}
	}
	catalog, err := s.replicateWorkspaceCatalogForPrincipal(principal, normalized)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, err
	}
	return normalized, catalog, http.StatusOK, nil
}

func (s *Server) buildPeerManagedWorkspacePreflightRequest(destinationRoot string, normalized []workspace.NormalizedReplicationWorkspace, catalog map[string]replicateWorkspaceCatalogEntry, selections []managedWorkspaceSelectionRequest) peerManagedWorkspacePreflightRequest {
	preserveHomeRelativePath := managedWorkspaceDestinationRootUsesHomeDefault(destinationRoot)
	destinations := make(map[string]string, len(selections))
	for _, selection := range selections {
		if source := strings.TrimSpace(selection.SourceWorkspacePath); source != "" {
			destinations[filepath.Clean(source)] = strings.TrimSpace(selection.DestinationPath)
		}
	}
	items := make([]peerManagedWorkspacePlanItem, 0, len(normalized))
	for _, item := range normalized {
		name := firstNonEmpty(strings.TrimSpace(catalog[item.SourceWorkspacePath].Name), defaultReplicatedWorkspaceName(item.SourceWorkspacePath))
		homeRelativePath := ""
		if preserveHomeRelativePath {
			homeRelativePath = sourceHomeRelativePath(item.SourceWorkspacePath)
		}
		items = append(items, peerManagedWorkspacePlanItem{
			SourceWorkspacePath:    item.SourceWorkspacePath,
			SourceHomeRelativePath: homeRelativePath,
			WorkspaceName:          name,
			DestinationPath:        destinations[filepath.Clean(item.SourceWorkspacePath)],
			GitWorkspace:           item.GitWorkspace,
		})
	}
	return peerManagedWorkspacePreflightRequest{DestinationRoot: strings.TrimSpace(destinationRoot), Workspaces: items}
}

func postPeerManagedWorkspacePreflight(ctx context.Context, target swarmTarget, localSwarmID, peerToken string, payload peerManagedWorkspacePreflightRequest) (peerManagedWorkspacePreflightResponse, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return peerManagedWorkspacePreflightResponse{}, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedWorkspacePreflightPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return peerManagedWorkspacePreflightResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return peerManagedWorkspacePreflightResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return peerManagedWorkspacePreflightResponse{}, fmt.Errorf("managed host workspace preflight failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded peerManagedWorkspacePreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return peerManagedWorkspacePreflightResponse{}, err
	}
	return decoded, nil
}

func (s *Server) peerManagedWorkspacePreflight(req peerManagedWorkspacePreflightRequest, requests ...*http.Request) (peerManagedWorkspacePreflightResponse, int, error) {
	if s == nil || s.workspace == nil {
		return peerManagedWorkspacePreflightResponse{}, http.StatusInternalServerError, errors.New("workspace service is not configured")
	}
	root, err := normalizeManagedWorkspaceDestinationRoot(req.DestinationRoot)
	if err != nil {
		return peerManagedWorkspacePreflightResponse{}, http.StatusBadRequest, err
	}
	if len(req.Workspaces) == 0 {
		return peerManagedWorkspacePreflightResponse{}, http.StatusBadRequest, errors.New("at least one workspace is required")
	}
	var r *http.Request
	if len(requests) > 0 {
		r = requests[0]
	}
	known, err := s.knownWorkspacePathSet(r)
	if err != nil {
		return peerManagedWorkspacePreflightResponse{}, http.StatusInternalServerError, err
	}
	plans := make([]managedWorkspacePlanResponse, 0, len(req.Workspaces))
	ready := true
	seenDestinations := map[string]struct{}{}
	for _, item := range req.Workspaces {
		plan := planPeerManagedWorkspace(root, item, known)
		if _, ok := seenDestinations[filepath.Clean(plan.DestinationPath)]; ok && plan.OK {
			plan.OK = false
			plan.Action = managedWorkspaceActionConflict
			plan.Error = "destination is selected more than once"
		}
		seenDestinations[filepath.Clean(plan.DestinationPath)] = struct{}{}
		if !plan.OK {
			ready = false
		}
		plans = append(plans, plan)
	}
	return peerManagedWorkspacePreflightResponse{OK: ready, Ready: ready, DestinationRoot: root, Workspaces: plans}, http.StatusOK, nil
}

func (s *Server) knownWorkspacePathSet(r *http.Request) (map[string]struct{}, error) {
	principal, ok := s.peerManagedWorkspacePrincipalForRequest(r)
	if !ok || !principal.Valid() {
		return nil, errors.New("managed workspace peer request requires persisted pairing account binding")
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if path != "" && path != "." {
			out[path] = struct{}{}
		}
	}
	return out, nil
}

func planPeerManagedWorkspace(root string, item peerManagedWorkspacePlanItem, known map[string]struct{}) managedWorkspacePlanResponse {
	workspaceName := sanitizeReplicationMountName(firstNonEmpty(strings.TrimSpace(item.WorkspaceName), defaultReplicatedWorkspaceName(item.SourceWorkspacePath)))
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	destination, err := managedWorkspaceDestinationPath(root, workspaceName, item.SourceHomeRelativePath, item.DestinationPath)
	plan := managedWorkspacePlanResponse{
		PlanID:              managedWorkspacePlanID(item.SourceWorkspacePath, root, destination, ""),
		SourceWorkspacePath: strings.TrimSpace(item.SourceWorkspacePath),
		SourceWorkspaceName: workspaceName,
		DestinationRoot:     root,
		DestinationPath:     destination,
		GitWorkspace:        item.GitWorkspace,
	}
	if err != nil {
		plan.OK = false
		plan.Action = managedWorkspaceActionConflict
		plan.Error = err.Error()
		plan.PlanID = managedWorkspacePlanID(plan.SourceWorkspacePath, root, destination, plan.Action)
		return plan
	}
	if !item.GitWorkspace {
		plan.OK = false
		plan.Action = managedWorkspaceActionConflict
		plan.Error = "source workspace must be git-backed for managed host replication"
		plan.PlanID = managedWorkspacePlanID(plan.SourceWorkspacePath, root, destination, plan.Action)
		return plan
	}
	info, statErr := os.Stat(destination)
	if statErr == nil {
		if !info.IsDir() {
			plan.OK = false
			plan.Action = managedWorkspaceActionConflict
			plan.Error = "destination exists and is not a directory"
		} else if _, ok := known[filepath.Clean(destination)]; ok {
			plan.OK = true
			plan.Action = managedWorkspaceActionLinkExisting
		} else if managedWorkspaceDestinationIsGitRepository(destination) {
			plan.OK = true
			plan.Action = managedWorkspaceActionLinkExisting
		} else {
			plan.OK = false
			plan.Action = managedWorkspaceActionConflict
			plan.Error = "destination exists but is not a git workspace"
		}
	} else if os.IsNotExist(statErr) {
		plan.OK = true
		plan.Action = managedWorkspaceActionImportBundle
	} else {
		plan.OK = false
		plan.Action = managedWorkspaceActionConflict
		plan.Error = statErr.Error()
	}
	plan.PlanID = managedWorkspacePlanID(plan.SourceWorkspacePath, root, destination, plan.Action)
	return plan
}

func managedWorkspaceDestinationIsGitRepository(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return false
	}
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func managedWorkspaceDestinationRootUsesHomeDefault(raw string) bool {
	root := strings.TrimSpace(raw)
	return root == "" || root == "~"
}

func normalizeManagedWorkspaceDestinationRoot(raw string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" || root == "~" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("managed host home directory is unavailable")
		}
		root = home
	} else if strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("managed host home directory is unavailable")
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
	} else if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("managed host home directory is unavailable")
		}
		root = filepath.Join(home, root)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("destination_root is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("destination_root must be a directory")
	}
	return root, nil
}

func managedWorkspaceDestinationPath(root, workspaceName, sourceHomeRelativePath, requested string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	requested = strings.TrimSpace(requested)
	var destination string
	if requested == "" {
		defaultRelative := cleanManagedWorkspaceRelativePath(sourceHomeRelativePath)
		if defaultRelative == "" {
			defaultRelative = sanitizeReplicationMountName(workspaceName)
		}
		destination = filepath.Join(root, defaultRelative)
	} else {
		if !filepath.IsAbs(requested) {
			return "", errors.New("destination_path must be absolute when provided")
		}
		destination = filepath.Clean(requested)
	}
	if destination == "" || destination == "." {
		return "", errors.New("destination_path is required")
	}
	if !pathWithinManagedWorkspaceRoot(root, destination) {
		return destination, errors.New("destination_path must stay inside destination_root")
	}
	return destination, nil
}

func sourceHomeRelativePath(sourcePath string) string {
	sourcePath = filepath.Clean(strings.TrimSpace(sourcePath))
	if sourcePath == "" || sourcePath == "." {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	home = filepath.Clean(home)
	rel, err := filepath.Rel(home, sourcePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return cleanManagedWorkspaceRelativePath(rel)
}

func cleanManagedWorkspaceRelativePath(raw string) string {
	rel := filepath.Clean(strings.TrimSpace(raw))
	if rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

func pathWithinManagedWorkspaceRoot(root, target string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "" || target == "" || root == "." || target == "." {
		return false
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ensureManagedWorkspaceLinkOnPeer(ctx context.Context, target swarmTarget, localSwarmID, peerToken string, payload peerManagedWorkspaceEnsureLinkRequest) (peerManagedWorkspaceEnsureLinkResponse, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedWorkspaceEnsureLinkPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return peerManagedWorkspaceEnsureLinkResponse{}, fmt.Errorf("managed host workspace ensure link failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded peerManagedWorkspaceEnsureLinkResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return peerManagedWorkspaceEnsureLinkResponse{}, err
	}
	return decoded, nil
}

func linkExistingManagedWorkspaceOnPeer(ctx context.Context, target swarmTarget, localSwarmID, peerToken, destinationRoot string, plan managedWorkspacePlanResponse) error {
	payload := peerManagedWorkspaceLinkExistingRequest{
		DestinationRoot:        destinationRoot,
		DestinationPath:        plan.DestinationPath,
		WorkspaceName:          plan.SourceWorkspaceName,
		SourceWorkspacePath:    plan.SourceWorkspacePath,
		SourceWorkspaceName:    plan.SourceWorkspaceName,
		SourceHomeRelativePath: sourceHomeRelativePath(plan.SourceWorkspacePath),
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedWorkspaceLinkExistingPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("managed host workspace link failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded peerManagedWorkspaceLinkExistingResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if filepath.Clean(decoded.DestinationPath) != filepath.Clean(plan.DestinationPath) {
		return errors.New("managed host linked a different destination than the confirmed plan")
	}
	return nil
}

func importManagedWorkspaceBundleToPeer(ctx context.Context, target swarmTarget, localSwarmID, peerToken, destinationRoot string, plan managedWorkspacePlanResponse) error {
	bundlePath, err := managedWorkspaceCreateGitBundle(ctx, plan.SourceWorkspacePath)
	if err != nil {
		return err
	}
	defer os.Remove(bundlePath)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("source_workspace_path", plan.SourceWorkspacePath)
	_ = writer.WriteField("source_home_relative_path", sourceHomeRelativePath(plan.SourceWorkspacePath))
	_ = writer.WriteField("workspace_name", plan.SourceWorkspaceName)
	_ = writer.WriteField("destination_root", destinationRoot)
	_ = writer.WriteField("destination_path", plan.DestinationPath)
	part, err := writer.CreateFormFile("bundle", filepath.Base(bundlePath))
	if err != nil {
		return err
	}
	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		file.Close()
		return err
	}
	file.Close()
	if err := writer.Close(); err != nil {
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedWorkspaceImportBundlePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("managed host workspace import failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded peerManagedWorkspaceImportBundleResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if filepath.Clean(decoded.DestinationPath) != filepath.Clean(plan.DestinationPath) {
		return errors.New("managed host imported a different destination than the confirmed plan")
	}
	return nil
}

func managedWorkspaceCreateGitBundle(ctx context.Context, workspacePath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", errors.New("workspace path is required")
	}
	file, err := os.CreateTemp("", "swarm-managed-workspace-*.bundle")
	if err != nil {
		return "", err
	}
	bundlePath := file.Name()
	file.Close()
	if err := os.Remove(bundlePath); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workspacePath, "bundle", "create", bundlePath, "--all")
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(bundlePath)
		return "", fmt.Errorf("create managed workspace git bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return bundlePath, nil
}

func writeManagedWorkspaceUploadedBundle(root, name string, file multipart.File) (string, error) {
	bundlePath := filepath.Join(root, fmt.Sprintf(".%s-%d.bundle", sanitizeReplicationMountName(name), time.Now().UnixNano()))
	out, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		return "", err
	}
	return bundlePath, nil
}

func managedWorkspaceCloneGitBundle(ctx context.Context, bundlePath, targetPath string) error {
	if strings.TrimSpace(targetPath) == "" {
		return errors.New("destination_path is required")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return errors.New("destination_path already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", bundlePath, targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(targetPath)
		return fmt.Errorf("import managed workspace git bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func managedWorkspacePlanID(source, root, destination, action string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{strings.TrimSpace(source), filepath.Clean(strings.TrimSpace(root)), filepath.Clean(strings.TrimSpace(destination)), strings.TrimSpace(action)}, "\x00")))
	return "managed_workspace_" + hex.EncodeToString(sum[:8])
}

func (s *Server) upsertManagedWorkspaceBindingForPrincipal(principal identity.Principal, target swarmTarget, sourceWorkspacePath, sourceWorkspaceName, destinationPath string) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topology == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, errors.New("topology service is not configured")
	}
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, identity.ErrPrincipalRequired
	}
	targetSwarmID := strings.TrimSpace(target.SwarmID)
	if targetSwarmID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, errors.New("managed workspace target swarm id is required")
	}
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	workspaceName := firstNonEmpty(strings.TrimSpace(sourceWorkspaceName), defaultReplicatedWorkspaceName(sourceWorkspacePath))
	workspaceID := ""
	workspaceGeneration := int64(1)
	if s.workspace != nil {
		_, entry, _, err := s.workspace.AddForPrincipalWithEntryWithoutSelection(principal, sourceWorkspacePath, workspaceName, "")
		if err != nil {
			return pebblestore.TopologyWorkspaceBindingRecord{}, err
		}
		workspaceID = strings.TrimSpace(entry.WorkspaceID)
		workspaceGeneration = int64(entry.WorkspaceGeneration)
		workspaceName = firstNonEmpty(strings.TrimSpace(entry.Name), workspaceName)
	}
	if workspaceID == "" {
		workspaceID = managedWorkspaceBindingWorkspaceID(strings.TrimSpace(principal.AccountScopeID), sourceWorkspacePath)
	}
	placement, ok, err := s.topology.GetRuntimePlacementForAccount(strings.TrimSpace(principal.AccountScopeID), targetSwarmID)
	if err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, err
	}
	if !ok {
		if placement, err = s.topology.EnsureLocalSelfPlacementForPrincipal(strings.TrimSpace(principal.AccountScopeID), strings.TrimSpace(principal.UserID)); err != nil {
			return pebblestore.TopologyWorkspaceBindingRecord{}, err
		}
		if !strings.EqualFold(placement.RuntimeSwarmID, targetSwarmID) {
			return pebblestore.TopologyWorkspaceBindingRecord{}, errors.New("managed workspace target placement is required")
		}
	}
	return s.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       pebblestore.CanonicalTopologyWorkspaceBindingID(targetSwarmID, sourceWorkspacePath),
		UserID:                          strings.TrimSpace(principal.UserID),
		AccountScopeID:                  strings.TrimSpace(principal.AccountScopeID),
		SourceWorkspaceID:               workspaceID,
		SourceWorkspaceGeneration:       workspaceGeneration,
		SourceWorkspacePath:             sourceWorkspacePath,
		SourceWorkspaceName:             workspaceName,
		DestinationRuntimeSwarmID:       targetSwarmID,
		DestinationAuthorityHostSwarmID: placement.AuthorityHostSwarmID,
		DestinationHostSwarmID:          placement.AuthorityHostSwarmID,
		DestinationContainerID:          placement.AuthorityContainerID,
		DestinationRuntimeKind:          placement.RuntimeKind,
		DestinationWorkspacePath:        strings.TrimSpace(destinationPath),
		PlacementGeneration:             placement.PlacementGeneration,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           placement.AuthorityHostSwarmID,
		ReplicationMode:                 workspace.ReplicationModeBundle,
		Writable:                        true,
		LegacyTargetKind:                managedWorkspaceTargetKind,
	})
}

func managedWorkspaceBindingWorkspaceID(accountScopeID, sourceWorkspacePath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(sourceWorkspacePath)))
	return "workspace_" + hex.EncodeToString(sum[:16])
}
