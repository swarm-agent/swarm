package pebblestore

import (
	"path/filepath"
	"sort"
	"strings"
)

const (
	WorkspaceGrantPrimary    = "primary"
	WorkspaceGrantAdditional = "additional"
	WorkspaceGrantWorktree   = "worktree"
	WorkspaceGrantTemporary  = "temporary"
)

// WorkspaceGrant is the typed, durable description of a workspace root that a
// session may use. Path remains present for historical records even when the
// workspace is no longer available on this host. Additional is a stable saved
// account workspace explicitly granted to this session; temporary remains an
// anonymous per-session filesystem permission.
type WorkspaceGrant struct {
	Kind                string `json:"kind"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64  `json:"workspace_generation,omitempty"`
	Path                string `json:"path"`
	Name                string `json:"name,omitempty"`
	Available           *bool  `json:"available,omitempty"`
}

// WorkspaceUsageProjection is the path-free identity projection consumed by
// V3 bootstrap, hydration, and realtime clients. Availability is tri-state:
// nil means historical data did not record it.
type WorkspaceUsageProjection struct {
	Kind                string `json:"kind"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64  `json:"workspace_generation,omitempty"`
	Name                string `json:"name,omitempty"`
	Available           *bool  `json:"available,omitempty"`
}

func NormalizeSessionWorkspaceGrants(session SessionSnapshot) []WorkspaceGrant {
	grants := append([]WorkspaceGrant(nil), session.WorkspaceGrants...)
	if len(grants) == 0 {
		legacyAvailable := true
		if path := strings.TrimSpace(session.WorkspacePath); path != "" {
			grants = append(grants, WorkspaceGrant{Kind: WorkspaceGrantPrimary, Path: path, Name: strings.TrimSpace(session.WorkspaceName), Available: &legacyAvailable})
		}
		if path := strings.TrimSpace(session.WorktreeRootPath); path != "" && path != strings.TrimSpace(session.WorkspacePath) {
			grants = append(grants, WorkspaceGrant{Kind: WorkspaceGrantWorktree, Path: path, Available: &legacyAvailable})
		}
		for _, path := range session.TemporaryWorkspaceRoots {
			grants = append(grants, WorkspaceGrant{Kind: WorkspaceGrantTemporary, Path: path, Available: &legacyAvailable})
		}
	}
	seen := map[string]struct{}{}
	out := make([]WorkspaceGrant, 0, len(grants))
	for _, grant := range grants {
		grant.Kind = strings.ToLower(strings.TrimSpace(grant.Kind))
		grant.WorkspaceID = strings.TrimSpace(grant.WorkspaceID)
		grant.Name = strings.TrimSpace(grant.Name)
		grant.Path = strings.TrimSpace(grant.Path)
		if grant.Kind == "" || grant.Path == "" {
			continue
		}
		if grant.Kind == WorkspaceGrantAdditional && grant.WorkspaceID == "" {
			continue
		}
		if clean, err := filepath.Abs(grant.Path); err == nil {
			grant.Path = filepath.Clean(clean)
		}
		key := grant.Kind + "\x00" + grant.WorkspaceID + "\x00" + grant.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, grant)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return workspaceGrantRank(out[i].Kind) < workspaceGrantRank(out[j].Kind)
		}
		if out[i].WorkspaceID != out[j].WorkspaceID {
			return out[i].WorkspaceID < out[j].WorkspaceID
		}
		return out[i].Path < out[j].Path
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func workspaceGrantRank(kind string) int {
	switch kind {
	case WorkspaceGrantPrimary:
		return 0
	case WorkspaceGrantAdditional:
		return 1
	case WorkspaceGrantWorktree:
		return 2
	case WorkspaceGrantTemporary:
		return 3
	default:
		return 4
	}
}

func WorkspaceUsageFromGrants(grants []WorkspaceGrant) []WorkspaceUsageProjection {
	out := make([]WorkspaceUsageProjection, 0, len(grants))
	for _, grant := range grants {
		// Anonymous temporary paths are access grants, not stable workspace
		// identities, and therefore do not belong in the global usage index.
		if strings.TrimSpace(grant.WorkspaceID) == "" {
			continue
		}
		out = append(out, WorkspaceUsageProjection{
			Kind: grant.Kind, WorkspaceID: grant.WorkspaceID,
			WorkspaceGeneration: grant.WorkspaceGeneration,
			Name: grant.Name, Available: grant.Available,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
