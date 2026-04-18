package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type workspaceCandidate struct {
	Path  string
	Name  string
	Score int
}

func discoverWorkspaceCandidates(root string, query string, limit int) ([]workspaceCandidate, error) {
	if limit <= 0 {
		limit = 200
	}
	maxFound := limit * 6
	if maxFound < 300 {
		maxFound = 300
	}
	roots := candidateRoots(root)
	seen := make(map[string]workspaceCandidate, 1024)

	for _, scanRoot := range roots {
		_ = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
			if len(seen) >= maxFound {
				return fs.SkipDir
			}
			if err != nil {
				return fs.SkipDir
			}
			if !d.IsDir() {
				return nil
			}

			name := strings.ToLower(strings.TrimSpace(d.Name()))
			if shouldSkipDirName(name) {
				return fs.SkipDir
			}

			rel, relErr := filepath.Rel(scanRoot, path)
			if relErr != nil {
				rel = path
			}
			depth := pathDepth(rel)
			if depth > 4 {
				return fs.SkipDir
			}

			if !isWorkspaceLike(path, depth) {
				return nil
			}
			clean := filepath.Clean(path)
			if _, exists := seen[clean]; exists {
				return nil
			}
			candidate := workspaceCandidate{
				Path: clean,
				Name: filepath.Base(clean),
			}
			seen[clean] = candidate
			return nil
		})
	}

	all := make([]workspaceCandidate, 0, len(seen))
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, candidate := range seen {
		candidate.Score = scoreCandidate(candidate, needle)
		if needle != "" && candidate.Score < 0 {
			continue
		}
		all = append(all, candidate)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if len(all[i].Path) != len(all[j].Path) {
			return len(all[i].Path) < len(all[j].Path)
		}
		return all[i].Path < all[j].Path
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func candidateRoots(seed string) []string {
	roots := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			return
		}
		info, err := os.Stat(clean)
		if err != nil || !info.IsDir() {
			return
		}
		roots = append(roots, clean)
		seen[clean] = struct{}{}
	}

	add(seed)
	add(filepath.Dir(seed))
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	return roots
}

func shouldSkipDirName(name string) bool {
	switch name {
	case "", ".", "..":
		return false
	case ".git", "node_modules", ".cache", "dist", "build", ".next", "vendor", ".turbo":
		return true
	default:
		return false
	}
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	parts := strings.Split(rel, string(filepath.Separator))
	count := 0
	for _, part := range parts {
		if strings.TrimSpace(part) != "" && part != "." {
			count++
		}
	}
	return count
}

func isWorkspaceLike(path string, depth int) bool {
	if depth <= 1 {
		return true
	}
	if dirHas(path, ".git") {
		return true
	}
	if fileExists(filepath.Join(path, "AGENTS.md")) {
		return true
	}
	if fileExists(filepath.Join(path, "CLAUDE.md")) {
		return true
	}
	if dirHas(path, ".swarm") {
		return true
	}
	return false
}

func dirHas(path string, name string) bool {
	info, err := os.Stat(filepath.Join(path, name))
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func scoreCandidate(candidate workspaceCandidate, query string) int {
	base := 20 - len(candidate.Path)/12
	if query == "" {
		return base
	}
	target := strings.ToLower(candidate.Name + " " + candidate.Path)
	return fuzzyScore(target, query) + base
}

func fuzzyScore(text string, query string) int {
	text = strings.ToLower(strings.TrimSpace(text))
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	if strings.Contains(text, query) {
		score := 50
		if strings.HasPrefix(text, query) {
			score += 20
		}
		score -= len(text) / 20
		return score
	}

	score := 0
	queryRunes := []rune(query)
	index := 0
	streak := 0
	for _, r := range text {
		if index >= len(queryRunes) {
			break
		}
		if r == queryRunes[index] {
			streak++
			score += 8 + (streak * 2)
			index++
			continue
		}
		streak = 0
	}
	if index < len(queryRunes) {
		return -1
	}
	score -= len(text) / 24
	return score
}
