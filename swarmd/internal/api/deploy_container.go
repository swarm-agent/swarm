package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

const syncManagedVaultKeyHeader = "X-Swarm-Sync-Managed-Vault-Key"

func principalRequestContext(r *http.Request) (context.Context, bool) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		return nil, false
	}
	return identity.ContextWithPrincipal(r.Context(), principal), true
}

type deployContainerCreatePayload struct {
	DeploymentID       string                                      `json:"deployment_id,omitempty"`
	Name               string                                      `json:"name"`
	Runtime            string                                      `json:"runtime"`
	Image              string                                      `json:"image"`
	GroupID            string                                      `json:"group_id,omitempty"`
	GroupName          string                                      `json:"group_name,omitempty"`
	GroupNetworkName   string                                      `json:"group_network_name,omitempty"`
	SyncEnabled        bool                                        `json:"sync_enabled"`
	SyncMode           string                                      `json:"sync_mode,omitempty"`
	SyncModules        []string                                    `json:"sync_modules,omitempty"`
	SyncVaultPassword  string                                      `json:"sync_vault_password,omitempty"`
	BypassPermissions  bool                                        `json:"bypass_permissions,omitempty"`
	AlwaysOn           bool                                        `json:"always_on,omitempty"`
	WorkspaceBootstrap []deployruntime.ContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
	ContainerPackages  deployruntime.ContainerPackageManifest      `json:"container_packages,omitempty"`
	Mounts             []localcontainers.Mount                     `json:"mounts"`
}

type deployContainerSettingsPayload struct {
	ID                string   `json:"id"`
	SyncEnabled       *bool    `json:"sync_enabled,omitempty"`
	SyncModules       []string `json:"sync_modules,omitempty"`
	SyncVaultPassword string   `json:"sync_vault_password,omitempty"`
	BypassPermissions *bool    `json:"bypass_permissions,omitempty"`
}

func (s *Server) handleDeployContainerRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	ctx, ok := principalRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	status, err := s.deployContainers.RuntimeStatus(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": status.PathID,
		"runtime": status,
	})
}

func (s *Server) handleDeployContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	ctx, ok := principalRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	items, err := s.deployContainers.List(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"path_id":     deployruntime.PathContainerList,
		"deployments": items,
	})
}

func (s *Server) handleDeployContainerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req deployContainerCreatePayload
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, ok := principalRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	log.Printf("deploy container create request name=%q runtime=%q group_id=%q group_network_name=%q sync_enabled=%t mounts=%d remote_addr=%s", strings.TrimSpace(req.Name), strings.TrimSpace(req.Runtime), strings.TrimSpace(req.GroupID), strings.TrimSpace(req.GroupNetworkName), req.SyncEnabled, len(req.Mounts), strings.TrimSpace(r.RemoteAddr))
	deployment, err := s.deployContainers.Create(ctx, deployruntime.ContainerCreateInput{
		DeploymentID:       req.DeploymentID,
		Name:               req.Name,
		Runtime:            req.Runtime,
		Image:              req.Image,
		GroupID:            req.GroupID,
		GroupName:          req.GroupName,
		GroupNetworkName:   req.GroupNetworkName,
		SyncEnabled:        req.SyncEnabled,
		SyncMode:           req.SyncMode,
		SyncModules:        req.SyncModules,
		SyncVaultPassword:  req.SyncVaultPassword,
		BypassPermissions:  req.BypassPermissions,
		AlwaysOn:           req.AlwaysOn,
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		WorkspaceBootstrap: req.WorkspaceBootstrap,
		ContainerPackages:  req.ContainerPackages,
		Mounts:             req.Mounts,
	})
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "start ") {
			statusCode = http.StatusConflict
		}
		log.Printf("deploy container create failed name=%q runtime=%q group_id=%q status=%d err=%v", strings.TrimSpace(req.Name), strings.TrimSpace(req.Runtime), strings.TrimSpace(req.GroupID), statusCode, err)
		writeJSON(w, statusCode, map[string]any{
			"ok":         false,
			"path_id":    deployruntime.PathContainerCreate,
			"deployment": deployment,
			"error":      err.Error(),
		})
		return
	}
	log.Printf("deploy container create success deployment_id=%q runtime=%q group_id=%q status=%q attach_status=%q", deployment.ID, deployment.Runtime, deployment.GroupID, deployment.Status, deployment.AttachStatus)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"path_id":    deployruntime.PathContainerCreate,
		"deployment": deployment,
	})
}

func (s *Server) handleDeployContainerDeleteRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, ok := principalRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var result localcontainers.DeleteResult
	deployments, listErr := s.deployContainers.List(ctx)
	if listErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerDelete, "result": result, "error": listErr.Error()})
		return
	}
	for _, deployment := range deployments {
		if !containsDeploymentID(req.IDs, deployment.ID) {
			continue
		}
		if deleteErr := s.deleteTargetHostDeployment(r.Context(), deployment); deleteErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerDelete, "result": result, "error": deleteErr.Error()})
			return
		}
	}
	result, err := s.deployContainers.Delete(ctx, req.IDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerDelete, "result": result, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerDelete, "result": result})
}

func containsDeploymentID(ids []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range ids {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

func (s *Server) handleDeployContainerSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req deployContainerSettingsPayload
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := decodeJSONBytes(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, principalOK := principalRequestContext(r)
	if _, peerAuthorized := authorizedPeerSwarmID(r); !peerAuthorized {
		if !principalOK {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		if deployment, ok, err := s.findRemoteManagedDeployment(ctx, req.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSettings, "error": err.Error()})
			return
		} else if ok {
			updated, forwardErr := s.updateTargetHostDeploymentSettings(r.Context(), deployment, req)
			if forwardErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSettings, "deployment": updated, "error": forwardErr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSettings, "deployment": updated})
			return
		}
	}
	var rawFields map[string]json.RawMessage
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&rawFields)
	_, modulesSet := rawFields["sync_modules"]
	settingsSvc, ok := s.deployContainers.(interface {
		UpdateSettings(context.Context, deployruntime.ContainerSettingsUpdateInput) (deployruntime.ContainerDeployment, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container settings not configured"))
		return
	}
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	deployment, err := settingsSvc.UpdateSettings(ctx, deployruntime.ContainerSettingsUpdateInput{
		ID:                req.ID,
		SyncEnabled:       req.SyncEnabled,
		SyncModules:       req.SyncModules,
		SyncModulesSet:    modulesSet,
		SyncVaultPassword: req.SyncVaultPassword,
		BypassPermissions: req.BypassPermissions,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSettings, "deployment": deployment, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSettings, "deployment": deployment})
}

func (s *Server) handleDeployContainerAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, principalOK := principalRequestContext(r)
	if _, peerAuthorized := authorizedPeerSwarmID(r); !peerAuthorized {
		if !principalOK {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		if deployment, ok, err := s.findRemoteManagedDeployment(ctx, req.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerAction, "error": err.Error()})
			return
		} else if ok {
			updated, forwardErr := s.actTargetHostDeployment(r.Context(), deployment, req.Action)
			if forwardErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerAction, "deployment": updated, "error": forwardErr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerAction, "deployment": updated})
			return
		}
	}
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	deployment, err := s.deployContainers.Act(ctx, deployruntime.ContainerActionInput{ID: req.ID, Action: req.Action})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"path_id":    deployruntime.PathContainerAction,
			"deployment": deployment,
			"error":      err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"path_id":    deployruntime.PathContainerAction,
		"deployment": deployment,
	})
}

func (s *Server) handleDeployContainerAttachRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID      string `json:"deployment_id"`
		BootstrapSecret   string `json:"bootstrap_secret"`
		ChildSwarmID      string `json:"child_swarm_id"`
		ChildDisplayName  string `json:"child_display_name"`
		ChildBackendURL   string `json:"child_backend_url"`
		ChildDesktopURL   string `json:"child_desktop_url"`
		ChildPublicKey    string `json:"child_public_key"`
		ChildFingerprint  string `json:"child_fingerprint"`
		RequestedAtMillis int64  `json:"requested_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("deploy attach request deployment_id=%q child_swarm_id=%q child_backend_url=%q remote_addr=%s", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.ChildSwarmID), strings.TrimSpace(req.ChildBackendURL), strings.TrimSpace(r.RemoteAddr))
	state, err := s.deployContainers.AttachRequest(context.Background(), deployruntime.ContainerAttachRequestInput{
		DeploymentID:      req.DeploymentID,
		BootstrapSecret:   req.BootstrapSecret,
		ChildSwarmID:      req.ChildSwarmID,
		ChildDisplayName:  req.ChildDisplayName,
		ChildBackendURL:   req.ChildBackendURL,
		ChildDesktopURL:   req.ChildDesktopURL,
		ChildPublicKey:    req.ChildPublicKey,
		ChildFingerprint:  req.ChildFingerprint,
		RequestedAtMillis: req.RequestedAtMillis,
	})
	if err != nil {
		log.Printf("deploy attach request failed deployment_id=%q child_swarm_id=%q err=%v", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.ChildSwarmID), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerAttachRequest,
			"attach":  state,
			"error":   err.Error(),
			"code":    strconv.Itoa(http.StatusBadRequest),
		})
		return
	}
	log.Printf("deploy attach request accepted deployment_id=%q attach_status=%q child_swarm_id=%q", state.DeploymentID, state.AttachStatus, state.ChildSwarmID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": deployruntime.PathContainerAttachRequest,
		"attach":  state,
	})
}

func (s *Server) handleDeployContainerAttachChildState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, err := s.deployContainers.ChildAttachState(context.Background(), deployruntime.ContainerAttachStatusInput{
		DeploymentID:    req.DeploymentID,
		BootstrapSecret: req.BootstrapSecret,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": "deploy.container.attach.child_state.v1",
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": "deploy.container.attach.child_state.v1",
		"state":   state,
	})
}

func (s *Server) handleDeployContainerAttachApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID             string `json:"deployment_id"`
		BootstrapSecret          string `json:"bootstrap_secret"`
		HostSwarmID              string `json:"host_swarm_id"`
		HostDisplayName          string `json:"host_display_name"`
		HostPublicKey            string `json:"host_public_key"`
		HostFingerprint          string `json:"host_fingerprint"`
		HostBackendURL           string `json:"host_backend_url"`
		HostDesktopURL           string `json:"host_desktop_url"`
		HostToChildPeerAuthToken string `json:"host_to_child_peer_auth_token,omitempty"`
		ChildToHostPeerAuthToken string `json:"child_to_host_peer_auth_token,omitempty"`
		GroupID                  string `json:"group_id"`
		GroupName                string `json:"group_name"`
		GroupNetworkName         string `json:"group_network_name"`
		SyncVaultPassword        string `json:"sync_vault_password,omitempty"`
		SyncManagedVaultKey      string `json:"sync_managed_vault_key,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("deploy attach approve deployment_id=%q host_swarm_id=%q group_id=%q group_network_name=%q remote_addr=%s", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.HostSwarmID), strings.TrimSpace(req.GroupID), strings.TrimSpace(req.GroupNetworkName), strings.TrimSpace(r.RemoteAddr))
	state, err := s.deployContainers.AttachApprove(context.Background(), deployruntime.ContainerAttachApproveInput{
		DeploymentID:             req.DeploymentID,
		BootstrapSecret:          req.BootstrapSecret,
		HostSwarmID:              req.HostSwarmID,
		HostDisplayName:          req.HostDisplayName,
		HostPublicKey:            req.HostPublicKey,
		HostFingerprint:          req.HostFingerprint,
		HostBackendURL:           req.HostBackendURL,
		HostDesktopURL:           req.HostDesktopURL,
		HostToChildPeerAuthToken: req.HostToChildPeerAuthToken,
		ChildToHostPeerAuthToken: req.ChildToHostPeerAuthToken,
		GroupID:                  req.GroupID,
		GroupName:                req.GroupName,
		GroupNetworkName:         req.GroupNetworkName,
		SyncVaultPassword:        req.SyncVaultPassword,
	})
	if err != nil {
		log.Printf("deploy attach approve failed deployment_id=%q group_id=%q err=%v", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.GroupID), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerAttachApprove,
			"attach":  state,
			"error":   err.Error(),
		})
		return
	}
	log.Printf("deploy attach approve success deployment_id=%q attach_status=%q child_swarm_id=%q", state.DeploymentID, state.AttachStatus, state.ChildSwarmID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": deployruntime.PathContainerAttachApprove,
		"attach":  state,
	})
}

func (s *Server) handleDeployContainerAttachFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID             string                                      `json:"deployment_id"`
		BootstrapSecret          string                                      `json:"bootstrap_secret"`
		UserID                   string                                      `json:"user_id,omitempty"`
		AccountScopeID           string                                      `json:"account_scope_id,omitempty"`
		HostSwarmID              string                                      `json:"host_swarm_id"`
		HostContainerID          string                                      `json:"host_container_id,omitempty"`
		ChildSwarmID             string                                      `json:"child_swarm_id,omitempty"`
		HostDisplayName          string                                      `json:"host_display_name"`
		HostPublicKey            string                                      `json:"host_public_key"`
		HostFingerprint          string                                      `json:"host_fingerprint"`
		HostBackendURL           string                                      `json:"host_backend_url"`
		HostDesktopURL           string                                      `json:"host_desktop_url"`
		GroupID                  string                                      `json:"group_id"`
		GroupName                string                                      `json:"group_name"`
		GroupNetworkName         string                                      `json:"group_network_name"`
		HostToChildPeerAuthToken string                                      `json:"host_to_child_peer_auth_token,omitempty"`
		ChildToHostPeerAuthToken string                                      `json:"child_to_host_peer_auth_token,omitempty"`
		SyncMode                 string                                      `json:"sync_mode,omitempty"`
		SyncModules              []string                                    `json:"sync_modules,omitempty"`
		SyncOwnerSwarmID         string                                      `json:"sync_owner_swarm_id,omitempty"`
		SyncBundlePassword       string                                      `json:"sync_bundle_password,omitempty"`
		SyncVaultPassword        string                                      `json:"sync_vault_password,omitempty"`
		SyncBundle               []byte                                      `json:"sync_bundle,omitempty"`
		WorkspaceBootstrap       []deployruntime.ContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	managedVaultKey := strings.TrimSpace(r.Header.Get(syncManagedVaultKeyHeader))
	log.Printf("deploy attach finalize deployment_id=%q host_swarm_id=%q group_id=%q group_network_name=%q managed_vault_key_present=%t remote_addr=%s", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.HostSwarmID), strings.TrimSpace(req.GroupID), strings.TrimSpace(req.GroupNetworkName), managedVaultKey != "", strings.TrimSpace(r.RemoteAddr))
	if err := s.deployContainers.FinalizeAttachFromHost(context.Background(), deployruntime.ContainerAttachFinalizeInput{
		DeploymentID:             req.DeploymentID,
		BootstrapSecret:          req.BootstrapSecret,
		UserID:                   req.UserID,
		AccountScopeID:           req.AccountScopeID,
		HostSwarmID:              req.HostSwarmID,
		HostContainerID:          req.HostContainerID,
		ChildSwarmID:             req.ChildSwarmID,
		HostDisplayName:          req.HostDisplayName,
		HostPublicKey:            req.HostPublicKey,
		HostFingerprint:          req.HostFingerprint,
		HostBackendURL:           req.HostBackendURL,
		HostDesktopURL:           req.HostDesktopURL,
		GroupID:                  req.GroupID,
		GroupName:                req.GroupName,
		GroupNetworkName:         req.GroupNetworkName,
		HostToChildPeerAuthToken: req.HostToChildPeerAuthToken,
		ChildToHostPeerAuthToken: req.ChildToHostPeerAuthToken,
		SyncMode:                 req.SyncMode,
		SyncModules:              req.SyncModules,
		SyncOwnerSwarmID:         req.SyncOwnerSwarmID,
		SyncBundlePassword:       req.SyncBundlePassword,
		SyncVaultPassword:        req.SyncVaultPassword,
		SyncManagedVaultKey:      managedVaultKey,
		SyncBundle:               req.SyncBundle,
		WorkspaceBootstrap:       req.WorkspaceBootstrap,
	}); err != nil {
		log.Printf("deploy attach finalize failed deployment_id=%q group_id=%q err=%v", strings.TrimSpace(req.DeploymentID), strings.TrimSpace(req.GroupID), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": "deploy.container.attach.finalize.v1",
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": "deploy.container.attach.finalize.v1",
	})
}

func syncRequestWithPeerAuth(r *http.Request, req deployruntime.ContainerSyncCredentialRequestInput) deployruntime.ContainerSyncCredentialRequestInput {
	if peerSwarmID, ok := authorizedPeerSwarmID(r); ok {
		req.PeerSwarmID = peerSwarmID
		req.PeerAuthorized = true
	}
	return req
}

func (s *Server) handleDeployContainerSyncCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID      string `json:"deployment_id"`
		BootstrapSecret   string `json:"bootstrap_secret"`
		VaultPassword     string `json:"vault_password,omitempty"`
		KnownSnapshotHash string `json:"known_snapshot_hash,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bundle, err := s.deployContainers.SyncCredentialBundle(context.Background(), syncRequestWithPeerAuth(r, deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:      req.DeploymentID,
		BootstrapSecret:   req.BootstrapSecret,
		VaultPassword:     req.VaultPassword,
		KnownSnapshotHash: req.KnownSnapshotHash,
	}))
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "unlock") {
			status = http.StatusLocked
		}
		writeJSON(w, status, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerSyncCredentials,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": deployruntime.PathContainerSyncCredentials,
		"bundle":  bundle,
	})
}

func (s *Server) handleDeployContainerSyncAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bundle, err := s.deployContainers.SyncAgentBundle(context.Background(), syncRequestWithPeerAuth(r, deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    req.DeploymentID,
		BootstrapSecret: req.BootstrapSecret,
	}))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerSyncAgents,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": deployruntime.PathContainerSyncAgents,
		"bundle":  bundle,
	})
}

func (s *Server) handleDeployContainerSyncSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		SyncSkillBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncSkillBundle, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container skill sync not configured"))
		return
	}
	bundle, err := syncSvc.SyncSkillBundle(context.Background(), syncRequestWithPeerAuth(r, deployruntime.ContainerSyncCredentialRequestInput{DeploymentID: req.DeploymentID, BootstrapSecret: req.BootstrapSecret}))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSyncSkills, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSyncSkills, "bundle": bundle})
}

func (s *Server) handleDeployContainerSyncPermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		SyncPermissionBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncPermissionBundle, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container permission sync not configured"))
		return
	}
	bundle, err := syncSvc.SyncPermissionBundle(context.Background(), syncRequestWithPeerAuth(r, deployruntime.ContainerSyncCredentialRequestInput{DeploymentID: req.DeploymentID, BootstrapSecret: req.BootstrapSecret}))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSyncPermissions, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSyncPermissions, "bundle": bundle})
}

func (s *Server) handleDeployContainerSyncModelDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		SyncModelDefaultsBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncModelDefaultsBundle, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container model defaults sync not configured"))
		return
	}
	bundle, err := syncSvc.SyncModelDefaultsBundle(context.Background(), syncRequestWithPeerAuth(r, deployruntime.ContainerSyncCredentialRequestInput{DeploymentID: req.DeploymentID, BootstrapSecret: req.BootstrapSecret}))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSyncModelDefaults, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSyncModelDefaults, "bundle": bundle})
}

func (s *Server) handleDeployContainerManagedModelDefaultsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		ApplyManagedModelDefaultsBundle(context.Context, deployruntime.ContainerSyncModelDefaultsBundle) error
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container model defaults sync not configured"))
		return
	}
	var bundle deployruntime.ContainerSyncModelDefaultsBundle
	if err := decodeJSON(r, &bundle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := syncSvc.ApplyManagedModelDefaultsBundle(context.Background(), bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerManagedModelDefaultsApply, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerManagedModelDefaultsApply})
}
func (s *Server) handleDeployContainerManagedCredentialsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		ApplyManagedCredentialBundle(context.Context, deployruntime.ContainerSyncCredentialBundle) error
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container credential sync not configured"))
		return
	}
	var bundle deployruntime.ContainerSyncCredentialBundle
	if err := decodeJSON(r, &bundle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := syncSvc.ApplyManagedCredentialBundle(context.Background(), bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerManagedCredentialsApply, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerManagedCredentialsApply})
}

func (s *Server) handleDeployContainerManagedAgentsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	syncSvc, ok := s.deployContainers.(interface {
		ApplyManagedAgentBundle(context.Context, deployruntime.ContainerSyncAgentBundle) error
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container agent sync not configured"))
		return
	}
	var bundle deployruntime.ContainerSyncAgentBundle
	if err := decodeJSON(r, &bundle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := syncSvc.ApplyManagedAgentBundle(context.Background(), bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerManagedAgentsApply, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerManagedAgentsApply})
}

func (s *Server) handleDeployContainerManagedSkillsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.discovery == nil {
		writeError(w, http.StatusInternalServerError, errors.New("discovery service not configured"))
		return
	}
	var bundle deployruntime.ContainerSyncSkillBundle
	if err := decodeJSON(r, &bundle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.discovery.ApplyManagedSkillBundle(bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "path_id": deployruntime.PathContainerSyncSkills, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerSyncSkills})
}

func (s *Server) handleDeployContainerWorkspaceBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	var req struct {
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspaces, err := s.deployContainers.WorkspaceBootstrap(context.Background(), deployruntime.ContainerWorkspaceBootstrapRequestInput{
		DeploymentID:    req.DeploymentID,
		BootstrapSecret: req.BootstrapSecret,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerWorkspaceBootstrap,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"path_id":    deployruntime.PathContainerWorkspaceBootstrap,
		"workspaces": workspaces,
	})
}
