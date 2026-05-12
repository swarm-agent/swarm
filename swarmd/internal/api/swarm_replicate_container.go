package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
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
	if strings.TrimSpace(payload.DeploymentID) == "" {
		payload.DeploymentID = suggestedReplicatedDeploymentID(payload.Name)
	}
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
	deployment, err := s.mirrorManagedHostDeployment(response.Deployment, *targetHost, payload)
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
	deployment.GroupID = firstNonEmptyString(strings.TrimSpace(deployment.GroupID), strings.TrimSpace(payload.GroupID))
	deployment.GroupName = firstNonEmptyString(strings.TrimSpace(deployment.GroupName), strings.TrimSpace(payload.GroupName))
	deployment.GroupNetworkName = firstNonEmptyString(strings.TrimSpace(deployment.GroupNetworkName), strings.TrimSpace(payload.GroupNetworkName))
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
	return mirror.MirrorDeployment(context.Background(), deployment)
}

func (s *Server) deleteTargetHostDeployment(ctx context.Context, deployment deployruntime.ContainerDeployment) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	targetSwarmID := strings.TrimSpace(deployment.HostSwarmID)
	if targetSwarmID == "" {
		return nil
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" || strings.EqualFold(targetSwarmID, localSwarmID) {
		return nil
	}
	target := swarmTarget{
		SwarmID:    targetSwarmID,
		Name:       strings.TrimSpace(deployment.HostDisplayName),
		BackendURL: firstNonEmptyString(strings.TrimSpace(deployment.HostBackendURL), strings.TrimSpace(deployment.HostAPIBaseURL)),
		DesktopURL: strings.TrimSpace(deployment.HostDesktopURL),
	}
	if strings.TrimSpace(target.BackendURL) == "" {
		return fmt.Errorf("target host backend URL is not configured")
	}
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, target, "/v1/deploy/container/delete", map[string]any{"ids": []string{strings.TrimSpace(deployment.ID)}}, &response); err != nil {
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
