package permission

import (
	"testing"
)

func TestEnvironments_ToolNormalizationAndPolicies(t *testing.T) {
	// Test tool normalization
	tools := []string{
		"manage_connections", "manage-connections", "manageconnections",
		"manage_environments", "manage-environments", "manageenvironments",
		"manage_deployments", "manage-deployments", "managedeployments",
	}
	expected := []string{
		"manage_connections", "manage_connections", "manage_connections",
		"manage_environments", "manage_environments", "manage_environments",
		"manage_deployments", "manage_deployments", "manage_deployments",
	}
	for i, tool := range tools {
		got := normalizePolicyToolName(tool)
		if got != expected[i] {
			t.Errorf("normalizePolicyToolName(%q) = %q, want %q", tool, got, expected[i])
		}
	}
}

func TestEnvironments_ReadOnlyActionsDefaultAllow(t *testing.T) {
	tests := []struct {
		toolName  string
		arguments string
	}{
		{"manage_connections", `{"action": "list"}`},
		{"manage_connections", `{"action": "get", "id": "conn-1"}`},
		{"manage_connections", `{"action": "capabilities", "id": "conn-1"}`},
		{"manage_environments", `{"action": "list"}`},
		{"manage_environments", `{"action": "get", "id": "env-1"}`},
		{"manage_environments", `{"action": "export", "id": "env-1"}`},
		{"manage_deployments", `{"action": "list"}`},
		{"manage_deployments", `{"action": "get", "id": "dep-1"}`},
		{"manage_deployments", `{"action": "check", "id": "dep-1"}`},
		{"manage_deployments", `{"action": "access", "id": "dep-1"}`},
		{"manage_deployments", `{"action": "release", "lease_id": "lease-1"}`},
	}

	for _, tt := range tests {
		ctx := buildPolicyEvalContext(tt.toolName, tt.arguments)
		decision := defaultPolicyDecision("auto", ctx.ToolName, tt.arguments)
		if decision != PolicyDecisionAllow {
			t.Errorf("expected PolicyDecisionAllow for %s with %s (got %v, context tool=%s)",
				tt.toolName, tt.arguments, decision, ctx.ToolName)
		}
	}
}

func TestEnvironments_SensitiveOperationsGatedByPermissions(t *testing.T) {
	tests := []struct {
		name             string
		toolName         string
		arguments        string
		expectedIdentity string
	}{
		// manage_deployments sensitive operations
		{
			name:             "deployment destroy",
			toolName:         "manage_deployments",
			arguments:        `{"action": "destroy", "id": "dep-1", "reason": "cleanup"}`,
			expectedIdentity: "deployment_destroy",
		},
		{
			name:             "deployment deploy",
			toolName:         "manage_deployments",
			arguments:        `{"action": "deploy", "environment_id": "env-1"}`,
			expectedIdentity: "deployment_deploy",
		},
		{
			name:             "deployment exec",
			toolName:         "manage_deployments",
			arguments:        `{"action": "exec", "id": "dep-1", "command": ["echo", "test"]}`,
			expectedIdentity: "deployment_exec",
		},
		{
			name:             "deployment stop",
			toolName:         "manage_deployments",
			arguments:        `{"action": "stop", "id": "dep-1"}`,
			expectedIdentity: "deployment_stop",
		},
		// manage_connections mutations
		{
			name:             "connection create",
			toolName:         "manage_connections",
			arguments:        `{"action": "create", "name": "SSH Host", "kind": "ssh", "host": "10.0.0.1", "user": "admin"}`,
			expectedIdentity: "connection_change",
		},
		{
			name:             "connection update",
			toolName:         "manage_connections",
			arguments:        `{"action": "update", "id": "conn-1", "name": "Renamed"}`,
			expectedIdentity: "connection_change",
		},
		{
			name:             "connection delete",
			toolName:         "manage_connections",
			arguments:        `{"action": "delete", "id": "conn-1"}`,
			expectedIdentity: "connection_change",
		},
		{
			name:             "connection check",
			toolName:         "manage_connections",
			arguments:        `{"action": "check", "id": "conn-1"}`,
			expectedIdentity: "connection_change",
		},
		// manage_environments mutations
		{
			name:             "environment create",
			toolName:         "manage_environments",
			arguments:        `{"action": "create", "name": "Testbench", "image": "golang:1.24"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment update",
			toolName:         "manage_environments",
			arguments:        `{"action": "update", "id": "env-1", "name": "Updated"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment delete",
			toolName:         "manage_environments",
			arguments:        `{"action": "delete", "id": "env-1"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment set_default_test",
			toolName:         "manage_environments",
			arguments:        `{"action": "set_default_test", "id": "env-1"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment import",
			toolName:         "manage_environments",
			arguments:        `{"action": "import", "json": "{\"name\": \"imported\"}"}`,
			expectedIdentity: "environment_change",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := buildPolicyEvalContext(tt.toolName, tt.arguments)
			if ctx.ToolName != tt.expectedIdentity {
				t.Fatalf("buildPolicyEvalContext(%q, %q).ToolName = %q, want %q",
					tt.toolName, tt.arguments, ctx.ToolName, tt.expectedIdentity)
			}

			// Without bypass: must return PolicyDecisionAsk
			decisionWithoutBypass := defaultPolicyDecision("auto", ctx.ToolName, tt.arguments)
			if decisionWithoutBypass != PolicyDecisionAsk {
				t.Errorf("expected PolicyDecisionAsk for sensitive op %q without bypass, got %v",
					tt.expectedIdentity, decisionWithoutBypass)
			}

			// With bypass: must return PolicyDecisionAllow
			decisionWithBypass := defaultPolicyDecision("auto+bypass_permissions", ctx.ToolName, tt.arguments)
			if decisionWithBypass != PolicyDecisionAllow {
				t.Errorf("expected PolicyDecisionAllow for sensitive op %q with bypass, got %v",
					tt.expectedIdentity, decisionWithBypass)
			}
		})
	}
}
