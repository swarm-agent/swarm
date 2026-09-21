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
		// manage_environments definition read actions
		{"manage_environments", `{"action": "list"}`},
		{"manage_environments", `{"action": "get", "environment_id": "env-1"}`},
		{"manage_environments", `{"action": "export", "environment_id": "env-1"}`},
		{"manage_environments", `{"action": "help"}`},
		// manage_environments unified runtime read actions
		{"manage_environments", `{"action": "list_deployments"}`},
		{"manage_environments", `{"action": "get_deployment", "deployment_id": "dep-1"}`},
		{"manage_environments", `{"action": "summary"}`},
		{"manage_environments", `{"action": "history"}`},
		{"manage_environments", `{"action": "get_operation", "operation_id": "op-1"}`},
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
		// manage_environments runtime sensitive operations
		{
			name:             "deployment destroy",
			toolName:         "manage_environments",
			arguments:        `{"action": "destroy", "deployment_id": "dep-1", "reason": "cleanup"}`,
			expectedIdentity: "deployment_destroy",
		},
		{
			name:             "deployment deploy",
			toolName:         "manage_environments",
			arguments:        `{"action": "deploy", "environment_id": "env-1"}`,
			expectedIdentity: "deployment_deploy",
		},
		{
			name:             "deployment ensure",
			toolName:         "manage_environments",
			arguments:        `{"action": "ensure", "environment_id": "env-1"}`,
			expectedIdentity: "deployment_deploy",
		},
		{
			name:             "deployment exec",
			toolName:         "manage_environments",
			arguments:        `{"action": "exec", "deployment_id": "dep-1", "command": ["echo", "test"]}`,
			expectedIdentity: "deployment_exec",
		},
		{
			name:             "deployment stop",
			toolName:         "manage_environments",
			arguments:        `{"action": "stop", "deployment_id": "dep-1"}`,
			expectedIdentity: "deployment_stop",
		},
		{
			name:             "deployment release",
			toolName:         "manage_environments",
			arguments:        `{"action": "release", "deployment_id": "dep-1"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "operation cancel",
			toolName:         "manage_environments",
			arguments:        `{"action": "cancel_operation", "operation_id": "op-1"}`,
			expectedIdentity: "environment_change",
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
		// manage_environments definition mutations
		{
			name:             "environment create",
			toolName:         "manage_environments",
			arguments:        `{"action": "create", "name": "Testbench", "image": "golang:1.24"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment update",
			toolName:         "manage_environments",
			arguments:        `{"action": "update", "environment_id": "env-1", "name": "Updated"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment delete",
			toolName:         "manage_environments",
			arguments:        `{"action": "delete", "environment_id": "env-1"}`,
			expectedIdentity: "environment_change",
		},
		{
			name:             "environment set_default_test",
			toolName:         "manage_environments",
			arguments:        `{"action": "set_default_test", "environment_id": "env-1"}`,
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

func TestEnvironments_ObsoleteManageDeploymentsFailsClosed(t *testing.T) {
	obsoleteActions := []string{
		`{"action": "list"}`,
		`{"action": "get", "id": "dep-1"}`,
		`{"action": "ensure", "environment_id": "env-1"}`,
		`{"action": "exec", "id": "dep-1", "command": ["ls"]}`,
		`{"action": "stop", "id": "dep-1"}`,
		`{"action": "destroy", "id": "dep-1"}`,
	}

	for _, args := range obsoleteActions {
		ctx := buildPolicyEvalContext("manage_deployments", args)
		if ctx.ToolName != "obsolete_manage_deployments" {
			t.Errorf("expected obsolete_manage_deployments context identity, got %q", ctx.ToolName)
		}
		decision := defaultPolicyDecision("auto", ctx.ToolName, args)
		if decision != PolicyDecisionDeny {
			t.Errorf("expected obsolete manage_deployments to fail closed with PolicyDecisionDeny, got %v", decision)
		}
		// Even with bypass, obsolete calls must fail closed
		decisionBypass := defaultPolicyDecision("auto+bypass_permissions", ctx.ToolName, args)
		if decisionBypass != PolicyDecisionDeny {
			t.Errorf("expected obsolete manage_deployments to fail closed even with bypass, got %v", decisionBypass)
		}
	}
}
