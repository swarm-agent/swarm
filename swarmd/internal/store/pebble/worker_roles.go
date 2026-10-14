package pebblestore

import (
	"errors"
	"fmt"
	"strings"
)

// Worker roles name the code-owned system agent that runs a worker's jobs.
// Identity, tools and permissions stay compiled; the role only selects which
// system agent (and which account-scoped model setting) a job uses.
const (
	WorkerRoleSwarm    = "swarm"
	WorkerRoleCoder    = "coder"
	WorkerRoleDesigner = "designer"
	WorkerRoleFinder   = "finder"
)

// WorkerRoles lists every accepted role. Empty is accepted and means swarm.
var WorkerRoles = []string{WorkerRoleSwarm, WorkerRoleCoder, WorkerRoleDesigner, WorkerRoleFinder}

const (
	// WorkerRunWorkspaceSourceJob: the request or its automation named the workspace.
	WorkerRunWorkspaceSourceJob = "worker_job"
	// WorkerRunWorkspaceSourceBinding: the worker's approved primary binding.
	WorkerRunWorkspaceSourceBinding = "worker_binding"
)

// ErrWorkerWorkspaceRequired is returned when a job neither names a workspace
// nor belongs to a worker with an approved primary binding.
var ErrWorkerWorkspaceRequired = errors.New("this worker works in several workspaces: name the workspace")

// NormalizeWorkerRole trims and lowercases a role and rejects unknown values.
func NormalizeWorkerRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "", nil
	}
	for _, known := range WorkerRoles {
		if role == known {
			return role, nil
		}
	}
	return "", fmt.Errorf("invalid worker role %q: use one of %s", role, strings.Join(WorkerRoles, ", "))
}

// WorkerRoleRunsPlan reports whether jobs of this role execute as an accepted
// V3 plan whose checkpoints end with a plan_manage terminal outcome. Only the
// Swarm agent has plan_manage; Coder, Designer and Finder run their compiled
// profile in an auto session the way project tasks assigned to them do.
func WorkerRoleRunsPlan(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "" || role == WorkerRoleSwarm
}

// WorkerPrimaryBinding validates the approved workspace shape of a worker and
// returns its primary workspace ID. A worker is either box-level (no workspace
// roles and no bindings; every job names its workspace) or bound to exactly one
// required primary workspace.
func WorkerPrimaryBinding(requirements []WorkerWorkspaceRequirement, bindings map[string]string) (string, error) {
	if len(requirements) == 0 && len(bindings) == 0 {
		return "", nil
	}
	if len(requirements) != 1 || requirements[0].Role != "primary" || !requirements[0].Required {
		return "", errors.New("a worker declares either no workspace roles or exactly one required primary workspace role")
	}
	if len(bindings) != 1 || strings.TrimSpace(bindings["primary"]) == "" {
		return "", errors.New("the required primary workspace role must be bound")
	}
	return strings.TrimSpace(bindings["primary"]), nil
}

// workerAutomationWorkspace returns the workspace an automation names.
func workerAutomationWorkspace(w WorkerRecord, automationID string) string {
	if automationID == "" {
		return ""
	}
	for _, a := range w.Automations {
		if a.ID == automationID {
			return strings.TrimSpace(a.WorkspaceID)
		}
	}
	return ""
}

// ResolveWorkerRunWorkspace applies the per-job workspace rule: a workspace the
// request names wins, then the automation's workspace, then the worker's
// primary binding. It returns the workspace ID and its WorkerRunWorkspaceSource.
func ResolveWorkerRunWorkspace(w WorkerRecord, automationID, requested string) (string, string, error) {
	primary, err := WorkerPrimaryBinding(w.WorkspaceRequirements, w.LocalBindings)
	if err != nil {
		return "", "", err
	}
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested, WorkerRunWorkspaceSourceJob, nil
	}
	if auto := workerAutomationWorkspace(w, automationID); auto != "" {
		return auto, WorkerRunWorkspaceSourceJob, nil
	}
	if primary != "" {
		return primary, WorkerRunWorkspaceSourceBinding, nil
	}
	return "", "", ErrWorkerWorkspaceRequired
}

// WorkerRunWorkspace returns the workspace a run works in: the workspace
// pinned at admission, or for older receipts the pinned revision's binding.
func WorkerRunWorkspace(r WorkerRunRecord, pinned WorkerRecord) string {
	if id := strings.TrimSpace(r.WorkspaceID); id != "" {
		return id
	}
	return strings.TrimSpace(pinned.LocalBindings["primary"])
}

// validateWorkerRoleRecord checks the role and its compatibility with the
// worker's automations.
func validateWorkerRoleRecord(w *WorkerRecord) error {
	role, err := NormalizeWorkerRole(w.Role)
	if err != nil {
		return err
	}
	if role != w.Role {
		return fmt.Errorf("worker role %q is not normalized", w.Role)
	}
	for _, a := range w.Automations {
		if len(a.WorkspaceID) > 128 {
			return fmt.Errorf("automation %q workspace_id exceeds 128 characters", a.ID)
		}
		if WorkerRoleRunsPlan(role) {
			continue
		}
		for _, req := range a.DeliverableRequirements {
			if req.Kind == WorkerDeliverableKindSocialPost {
				return fmt.Errorf("automation %q declares a social_post deliverable, which only the swarm role can produce (its checkpoint result carries the post)", a.ID)
			}
		}
	}
	return nil
}
