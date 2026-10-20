package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/sandbox"
)

// LeaseEnvironmentToolName is the agent's tool for leasing its folder's
// environment for bash (sandbox/lease.go). The agent only takes leases: the
// person's AI defines the environment and its maximum, and Swarm releases
// every lease when the agent's run ends.
const LeaseEnvironmentToolName = "lease_environment"

func leaseEnvironmentDefinition() Definition {
	return Definition{
		Type: "function",
		Name: LeaseEnvironmentToolName,
		Description: "Lease your folder's environment for bash. A lease is its own container of the environment the person set up for this folder (its image and setup), with your folder and your working directory (your worktree) mounted at the same paths, so bash runs where you already work and your file and Git tools see the same files; the folder's granted secrets are available as stand-ins. " +
			"action=list shows the environment, how many leases it allows and how many are in use, and your leases. action=lease starts one (up to the folder's maximum; it says when all are in use). While you hold one lease, bash runs in it; with several, pass bash environment=<lease id>. " +
			"action=release ends one early. You never clean up: Swarm releases all your leases when your run ends. Without a lease, bash runs as usual.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "enum": []string{"list", "lease", "release"}},
				"lease_id": map[string]any{"type": "string", "description": "The lease to release (action=release)."},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func executeLeaseEnvironment(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	manager := sandbox.Leases()
	if manager == nil {
		return "", errors.New("leases are unavailable on this machine (it has no working Docker)")
	}
	owner := strings.TrimSpace(scope.SessionID)
	if owner == "" {
		return "", errors.New("lease_environment needs an agent session")
	}
	if scope.ReadOnlyBash || scope.ReviewCheckout != "" {
		return "", errors.New("a review cannot lease an environment")
	}
	path := strings.TrimSpace(scope.PrimaryPath)
	if path == "" {
		return "", errors.New("lease_environment needs the folder you work in")
	}
	out := map[string]any{}
	switch strings.ToLower(strings.TrimSpace(asString(args["action"]))) {
	case "list":
		info, err := manager.LeaseInfo(path)
		if err != nil {
			out["available"] = false
			out["reason"] = err.Error()
		} else {
			out["available"] = info.Max > 0
			out["folder"] = info.Root
			out["max_leases"] = info.Max
			out["in_use"] = info.InUse
		}
	case "lease":
		lease, err := manager.TakeLease(ctx, path, owner)
		if err != nil {
			return "", err
		}
		out["lease"] = lease
		out["next"] = "Work as usual: bash now runs in this lease, in your own working directory (your folder and your worktree are mounted there at the same paths), and your file and Git tools see the same files. Do not pass workspace_path for this. While it is your only lease, bash uses it by itself; with several, pass bash environment=" + lease.ID + ". Swarm releases it when your run ends."
	case "release":
		id := strings.TrimSpace(asString(args["lease_id"]))
		if id == "" {
			return "", errors.New("release needs lease_id")
		}
		if err := manager.ReleaseLease(ctx, id, owner); err != nil {
			return "", err
		}
		out["released"] = id
	default:
		return "", errors.New("action must be list, lease or release")
	}
	out["your_leases"] = manager.OwnerLeases(owner)
	encoded, err := json.Marshal(out)
	return string(encoded), err
}
