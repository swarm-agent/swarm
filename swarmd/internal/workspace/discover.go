package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const workspaceDiscoverMaxDepth = 3

var errWorkspaceDiscoverLimitReached = errors.New("workspace discover limit reached")

type DiscoverEntry struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	IsGitRepo    bool   `json:"is_git_repo"`
	HasClaude    bool   `json:"has_claude"`
	HasSwarm     bool   `json:"has_swarm"`
	LastModified int64  `json:"last_modified,omitempty"`
}

func (s *Service) Discover(roots []string, limit int) ([]DiscoverEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	searchRoots := workspaceDiscoverRoots(roots)
	seen := make(map[string]DiscoverEntry, limit*2)
	maxCollected := limit * 4

	for _, root := range searchRoots {
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if len(seen) >= maxCollected {
				return errWorkspaceDiscoverLimitReached
			}
			if path != root && shouldSkipWorkspaceDiscoverDir(d.Name()) {
				return fs.SkipDir
			}
			depth, err := workspaceDiscoverDepth(root, path)
			if err != nil {
				return nil
			}
			if depth > workspaceDiscoverMaxDepth {
				return fs.SkipDir
			}
			info, err := d.Info()
			if err != nil || !info.IsDir() {
				return nil
			}
			item := buildWorkspaceDiscoverEntry(path, d.Name(), info)
			if item == nil {
				return nil
			}
			if _, ok := seen[item.Path]; ok {
				return nil
			}
			seen[item.Path] = *item
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errWorkspaceDiscoverLimitReached) {
			continue
		}
		if len(seen) >= maxCollected {
			break
		}
	}

	out := make([]DiscoverEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftName := strings.ToLower(strings.TrimSpace(out[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(out[j].Name))
		if leftName != rightName {
			return leftName < rightName
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func workspaceDiscoverRoots(requested []string) []string {
	roots := make([]string, 0, len(requested)+8)
	seen := make(map[string]struct{}, len(requested)+8)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		resolved, err := resolvePath(path)
		if err != nil {
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return
		}
		if _, ok := seen[resolved]; ok {
			return
		}
		seen[resolved] = struct{}{}
		roots = append(roots, resolved)
	}
	if len(requested) > 0 {
		for _, root := range requested {
			add(root)
		}
		return roots
	}
	if workspaceRoot, ok := remoteChildWorkspaceRoot(); ok {
		add(workspaceRoot)
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		add(home)
		add(filepath.Join(home, "projects"))
		add(filepath.Join(home, "code"))
		add(filepath.Join(home, "dev"))
		add(filepath.Join(home, "Development"))
		add(filepath.Join(home, "Documents"))
		add(filepath.Join(home, "src"))
		add(filepath.Join(home, "workspace"))
		add(filepath.Join(home, "repos"))
		add(filepath.Join(home, "github"))
	}
	return roots
}

func workspaceDiscoverDepth(root, path string) (int, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0, err
	}
	if rel == "." {
		return 0, nil
	}
	return strings.Count(rel, string(filepath.Separator)) + 1, nil
}

func shouldSkipWorkspaceDiscoverDir(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	switch name {
	case "", ".", "..":
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", ".cache", "dist", "build", ".next", "vendor", ".turbo":
		return true
	default:
		return false
	}
}

func buildWorkspaceDiscoverEntry(fullPath, name string, info fs.FileInfo) *DiscoverEntry {
	clean := filepath.Clean(fullPath)
	isGitRepo, hasClaude, hasSwarm := detectWorkspaceSignals(clean)
	if !isGitRepo && !hasClaude && !hasSwarm {
		return nil
	}
	return &DiscoverEntry{
		Path:         clean,
		Name:         strings.TrimSpace(name),
		IsGitRepo:    isGitRepo,
		HasClaude:    hasClaude,
		HasSwarm:     hasSwarm,
		LastModified: info.ModTime().UnixMilli(),
	}
}

func detectWorkspaceSignals(path string) (isGitRepo bool, hasClaude bool, hasSwarm bool) {
	clean := filepath.Clean(path)
	gitPath := filepath.Join(clean, ".git")
	claudeDir := filepath.Join(clean, ".claude")
	claudeMD := filepath.Join(clean, "CLAUDE.md")
	swarmDir := filepath.Join(clean, ".swarm")
	agentsMD := filepath.Join(clean, "AGENTS.md")
	return pathExists(gitPath), pathExists(claudeDir) || fileExists(claudeMD), pathExists(swarmDir) || fileExists(agentsMD)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
