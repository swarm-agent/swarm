package api

import (
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type targetWorkspaceRoute struct {
	AccountScopeID     string
	TargetSwarmID      string
	RuntimeSwarmID     string
	HostSwarmID        string
	WorkspaceBindingID string
	WorkspaceName      string

	HostWorkspacePath    string
	RuntimeWorkspacePath string
}

func newTargetWorkspaceRoute(accountScopeID string, target swarmTarget, binding pebblestore.TopologyWorkspaceBindingRecord) (targetWorkspaceRoute, error) {
	targetHostSwarmID := strings.TrimSpace(target.HostSwarmID)
	bindingHostSwarmID := strings.TrimSpace(binding.DestinationHostSwarmID)
	if targetHostSwarmID != "" && bindingHostSwarmID != "" && !strings.EqualFold(targetHostSwarmID, bindingHostSwarmID) {
		return targetWorkspaceRoute{}, errors.New("target workspace route host swarm id does not match binding host swarm id")
	}
	route := targetWorkspaceRoute{
		AccountScopeID:     strings.TrimSpace(accountScopeID),
		TargetSwarmID:      strings.TrimSpace(target.SwarmID),
		RuntimeSwarmID:     strings.TrimSpace(binding.DestinationRuntimeSwarmID),
		HostSwarmID:        firstNonEmpty(bindingHostSwarmID, targetHostSwarmID, strings.TrimSpace(binding.DestinationRuntimeSwarmID)),
		WorkspaceBindingID: strings.TrimSpace(binding.BindingID),
		WorkspaceName:      strings.TrimSpace(binding.SourceWorkspaceName),

		HostWorkspacePath:    strings.TrimSpace(binding.SourceWorkspacePath),
		RuntimeWorkspacePath: strings.TrimSpace(binding.DestinationWorkspacePath),
	}
	if err := route.validate(); err != nil {
		return targetWorkspaceRoute{}, err
	}
	return route, nil
}

func (r targetWorkspaceRoute) validate() error {
	if strings.TrimSpace(r.AccountScopeID) == "" {
		return errors.New("target workspace route account scope id is required")
	}
	if strings.TrimSpace(r.TargetSwarmID) == "" {
		return errors.New("target workspace route target swarm id is required")
	}
	if strings.TrimSpace(r.RuntimeSwarmID) == "" {
		return errors.New("target workspace route runtime swarm id is required")
	}
	if !strings.EqualFold(strings.TrimSpace(r.RuntimeSwarmID), strings.TrimSpace(r.TargetSwarmID)) {
		return errors.New("target workspace route runtime swarm id does not match target swarm id")
	}
	if strings.TrimSpace(r.HostSwarmID) == "" {
		return errors.New("target workspace route host swarm id is required")
	}
	if strings.TrimSpace(r.WorkspaceBindingID) == "" {
		return errors.New("target workspace route workspace binding id is required")
	}
	if strings.TrimSpace(r.WorkspaceName) == "" {
		return errors.New("target workspace route workspace name is required")
	}
	if strings.TrimSpace(r.HostWorkspacePath) == "" {
		return errors.New("target workspace route host workspace path is missing from binding")
	}
	if strings.TrimSpace(r.RuntimeWorkspacePath) == "" {
		return errors.New("target workspace route runtime workspace path is missing from binding")
	}
	return nil
}

func (r targetWorkspaceRoute) routeID() string {
	return "swarm:" + strings.TrimSpace(r.RuntimeSwarmID) + ":binding:" + strings.TrimSpace(r.WorkspaceBindingID)
}
