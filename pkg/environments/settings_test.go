package environments

import (
	"strings"
	"testing"
)

func TestWorkspaceSettingsValidation(t *testing.T) {
	tests := []struct {
		name      string
		settings  *WorkspaceSettings
		expectErr bool
	}{
		{
			name:      "nil settings",
			settings:  nil,
			expectErr: true,
		},
		{
			name: "missing workspace_id",
			settings: &WorkspaceSettings{
				AccountScopeID: "account-1",
			},
			expectErr: true,
		},
		{
			name: "missing account_scope_id",
			settings: &WorkspaceSettings{
				WorkspaceID: "workspace-1",
			},
			expectErr: true,
		},
		{
			name: "valid empty defaults",
			settings: &WorkspaceSettings{
				WorkspaceID:    "workspace-1",
				AccountScopeID: "account-1",
			},
			expectErr: false,
		},
		{
			name: "valid with defaults",
			settings: &WorkspaceSettings{
				WorkspaceID:              "workspace-1",
				AccountScopeID:           "account-1",
				DefaultTestEnvironmentID: "env-test-1",
				DefaultConnectionID:      "conn-1",
			},
			expectErr: false,
		},
		{
			name: "workspace_id exceeds max bytes",
			settings: &WorkspaceSettings{
				WorkspaceID:    strings.Repeat("w", 200),
				AccountScopeID: "account-1",
			},
			expectErr: true,
		},
		{
			name: "default_test_environment_id exceeds max bytes",
			settings: &WorkspaceSettings{
				WorkspaceID:              "workspace-1",
				AccountScopeID:           "account-1",
				DefaultTestEnvironmentID: strings.Repeat("e", 200),
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.settings.Validate()
			if tc.expectErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestWorkspaceSettingsClone(t *testing.T) {
	orig := &WorkspaceSettings{
		WorkspaceID:              "ws-1",
		AccountScopeID:           "acc-1",
		DefaultTestEnvironmentID: "env-1",
		DefaultConnectionID:      "conn-1",
		UpdatedAt:                12345,
	}
	cp := orig.Clone()
	if cp == nil {
		t.Fatal("expected non-nil clone")
	}
	if *cp != *orig {
		t.Fatalf("clone mismatch: got %+v, want %+v", cp, orig)
	}
	cp.DefaultConnectionID = "conn-2"
	if orig.DefaultConnectionID != "conn-1" {
		t.Error("clone mutation affected original")
	}
}
