package api

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/workspace"
)

var errAccountOwnedWorkspacePathRequired = errors.New("account-owned workspace path is required")

type accountOwnedPath struct {
	RequestedPath string
	ResolvedPath  string
	WorkspacePath string
	Scope         workspace.Scope
}

func (s *Server) resolveAccountOwnedPath(principal identity.Principal, path string) (accountOwnedPath, error) {
	if !principal.Valid() {
		return accountOwnedPath{}, identity.ErrPrincipalRequired
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return accountOwnedPath{}, errors.New("workspace_path is required")
	}
	if s == nil || s.workspace == nil {
		return accountOwnedPath{}, errors.New("workspace service not configured")
	}
	scope, err := s.workspace.ScopeForPathForPrincipal(principal, path)
	if err != nil {
		return accountOwnedPath{}, fmt.Errorf("resolve account-owned workspace path: %w", err)
	}
	if !scope.Matched || strings.TrimSpace(scope.WorkspacePath) == "" {
		return accountOwnedPath{}, errAccountOwnedWorkspacePathRequired
	}
	return accountOwnedPath{
		RequestedPath: path,
		ResolvedPath:  strings.TrimSpace(scope.ResolvedPath),
		WorkspacePath: strings.TrimSpace(scope.WorkspacePath),
		Scope:         scope,
	}, nil
}
