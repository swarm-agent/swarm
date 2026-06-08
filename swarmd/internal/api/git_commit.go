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
)

const workspaceGitCommitTimeout = 30 * time.Second

type workspaceGitCommitRequest struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	Message       string `json:"message,omitempty"`
	All           bool   `json:"all,omitempty"`
}

type workspaceGitCommitResponse struct {
	OK            bool     `json:"ok"`
	WorkspacePath string   `json:"workspace_path,omitempty"`
	CWD           string   `json:"cwd,omitempty"`
	Argv          []string `json:"argv,omitempty"`
	ExitCode      int      `json:"exit_code"`
	TimedOut      bool     `json:"timed_out,omitempty"`
	Output        string   `json:"output,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req workspaceGitCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeGitCommitResponse(w, r, req, principal)
}

func (s *Server) handleManagedHostWorkspaceGitCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req workspaceGitCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	targetSwarmID := strings.TrimSpace(r.URL.Query().Get("swarm_id"))
	if targetSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("swarm_id is required"))
		return
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetSwarmID), targetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	var peerResp workspaceGitCommitResponse
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostWorkspaceGitCommitPath, req, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, peerResp)
}

func (s *Server) writeGitCommitResponse(w http.ResponseWriter, r *http.Request, req workspaceGitCommitRequest, principal identity.Principal) {
	if sessionID := strings.TrimSpace(r.URL.Query().Get("session_id")); sessionID != "" {
		if err := s.enforceSessionBindingWriteAccess(principal, sessionID, "git commit"); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
	}
	workspacePath, err := s.resolveGitCommitWorkspacePath(req, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, errors.New("commit message is required"))
		return
	}
	result, err := runWorkspaceGitCommit(r.Context(), workspacePath, message, req.All)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

func (s *Server) resolveGitCommitWorkspacePath(req workspaceGitCommitRequest, principal identity.Principal) (string, error) {
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(req.CWD)
	}
	if workspacePath == "" && s != nil && s.workspace != nil {
		current, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
		if err != nil {
			return "", err
		}
		if ok {
			workspacePath = strings.TrimSpace(current.ResolvedPath)
		}
	}
	if workspacePath == "" {
		return "", errors.New("workspace_path is required")
	}
	owned, err := s.resolveAccountOwnedPath(principal, workspacePath)
	if err != nil {
		return "", err
	}
	return owned.ResolvedPath, nil
}

func runWorkspaceGitCommit(parent context.Context, workspacePath, message string, all bool) (workspaceGitCommitResponse, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	message = strings.TrimSpace(message)
	if workspacePath == "" {
		return workspaceGitCommitResponse{}, errors.New("workspace_path is required")
	}
	if message == "" {
		return workspaceGitCommitResponse{}, errors.New("commit message is required")
	}

	argv := []string{"commit", "-m", message}
	if all {
		argv = append(argv, "--all")
	}
	ctx, cancel := context.WithTimeout(parent, workspaceGitCommitTimeout)
	defer cancel()

	secretCheckOutput, secretCheckErr := runWorkspaceGitSecretCheck(ctx, workspacePath)
	if secretCheckErr != nil {
		combined := strings.TrimSpace(secretCheckOutput)
		response := workspaceGitCommitResponse{
			OK:            false,
			WorkspacePath: workspacePath,
			CWD:           workspacePath,
			Argv:          []string{"scripts/check-secrets.sh"},
			ExitCode:      workspaceGitCommandExitCode(secretCheckErr),
			TimedOut:      errors.Is(ctx.Err(), context.DeadlineExceeded),
			Output:        combined,
			Summary:       "secret check failed before git commit",
		}
		if combined != "" {
			response.Error = fmt.Sprintf("secret check failed before git commit: %s", combined)
		} else {
			response.Error = fmt.Sprintf("secret check failed before git commit: %v", secretCheckErr)
		}
		return response, errors.New(response.Error)
	}

	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = workspacePath
	cmd.Env = filteredWorkspaceGitCommitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	exitCode := workspaceGitCommandExitCode(err)
	combinedParts := []string{}
	if strings.TrimSpace(secretCheckOutput) != "" {
		combinedParts = append(combinedParts, strings.TrimSpace(secretCheckOutput))
	}
	if strings.TrimSpace(string(output)) != "" {
		combinedParts = append(combinedParts, strings.TrimSpace(string(output)))
	}
	combined := strings.Join(combinedParts, "\n")
	response := workspaceGitCommitResponse{
		OK:            err == nil,
		WorkspacePath: workspacePath,
		CWD:           workspacePath,
		Argv:          append([]string{"git"}, argv...),
		ExitCode:      exitCode,
		TimedOut:      timedOut,
		Output:        combined,
		Summary:       workspaceGitCommitSummary(argv, exitCode, timedOut),
	}
	if err != nil {
		if combined != "" {
			response.Error = fmt.Sprintf("git commit failed: %s", combined)
			return response, errors.New(response.Error)
		}
		response.Error = fmt.Sprintf("git commit failed: %v", err)
		return response, errors.New(response.Error)
	}
	return response, nil
}

func runWorkspaceGitSecretCheck(parent context.Context, workspacePath string) (string, error) {
	rootCmd := exec.CommandContext(parent, "git", "rev-parse", "--show-toplevel")
	rootCmd.Dir = workspacePath
	rootCmd.Env = filteredWorkspaceGitCommitEnv(os.Environ())
	rootOutput, err := rootCmd.CombinedOutput()
	if err != nil {
		return string(rootOutput), err
	}
	repoRoot := strings.TrimSpace(string(rootOutput))
	if repoRoot == "" {
		return "", errors.New("git rev-parse returned an empty repository root")
	}
	secretCheckScript := filepath.Join(repoRoot, "scripts", "check-secrets.sh")
	if _, err := os.Stat(secretCheckScript); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(parent, "bash", secretCheckScript)
	cmd.Dir = repoRoot
	cmd.Env = filteredWorkspaceGitCommitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func workspaceGitCommandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func filteredWorkspaceGitCommitEnv(base []string) []string {
	if len(base) == 0 {
		return nil
	}
	blocked := map[string]struct{}{
		"GIT_AUTHOR_NAME":     {},
		"GIT_AUTHOR_EMAIL":    {},
		"GIT_AUTHOR_DATE":     {},
		"GIT_COMMITTER_NAME":  {},
		"GIT_COMMITTER_EMAIL": {},
		"GIT_COMMITTER_DATE":  {},
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.Index(entry, "="); idx >= 0 {
			key = entry[:idx]
		}
		if _, deny := blocked[key]; deny {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func workspaceGitCommitSummary(argv []string, exitCode int, timedOut bool) string {
	summary := fmt.Sprintf("git %s exited %d", strings.Join(argv, " "), exitCode)
	if timedOut {
		summary += " (timed out)"
	}
	return summary
}
