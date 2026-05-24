package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const gitSyncCommandTimeout = 45 * time.Second

const (
	managedHostGitSyncApplyPath = "/v1/swarm/managed-hosts/git/sync/apply"
	peerGitSyncApplyPath        = "/v1/swarm/peer/git/sync/apply"
	peerUpdateRunPath           = "/v1/swarm/peer/update/run"
	peerUpdateStatusPath        = "/v1/swarm/peer/update/status"
)

type gitSyncInspectRequest struct {
	Path          string `json:"path,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	DevRoot       string `json:"dev_root,omitempty"`
	RequireClean  *bool  `json:"require_clean,omitempty"`
}

type gitSyncApplyRequest struct {
	TargetPath    string `json:"target_path,omitempty"`
	Path          string `json:"path,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	DevRoot       string `json:"dev_root,omitempty"`
	SourceRepo    string `json:"source_repo,omitempty"`
	SyncRef       string `json:"sync_ref,omitempty"`
	GitBundle     []byte `json:"git_bundle,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	Commit        string `json:"commit,omitempty"`
	TreeSHA       string `json:"tree_sha,omitempty"`
	Tree          string `json:"tree,omitempty"`
	Destructive   bool   `json:"destructive,omitempty"`
	RequireClean  *bool  `json:"require_clean,omitempty"`
}

type managedHostGitSyncApplyRequest struct {
	TargetSwarmID       string `json:"target_swarm_id,omitempty"`
	SourceWorkspacePath string `json:"source_workspace_path,omitempty"`
	WorkspacePath       string `json:"workspace_path,omitempty"`
	Path                string `json:"path,omitempty"`
	DevRoot             string `json:"dev_root,omitempty"`
	SourceRepo          string `json:"source_repo,omitempty"`
	SyncRef             string `json:"sync_ref,omitempty"`
	Branch              string `json:"branch,omitempty"`
	CommitSHA           string `json:"commit_sha,omitempty"`
	Commit              string `json:"commit,omitempty"`
	TreeSHA             string `json:"tree_sha,omitempty"`
	Tree                string `json:"tree,omitempty"`
	Destructive         bool   `json:"destructive,omitempty"`
	RequireClean        *bool  `json:"require_clean,omitempty"`
}

type gitSyncInspectResponse struct {
	OK          bool     `json:"ok"`
	Path        string   `json:"path,omitempty"`
	RepoRoot    string   `json:"repo_root,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Head        string   `json:"head,omitempty"`
	HeadShort   string   `json:"head_short,omitempty"`
	Tree        string   `json:"tree,omitempty"`
	Clean       bool     `json:"clean"`
	StatusShort []string `json:"status_short,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type gitSyncApplyResponse struct {
	OK       bool                   `json:"ok"`
	Warning  string                 `json:"warning,omitempty"`
	Before   gitSyncInspectResponse `json:"before,omitempty"`
	After    gitSyncInspectResponse `json:"after,omitempty"`
	Commands []gitSyncCommandResult `json:"commands,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

type managedHostGitSyncApplyResponse struct {
	OK      bool                                  `json:"ok"`
	Warning string                                `json:"warning,omitempty"`
	Source  gitSyncInspectResponse                `json:"source,omitempty"`
	Targets []managedHostGitSyncApplyTargetResult `json:"targets,omitempty"`
	Error   string                                `json:"error,omitempty"`
}

type managedHostGitSyncApplyTargetResult struct {
	OK         bool                                       `json:"ok"`
	SwarmID    string                                     `json:"swarm_id,omitempty"`
	Name       string                                     `json:"name,omitempty"`
	TargetPath string                                     `json:"target_path,omitempty"`
	Binding    pebblestore.TopologyWorkspaceBindingRecord `json:"binding,omitempty"`
	Apply      gitSyncApplyResponse                       `json:"apply,omitempty"`
	Error      string                                     `json:"error,omitempty"`
}

type gitSyncCommandResult struct {
	Argv     []string `json:"argv,omitempty"`
	ExitCode int      `json:"exit_code"`
	Output   string   `json:"output,omitempty"`
}

func (s *Server) handleGitSyncInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req gitSyncInspectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := firstNonEmpty(req.Path, req.WorkspacePath, req.DevRoot)
	owned, err := s.resolveAccountOwnedPath(principal, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path = owned.ResolvedPath
	requireClean := true
	if req.RequireClean != nil {
		requireClean = *req.RequireClean
	}
	resp, err := inspectGitSyncRepo(r.Context(), path, requireClean)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGitSyncApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req gitSyncApplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	targetPath := firstNonEmpty(req.TargetPath, req.Path, req.WorkspacePath, req.DevRoot)
	owned, err := s.resolveAccountOwnedPath(principal, targetPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, gitSyncApplyResponse{OK: false, Warning: gitSyncDestructiveWarning(), Error: err.Error()})
		return
	}
	req.TargetPath = owned.ResolvedPath
	req.Path = ""
	req.WorkspacePath = ""
	req.DevRoot = ""
	resp, err := applyGitSync(r.Context(), req)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleManagedHostGitSyncApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req managedHostGitSyncApplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.TargetSwarmID) == "" {
		req.TargetSwarmID = strings.TrimSpace(r.URL.Query().Get("swarm_id"))
	}
	resp, status, err := s.applyManagedHostGitSync(r.Context(), r, req)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePeerGitSyncApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusForbidden, errors.New("peer git sync apply requires persisted peer account binding"))
		return
	}
	var req gitSyncApplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, gitSyncApplyResponse{OK: false, Warning: gitSyncDestructiveWarning(), Error: err.Error()})
		return
	}
	targetPath := firstNonEmpty(req.TargetPath, req.Path, req.WorkspacePath, req.DevRoot)
	owned, err := s.resolveAccountOwnedPath(principal, targetPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, gitSyncApplyResponse{OK: false, Warning: gitSyncDestructiveWarning(), Error: err.Error()})
		return
	}
	req.TargetPath = owned.ResolvedPath
	req.Path = ""
	req.WorkspacePath = ""
	req.DevRoot = ""
	resp, err := applyGitSync(r.Context(), req)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) applyManagedHostGitSync(ctx context.Context, r *http.Request, req managedHostGitSyncApplyRequest) (managedHostGitSyncApplyResponse, int, error) {
	if s == nil || s.topology == nil {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusInternalServerError, errors.New("topology service is not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	sourcePath := firstNonEmpty(req.SourceWorkspacePath, req.WorkspacePath, req.Path, req.DevRoot)
	if sourcePath == "" {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, errors.New("source_workspace_path is required")
	}
	ownedSource, err := s.resolveAccountOwnedPath(principal, sourcePath)
	if err != nil {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, err
	}
	sourcePath = ownedSource.WorkspacePath
	if strings.TrimSpace(req.TargetSwarmID) == "" {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	if !req.Destructive {
		return managedHostGitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, errors.New("destructive=true is required because managed git sync resets --hard and runs git clean -fd on managed hosts")
	}
	source, err := inspectGitSyncRepo(ctx, sourcePath, true)
	if err != nil {
		return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, fmt.Errorf("inspect source workspace: %w", err)
	}
	branch := firstNonEmpty(req.Branch, source.Branch)
	commit := firstNonEmpty(req.CommitSHA, req.Commit, source.Head)
	tree := firstNonEmpty(req.TreeSHA, req.Tree, source.Tree)
	if commit != source.Head {
		return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusConflict, fmt.Errorf("source head mismatch: got %s want %s", source.Head, commit)
	}
	if tree != source.Tree {
		return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusConflict, fmt.Errorf("source tree mismatch: got %s want %s", source.Tree, tree)
	}

	bindings, err := s.managedGitSyncWorkspaceBindingsForAccount(principal.AccountScopeID, source.RepoRoot, req.TargetSwarmID)
	if err != nil {
		return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusBadRequest, err
	}
	if len(bindings) == 0 {
		return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusNotFound, errors.New("no topology workspace bindings found for managed git sync")
	}

	var gitBundle []byte
	if strings.TrimSpace(req.SourceRepo) == "" {
		bundlePath, err := createGitBundle(ctx, source.RepoRoot)
		if err != nil {
			return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusInternalServerError, err
		}
		defer os.Remove(bundlePath)
		gitBundle, err = os.ReadFile(bundlePath)
		if err != nil {
			return managedHostGitSyncApplyResponse{Source: source, Warning: gitSyncDestructiveWarning()}, http.StatusInternalServerError, fmt.Errorf("read git sync bundle: %w", err)
		}
	}

	response := managedHostGitSyncApplyResponse{OK: true, Warning: gitSyncDestructiveWarning(), Source: source, Targets: make([]managedHostGitSyncApplyTargetResult, 0, len(bindings))}
	for _, binding := range bindings {
		targetSwarmID := strings.TrimSpace(binding.DestinationRuntimeSwarmID)
		if targetSwarmID == "" {
			targetSwarmID = strings.TrimSpace(binding.DestinationHostSwarmID)
		}
		if targetSwarmID == "" {
			result := managedHostGitSyncApplyTargetResult{TargetPath: strings.TrimSpace(binding.DestinationWorkspacePath), Binding: binding, Error: "topology workspace binding is missing destination runtime swarm id"}
			response.OK = false
			response.Targets = append(response.Targets, result)
			return response, http.StatusBadRequest, errors.New(result.Error)
		}
		target, _, _, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetSwarmID), targetSwarmID)
		result := managedHostGitSyncApplyTargetResult{SwarmID: targetSwarmID, TargetPath: strings.TrimSpace(binding.DestinationWorkspacePath), Binding: binding}
		if err != nil {
			result.Error = err.Error()
			response.OK = false
			response.Targets = append(response.Targets, result)
			return response, status, err
		}
		result.SwarmID = strings.TrimSpace(target.SwarmID)
		result.Name = strings.TrimSpace(target.Name)
		applyReq := gitSyncApplyRequest{
			TargetPath:   strings.TrimSpace(binding.DestinationWorkspacePath),
			SourceRepo:   strings.TrimSpace(req.SourceRepo),
			SyncRef:      strings.TrimSpace(req.SyncRef),
			GitBundle:    gitBundle,
			Branch:       branch,
			CommitSHA:    commit,
			TreeSHA:      tree,
			Destructive:  true,
			RequireClean: req.RequireClean,
		}
		var peerResp gitSyncApplyResponse
		if err := s.postPeerJSONToSwarmTarget(ctx, *target, peerGitSyncApplyPath, applyReq, &peerResp); err != nil {
			result.Apply = peerResp
			result.Error = err.Error()
			response.OK = false
			response.Targets = append(response.Targets, result)
			return response, http.StatusBadGateway, err
		}
		result.OK = peerResp.OK
		result.Apply = peerResp
		if !peerResp.OK {
			response.OK = false
			if strings.TrimSpace(peerResp.Error) != "" {
				result.Error = peerResp.Error
			}
			response.Targets = append(response.Targets, result)
			return response, http.StatusBadGateway, errors.New(firstNonEmpty(result.Error, "managed host git sync failed"))
		}
		response.Targets = append(response.Targets, result)
	}
	return response, http.StatusOK, nil
}

func (s *Server) managedGitSyncWorkspaceBindingsForAccount(accountScopeID, sourceRepoRoot, targetSwarmID string) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topology == nil {
		return nil, errors.New("topology service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, identity.ErrPrincipalRequired
	}
	bindings, err := s.topology.ListWorkspaceBindingsBySourcePathForAccount(accountScopeID, strings.TrimSpace(sourceRepoRoot), 100000)
	if err != nil {
		return nil, err
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	out := make([]pebblestore.TopologyWorkspaceBindingRecord, 0, len(bindings))
	for _, binding := range bindings {
		runtimeSwarmID := strings.TrimSpace(binding.DestinationRuntimeSwarmID)
		if runtimeSwarmID == "" {
			runtimeSwarmID = strings.TrimSpace(binding.DestinationHostSwarmID)
		}
		if runtimeSwarmID == "" || strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
			continue
		}
		if targetSwarmID != "" && !strings.EqualFold(runtimeSwarmID, targetSwarmID) {
			continue
		}
		out = append(out, binding)
	}
	return out, nil
}

func inspectGitSyncRepo(ctx context.Context, path string, requireClean bool) (gitSyncInspectResponse, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return gitSyncInspectResponse{}, errors.New("path is required")
	}
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return gitSyncInspectResponse{Path: path}, fmt.Errorf("resolve path: %w", err)
	}
	if stat, err := os.Stat(cleaned); err != nil {
		return gitSyncInspectResponse{Path: cleaned}, fmt.Errorf("stat path: %w", err)
	} else if !stat.IsDir() {
		return gitSyncInspectResponse{Path: cleaned}, errors.New("path must be a directory")
	}

	run := func(args ...string) (string, error) {
		result, err := runGitSyncCommand(ctx, cleaned, args...)
		if err != nil {
			return result.Output, err
		}
		return result.Output, nil
	}
	repoRoot, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return gitSyncInspectResponse{Path: cleaned}, fmt.Errorf("git rev-parse --show-toplevel failed: %w", err)
	}
	repoRoot = strings.TrimSpace(repoRoot)
	branch, err := run("branch", "--show-current")
	if err != nil {
		return gitSyncInspectResponse{Path: cleaned, RepoRoot: repoRoot}, fmt.Errorf("git branch --show-current failed: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return gitSyncInspectResponse{Path: cleaned, RepoRoot: repoRoot}, errors.New("repository is not on a named branch")
	}
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return gitSyncInspectResponse{Path: cleaned, RepoRoot: repoRoot, Branch: branch}, fmt.Errorf("git rev-parse HEAD failed: %w", err)
	}
	head = strings.TrimSpace(head)
	headShort, _ := run("rev-parse", "--short", "HEAD")
	tree, err := run("rev-parse", "HEAD^{tree}")
	if err != nil {
		return gitSyncInspectResponse{Path: cleaned, RepoRoot: repoRoot, Branch: branch, Head: head}, fmt.Errorf("git rev-parse HEAD^{tree} failed: %w", err)
	}
	statusRaw, err := run("status", "--short")
	if err != nil {
		return gitSyncInspectResponse{Path: cleaned, RepoRoot: repoRoot, Branch: branch, Head: head, Tree: strings.TrimSpace(tree)}, fmt.Errorf("git status --short failed: %w", err)
	}
	status := splitNonEmptyLines(statusRaw)
	resp := gitSyncInspectResponse{
		OK:          true,
		Path:        cleaned,
		RepoRoot:    repoRoot,
		Branch:      branch,
		Head:        head,
		HeadShort:   strings.TrimSpace(headShort),
		Tree:        strings.TrimSpace(tree),
		Clean:       len(status) == 0,
		StatusShort: status,
	}
	if requireClean && !resp.Clean {
		resp.OK = false
		return resp, errors.New("repository has uncommitted or untracked changes")
	}
	return resp, nil
}

func applyGitSync(ctx context.Context, req gitSyncApplyRequest) (gitSyncApplyResponse, error) {
	targetPath := firstNonEmpty(req.TargetPath, req.Path, req.WorkspacePath, req.DevRoot)
	branch := strings.TrimSpace(req.Branch)
	commit := firstNonEmpty(req.CommitSHA, req.Commit)
	tree := firstNonEmpty(req.TreeSHA, req.Tree)
	if targetPath == "" {
		return gitSyncApplyResponse{}, errors.New("target_path is required")
	}
	if err := validateGitSyncBranch(branch); err != nil {
		return gitSyncApplyResponse{}, err
	}
	if err := validateGitSyncHex("commit_sha", commit); err != nil {
		return gitSyncApplyResponse{}, err
	}
	if err := validateGitSyncHex("tree_sha", tree); err != nil {
		return gitSyncApplyResponse{}, err
	}
	if strings.TrimSpace(req.SyncRef) != "" {
		if err := validateGitSyncRef(req.SyncRef); err != nil {
			return gitSyncApplyResponse{}, err
		}
	}
	if !req.Destructive {
		return gitSyncApplyResponse{Warning: gitSyncDestructiveWarning()}, errors.New("destructive=true is required because git sync resets --hard and runs git clean -fd")
	}

	before, err := inspectGitSyncRepo(ctx, targetPath, false)
	if err != nil {
		return gitSyncApplyResponse{Before: before, Warning: gitSyncDestructiveWarning()}, err
	}
	repoRoot := before.RepoRoot
	if repoRoot == "" {
		repoRoot = before.Path
	}

	commands := make([]gitSyncCommandResult, 0, 7)
	run := func(args ...string) error {
		result, err := runGitSyncCommand(ctx, repoRoot, args...)
		commands = append(commands, result)
		return err
	}
	if len(req.GitBundle) > 0 {
		bundlePath, err := writeGitSyncBundleTemp(req.GitBundle)
		if err != nil {
			return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, err
		}
		defer os.Remove(bundlePath)
		if err := run("fetch", bundlePath, commit); err != nil {
			return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("git bundle fetch failed: %w", err)
		}
	} else if strings.TrimSpace(req.SourceRepo) != "" || strings.TrimSpace(req.SyncRef) != "" {
		sourceRepo := strings.TrimSpace(req.SourceRepo)
		if sourceRepo == "" {
			sourceRepo = "."
		}
		ref := strings.TrimSpace(req.SyncRef)
		if ref == "" {
			ref = commit
		}
		if err := run("fetch", sourceRepo, ref); err != nil {
			return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("git fetch failed: %w", err)
		}
	}
	if err := run("cat-file", "-e", commit+"^{commit}"); err != nil {
		return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("target commit is not available: %w", err)
	}
	if err := run("checkout", "--force", "-B", branch, commit); err != nil {
		return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("git checkout failed: %w", err)
	}
	if err := run("reset", "--hard", commit); err != nil {
		return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("git reset --hard failed: %w", err)
	}
	if err := run("clean", "-fd"); err != nil {
		return gitSyncApplyResponse{Before: before, Commands: commands, Warning: gitSyncDestructiveWarning()}, fmt.Errorf("git clean -fd failed: %w", err)
	}
	if strings.TrimSpace(req.SyncRef) != "" {
		_ = run("update-ref", "-d", strings.TrimSpace(req.SyncRef))
	}
	after, err := inspectGitSyncRepo(ctx, repoRoot, true)
	resp := gitSyncApplyResponse{OK: err == nil, Before: before, After: after, Commands: commands, Warning: gitSyncDestructiveWarning()}
	if err != nil {
		return resp, err
	}
	if after.Branch != branch {
		resp.OK = false
		return resp, fmt.Errorf("branch mismatch after sync: got %q want %q", after.Branch, branch)
	}
	if after.Head != commit {
		resp.OK = false
		return resp, fmt.Errorf("head mismatch after sync: got %s want %s", after.Head, commit)
	}
	if after.Tree != tree {
		resp.OK = false
		return resp, fmt.Errorf("tree mismatch after sync: got %s want %s", after.Tree, tree)
	}
	return resp, nil
}

func writeGitSyncBundleTemp(bundle []byte) (string, error) {
	if len(bundle) == 0 {
		return "", errors.New("git_bundle is empty")
	}
	file, err := os.CreateTemp("", "swarm-git-sync-*.bundle")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.Write(bundle); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write git sync bundle: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func runGitSyncCommand(parent context.Context, dir string, args ...string) (gitSyncCommandResult, error) {
	ctx, cancel := context.WithTimeout(parent, gitSyncCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	result := gitSyncCommandResult{Argv: append([]string{"git"}, args...), ExitCode: exitCode, Output: strings.TrimSpace(string(output))}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("git %s timed out", strings.Join(args, " "))
	}
	if err != nil {
		if result.Output != "" {
			return result, fmt.Errorf("git %s failed: %s", strings.Join(args, " "), result.Output)
		}
		return result, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return result, nil
}

func validateGitSyncBranch(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("branch is required")
	}
	if strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n\r") {
		return fmt.Errorf("invalid branch %q", branch)
	}
	result, err := runGitSyncCommand(context.Background(), ".", "check-ref-format", "--branch", branch)
	if err != nil || strings.TrimSpace(result.Output) == "" {
		return fmt.Errorf("invalid branch %q", branch)
	}
	return nil
}

func validateGitSyncRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n\r") {
		return fmt.Errorf("invalid sync_ref %q", ref)
	}
	if _, err := runGitSyncCommand(context.Background(), ".", "check-ref-format", ref); err != nil {
		return fmt.Errorf("invalid sync_ref %q", ref)
	}
	return nil
}

func validateGitSyncHex(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) != 40 {
		return fmt.Errorf("%s must be a full 40-character SHA", name)
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return fmt.Errorf("%s must be hexadecimal", name)
	}
	return nil
}

func splitNonEmptyLines(value string) []string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func gitSyncDestructiveWarning() string {
	return "git sync performs git reset --hard and git clean -fd in the target repo; uncommitted and untracked target files are deleted"
}
