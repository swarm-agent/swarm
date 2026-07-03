package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func principalForContext(ctx context.Context) (identity.Principal, bool) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return identity.Principal{}, false
	}
	return principal, true
}

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
	deployment, err := s.mirrorManagedHostDeployment(ctx, response.Deployment, *targetHost, mirrorPayload)
	if err != nil {
		return deployment, err
	}
	return deployment, nil
}

func (s *Server) mirrorManagedHostDeployment(ctx context.Context, deployment deployruntime.ContainerDeployment, target swarmTarget, payload deployContainerCreatePayload) (deployruntime.ContainerDeployment, error) {
	if s == nil || s.deployContainers == nil {
		return deployment, errors.New("deploy container service is not configured")
	}
	mirror, ok := s.deployContainers.(interface {
		MirrorDeployment(context.Context, deployruntime.ContainerDeployment) (deployruntime.ContainerDeployment, error)
	})
	if !ok {
		return deployment, errors.New("deploy container mirror service is not configured")
	}
	if principal, ok := principalForContext(ctx); ok {
		deployment.UserID = firstNonEmptyString(strings.TrimSpace(deployment.UserID), strings.TrimSpace(principal.UserID))
		deployment.AccountScopeID = firstNonEmptyString(strings.TrimSpace(deployment.AccountScopeID), strings.TrimSpace(principal.AccountScopeID))
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
	deployment.WorkspaceBootstrap = append([]deployruntime.ContainerWorkspaceBootstrap(nil), firstNonEmptyWorkspaceBootstrap(payload.WorkspaceBootstrap, deployment.WorkspaceBootstrap)...)
	deployment.ContainerPackages = firstNonEmptyContainerPackageManifest(payload.ContainerPackages, deployment.ContainerPackages)
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
	return mirror.MirrorDeployment(ctx, deployment)
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
	userID := strings.TrimSpace(deployment.UserID)
	accountScopeID := strings.TrimSpace(deployment.AccountScopeID)
	if userID == "" || accountScopeID == "" {
		return errors.New("managed host topology sync requires deployment user and account scope")
	}
	if err := s.topology.UpsertHostContainer(pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		UserID:              userID,
		AccountScopeID:      accountScopeID,
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
		UserID:               userID,
		AccountScopeID:       accountScopeID,
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
		UserID:          userID,
		AccountScopeID:  accountScopeID,
		HostContainerID: hostContainerID,
		RuntimeSwarmID:  childSwarmID,
		State:           firstNonEmptyString(strings.TrimSpace(deployment.AttachStatus), strings.TrimSpace(deployment.Status)),
		DeploymentID:    strings.TrimSpace(deployment.ID),
		LastError:       strings.TrimSpace(deployment.LastAttachError),
		CreatedAt:       deployment.CreatedAt,
		UpdatedAt:       deployment.UpdatedAt,
	})
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
	return s.mirrorManagedHostDeployment(ctx, response.Deployment, target, deploymentToMirrorPayload(response.Deployment))
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
	return s.mirrorManagedHostDeployment(ctx, response.Deployment, target, deploymentToMirrorPayload(response.Deployment))
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
