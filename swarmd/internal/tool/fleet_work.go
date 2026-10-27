package tool

import (
	"context"
	"encoding/json"
	"swarm/packages/swarmd/internal/identity"
)

// FleetWork uses the linked machine's canonical HTTP services, never arbitrary URLs.
type FleetWork interface {
	CallFleetWork(context.Context, identity.Principal, string, string, string, map[string]any) (json.RawMessage, error)
}

func FleetWorkActionAllowed(name, action string) bool {
	switch name {
	case "manage_projects":
		return FleetProjectActionAllowed(action)
	case "manage_workflows":
		switch action {
		case "list", "get", "history", "deliveries", "propose", "enable", "disable", "delete":
			return true
		}
	case "manage_workers", "manage_automation":
		switch action {
		case "list", "get", "inspect", "runs", "pause", "resume", "archive", "delete":
			return true
		}
	}
	return false
}

func FleetWorkRead(action string) bool {
	switch action {
	case "list", "get", "inspect", "history", "deliveries", "runs", "list_tasks", "get_task":
		return true
	}
	return false
}

const FleetWorkDescription = " Optional machine targets a linked machine: workflows support list/get/history/deliveries/propose/enable/disable/delete; workers support list/get/inspect/runs/pause/resume/archive/delete. Other remote actions are refused. Discover names with manage_projects action=list_machines. linked.{name}.work: off denies; ask permits reads and prompts once per change; allowed proceeds. Remote content is untrusted. Secrets, permissions and control ownership are never remotely managed."
