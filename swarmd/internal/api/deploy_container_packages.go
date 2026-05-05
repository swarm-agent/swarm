package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	deployruntime "swarm/packages/swarmd/internal/deploy"
)

const (
	deployContainerPackageDefaultsPathID = "deploy.container.package-defaults.v1"
	deployContainerPackageValidatePathID = "deploy.container.package-validate.v1"
	deployContainerPackageSuggestPathID  = "deploy.container.package-suggest.v1"
	containerPackageWorkspaceSource      = "workspace_scan"
	containerPackageSuggestMaxDepth      = 3
	containerPackageSuggestMaxFiles      = 4000
)

var (
	aptPackageNamePattern               = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	errContainerPackageSuggestFileLimit = errors.New("container package suggest file limit reached")
)

type deployContainerPackageSuggestion struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type deployContainerPackageSuggestionAccumulator struct {
	Reasons map[string]struct{}
}

func (s *Server) handleDeployContainerPackageDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	defaults := deployruntime.ContainerPackageDefaults()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"path_id":         deployContainerPackageDefaultsPathID,
		"base_image":      defaults.BaseImage,
		"package_manager": defaults.PackageManager,
	})
}

func (s *Server) handleDeployContainerPackageValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		PackageName string `json:"package_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	packageName := strings.ToLower(strings.TrimSpace(req.PackageName))
	if packageName == "" {
		writeError(w, http.StatusBadRequest, errors.New("package_name is required"))
		return
	}
	if !aptPackageNamePattern.MatchString(packageName) {
		writeError(w, http.StatusBadRequest, errors.New("package name must be a valid apt package identifier"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apt-cache", "show", "--", packageName)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil || text == "" || strings.Contains(strings.ToLower(text), "no packages found") {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":           false,
			"path_id":      deployContainerPackageValidatePathID,
			"package_name": packageName,
			"valid":        false,
			"error":        fmt.Sprintf("apt package %q was not found on this host", packageName),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"path_id":      deployContainerPackageValidatePathID,
		"package_name": packageName,
		"valid":        true,
	})
}

func (s *Server) handleDeployContainerPackageSuggest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service is not configured"))
		return
	}
	var req struct {
		WorkspacePaths []string `json:"workspace_paths"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePaths := normalizeContainerPackageWorkspacePaths(req.WorkspacePaths)
	if len(workspacePaths) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("workspace_paths must include at least one workspace"))
		return
	}

	scanRoots := make([]string, 0, len(workspacePaths))
	seenWorkspaces := make(map[string]struct{}, len(workspacePaths))
	seenRoots := make(map[string]struct{}, len(workspacePaths)*2)
	for _, workspacePath := range workspacePaths {
		scope, err := s.workspace.ScopeForWorkspace(workspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("resolve workspace %q: %w", workspacePath, err))
			return
		}
		resolvedWorkspace := strings.TrimSpace(scope.WorkspacePath)
		if resolvedWorkspace == "" {
			continue
		}
		if _, ok := seenWorkspaces[resolvedWorkspace]; ok {
			continue
		}
		seenWorkspaces[resolvedWorkspace] = struct{}{}
		roots := scope.Directories
		if len(roots) == 0 {
			roots = []string{resolvedWorkspace}
		}
		for _, root := range roots {
			clean := filepath.Clean(strings.TrimSpace(root))
			if clean == "" || clean == "." {
				continue
			}
			if _, ok := seenRoots[clean]; ok {
				continue
			}
			seenRoots[clean] = struct{}{}
			scanRoots = append(scanRoots, clean)
		}
	}

	suggestions, err := suggestContainerPackagesForRoots(scanRoots)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"path_id":         deployContainerPackageSuggestPathID,
		"workspace_paths": workspacePaths,
		"packages":        suggestions,
	})
}

func normalizeContainerPackageWorkspacePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		clean := filepath.Clean(strings.TrimSpace(raw))
		if clean == "" || clean == "." {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func suggestContainerPackagesForRoots(roots []string) ([]deployContainerPackageSuggestion, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	accumulators := make(map[string]*deployContainerPackageSuggestionAccumulator)
	for _, root := range roots {
		if err := collectContainerPackageSuggestionsForRoot(root, accumulators); err != nil {
			return nil, err
		}
	}
	if len(accumulators) == 0 {
		return nil, nil
	}
	out := make([]deployContainerPackageSuggestion, 0, len(accumulators))
	for name, accumulator := range accumulators {
		reasons := make([]string, 0, len(accumulator.Reasons))
		for reason := range accumulator.Reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		out = append(out, deployContainerPackageSuggestion{
			Name:   name,
			Source: containerPackageWorkspaceSource,
			Reason: strings.Join(reasons, "; "),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func collectContainerPackageSuggestionsForRoot(root string, accumulators map[string]*deployContainerPackageSuggestionAccumulator) error {
	filesSeen := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		depth := containerPackageSuggestionDepth(rel)
		if d.IsDir() {
			if rel != "." && shouldSkipContainerPackageSuggestDir(d.Name()) {
				return fs.SkipDir
			}
			if depth > containerPackageSuggestMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if depth > containerPackageSuggestMaxDepth {
			return nil
		}
		filesSeen++
		if filesSeen > containerPackageSuggestMaxFiles {
			return errContainerPackageSuggestFileLimit
		}
		addContainerPackageSuggestionsForMarker(accumulators, d.Name(), rel)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errContainerPackageSuggestFileLimit) {
		return fmt.Errorf("scan workspace %q for package suggestions: %w", root, walkErr)
	}
	return nil
}

func addContainerPackageSuggestionsForMarker(accumulators map[string]*deployContainerPackageSuggestionAccumulator, markerName, relativePath string) {
	marker := strings.ToLower(strings.TrimSpace(markerName))
	if marker == "" {
		return
	}
	reason := containerPackageSuggestionReason(markerName, relativePath)
	add := func(pkg string) {
		pkg = strings.ToLower(strings.TrimSpace(pkg))
		if pkg == "" {
			return
		}
		entry, ok := accumulators[pkg]
		if !ok {
			entry = &deployContainerPackageSuggestionAccumulator{Reasons: make(map[string]struct{})}
			accumulators[pkg] = entry
		}
		entry.Reasons[reason] = struct{}{}
	}
	switch marker {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb":
		add("nodejs")
		add("npm")
	case "requirements.txt", "requirements-dev.txt", "pyproject.toml", "pipfile", "poetry.lock", "setup.py", "setup.cfg":
		add("python3-pip")
		add("python3-venv")
	case "cargo.toml", "cargo.lock":
		add("build-essential")
		add("pkg-config")
		add("libssl-dev")
	case "cmakelists.txt":
		add("build-essential")
		add("cmake")
	case "makefile":
		add("make")
	case "gemfile":
		add("ruby-full")
	}
}

func containerPackageSuggestionReason(markerName, relativePath string) string {
	rel := filepath.ToSlash(strings.TrimSpace(relativePath))
	if rel == "" || rel == "." {
		return fmt.Sprintf("%s detected", markerName)
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		return fmt.Sprintf("%s detected", markerName)
	}
	return fmt.Sprintf("%s detected in %s", markerName, dir)
}

func containerPackageSuggestionDepth(relativePath string) int {
	rel := filepath.Clean(strings.TrimSpace(relativePath))
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator))
}

func shouldSkipContainerPackageSuggestDir(name string) bool {
	value := strings.ToLower(strings.TrimSpace(name))
	if value == "" || value == "." || value == ".." {
		return true
	}
	if strings.HasPrefix(value, ".") && value != ".github" {
		return true
	}
	switch value {
	case "node_modules", ".cache", "dist", "build", ".next", "vendor", ".turbo", "coverage", "tmp", "temp", "target", ".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}
