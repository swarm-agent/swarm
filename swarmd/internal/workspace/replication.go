package workspace

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ReplicationTargetModeLocal  = "local"
	ReplicationTargetModeRemote = "remote"

	ReplicationModeBundle = "bundle"
	ReplicationModeCopy   = "copy"

	ReplicationSyncModeManaged = "managed"

	ReplicationSyncModuleCredentials = "credentials"
	ReplicationSyncModuleAgents      = "agents"
	ReplicationSyncModuleCustomTools = "custom_tools"
	ReplicationSyncModuleSkills      = "skills"
)

type ReplicationWorkspaceInput struct {
	SourceWorkspacePath string `json:"source_workspace_path"`
	ReplicationMode     string `json:"replication_mode,omitempty"`
	Writable            *bool  `json:"writable,omitempty"`
}

type NormalizedReplicationWorkspace struct {
	SourceWorkspacePath string `json:"source_workspace_path"`
	ReplicationMode     string `json:"replication_mode"`
	Writable            bool   `json:"writable"`
	GitWorkspace        bool   `json:"git_workspace"`
}

type ReplicationSyncInput struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode,omitempty"`
	Modules []string `json:"modules,omitempty"`
}

type NormalizedReplicationSync struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode,omitempty"`
	Modules []string `json:"modules,omitempty"`
}

func NormalizeReplicationTargetMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ReplicationTargetModeLocal:
		return ReplicationTargetModeLocal
	case ReplicationTargetModeRemote:
		return ReplicationTargetModeRemote
	default:
		return ""
	}
}

func NormalizeReplicationMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ReplicationModeBundle:
		return ReplicationModeBundle
	case ReplicationModeCopy:
		return ReplicationModeCopy
	default:
		return ""
	}
}

func NormalizeReplicationSyncMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ReplicationSyncModeManaged:
		return ReplicationSyncModeManaged
	default:
		return ""
	}
}

func NormalizeReplicationSyncModule(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ReplicationSyncModuleCredentials:
		return ReplicationSyncModuleCredentials
	case ReplicationSyncModuleAgents:
		return ReplicationSyncModuleAgents
	case ReplicationSyncModuleCustomTools:
		return ReplicationSyncModuleCustomTools
	case ReplicationSyncModuleSkills:
		return ReplicationSyncModuleSkills
	default:
		return ""
	}
}

func DefaultReplicationSyncModules() []string {
	return []string{ReplicationSyncModuleCredentials, ReplicationSyncModuleAgents, ReplicationSyncModuleCustomTools, ReplicationSyncModuleSkills}
}

func NormalizeReplicationSyncModules(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := NormalizeReplicationSyncModule(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ReplicationSyncModuleEnabled(modules []string, module string) bool {
	module = NormalizeReplicationSyncModule(module)
	if module == "" {
		return false
	}
	for _, current := range NormalizeReplicationSyncModules(modules) {
		if current == module {
			return true
		}
	}
	return false
}

func NormalizeReplicationSync(input ReplicationSyncInput) NormalizedReplicationSync {
	out := NormalizedReplicationSync{Enabled: input.Enabled}
	if !out.Enabled {
		return out
	}
	out.Mode = NormalizeReplicationSyncMode(input.Mode)
	if out.Mode == "" {
		out.Mode = ReplicationSyncModeManaged
	}
	out.Modules = NormalizeReplicationSyncModules(input.Modules)
	if len(out.Modules) == 0 {
		out.Modules = DefaultReplicationSyncModules()
	}
	return out
}

func (s *Service) IsGitWorkspace(path string) (bool, error) {
	_, gitWorkspace, err := normalizeReplicationWorkspacePath(path)
	if err != nil {
		return false, err
	}
	return gitWorkspace, nil
}

func (s *Service) DefaultReplicationMode(path string) (string, error) {
	_, gitWorkspace, err := normalizeReplicationWorkspacePath(path)
	if err != nil {
		return "", err
	}
	if gitWorkspace {
		return ReplicationModeBundle, nil
	}
	return ReplicationModeCopy, nil
}

func (s *Service) NormalizeReplicationWorkspace(input ReplicationWorkspaceInput) (NormalizedReplicationWorkspace, error) {
	resolvedPath, gitWorkspace, err := normalizeReplicationWorkspacePath(input.SourceWorkspacePath)
	if err != nil {
		return NormalizedReplicationWorkspace{}, err
	}
	mode := NormalizeReplicationMode(input.ReplicationMode)
	if mode == "" {
		if gitWorkspace {
			mode = ReplicationModeBundle
		} else {
			mode = ReplicationModeCopy
		}
	}
	writable := true
	if input.Writable != nil {
		writable = *input.Writable
	}
	return NormalizedReplicationWorkspace{
		SourceWorkspacePath: resolvedPath,
		ReplicationMode:     mode,
		Writable:            writable,
		GitWorkspace:        gitWorkspace,
	}, nil
}

func (s *Service) NormalizeReplicationWorkspaces(inputs []ReplicationWorkspaceInput) ([]NormalizedReplicationWorkspace, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([]NormalizedReplicationWorkspace, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		normalized, err := s.NormalizeReplicationWorkspace(input)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized.SourceWorkspacePath]; ok {
			return nil, fmt.Errorf("workspace %q was selected more than once", normalized.SourceWorkspacePath)
		}
		seen[normalized.SourceWorkspacePath] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func (s *Service) AddReplicationLink(path string, link pebblestore.WorkspaceReplicationLink) (pebblestore.WorkspaceReplicationLink, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkspaceReplicationLink{}, fmt.Errorf("workspace store is not configured")
	}
	_, stored, err := s.store.AddReplicationLink(path, link)
	if err != nil {
		return pebblestore.WorkspaceReplicationLink{}, err
	}
	return stored, nil
}

func (s *Service) RemoveReplicationLink(path, linkID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("workspace store is not configured")
	}
	_, err := s.store.RemoveReplicationLink(path, linkID)
	return err
}

func (s *Service) RemoveReplicationLinksByTargetSwarmID(targetSwarmID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("workspace store is not configured")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return fmt.Errorf("target swarm id is required")
	}

	entries, err := s.ListKnown(100000)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, link := range entry.ReplicationLinks {
			if !strings.EqualFold(strings.TrimSpace(link.TargetSwarmID), targetSwarmID) {
				continue
			}
			if err := s.RemoveReplicationLink(entry.Path, link.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeReplicationWorkspacePath(path string) (resolvedPath string, gitWorkspace bool, err error) {
	resolvedPath, err = resolvePath(path)
	if err != nil {
		return "", false, err
	}
	if err := ensureWorkspaceDirectory(resolvedPath); err != nil {
		return "", false, err
	}
	gitWorkspace, _, _ = detectWorkspaceSignals(resolvedPath)
	return resolvedPath, gitWorkspace, nil
}
