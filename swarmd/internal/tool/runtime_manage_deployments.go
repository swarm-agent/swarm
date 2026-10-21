package tool

import (
	"context"
	"errors"
)

// ErrEnvironmentToolsRemoved answers a call to a removed environment tool:
// agents no longer manage environments; bash runs in the folder's environment
// by itself (lease_environment).
var ErrEnvironmentToolsRemoved = errors.New("manage_environments, manage_connections and manage_deployments are removed: when your folder has an environment set up, bash runs in it by itself; lease_environment takes extra leases")

// ErrManageDeploymentsObsolete indicates that manage_deployments has been removed.
var ErrManageDeploymentsObsolete = ErrEnvironmentToolsRemoved

func (r *Runtime) executeManageDeployments(_ context.Context, _ WorkspaceScope, _ map[string]any) (string, error) {
	return "", ErrManageDeploymentsObsolete
}

func parseCommandList(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed, nil
	case []any:
		res := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				res = append(res, s)
			}
		}
		return res, nil
	default:
		return nil, errors.New("command must be an array of strings")
	}
}

func asStringMap(raw any) map[string]string {
	if raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			res[k] = s
		}
	}
	return res
}
