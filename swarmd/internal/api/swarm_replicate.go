package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	deployruntime "swarm/packages/swarmd/internal/deploy"
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
	Runtime           string                                 `json:"runtime,omitempty"`
	BypassPermissions bool                                   `json:"bypass_permissions,omitempty"`
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
	SourceWorkspacePath string                               `json:"source_workspace_path"`
	SourceWorkspaceName string                               `json:"source_workspace_name"`
	Link                pebblestore.WorkspaceReplicationLink `json:"link"`
}

type replicateWorkspaceCatalogEntry struct {
	Name        string
	ThemeID     string
	Directories []string
	Active      bool
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
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service is not configured"))
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}

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
	if targetMode != workspace.ReplicationTargetModeLocal {
		writeError(w, http.StatusBadRequest, errors.New("remote replication is not implemented yet"))
		return
	}

	swarmName := strings.TrimSpace(req.SwarmName)
	if swarmName == "" {
		writeError(w, http.StatusBadRequest, errors.New("swarm_name is required"))
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
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	groupID := strings.TrimSpace(state.CurrentGroupID)
	if groupID == "" {
		writeError(w, http.StatusBadRequest, errors.New("current swarm group is not selected"))
		return
	}
	groupName, groupNetworkName := lookupCurrentGroupDetails(state, groupID)

	workspaceCatalog, err := s.replicateWorkspaceCatalog(normalizedWorkspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	mounts, childWorkspacePaths, bootstrap := buildReplicationPlan(normalizedWorkspaces, workspaceCatalog, syncConfig)
	deployment, err := s.deployContainers.Create(context.Background(), deployruntime.ContainerCreateInput{
		Name:               swarmName,
		Runtime:            strings.TrimSpace(req.Runtime),
		BypassPermissions:  req.BypassPermissions,
		GroupID:            groupID,
		GroupName:          groupName,
		GroupNetworkName:   groupNetworkName,
		SyncEnabled:        syncConfig.Enabled,
		SyncMode:           syncConfig.Mode,
		SyncModules:        append([]string(nil), syncConfig.Modules...),
		SyncVaultPassword:  strings.TrimSpace(req.Sync.VaultPassword),
		Mounts:             mounts,
		WorkspaceBootstrap: bootstrap,
		ContainerPackages:  mapReplicateContainerPackagesInput(req.ContainerPackages),
	})
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
		storedLink, linkErr := s.workspace.AddReplicationLink(normalized.SourceWorkspacePath, pebblestore.WorkspaceReplicationLink{
			TargetKind:          targetMode,
			TargetSwarmID:       childSwarmID,
			TargetSwarmName:     childSwarmName,
			TargetWorkspacePath: childWorkspacePaths[normalized.SourceWorkspacePath],
			ReplicationMode:     normalized.ReplicationMode,
			Writable:            normalized.Writable,
			Sync: pebblestore.WorkspaceReplicationSync{
				Enabled: syncConfig.Enabled,
				Mode:    syncConfig.Mode,
				Modules: append([]string(nil), syncConfig.Modules...),
			},
		})
		if linkErr != nil {
			writeError(w, http.StatusInternalServerError, linkErr)
			return
		}
		response.Workspaces = append(response.Workspaces, swarmReplicateWorkspaceResponse{
			SourceWorkspacePath: normalized.SourceWorkspacePath,
			SourceWorkspaceName: workspaceCatalog[normalized.SourceWorkspacePath].Name,
			Link:                storedLink,
		})
	}

	writeJSON(w, http.StatusOK, response)
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

func (s *Server) replicateWorkspaceCatalog(workspaces []workspace.NormalizedReplicationWorkspace) (map[string]replicateWorkspaceCatalogEntry, error) {
	entries, err := s.workspace.ListKnown(100000)
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
	usedTargets := make(map[string]int, len(workspaces)*2)
	anyCurrent := false
	for index, item := range workspaces {
		catalog := workspaceCatalog[item.SourceWorkspacePath]
		name := strings.TrimSpace(catalog.Name)
		targetPath := nextReplicationTargetPath(name, item.SourceWorkspacePath, index, usedTargets)
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
			directoryTarget := nextReplicationDirectoryTargetPath(name, directory, dirIndex, usedTargets)
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
