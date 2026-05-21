package api

import (
	"errors"
	"fmt"
	"strings"

	containerprofiles "swarm/packages/swarmd/internal/containerprofiles"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func (s *Server) verifyContainerProfileMountsForPrincipal(principal identity.Principal, mounts []containerprofiles.Mount) ([]containerprofiles.Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	verified := make([]containerprofiles.Mount, 0, len(mounts))
	for _, mount := range mounts {
		sourcePath, workspacePath, workspaceName, err := s.verifyWorkspaceMountPathForPrincipal(principal, mount.SourcePath, mount.WorkspacePath)
		if err != nil {
			return nil, err
		}
		mount.SourcePath = sourcePath
		mount.WorkspacePath = workspacePath
		mount.WorkspaceName = workspaceName
		verified = append(verified, mount)
	}
	return verified, nil
}

func (s *Server) verifyLocalContainerMountsForPrincipal(principal identity.Principal, mounts []localcontainers.Mount) ([]localcontainers.Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	verified := make([]localcontainers.Mount, 0, len(mounts))
	for _, mount := range mounts {
		sourcePath, workspacePath, workspaceName, err := s.verifyWorkspaceMountPathForPrincipal(principal, mount.SourcePath, mount.WorkspacePath)
		if err != nil {
			return nil, err
		}
		mount.SourcePath = sourcePath
		mount.WorkspacePath = workspacePath
		mount.WorkspaceName = workspaceName
		verified = append(verified, mount)
	}
	return verified, nil
}

func (s *Server) verifyWorkspaceMountPathForPrincipal(principal identity.Principal, sourcePath, workspacePath string) (string, string, string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	workspacePath = strings.TrimSpace(workspacePath)
	pathToVerify := sourcePath
	if pathToVerify == "" {
		pathToVerify = workspacePath
	}
	if pathToVerify == "" {
		return "", "", "", errors.New("mount source_path is required")
	}

	owned, err := s.resolveAccountOwnedPath(principal, pathToVerify)
	if err != nil {
		return "", "", "", fmt.Errorf("verify workspace mount source_path: %w", err)
	}
	if workspacePath != "" {
		declared, err := s.resolveAccountOwnedPath(principal, workspacePath)
		if err != nil {
			return "", "", "", fmt.Errorf("verify workspace mount workspace_path: %w", err)
		}
		if strings.TrimSpace(declared.WorkspacePath) != strings.TrimSpace(owned.WorkspacePath) {
			return "", "", "", errors.New("workspace mount source_path must belong to workspace_path")
		}
	}

	return strings.TrimSpace(owned.ResolvedPath), strings.TrimSpace(owned.WorkspacePath), strings.TrimSpace(owned.Scope.WorkspaceName), nil
}
