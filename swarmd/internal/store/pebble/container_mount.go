package pebblestore

import "strings"

// ContainerMount describes a host/workspace mount associated with a runtime container.
// It is kept with topology/deploy records after the legacy local-container store removal.
type ContainerMount struct {
	SourcePath    string `json:"source_path"`
	TargetPath    string `json:"target_path,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type SwarmLocalContainerMount = ContainerMount

func normalizeContainerMounts(mounts []ContainerMount) []ContainerMount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]ContainerMount, 0, len(mounts))
	for _, mount := range mounts {
		mount.SourcePath = strings.TrimSpace(mount.SourcePath)
		mount.TargetPath = strings.TrimSpace(mount.TargetPath)
		mount.Mode = strings.TrimSpace(mount.Mode)
		mount.WorkspacePath = strings.TrimSpace(mount.WorkspacePath)
		mount.WorkspaceName = strings.TrimSpace(mount.WorkspaceName)
		if mount.SourcePath == "" && mount.TargetPath == "" && mount.WorkspacePath == "" {
			continue
		}
		out = append(out, mount)
	}
	return out
}

func normalizeSwarmLocalContainerMounts(mounts []SwarmLocalContainerMount) []SwarmLocalContainerMount {
	return normalizeContainerMounts(mounts)
}
