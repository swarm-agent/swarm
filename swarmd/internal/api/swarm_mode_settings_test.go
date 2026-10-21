package api

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testAgentModelSettingsRecord(accountScopeID string) pebblestore.AgentModelSettingsRecord {
	assignment := func(model string) pebblestore.AgentModelAssignment {
		return pebblestore.AgentModelAssignment{Provider: "codex", Model: model, Thinking: "high"}
	}
	return pebblestore.AgentModelSettingsRecord{
		AccountScopeID: accountScopeID,
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: assignment("action"), Plan: assignment("plan")},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: assignment("compact"), Finder: assignment("finder"), Coder: assignment("coder"),
			Designer: assignment("designer"), Router: assignment("router"),
		},
		UpdatedAt: 1,
	}
}
