package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

const syncManagedVaultKeyHeader = "X-Swarm-Sync-Managed-Vault-Key"

func (s *Server) handleDeployContainerRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	status, err := s.deployContainers.RuntimeStatus(context.Background())
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
	items, err := s.deployContainers.List(context.Background())
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
	var req struct {
		Name              string `json:"name"`
		Runtime           string `json:"runtime"`
		Image             string `json:"image"`
		GroupID           string `json:"group_id"`
		GroupName         string `json:"group_name"`
		GroupNetworkName  string `json:"group_network_name"`
		SyncEnabled       bool   `json:"sync_enabled"`
		SyncVaultPassword string `json:"sync_vault_password,omitempty"`
		BypassPermissions bool   `json:"bypass_permissions,omitempty"`
		AlwaysOn          bool   `json:"always_on,omitempty"`
		ContainerPackages struct {
			BaseImage      string `json:"base_image,omitempty"`
			PackageManager string `json:"package_manager,omitempty"`
			Packages       []struct {
				Name   string `json:"name"`
				Source string `json:"source,omitempty"`
				Reason string `json:"reason,omitempty"`
			} `json:"packages,omitempty"`
		} `json:"container_packages,omitempty"`
		Mounts []localcontainers.Mount `json:"mounts"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("deploy container create request name=%q runtime=%q group_id=%q group_network_name=%q sync_enabled=%t mounts=%d remote_addr=%s", strings.TrimSpace(req.Name), strings.TrimSpace(req.Runtime), strings.TrimSpace(req.GroupID), strings.TrimSpace(req.GroupNetworkName), req.SyncEnabled, len(req.Mounts), strings.TrimSpace(r.RemoteAddr))
	packages := make([]deployruntime.ContainerPackageSelection, 0, len(req.ContainerPackages.Packages))
	for _, pkg := range req.ContainerPackages.Packages {
		packages = append(packages, deployruntime.ContainerPackageSelection{
			Name:   pkg.Name,
			Source: pkg.Source,
			Reason: pkg.Reason,
		})
	}
	deployment, err := s.deployContainers.Create(context.Background(), deployruntime.ContainerCreateInput{
		Name:              req.Name,
		Runtime:           req.Runtime,
		Image:             req.Image,
		GroupID:           req.GroupID,
		GroupName:         req.GroupName,
		GroupNetworkName:  req.GroupNetworkName,
		SyncEnabled:       req.SyncEnabled,
		SyncVaultPassword: req.SyncVaultPassword,
		BypassPermissions: req.BypassPermissions,
		AlwaysOn:          req.AlwaysOn,
		ContainerPackages: deployruntime.ContainerPackageManifest{
			BaseImage:      req.ContainerPackages.BaseImage,
			PackageManager: req.ContainerPackages.PackageManager,
			Packages:       packages,
		},
		Mounts: req.Mounts,
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
	deployment, err := s.deployContainers.Act(context.Background(), deployruntime.ContainerActionInput{ID: req.ID, Action: req.Action})
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

func (s *Server) handleDeployContainerDelete(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.deployContainers.Delete(context.Background(), req.IDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": deployruntime.PathContainerDelete,
			"result":  result,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": deployruntime.PathContainerDelete,
		"result":  result,
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
	if cleanupErr := s.retireStaleSessionRoutesForChild(state.ChildSwarmID, state.ChildBackendURL); cleanupErr != nil {
		log.Printf("deploy attach request stale route cleanup failed deployment_id=%q child_swarm_id=%q err=%v", state.DeploymentID, state.ChildSwarmID, cleanupErr)
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
	if cleanupErr := s.retireStaleSessionRoutesForChild(state.ChildSwarmID, state.ChildBackendURL); cleanupErr != nil {
		log.Printf("deploy attach approve stale route cleanup failed deployment_id=%q child_swarm_id=%q err=%v", state.DeploymentID, state.ChildSwarmID, cleanupErr)
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
		HostSwarmID              string                                      `json:"host_swarm_id"`
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
		HostSwarmID:              req.HostSwarmID,
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
		DeploymentID    string `json:"deployment_id"`
		BootstrapSecret string `json:"bootstrap_secret"`
		VaultPassword   string `json:"vault_password,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bundle, err := s.deployContainers.SyncCredentialBundle(context.Background(), deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    req.DeploymentID,
		BootstrapSecret: req.BootstrapSecret,
		VaultPassword:   req.VaultPassword,
	})
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
	bundle, err := s.deployContainers.SyncAgentBundle(context.Background(), deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    req.DeploymentID,
		BootstrapSecret: req.BootstrapSecret,
	})
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
