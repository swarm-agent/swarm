package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) createReplicatedContainer(ctx context.Context, targetHost *swarmTarget, payload deployContainerCreatePayload, targetHostIsLocal bool) (deployruntime.ContainerDeployment, error) {
	if targetHostIsLocal {
		return s.deployContainers.Create(ctx, deployruntime.ContainerCreateInput{
			DeploymentID:       payload.DeploymentID,
			Name:               payload.Name,
			Runtime:            payload.Runtime,
			Image:              payload.Image,
			GroupID:            payload.GroupID,
			GroupName:          payload.GroupName,
			GroupNetworkName:   payload.GroupNetworkName,
			SyncEnabled:        payload.SyncEnabled,
			SyncMode:           payload.SyncMode,
			SyncModules:        payload.SyncModules,
			SyncVaultPassword:  payload.SyncVaultPassword,
			BypassPermissions:  payload.BypassPermissions,
			AlwaysOn:           payload.AlwaysOn,
			WorkspaceBootstrap: payload.WorkspaceBootstrap,
			ContainerPackages:  payload.ContainerPackages,
			Mounts:             payload.Mounts,
		})
	}
	if targetHost == nil {
		return deployruntime.ContainerDeployment{}, errors.New("managed host target was not resolved")
	}
	// The target host owns container launch and child attachment. Do not send the
	// Primary's swarm group to the managed host: that group is only valid in the
	// Primary's swarm state and is mirrored locally after the remote create.
	mirrorPayload := payload
	payload.GroupID = ""
	payload.GroupName = ""
	payload.GroupNetworkName = ""
	if strings.TrimSpace(payload.DeploymentID) == "" {
		payload.DeploymentID = suggestedReplicatedDeploymentID(payload.Name)
	}
	mirrorPayload.DeploymentID = payload.DeploymentID
	var response struct {
		OK         bool                              `json:"ok"`
		Deployment deployruntime.ContainerDeployment `json:"deployment"`
		Error      string                            `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, *targetHost, "/v1/deploy/container/create", payload, &response); err != nil {
		return response.Deployment, err
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) != "" {
			return response.Deployment, errors.New(strings.TrimSpace(response.Error))
		}
		return response.Deployment, errors.New("managed host container create failed")
	}
	deployment, err := s.mirrorManagedHostDeployment(response.Deployment, *targetHost, mirrorPayload)
	if err != nil {
		return deployment, err
	}
	return deployment, nil
}

func (s *Server) mirrorManagedHostDeployment(deployment deployruntime.ContainerDeployment, target swarmTarget, payload deployContainerCreatePayload) (deployruntime.ContainerDeployment, error) {
	if s == nil || s.deployContainers == nil {
		return deployment, errors.New("deploy container service is not configured")
	}
	mirror, ok := s.deployContainers.(interface {
		MirrorDeployment(context.Context, deployruntime.ContainerDeployment) (deployruntime.ContainerDeployment, error)
	})
	if !ok {
		return deployment, errors.New("deploy container mirror service is not configured")
	}
	deployment.ID = firstNonEmptyString(strings.TrimSpace(deployment.ID), strings.TrimSpace(payload.DeploymentID))
	deployment.Kind = firstNonEmptyString(strings.TrimSpace(deployment.Kind), "container")
	deployment.Name = firstNonEmptyString(strings.TrimSpace(deployment.Name), strings.TrimSpace(payload.Name))
	deployment.Status = firstNonEmptyString(strings.TrimSpace(deployment.Status), "running")
	deployment.GroupID = strings.TrimSpace(payload.GroupID)
	deployment.GroupName = strings.TrimSpace(payload.GroupName)
	deployment.GroupNetworkName = strings.TrimSpace(payload.GroupNetworkName)
	deployment.SyncEnabled = payload.SyncEnabled
	deployment.SyncMode = strings.TrimSpace(payload.SyncMode)
	deployment.SyncModules = append([]string(nil), payload.SyncModules...)
	deployment.WorkspaceBootstrap = append([]deployruntime.ContainerWorkspaceBootstrap(nil), payload.WorkspaceBootstrap...)
	deployment.ContainerPackages = payload.ContainerPackages
	deployment.BypassPermissions = payload.BypassPermissions
	deployment.AlwaysOn = payload.AlwaysOn
	deployment.HostSwarmID = strings.TrimSpace(target.SwarmID)
	deployment.HostDisplayName = firstNonEmptyString(strings.TrimSpace(target.Name), strings.TrimSpace(deployment.HostDisplayName), strings.TrimSpace(target.SwarmID))
	deployment.HostAPIBaseURL = firstNonEmptyString(strings.TrimSpace(target.BackendURL), strings.TrimSpace(deployment.HostAPIBaseURL))
	deployment.HostBackendURL = firstNonEmptyString(strings.TrimSpace(target.BackendURL), strings.TrimSpace(deployment.HostBackendURL), strings.TrimSpace(deployment.HostAPIBaseURL))
	deployment.HostDesktopURL = firstNonEmptyString(strings.TrimSpace(target.DesktopURL), strings.TrimSpace(deployment.HostDesktopURL))
	hostContainerID := canonicalHostContainerIDForManagedDeployment(deployment)
	deployment.HostContainerID = hostContainerID
	if strings.TrimSpace(deployment.ChildSwarmID) != "" && hostContainerID != "" {
		deployment.AttachmentID = pebblestore.CanonicalTopologyAttachmentID(hostContainerID, strings.TrimSpace(deployment.ChildSwarmID))
	}
	if err := s.syncManagedHostCanonicalTopology(deployment); err != nil {
		return deployment, err
	}
	return mirror.MirrorDeployment(context.Background(), deployment)
}

func canonicalHostContainerIDForManagedDeployment(deployment deployruntime.ContainerDeployment) string {
	hostSwarmID := firstNonEmptyString(strings.TrimSpace(deployment.HostSwarmID), strings.TrimSpace(deployment.SyncOwnerSwarmID))
	runtimeContainerRef := firstNonEmptyString(strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID))
	return pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef)
}

func (s *Server) syncManagedHostCanonicalTopology(deployment deployruntime.ContainerDeployment) error {
	if s == nil || s.topology == nil {
		return nil
	}
	hostContainerID := firstNonEmptyString(strings.TrimSpace(deployment.HostContainerID), canonicalHostContainerIDForManagedDeployment(deployment))
	if hostContainerID == "" {
		return nil
	}
	if err := s.topology.UpsertHostContainer(pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		HostSwarmID:         strings.TrimSpace(deployment.HostSwarmID),
		RuntimeContainerRef: firstNonEmptyString(strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID)),
		Name:                firstNonEmptyString(strings.TrimSpace(deployment.Name), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID)),
		ContainerName:       firstNonEmptyString(strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.Name), strings.TrimSpace(deployment.ID)),
		ContainerID:         strings.TrimSpace(deployment.ContainerID),
		Runtime:             strings.TrimSpace(deployment.Runtime),
		Image:               strings.TrimSpace(deployment.Image),
		Status:              firstNonEmptyString(strings.TrimSpace(deployment.AttachStatus), strings.TrimSpace(deployment.Status)),
		HostAPIBaseURL:      firstNonEmptyString(strings.TrimSpace(deployment.HostAPIBaseURL), strings.TrimSpace(deployment.HostBackendURL)),
		HostPort:            deployment.BackendHostPort,
		RuntimePort:         deployment.DesktopHostPort,
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceDeployContainer},
		CreatedAt:           deployment.CreatedAt,
		UpdatedAt:           deployment.UpdatedAt,
	}); err != nil {
		return err
	}
	childSwarmID := strings.TrimSpace(deployment.ChildSwarmID)
	if childSwarmID == "" {
		return nil
	}
	if err := s.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
		Name:                 firstNonEmptyString(strings.TrimSpace(deployment.ChildDisplayName), strings.TrimSpace(deployment.Name), childSwarmID),
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           strings.TrimSpace(deployment.ChildBackendURL),
		DesktopURL:           strings.TrimSpace(deployment.ChildDesktopURL),
		Status:               firstNonEmptyString(strings.TrimSpace(deployment.AttachStatus), strings.TrimSpace(deployment.Status)),
		OwnerHostSwarmID:     strings.TrimSpace(deployment.HostSwarmID),
		OwnerHostContainerID: hostContainerID,
		ObservedSources:      []string{pebblestore.TopologyRuntimeSourceDeployContainer},
		CreatedAt:            deployment.CreatedAt,
		UpdatedAt:            deployment.UpdatedAt,
	}); err != nil {
		return err
	}
	attachmentID := firstNonEmptyString(strings.TrimSpace(deployment.AttachmentID), pebblestore.CanonicalTopologyAttachmentID(hostContainerID, childSwarmID))
	return s.topology.UpsertAttachment(pebblestore.TopologyAttachmentRecord{
		AttachmentID:    attachmentID,
		HostContainerID: hostContainerID,
		RuntimeSwarmID:  childSwarmID,
		State:           firstNonEmptyString(strings.TrimSpace(deployment.AttachStatus), strings.TrimSpace(deployment.Status)),
		DeploymentID:    strings.TrimSpace(deployment.ID),
		LastError:       strings.TrimSpace(deployment.LastAttachError),
		CreatedAt:       deployment.CreatedAt,
		UpdatedAt:       deployment.UpdatedAt,
	})
}

func (s *Server) resolveManagedHostCanonicalDeleteIDs(managedSwarmID string, ids []string) ([]string, error) {
	managedSwarmID = strings.TrimSpace(managedSwarmID)
	if managedSwarmID == "" {
		return nil, errors.New("managed swarm id is required")
	}
	resolved := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	if s != nil && s.topology != nil {
		hostContainers, err := s.topology.ListHostContainersByHost(managedSwarmID, 100000)
		if err != nil {
			return nil, err
		}
		for _, rawID := range ids {
			candidateID := strings.TrimSpace(rawID)
			if candidateID == "" {
				continue
			}
			canonicalID := ""
			for _, hostContainer := range hostContainers {
				if candidateID == strings.TrimSpace(hostContainer.HostContainerID) ||
					candidateID == strings.TrimSpace(hostContainer.ContainerID) ||
					candidateID == strings.TrimSpace(hostContainer.ContainerName) ||
					candidateID == strings.TrimSpace(hostContainer.RuntimeContainerRef) {
					canonicalID = managedHostLocalDeleteID(hostContainer, candidateID)
					break
				}
				attachments, err := s.topology.ListAttachmentsByHostContainer(hostContainer.HostContainerID, 100)
				if err != nil {
					return nil, err
				}
				for _, attachment := range attachments {
					if candidateID == strings.TrimSpace(attachment.DeploymentID) {
						canonicalID = managedHostLocalDeleteID(hostContainer, candidateID)
						break
					}
				}
				if canonicalID != "" {
					break
				}
			}
			if canonicalID == "" {
				canonicalID = candidateID
			}
			if _, ok := seen[canonicalID]; ok {
				continue
			}
			seen[canonicalID] = struct{}{}
			resolved = append(resolved, canonicalID)
		}
	}
	if len(resolved) == 0 {
		deploymentSvc, ok := s.deployContainers.(interface {
			List(context.Context) ([]deployruntime.ContainerDeployment, error)
		})
		if !ok {
			return nil, errors.New("deploy container service does not support deployment listing")
		}
		deployments, err := deploymentSvc.List(context.Background())
		if err != nil {
			return nil, err
		}
		for _, rawID := range ids {
			candidateID := strings.TrimSpace(rawID)
			if candidateID == "" {
				continue
			}
			canonicalID := ""
			for _, deployment := range deployments {
				if !strings.EqualFold(strings.TrimSpace(deployment.HostSwarmID), managedSwarmID) {
					continue
				}
				if candidateID != strings.TrimSpace(deployment.ID) &&
					candidateID != strings.TrimSpace(deployment.HostContainerID) &&
					candidateID != strings.TrimSpace(deployment.ContainerID) &&
					candidateID != strings.TrimSpace(deployment.ContainerName) {
					continue
				}
				canonicalID = firstNonEmptyString(strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ID), candidateID)
				break
			}
			if canonicalID == "" {
				canonicalID = candidateID
			}
			if _, ok := seen[canonicalID]; ok {
				continue
			}
			seen[canonicalID] = struct{}{}
			resolved = append(resolved, canonicalID)
		}
	}
	if len(resolved) == 0 {
		return nil, errors.New("at least one managed host container id is required")
	}
	return resolved, nil
}

func managedHostLocalDeleteID(hostContainer pebblestore.TopologyHostContainerRecord, fallback string) string {
	return firstNonEmptyString(
		strings.TrimSpace(hostContainer.ContainerName),
		strings.TrimSpace(hostContainer.ContainerID),
		strings.TrimSpace(hostContainer.RuntimeContainerRef),
		strings.TrimSpace(fallback),
	)
}

func (s *Server) findRemoteManagedDeployment(ctx context.Context, id string) (deployruntime.ContainerDeployment, bool, error) {
	if s == nil || s.deployContainers == nil {
		return deployruntime.ContainerDeployment{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return deployruntime.ContainerDeployment{}, false, nil
	}
	deployments, err := s.deployContainers.List(ctx)
	if err != nil {
		return deployruntime.ContainerDeployment{}, false, err
	}
	localSwarmID, err := s.localSwarmIDForManagedDeploymentRouting()
	if err != nil {
		return deployruntime.ContainerDeployment{}, false, err
	}
	for _, deployment := range deployments {
		if id != strings.TrimSpace(deployment.ID) &&
			id != strings.TrimSpace(deployment.HostContainerID) &&
			id != strings.TrimSpace(deployment.ContainerID) &&
			id != strings.TrimSpace(deployment.ContainerName) {
			continue
		}
		hostSwarmID := firstNonEmptyString(strings.TrimSpace(deployment.HostSwarmID), strings.TrimSpace(deployment.SyncOwnerSwarmID))
		if hostSwarmID == "" || localSwarmID == "" || strings.EqualFold(hostSwarmID, localSwarmID) {
			return deployruntime.ContainerDeployment{}, false, nil
		}
		return deployment, true, nil
	}
	return deployruntime.ContainerDeployment{}, false, nil
}

func (s *Server) localSwarmIDForManagedDeploymentRouting() (string, error) {
	if s == nil {
		return "", errors.New("server is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return "", err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(state.Node.SwarmID), nil
}

func (s *Server) targetForRemoteManagedDeployment(deployment deployruntime.ContainerDeployment) (swarmTarget, bool, error) {
	if s == nil {
		return swarmTarget{}, false, errors.New("server is not configured")
	}
	targetSwarmID := strings.TrimSpace(deployment.HostSwarmID)
	if targetSwarmID == "" {
		return swarmTarget{}, false, nil
	}
	localSwarmID, err := s.localSwarmIDForManagedDeploymentRouting()
	if err != nil {
		return swarmTarget{}, false, err
	}
	if localSwarmID == "" || strings.EqualFold(targetSwarmID, localSwarmID) {
		return swarmTarget{}, false, nil
	}
	target := swarmTarget{
		SwarmID:    targetSwarmID,
		Name:       strings.TrimSpace(deployment.HostDisplayName),
		BackendURL: firstNonEmptyString(strings.TrimSpace(deployment.HostBackendURL), strings.TrimSpace(deployment.HostAPIBaseURL)),
		DesktopURL: strings.TrimSpace(deployment.HostDesktopURL),
	}
	if strings.TrimSpace(target.BackendURL) == "" {
		return swarmTarget{}, true, fmt.Errorf("target host backend URL is not configured")
	}
	return target, true, nil
}

func (s *Server) deleteTargetHostDeployment(ctx context.Context, deployment deployruntime.ContainerDeployment) error {
	target, remote, err := s.targetForRemoteManagedDeployment(deployment)
	if err != nil || !remote {
		return err
	}
	deleteID := strings.TrimSpace(deployment.ID)
	if deleteID == "" {
		return fmt.Errorf("managed host deployment identity is not configured")
	}
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, target, "/v1/deploy/container/delete", map[string]any{"ids": []string{deleteID}}, &response); err != nil {
		return err
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) != "" {
			return errors.New(strings.TrimSpace(response.Error))
		}
		return errors.New("managed host container delete failed")
	}
	return nil
}

func (s *Server) actTargetHostDeployment(ctx context.Context, deployment deployruntime.ContainerDeployment, action string) (deployruntime.ContainerDeployment, error) {
	target, remote, err := s.targetForRemoteManagedDeployment(deployment)
	if err != nil || !remote {
		return deployment, err
	}
	requestID := strings.TrimSpace(deployment.ID)
	if requestID == "" {
		return deployment, fmt.Errorf("managed host deployment identity is not configured")
	}
	var response struct {
		OK         bool                              `json:"ok"`
		Deployment deployruntime.ContainerDeployment `json:"deployment"`
		Error      string                            `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, target, "/v1/deploy/container/action", map[string]any{"id": requestID, "action": strings.TrimSpace(action)}, &response); err != nil {
		return deployment, err
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) != "" {
			return response.Deployment, errors.New(strings.TrimSpace(response.Error))
		}
		return response.Deployment, errors.New("managed host container action failed")
	}
	response.Deployment = mergeDeploymentMirrorResponse(deployment, response.Deployment)
	return s.mirrorManagedHostDeployment(response.Deployment, target, deploymentToMirrorPayload(response.Deployment))
}

func (s *Server) updateTargetHostDeploymentSettings(ctx context.Context, deployment deployruntime.ContainerDeployment, payload deployContainerSettingsPayload) (deployruntime.ContainerDeployment, error) {
	target, remote, err := s.targetForRemoteManagedDeployment(deployment)
	if err != nil || !remote {
		return deployment, err
	}
	payload.ID = strings.TrimSpace(deployment.ID)
	if payload.ID == "" {
		return deployment, fmt.Errorf("managed host deployment identity is not configured")
	}
	var response struct {
		OK         bool                              `json:"ok"`
		Deployment deployruntime.ContainerDeployment `json:"deployment"`
		Error      string                            `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, target, "/v1/deploy/container/settings", payload, &response); err != nil {
		return deployment, err
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) != "" {
			return response.Deployment, errors.New(strings.TrimSpace(response.Error))
		}
		return response.Deployment, errors.New("managed host container settings update failed")
	}
	response.Deployment = mergeDeploymentMirrorResponse(deployment, response.Deployment)
	return s.mirrorManagedHostDeployment(response.Deployment, target, deploymentToMirrorPayload(response.Deployment))
}

func deploymentToMirrorPayload(deployment deployruntime.ContainerDeployment) deployContainerCreatePayload {
	return deployContainerCreatePayload{
		DeploymentID:       strings.TrimSpace(deployment.ID),
		Name:               strings.TrimSpace(deployment.Name),
		Runtime:            strings.TrimSpace(deployment.Runtime),
		Image:              strings.TrimSpace(deployment.Image),
		GroupID:            strings.TrimSpace(deployment.GroupID),
		GroupName:          strings.TrimSpace(deployment.GroupName),
		GroupNetworkName:   strings.TrimSpace(deployment.GroupNetworkName),
		SyncEnabled:        deployment.SyncEnabled,
		SyncMode:           strings.TrimSpace(deployment.SyncMode),
		SyncModules:        append([]string(nil), deployment.SyncModules...),
		BypassPermissions:  deployment.BypassPermissions,
		AlwaysOn:           deployment.AlwaysOn,
		WorkspaceBootstrap: append([]deployruntime.ContainerWorkspaceBootstrap(nil), deployment.WorkspaceBootstrap...),
		ContainerPackages:  deployment.ContainerPackages,
	}
}

func mergeDeploymentMirrorResponse(previous deployruntime.ContainerDeployment, updated deployruntime.ContainerDeployment) deployruntime.ContainerDeployment {
	updated.ID = firstNonEmptyString(strings.TrimSpace(updated.ID), strings.TrimSpace(previous.ID))
	updated.Name = firstNonEmptyString(strings.TrimSpace(updated.Name), strings.TrimSpace(previous.Name))
	updated.Runtime = firstNonEmptyString(strings.TrimSpace(updated.Runtime), strings.TrimSpace(previous.Runtime))
	updated.Image = firstNonEmptyString(strings.TrimSpace(updated.Image), strings.TrimSpace(previous.Image))
	updated.GroupID = firstNonEmptyString(strings.TrimSpace(updated.GroupID), strings.TrimSpace(previous.GroupID))
	updated.GroupName = firstNonEmptyString(strings.TrimSpace(updated.GroupName), strings.TrimSpace(previous.GroupName))
	updated.GroupNetworkName = firstNonEmptyString(strings.TrimSpace(updated.GroupNetworkName), strings.TrimSpace(previous.GroupNetworkName))
	updated.HostSwarmID = firstNonEmptyString(strings.TrimSpace(updated.HostSwarmID), strings.TrimSpace(previous.HostSwarmID), strings.TrimSpace(previous.SyncOwnerSwarmID))
	updated.HostDisplayName = firstNonEmptyString(strings.TrimSpace(updated.HostDisplayName), strings.TrimSpace(previous.HostDisplayName))
	updated.HostBackendURL = firstNonEmptyString(strings.TrimSpace(updated.HostBackendURL), strings.TrimSpace(previous.HostBackendURL), strings.TrimSpace(previous.HostAPIBaseURL))
	updated.HostAPIBaseURL = firstNonEmptyString(strings.TrimSpace(updated.HostAPIBaseURL), strings.TrimSpace(previous.HostAPIBaseURL), strings.TrimSpace(previous.HostBackendURL))
	updated.HostDesktopURL = firstNonEmptyString(strings.TrimSpace(updated.HostDesktopURL), strings.TrimSpace(previous.HostDesktopURL))
	updated.HostContainerID = firstNonEmptyString(strings.TrimSpace(updated.HostContainerID), strings.TrimSpace(previous.HostContainerID))
	updated.AttachmentID = firstNonEmptyString(strings.TrimSpace(updated.AttachmentID), strings.TrimSpace(previous.AttachmentID))
	updated.SyncOwnerSwarmID = firstNonEmptyString(strings.TrimSpace(updated.SyncOwnerSwarmID), strings.TrimSpace(previous.SyncOwnerSwarmID), strings.TrimSpace(previous.HostSwarmID))
	updated.ChildSwarmID = firstNonEmptyString(strings.TrimSpace(updated.ChildSwarmID), strings.TrimSpace(previous.ChildSwarmID))
	updated.ChildDisplayName = firstNonEmptyString(strings.TrimSpace(updated.ChildDisplayName), strings.TrimSpace(previous.ChildDisplayName))
	updated.ChildBackendURL = firstNonEmptyString(strings.TrimSpace(updated.ChildBackendURL), strings.TrimSpace(previous.ChildBackendURL))
	updated.ChildDesktopURL = firstNonEmptyString(strings.TrimSpace(updated.ChildDesktopURL), strings.TrimSpace(previous.ChildDesktopURL))
	updated.SyncEnabled = updated.SyncEnabled || previous.SyncEnabled
	updated.SyncMode = firstNonEmptyString(strings.TrimSpace(updated.SyncMode), strings.TrimSpace(previous.SyncMode))
	if len(updated.SyncModules) == 0 {
		updated.SyncModules = append([]string(nil), previous.SyncModules...)
	}
	updated.BypassPermissions = updated.BypassPermissions || previous.BypassPermissions
	updated.AlwaysOn = updated.AlwaysOn || previous.AlwaysOn
	updated.WorkspaceBootstrap = append([]deployruntime.ContainerWorkspaceBootstrap(nil), firstNonEmptyWorkspaceBootstrap(updated.WorkspaceBootstrap, previous.WorkspaceBootstrap)...)
	updated.ContainerPackages = firstNonEmptyContainerPackageManifest(updated.ContainerPackages, previous.ContainerPackages)
	return updated
}

func firstNonEmptyWorkspaceBootstrap(primary []deployruntime.ContainerWorkspaceBootstrap, fallback []deployruntime.ContainerWorkspaceBootstrap) []deployruntime.ContainerWorkspaceBootstrap {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func firstNonEmptyContainerPackageManifest(primary deployruntime.ContainerPackageManifest, fallback deployruntime.ContainerPackageManifest) deployruntime.ContainerPackageManifest {
	if strings.TrimSpace(primary.BaseImage) != "" || strings.TrimSpace(primary.PackageManager) != "" || len(primary.Packages) > 0 {
		return primary
	}
	return fallback
}

func (s *Server) handleSwarmManagedHostContainerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ManagedSwarmID  string   `json:"managed_swarm_id"`
		ManagedHostName string   `json:"managed_host_name"`
		BackendURL      string   `json:"backend_url"`
		IDs             []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	target := swarmTarget{SwarmID: strings.TrimSpace(req.ManagedSwarmID), Name: strings.TrimSpace(req.ManagedHostName), BackendURL: strings.TrimSpace(req.BackendURL)}
	if target.SwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("managed_swarm_id is required"))
		return
	}
	if target.BackendURL == "" {
		if resolved, ok, err := s.findSwarmTargetByID(r, target.SwarmID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		} else if ok {
			target = resolved
		}
	}
	if target.BackendURL == "" {
		writeError(w, http.StatusBadRequest, errors.New("managed host backend URL is not configured"))
		return
	}
	canonicalDeleteIDs, err := s.resolveManagedHostCanonicalDeleteIDs(target.SwarmID, req.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var response struct {
		OK     bool                         `json:"ok"`
		Result localcontainers.DeleteResult `json:"result"`
		Error  string                       `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(r.Context(), target, "/v1/swarm/containers/local/delete", map[string]any{"ids": canonicalDeleteIDs}, &response); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "result": response.Result, "error": err.Error()})
		return
	}
	if !response.OK {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "managed host container delete failed"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "result": response.Result, "error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": response.Result})
}

func (s *Server) findSwarmTargetByID(r *http.Request, swarmID string) (swarmTarget, bool, error) {
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return swarmTarget{}, false, nil
	}
	targets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		return swarmTarget{}, false, err
	}
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.SwarmID), swarmID) {
			return target, true, nil
		}
	}
	return swarmTarget{}, false, nil
}

func managedHostResponseStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "start ") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func suggestedReplicatedDeploymentID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
