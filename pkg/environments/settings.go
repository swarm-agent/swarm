package environments

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// WorkspaceSettings holds environment and deployment configuration for a workspace.
type WorkspaceSettings struct {
	WorkspaceID              string `json:"workspace_id"`
	AccountScopeID           string `json:"account_scope_id"`
	DefaultTestEnvironmentID string `json:"default_test_environment_id,omitempty"`
	DefaultConnectionID      string `json:"default_connection_id,omitempty"`
	UpdatedAt                int64  `json:"updated_at,omitempty"`
}

// Validate checks that WorkspaceSettings fields adhere to scoping and length rules.
func (s *WorkspaceSettings) Validate() error {
	if s == nil {
		return errors.New("workspace settings is nil")
	}
	s.WorkspaceID = strings.TrimSpace(s.WorkspaceID)
	if s.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(s.WorkspaceID) > maxIDBytes || !utf8.ValidString(s.WorkspaceID) {
		return errors.New("workspace_id exceeds max length or is invalid UTF-8")
	}
	s.AccountScopeID = strings.TrimSpace(s.AccountScopeID)
	if s.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(s.AccountScopeID) > maxIDBytes || !utf8.ValidString(s.AccountScopeID) {
		return errors.New("account_scope_id exceeds max length or is invalid UTF-8")
	}
	s.DefaultTestEnvironmentID = strings.TrimSpace(s.DefaultTestEnvironmentID)
	if len(s.DefaultTestEnvironmentID) > maxIDBytes || !utf8.ValidString(s.DefaultTestEnvironmentID) {
		return errors.New("default_test_environment_id exceeds max length or is invalid UTF-8")
	}
	s.DefaultConnectionID = strings.TrimSpace(s.DefaultConnectionID)
	if len(s.DefaultConnectionID) > maxIDBytes || !utf8.ValidString(s.DefaultConnectionID) {
		return errors.New("default_connection_id exceeds max length or is invalid UTF-8")
	}
	return nil
}

// Clone creates a deep copy of WorkspaceSettings.
func (s *WorkspaceSettings) Clone() *WorkspaceSettings {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}
