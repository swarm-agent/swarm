package api

import (
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) verifyContainerProfileMountsForPrincipal(principal identity.Principal, mounts []pebblestore.ContainerProfileMount) ([]pebblestore.ContainerProfileMount, error) {
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, identity.ErrPrincipalRequired
	}
	verified := make([]pebblestore.ContainerProfileMount, 0, len(mounts))
	for _, mount := range mounts {
		mount.SourcePath = strings.TrimSpace(mount.SourcePath)
		mount.TargetPath = strings.TrimSpace(mount.TargetPath)
		mount.Mode = strings.TrimSpace(mount.Mode)
		mount.WorkspacePath = strings.TrimSpace(mount.WorkspacePath)
		mount.WorkspaceName = strings.TrimSpace(mount.WorkspaceName)
		if mount.SourcePath == "" && mount.WorkspacePath == "" {
			return nil, fmt.Errorf("mount source_path or workspace_path is required")
		}
		verified = append(verified, mount)
	}
	return verified, nil
}
