package api

import (
	"bytes"
	"context"
	"encoding/json"
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

	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/workspace"
)

const replicateWorkspaceMountRoot = "/workspaces"

type swarmReplicateContainerPackageRequest struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type swarmReplicateContainerPackagesRequest struct {
	BaseImage      string                                  `json:"base_image,omitempty"`
	PackageManager string                                  `json:"package_manager,omitempty"`
	Packages       []swarmReplicateContainerPackageRequest `json:"packages,omitempty"`
}

type swarmReplicateRequest struct {
	Mode              string                                 `json:"mode"`
	SwarmName         string                                 `json:"swarm_name"`
	TargetHostSwarmID string                                 `json:"target_host_swarm_id,omitempty"`
	Runtime           string                                 `json:"runtime,omitempty"`
	BypassPermissions bool                                   `json:"bypass_permissions,omitempty"`
	AlwaysOn          bool                                   `json:"always_on,omitempty"`
	Sync              swarmReplicateSyncRequest              `json:"sync"`
	Workspaces        []swarmReplicateWorkspaceRequest       `json:"workspaces"`
	ContainerPackages swarmReplicateContainerPackagesRequest `json:"container_packages,omitempty"`
}

type swarmReplicateSyncRequest struct {
	Enabled       bool     `json:"enabled"`
	Mode          string   `json:"mode,omitempty"`
	Modules       []string `json:"modules,omitempty"`
	VaultPassword string   `json:"vault_password,omitempty"`
}

type swarmReplicateWorkspaceRequest struct {
	SourceWorkspacePath string `json:"source_workspace_path"`
	ReplicationMode     string `json:"replication_mode,omitempty"`
	Writable            *bool  `json:"writable,omitempty"`
}

type swarmReplicateResponse struct {
	OK         bool                              `json:"ok"`
	Swarm      swarmReplicateSwarmResponse       `json:"swarm"`
	Workspaces []swarmReplicateWorkspaceResponse `json:"workspaces"`
}

type swarmReplicateFailureDetails struct {
	DeploymentID    string `json:"deployment_id,omitempty"`
	AttachStatus    string `json:"attach_status,omitempty"`
	LastAttachError string `json:"last_attach_error,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	ContainerName   string `json:"container_name,omitempty"`
	BackendHostPort int    `json:"backend_host_port,omitempty"`
	DesktopHostPort int    `json:"desktop_host_port,omitempty"`
	ChildBackendURL string `json:"child_backend_url,omitempty"`
	ChildDesktopURL string `json:"child_desktop_url,omitempty"`
}

type swarmReplicateSwarmResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Mode              string `json:"mode"`
	DeploymentID      string `json:"deployment_id,omitempty"`
	AttachStatus      string `json:"attach_status,omitempty"`
	GroupID           string `json:"group_id,omitempty"`
	BypassPermissions bool   `json:"bypass_permissions,omitempty"`
}

type swarmReplicateWorkspaceResponse struct {
	SourceWorkspacePath string                                     `json:"source_workspace_path"`
	SourceWorkspaceName string                                     `json:"source_workspace_name"`
	Binding             pebblestore.TopologyWorkspaceBindingRecord `json:"binding"`
}

type replicateWorkspaceCatalogEntry struct {
	Name        string
	ThemeID     string
	Directories []string
	Active      bool
}

type replicationTargetAssignment struct {
	SourcePath string
	TargetPath string
}

func (s *Server) handleSwarmReplicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service is not configured"))
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)

	var req swarmReplicateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	targetMode := workspace.NormalizeReplicationTargetMode(req.Mode)
	if targetMode == "" {
		writeError(w, http.StatusBadRequest, errors.New("mode must be local or remote"))
		return
	}
	swarmName := strings.TrimSpace(req.SwarmName)
	if targetMode == workspace.ReplicationTargetModeLocal && swarmName == "" {
		writeError(w, http.StatusBadRequest, errors.New("swarm_name is required"))
		return
	}
	if targetMode == workspace.ReplicationTargetModeLocal && s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service is not configured"))
		return
	}
	targetHostSwarmID := strings.TrimSpace(req.TargetHostSwarmID)
	if targetHostSwarmID != "" && targetMode != workspace.ReplicationTargetModeLocal {
		writeError(w, http.StatusBadRequest, errors.New("target_host_swarm_id is only supported for local container creation"))
		return
	}
	if len(req.Workspaces) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("at least one workspace is required"))
		return
	}

	normalizedWorkspaces, err := s.workspace.NormalizeReplicationWorkspaces(mapReplicateWorkspaceInputs(req.Workspaces))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	syncConfig := workspace.NormalizeReplicationSync(workspace.ReplicationSyncInput{
		Enabled: req.Sync.Enabled,
		Mode:    req.Sync.Mode,
		Modules: req.Sync.Modules,
	})

	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stateCfg := cfg
	if strings.TrimSpace(stateCfg.SwarmName) == "" {
		stateCfg.SwarmName = defaultOnboardingSwarmName(stateCfg)
		if err := s.persistUISwarmName(stateCfg.SwarmName); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	state, err := s.currentSwarmState(stateCfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	targetHostIsLocal := targetHostSwarmID == "" || strings.EqualFold(targetHostSwarmID, "local") || strings.EqualFold(targetHostSwarmID, "self") || strings.EqualFold(targetHostSwarmID, localSwarmID)
	var targetHost *swarmTarget
	peerToken := ""
	if !targetHostIsLocal {
		resolved, _, token, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetHostSwarmID), targetHostSwarmID)
		if err != nil {
			writeError(w, status, err)
			return
		}
		targetHost = resolved
		peerToken = token
	}
	workspaceCatalog, err := s.replicateWorkspaceCatalogForPrincipal(principal, normalizedWorkspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if targetMode == workspace.ReplicationTargetModeRemote {
		response, statusCode, err := s.replicateToRemoteSwarm(r, normalizedWorkspaces, workspaceCatalog, syncConfig)
		if err != nil {
			writeError(w, statusCode, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service is not configured"))
		return
	}

	groupID := ""
	groupName := ""
	groupNetworkName := ""
	if targetHostIsLocal {
		groupID = strings.TrimSpace(state.CurrentGroupID)
		if groupID == "" {
			writeError(w, http.StatusBadRequest, errors.New("current swarm group is not selected"))
			return
		}
		groupName, groupNetworkName = lookupCurrentGroupDetails(state, groupID)
	}

	mounts, childWorkspacePaths, bootstrap := buildReplicationPlan(normalizedWorkspaces, workspaceCatalog, syncConfig)
	if !targetHostIsLocal {
		if targetHost == nil {
			writeError(w, http.StatusBadRequest, errors.New("managed host target was not resolved"))
			return
		}
		managedHostWorkspacePaths, materializeErr := s.materializeManagedHostReplicationWorkspaces(r.Context(), *targetHost, localSwarmID, peerToken, normalizedWorkspaces, workspaceCatalog, syncConfig)
		if materializeErr != nil {
			writeError(w, http.StatusBadGateway, materializeErr)
			return
		}
		mounts = rewriteReplicationMountsForTargetHost(mounts, managedHostWorkspacePaths)
		bootstrap = rewriteReplicationBootstrapForTargetHost(bootstrap, managedHostWorkspacePaths)
	}
	containerPackages := deployruntime.ContainerPackageManifest{}
	if cfg.DevMode {
		containerPackages = mapReplicateContainerPackagesInput(req.ContainerPackages)
	}
	createPayload := deployContainerCreatePayload{
		Name:               swarmName,
		Runtime:            strings.TrimSpace(req.Runtime),
		BypassPermissions:  req.BypassPermissions,
		AlwaysOn:           req.AlwaysOn,
		GroupID:            groupID,
		GroupName:          groupName,
		GroupNetworkName:   groupNetworkName,
		SyncEnabled:        syncConfig.Enabled,
		SyncMode:           syncConfig.Mode,
		SyncModules:        append([]string(nil), syncConfig.Modules...),
		SyncVaultPassword:  strings.TrimSpace(req.Sync.VaultPassword),
		Mounts:             mounts,
		WorkspaceBootstrap: bootstrap,
		ContainerPackages:  containerPackages,
	}
	deployment, err := s.createReplicatedContainer(ctx, targetHost, createPayload, targetHostIsLocal)
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "start ") {
			statusCode = http.StatusConflict
		}
		writeJSON(w, statusCode, map[string]any{
			"ok":           false,
			"path_id":      "swarm.replicate.v1",
			"deployment":   deployment,
			"failure":      replicateFailureDetails(deployment),
			"error":        err.Error(),
			"error_detail": replicateFailureSummary(err.Error(), deployment),
		})
		return
	}
	deployment, err = s.waitForReplicatedSwarmAttach(context.Background(), deployment.ID, 20*time.Second)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":           false,
			"path_id":      "swarm.replicate.v1",
			"deployment":   deployment,
			"failure":      replicateFailureDetails(deployment),
			"error":        err.Error(),
			"error_detail": replicateFailureSummary(err.Error(), deployment),
		})
		return
	}
	childSwarmID := strings.TrimSpace(deployment.ChildSwarmID)
	if childSwarmID == "" {
		message := "replicated swarm did not finish attaching to the host yet"
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":           false,
			"path_id":      "swarm.replicate.v1",
			"deployment":   deployment,
			"failure":      replicateFailureDetails(deployment),
			"error":        message,
			"error_detail": replicateFailureSummary(message, deployment),
		})
		return
	}
	childSwarmName := firstNonEmpty(strings.TrimSpace(deployment.ChildDisplayName), swarmName, childSwarmID)

	response := swarmReplicateResponse{
		OK: true,
		Swarm: swarmReplicateSwarmResponse{
			ID:                childSwarmID,
			Name:              childSwarmName,
			Mode:              targetMode,
			DeploymentID:      strings.TrimSpace(deployment.ID),
			AttachStatus:      strings.TrimSpace(deployment.AttachStatus),
			GroupID:           groupID,
			BypassPermissions: deployment.BypassPermissions,
		},
		Workspaces: make([]swarmReplicateWorkspaceResponse, 0, len(normalizedWorkspaces)),
	}
	for _, normalized := range normalizedWorkspaces {
		linkTargetKind := targetMode
		linkTargetSwarmID := childSwarmID
		if !targetHostIsLocal && targetHost != nil {
			linkTargetKind = "host"
			linkTargetSwarmID = strings.TrimSpace(targetHost.SwarmID)
		}
		bindingTargetSwarmID := linkTargetSwarmID
		bindingHostSwarmID := ""
		bindingContainerID := ""
		if targetHostIsLocal {
			bindingTargetSwarmID = childSwarmID
		} else if targetHost != nil {
			bindingTargetSwarmID = childSwarmID
			bindingHostSwarmID = strings.TrimSpace(targetHost.SwarmID)
			bindingContainerID = strings.TrimSpace(deployment.HostContainerID)
		}
		binding := pebblestore.TopologyWorkspaceBindingRecord{
			BindingID:                 pebblestore.CanonicalTopologyWorkspaceBindingID(strings.TrimSpace(deployment.ID), normalized.SourceWorkspacePath),
			SourceWorkspacePath:       normalized.SourceWorkspacePath,
			SourceWorkspaceName:       workspaceCatalog[normalized.SourceWorkspacePath].Name,
			DestinationRuntimeSwarmID: bindingTargetSwarmID,
			DestinationHostSwarmID:    bindingHostSwarmID,
			DestinationContainerID:    bindingContainerID,
			DestinationWorkspacePath:  childWorkspacePaths[normalized.SourceWorkspacePath],
			ReplicationMode:           normalized.ReplicationMode,
			Writable:                  normalized.Writable,
			Sync: pebblestore.WorkspaceReplicationSync{
				Enabled: syncConfig.Enabled,
				Mode:    syncConfig.Mode,
				Modules: append([]string(nil), syncConfig.Modules...),
			},
			LegacyTargetKind: linkTargetKind,
			CreatedAt:        time.Now().UnixMilli(),
		}
		binding.UpdatedAt = binding.CreatedAt
		storedBinding, err := s.topology.UpsertWorkspaceBinding(binding)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		binding = storedBinding
		response.Workspaces = append(response.Workspaces, swarmReplicateWorkspaceResponse{
			SourceWorkspacePath: normalized.SourceWorkspacePath,
			SourceWorkspaceName: workspaceCatalog[normalized.SourceWorkspacePath].Name,
			Binding:             binding,
		})
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) materializeManagedHostReplicationWorkspaces(ctx context.Context, target swarmTarget, localSwarmID, peerToken string, workspaces []workspace.NormalizedReplicationWorkspace, catalog map[string]replicateWorkspaceCatalogEntry, syncConfig workspace.NormalizedReplicationSync) (map[string]string, error) {
	remoteWorkspaces, err := s.discoverPeerWorkspaces(ctx, target, localSwarmID, peerToken)
	if err != nil {
		return nil, err
	}
	remotePaths := make(map[string]string, len(workspaces))
	for _, normalized := range workspaces {
		workspaceCatalog := catalog[normalized.SourceWorkspacePath]
		remotePath := matchingPeerWorkspacePath(remoteWorkspaces, normalized.SourceWorkspacePath, workspaceCatalog)
		if remotePath == "" {
			if normalized.ReplicationMode != workspace.ReplicationModeBundle {
				return nil, fmt.Errorf("managed host container creation for workspace %q currently supports git bundle mode only; archive copy is not implemented", normalized.SourceWorkspacePath)
			}
			if !normalized.GitWorkspace {
				return nil, fmt.Errorf("managed host container creation for workspace %q requires a git workspace", normalized.SourceWorkspacePath)
			}
			remotePath, err = s.importWorkspaceBundleToPeer(ctx, target, localSwarmID, peerToken, normalized, workspaceCatalog)
			if err != nil {
				return nil, err
			}
			remoteWorkspaces = append(remoteWorkspaces, peerWorkspaceInfo{Path: remotePath, Name: workspaceCatalog.Name, GitWorkspace: true})
		}
		remotePaths[normalized.SourceWorkspacePath] = remotePath
		_ = syncConfig
	}
	return remotePaths, nil
}

func rewriteReplicationMountsForTargetHost(mounts []localcontainers.Mount, remotePaths map[string]string) []localcontainers.Mount {
	out := make([]localcontainers.Mount, 0, len(mounts))
	for _, mount := range mounts {
		next := mount
		if remotePath := strings.TrimSpace(remotePaths[mount.WorkspacePath]); remotePath != "" {
			if strings.EqualFold(strings.TrimSpace(mount.SourcePath), strings.TrimSpace(mount.WorkspacePath)) {
				next.SourcePath = remotePath
			} else if rel, err := filepath.Rel(strings.TrimSpace(mount.WorkspacePath), strings.TrimSpace(mount.SourcePath)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				next.SourcePath = filepath.Join(remotePath, rel)
			}
			next.WorkspacePath = remotePath
		}
		out = append(out, next)
	}
	return out
}

func rewriteReplicationBootstrapForTargetHost(items []deployruntime.ContainerWorkspaceBootstrap, remotePaths map[string]string) []deployruntime.ContainerWorkspaceBootstrap {
	out := make([]deployruntime.ContainerWorkspaceBootstrap, 0, len(items))
	for _, item := range items {
		next := item
		if remotePath := strings.TrimSpace(remotePaths[item.SourceWorkspacePath]); remotePath != "" {
			originalSource := strings.TrimSpace(item.SourceWorkspacePath)
			originalTarget := strings.TrimSpace(item.TargetWorkspacePath)
			if originalTarget == "" {
				originalTarget = originalSource
			}
			// The managed host must launch the container from its own materialized
			// workspace path, but the child must register the path where that workspace
			// is mounted inside the container. Keep those namespaces separate:
			// SourceWorkspacePath = managed-host filesystem path used for replication
			// metadata; TargetWorkspacePath = child/container filesystem path.
			next.SourceWorkspacePath = remotePath
			next.TargetWorkspacePath = originalTarget
			for index, directory := range next.Directories {
				originalDirectorySource := strings.TrimSpace(directory.SourcePath)
				originalDirectoryTarget := strings.TrimSpace(directory.TargetPath)
				if originalDirectoryTarget == "" {
					originalDirectoryTarget = originalDirectorySource
				}
				if rel, err := filepath.Rel(originalSource, originalDirectorySource); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					next.Directories[index].SourcePath = filepath.Join(remotePath, rel)
				}
				next.Directories[index].TargetPath = originalDirectoryTarget
			}
		}
		out = append(out, next)
	}
	return out
}

func (s *Server) replicateToRemoteSwarm(r *http.Request, workspaces []workspace.NormalizedReplicationWorkspace, catalog map[string]replicateWorkspaceCatalogEntry, syncConfig workspace.NormalizedReplicationSync) (swarmReplicateResponse, int, error) {
	target, err := s.currentRemoteSwarmTargetForRequest(r)
	if err != nil {
		return swarmReplicateResponse{}, http.StatusBadRequest, err
	}
	if target == nil {
		return swarmReplicateResponse{}, http.StatusBadRequest, errors.New("select a managed swarm target before using remote replication")
	}
	if strings.TrimSpace(target.SwarmID) == "" || strings.TrimSpace(target.BackendURL) == "" {
		return swarmReplicateResponse{}, http.StatusBadRequest, errors.New("selected managed swarm target is missing route details")
	}
	if strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManager) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") {
		return swarmReplicateResponse{}, http.StatusBadRequest, errors.New("remote replication target must be a managed host, not its manager")
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, *target)
	if err != nil {
		return swarmReplicateResponse{}, http.StatusBadRequest, err
	}
	if s.topology == nil {
		return swarmReplicateResponse{}, http.StatusInternalServerError, errors.New("topology service is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return swarmReplicateResponse{}, http.StatusInternalServerError, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return swarmReplicateResponse{}, http.StatusInternalServerError, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		return swarmReplicateResponse{}, http.StatusInternalServerError, errors.New("local swarm id is not configured")
	}
	remoteWorkspaces, err := s.discoverPeerWorkspaces(r.Context(), *target, localSwarmID, peerToken)
	if err != nil {
		return swarmReplicateResponse{}, http.StatusBadGateway, err
	}

	response := swarmReplicateResponse{
		OK: true,
		Swarm: swarmReplicateSwarmResponse{
			ID:           strings.TrimSpace(target.SwarmID),
			Name:         firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(target.SwarmID)),
			Mode:         workspace.ReplicationTargetModeRemote,
			AttachStatus: strings.TrimSpace(target.AttachStatus),
		},
		Workspaces: make([]swarmReplicateWorkspaceResponse, 0, len(workspaces)),
	}
	for _, normalized := range workspaces {
		workspaceCatalog := catalog[normalized.SourceWorkspacePath]
		remotePath := matchingPeerWorkspacePath(remoteWorkspaces, normalized.SourceWorkspacePath, workspaceCatalog)
		if remotePath == "" {
			if normalized.ReplicationMode != workspace.ReplicationModeBundle {
				return swarmReplicateResponse{}, http.StatusBadRequest, fmt.Errorf("remote replication for workspace %q currently supports git bundle mode only; archive copy is not implemented", normalized.SourceWorkspacePath)
			}
			if !normalized.GitWorkspace {
				return swarmReplicateResponse{}, http.StatusBadRequest, fmt.Errorf("remote replication for workspace %q requires a git workspace", normalized.SourceWorkspacePath)
			}
			remotePath, err = s.importWorkspaceBundleToPeer(r.Context(), *target, localSwarmID, peerToken, normalized, workspaceCatalog)
			if err != nil {
				return swarmReplicateResponse{}, http.StatusBadGateway, err
			}
		}
		binding, err := s.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
			BindingID:                 pebblestore.CanonicalTopologyWorkspaceBindingID(strings.TrimSpace(target.SwarmID), normalized.SourceWorkspacePath),
			SourceWorkspacePath:       normalized.SourceWorkspacePath,
			SourceWorkspaceName:       firstNonEmpty(strings.TrimSpace(workspaceCatalog.Name), defaultReplicatedWorkspaceName(normalized.SourceWorkspacePath)),
			DestinationRuntimeSwarmID: strings.TrimSpace(target.SwarmID),
			DestinationWorkspacePath:  remotePath,
			ReplicationMode:           normalized.ReplicationMode,
			Writable:                  normalized.Writable,
			Sync: pebblestore.WorkspaceReplicationSync{
				Enabled: syncConfig.Enabled,
				Mode:    syncConfig.Mode,
				Modules: append([]string(nil), syncConfig.Modules...),
			},
			LegacyTargetKind: workspace.ReplicationTargetModeRemote,
		})
		if err != nil {
			return swarmReplicateResponse{}, http.StatusInternalServerError, err
		}
		addWorkspaceReplicationResponse(&response, normalized, workspaceCatalog, binding)
	}
	return response, http.StatusOK, nil
}

func (s *Server) discoverPeerWorkspaces(ctx context.Context, target swarmTarget, localSwarmID, peerToken string) ([]peerWorkspaceInfo, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerWorkspaceDiscoverPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("managed workspace discover failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload peerWorkspaceDiscoverResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Workspaces, nil
}

func (s *Server) importWorkspaceBundleToPeer(ctx context.Context, target swarmTarget, localSwarmID, peerToken string, source workspace.NormalizedReplicationWorkspace, catalog replicateWorkspaceCatalogEntry) (string, error) {
	bundlePath, err := createGitBundle(ctx, source.SourceWorkspacePath)
	if err != nil {
		return "", err
	}
	defer os.Remove(bundlePath)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("source_workspace_path", source.SourceWorkspacePath)
	_ = writer.WriteField("workspace_name", firstNonEmpty(strings.TrimSpace(catalog.Name), defaultReplicatedWorkspaceName(source.SourceWorkspacePath)))
	_ = writer.WriteField("replication_mode", source.ReplicationMode)
	part, err := writer.CreateFormFile("bundle", filepath.Base(bundlePath))
	if err != nil {
		return "", err
	}
	file, err := os.Open(bundlePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		file.Close()
		return "", err
	}
	file.Close()
	if err := writer.Close(); err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerWorkspaceImportBundlePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("managed workspace bundle import failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload peerWorkspaceImportBundleResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	remotePath := strings.TrimSpace(payload.WorkspacePath)
	if remotePath == "" {
		return "", errors.New("managed workspace bundle import did not return a workspace path")
	}
	return remotePath, nil
}

func createGitBundle(ctx context.Context, workspacePath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", errors.New("workspace path is required")
	}
	file, err := os.CreateTemp("", "swarm-workspace-*.bundle")
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
		return "", fmt.Errorf("create git bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return bundlePath, nil
}

func matchingPeerWorkspacePath(workspaces []peerWorkspaceInfo, sourcePath string, catalog replicateWorkspaceCatalogEntry) string {
	sourceBase := strings.TrimSpace(filepath.Base(strings.TrimSpace(sourcePath)))
	catalogName := strings.TrimSpace(catalog.Name)
	for _, candidate := range workspaces {
		candidatePath := strings.TrimSpace(candidate.Path)
		if candidatePath == "" {
			continue
		}
		if strings.EqualFold(candidatePath, strings.TrimSpace(sourcePath)) {
			return candidatePath
		}
		candidateName := strings.TrimSpace(candidate.Name)
		candidateBase := strings.TrimSpace(filepath.Base(candidatePath))
		if catalogName != "" && strings.EqualFold(candidateName, catalogName) {
			return candidatePath
		}
		if sourceBase != "" && strings.EqualFold(candidateBase, sourceBase) {
			return candidatePath
		}
	}
	return ""
}

func (s *Server) waitForReplicatedSwarmAttach(ctx context.Context, deploymentID string, timeout time.Duration) (deployruntime.ContainerDeployment, error) {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return deployruntime.ContainerDeployment{}, errors.New("deployment id is required")
	}
	deadline := time.Now().Add(timeout)
	for {
		deployments, err := s.deployContainers.List(ctx)
		if err != nil {
			return deployruntime.ContainerDeployment{}, err
		}
		var current deployruntime.ContainerDeployment
		found := false
		for _, deployment := range deployments {
			if strings.TrimSpace(deployment.ID) != deploymentID {
				continue
			}
			current = deployment
			found = true
			break
		}
		if found {
			if strings.EqualFold(strings.TrimSpace(current.AttachStatus), "attached") && strings.TrimSpace(current.ChildSwarmID) != "" {
				return current, nil
			}
			if strings.EqualFold(strings.TrimSpace(current.AttachStatus), "failed") {
				message := strings.TrimSpace(current.LastAttachError)
				if message == "" {
					message = "replicated swarm attach failed"
				}
				return current, errors.New(message)
			}
			if time.Now().After(deadline) {
				return current, errors.New("replicated swarm did not finish attaching to the host yet")
			}
		} else if time.Now().After(deadline) {
			return deployruntime.ContainerDeployment{}, errors.New("replicated swarm deployment disappeared before attach completed")
		}
		select {
		case <-ctx.Done():
			return deployruntime.ContainerDeployment{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func replicateFailureDetails(deployment deployruntime.ContainerDeployment) swarmReplicateFailureDetails {
	return swarmReplicateFailureDetails{
		DeploymentID:    strings.TrimSpace(deployment.ID),
		AttachStatus:    strings.TrimSpace(deployment.AttachStatus),
		LastAttachError: strings.TrimSpace(deployment.LastAttachError),
		Runtime:         strings.TrimSpace(deployment.Runtime),
		ContainerName:   strings.TrimSpace(deployment.ContainerName),
		BackendHostPort: deployment.BackendHostPort,
		DesktopHostPort: deployment.DesktopHostPort,
		ChildBackendURL: strings.TrimSpace(deployment.ChildBackendURL),
		ChildDesktopURL: strings.TrimSpace(deployment.ChildDesktopURL),
	}
}

func replicateFailureSummary(message string, deployment deployruntime.ContainerDeployment) string {
	message = strings.TrimSpace(message)
	parts := make([]string, 0, 9)
	if message != "" {
		parts = append(parts, message)
	}
	if attachStatus := strings.TrimSpace(deployment.AttachStatus); attachStatus != "" {
		parts = append(parts, fmt.Sprintf("attach status: %s", attachStatus))
	}
	if lastAttachError := strings.TrimSpace(deployment.LastAttachError); lastAttachError != "" && lastAttachError != message {
		parts = append(parts, fmt.Sprintf("last attach error: %s", lastAttachError))
	}
	if runtimeName := strings.TrimSpace(deployment.Runtime); runtimeName != "" {
		parts = append(parts, fmt.Sprintf("runtime: %s", runtimeName))
	}
	if containerName := strings.TrimSpace(deployment.ContainerName); containerName != "" {
		parts = append(parts, fmt.Sprintf("container: %s", containerName))
	}
	if deployment.BackendHostPort > 0 {
		parts = append(parts, fmt.Sprintf("backend port: %d", deployment.BackendHostPort))
	}
	if deployment.DesktopHostPort > 0 {
		parts = append(parts, fmt.Sprintf("desktop port: %d", deployment.DesktopHostPort))
	}
	if childBackendURL := strings.TrimSpace(deployment.ChildBackendURL); childBackendURL != "" {
		parts = append(parts, fmt.Sprintf("child backend: %s", childBackendURL))
	}
	if childDesktopURL := strings.TrimSpace(deployment.ChildDesktopURL); childDesktopURL != "" {
		parts = append(parts, fmt.Sprintf("child desktop: %s", childDesktopURL))
	}
	return strings.Join(parts, "\n")
}

func mapReplicateWorkspaceInputs(inputs []swarmReplicateWorkspaceRequest) []workspace.ReplicationWorkspaceInput {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]workspace.ReplicationWorkspaceInput, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, workspace.ReplicationWorkspaceInput{
			SourceWorkspacePath: input.SourceWorkspacePath,
			ReplicationMode:     input.ReplicationMode,
			Writable:            input.Writable,
		})
	}
	return out
}

func mapReplicateContainerPackagesInput(input swarmReplicateContainerPackagesRequest) deployruntime.ContainerPackageManifest {
	packages := make([]deployruntime.ContainerPackageSelection, 0, len(input.Packages))
	for _, pkg := range input.Packages {
		packages = append(packages, deployruntime.ContainerPackageSelection{
			Name:   pkg.Name,
			Source: pkg.Source,
			Reason: pkg.Reason,
		})
	}
	return deployruntime.ContainerPackageManifest{
		BaseImage:      input.BaseImage,
		PackageManager: input.PackageManager,
		Packages:       packages,
	}
}

func (s *Server) replicateWorkspaceCatalogForPrincipal(principal identity.Principal, workspaces []workspace.NormalizedReplicationWorkspace) (map[string]replicateWorkspaceCatalogEntry, error) {
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, identity.ErrPrincipalRequired
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		return nil, err
	}
	out := make(map[string]replicateWorkspaceCatalogEntry, len(workspaces))
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			continue
		}
		name := strings.TrimSpace(entry.WorkspaceName)
		if name == "" {
			name = defaultReplicatedWorkspaceName(path)
		}
		directories := make([]string, 0, len(entry.Directories))
		for _, directory := range entry.Directories {
			if trimmed := strings.TrimSpace(directory); trimmed != "" {
				directories = append(directories, trimmed)
			}
		}
		out[path] = replicateWorkspaceCatalogEntry{
			Name:        name,
			ThemeID:     strings.TrimSpace(entry.ThemeID),
			Directories: directories,
			Active:      entry.Active,
		}
	}
	for _, item := range workspaces {
		if _, ok := out[item.SourceWorkspacePath]; !ok {
			out[item.SourceWorkspacePath] = replicateWorkspaceCatalogEntry{
				Name:        defaultReplicatedWorkspaceName(item.SourceWorkspacePath),
				Directories: []string{item.SourceWorkspacePath},
			}
		}
	}
	return out, nil
}

func buildReplicationPlan(workspaces []workspace.NormalizedReplicationWorkspace, workspaceCatalog map[string]replicateWorkspaceCatalogEntry, syncConfig workspace.NormalizedReplicationSync) ([]localcontainers.Mount, map[string]string, []deployruntime.ContainerWorkspaceBootstrap) {
	if len(workspaces) == 0 {
		return nil, nil, nil
	}
	mounts := make([]localcontainers.Mount, 0, len(workspaces)*2)
	childPaths := make(map[string]string, len(workspaces))
	bootstraps := make([]deployruntime.ContainerWorkspaceBootstrap, 0, len(workspaces))
	selectedWorkspaceTargets := assignReplicationWorkspaceTargets(workspaces, workspaceCatalog)
	selectedWorkspaceTargetsBySource := make(map[string]replicationTargetAssignment, len(selectedWorkspaceTargets))
	usedTargets := make(map[string]int, len(workspaces)*2)
	for _, assigned := range selectedWorkspaceTargets {
		selectedWorkspaceTargetsBySource[assigned.SourcePath] = assigned
		if target := strings.TrimSpace(assigned.TargetPath); target != "" {
			usedTargets[target]++
		}
	}
	anyCurrent := false
	for _, item := range workspaces {
		catalog := workspaceCatalog[item.SourceWorkspacePath]
		name := strings.TrimSpace(catalog.Name)
		targetPath := strings.TrimSpace(selectedWorkspaceTargetsBySource[item.SourceWorkspacePath].TargetPath)
		mode := pebblestore.ContainerMountModeReadWrite
		if !item.Writable {
			mode = pebblestore.ContainerMountModeReadOnly
		}
		mounts = append(mounts, localcontainers.Mount{
			SourcePath:    item.SourceWorkspacePath,
			TargetPath:    targetPath,
			Mode:          mode,
			WorkspacePath: item.SourceWorkspacePath,
			WorkspaceName: name,
		})
		childPaths[item.SourceWorkspacePath] = targetPath
		directories := make([]deployruntime.ContainerWorkspaceBootstrapDirectory, 0, len(catalog.Directories))
		for dirIndex, directory := range catalog.Directories {
			directory = strings.TrimSpace(directory)
			if directory == "" || directory == item.SourceWorkspacePath {
				continue
			}
			directoryTarget := ""
			if assigned, ok := selectedWorkspaceTargetsBySource[directory]; ok {
				directoryTarget = strings.TrimSpace(assigned.TargetPath)
			}
			if directoryTarget == "" {
				directoryTarget = nextReplicationDirectoryTargetPath(name, directory, dirIndex, usedTargets)
			}
			mounts = append(mounts, localcontainers.Mount{
				SourcePath:    directory,
				TargetPath:    directoryTarget,
				Mode:          mode,
				WorkspacePath: item.SourceWorkspacePath,
				WorkspaceName: name,
			})
			directories = append(directories, deployruntime.ContainerWorkspaceBootstrapDirectory{
				SourcePath: directory,
				TargetPath: directoryTarget,
			})
		}
		makeCurrent := catalog.Active
		if makeCurrent {
			anyCurrent = true
		}
		bootstraps = append(bootstraps, deployruntime.ContainerWorkspaceBootstrap{
			SourceWorkspacePath: item.SourceWorkspacePath,
			SourceWorkspaceName: name,
			TargetWorkspacePath: targetPath,
			ThemeID:             catalog.ThemeID,
			Directories:         directories,
			ReplicationMode:     item.ReplicationMode,
			Writable:            item.Writable,
			Sync: pebblestore.WorkspaceReplicationSync{
				Enabled: syncConfig.Enabled,
				Mode:    syncConfig.Mode,
				Modules: append([]string(nil), syncConfig.Modules...),
			},
			MakeCurrent: makeCurrent,
		})
	}
	if !anyCurrent && len(bootstraps) > 0 {
		bootstraps[0].MakeCurrent = true
	}
	return mounts, childPaths, bootstraps
}

func assignReplicationWorkspaceTargets(workspaces []workspace.NormalizedReplicationWorkspace, workspaceCatalog map[string]replicateWorkspaceCatalogEntry) []replicationTargetAssignment {
	usedTargets := make(map[string]int, len(workspaces)*2)
	out := make([]replicationTargetAssignment, 0, len(workspaces))
	for index, item := range workspaces {
		catalog := workspaceCatalog[item.SourceWorkspacePath]
		name := strings.TrimSpace(catalog.Name)
		out = append(out, replicationTargetAssignment{
			SourcePath: item.SourceWorkspacePath,
			TargetPath: nextReplicationTargetPath(name, item.SourceWorkspacePath, index, usedTargets),
		})
	}
	return out
}

func nextReplicationTargetPath(name, sourcePath string, index int, used map[string]int) string {
	base := sanitizeReplicationMountName(firstNonEmpty(name, filepath.Base(strings.TrimSpace(sourcePath))))
	if base == "" {
		base = fmt.Sprintf("workspace-%d", index+1)
	}
	return nextReplicationMountPath(base, used)
}

func nextReplicationDirectoryTargetPath(workspaceName, sourcePath string, index int, used map[string]int) string {
	base := sanitizeReplicationMountName(firstNonEmpty(filepath.Base(strings.TrimSpace(sourcePath)), fmt.Sprintf("dir-%d", index+1)))
	workspaceBase := sanitizeReplicationMountName(workspaceName)
	candidateBase := strings.Trim(strings.Join([]string{workspaceBase, "dir", base}, "-"), "-")
	if candidateBase == "" {
		candidateBase = fmt.Sprintf("workspace-dir-%d", index+1)
	}
	return nextReplicationMountPath(candidateBase, used)
}

func nextReplicationMountPath(base string, used map[string]int) string {
	candidate := filepath.ToSlash(filepath.Join(replicateWorkspaceMountRoot, base))
	if count := used[candidate]; count > 0 {
		candidate = filepath.ToSlash(filepath.Join(replicateWorkspaceMountRoot, fmt.Sprintf("%s-%d", base, count+1)))
	}
	used[candidate]++
	return candidate
}

func sanitizeReplicationMountName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultReplicatedWorkspaceName(path string) string {
	name := strings.TrimSpace(filepath.Base(strings.TrimSpace(path)))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "workspace"
	}
	return name
}

func lookupCurrentGroupDetails(state swarmruntime.LocalState, groupID string) (string, string) {
	groupID = strings.TrimSpace(groupID)
	for _, item := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(item.Group.ID), groupID) {
			continue
		}
		return strings.TrimSpace(item.Group.Name), strings.TrimSpace(item.Group.NetworkName)
	}
	return "", ""
}
