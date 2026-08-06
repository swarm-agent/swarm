package client

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type GitSnapshot struct {
	WorkspacePath  string          `json:"workspace_path"`
	RepoRoot       string          `json:"repo_root,omitempty"`
	GitDir         string          `json:"git_dir,omitempty"`
	HasGit         bool            `json:"has_git"`
	Clean          bool            `json:"clean"`
	Branch         string          `json:"branch,omitempty"`
	HeadOID        string          `json:"head_oid,omitempty"`
	Upstream       string          `json:"upstream,omitempty"`
	AheadCount     int             `json:"ahead_count"`
	BehindCount    int             `json:"behind_count"`
	StashCount     int             `json:"stash_count"`
	DirtyCount     int             `json:"dirty_count"`
	StagedCount    int             `json:"staged_count"`
	ModifiedCount  int             `json:"modified_count"`
	UntrackedCount int             `json:"untracked_count"`
	ConflictCount  int             `json:"conflict_count"`
	Files          []GitFileStatus `json:"files"`
	Remotes        []GitRemote     `json:"remotes,omitempty"`
	RecentCommits  []GitCommit     `json:"recent_commits,omitempty"`
	RefreshedAt    time.Time       `json:"refreshed_at"`
	DurationMS     int64           `json:"duration_ms"`
}

type GitFileStatus struct {
	Kind      string `json:"kind"`
	XY        string `json:"xy,omitempty"`
	Path      string `json:"path"`
	OrigPath  string `json:"orig_path,omitempty"`
	Staged    bool   `json:"staged,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	Untracked bool   `json:"untracked,omitempty"`
	Conflict  bool   `json:"conflict,omitempty"`
	Submodule string `json:"submodule,omitempty"`
}

type GitRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

type GitCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author,omitempty"`
	UnixTime  int64  `json:"unix_time,omitempty"`
	Subject   string `json:"subject"`
}

type GitCommitResponse struct {
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

type GitCommitSuggestionResponse struct {
	OK            bool   `json:"ok"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	Message       string `json:"message"`
}

func (c *API) GetGitStatus(ctx context.Context, workspacePath string, recentLimit int) (GitSnapshot, error) {
	query := url.Values{}
	if strings.TrimSpace(workspacePath) != "" {
		query.Set("workspace_path", strings.TrimSpace(workspacePath))
	}
	if recentLimit > 0 {
		query.Set("recent_limit", strconv.Itoa(recentLimit))
	}
	path := "/v1/workspace/git/status"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		OK     bool        `json:"ok"`
		Status GitSnapshot `json:"status"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return GitSnapshot{}, err
	}
	return resp.Status, nil
}

func (c *API) CommitWorkspaceChanges(ctx context.Context, workspacePath, message, sessionID string) (GitCommitResponse, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	message = strings.TrimSpace(message)
	if workspacePath == "" {
		return GitCommitResponse{}, fmt.Errorf("workspace path is required")
	}
	if message == "" {
		return GitCommitResponse{}, fmt.Errorf("commit message is required")
	}
	path := gitCommitPath("/v1/workspace/git/commit", sessionID)
	var resp GitCommitResponse
	if err := c.postJSON(ctx, path, map[string]any{
		"workspace_path": workspacePath,
		"cwd":            workspacePath,
		"message":        message,
		"all":            true,
	}, &resp, true); err != nil {
		return GitCommitResponse{}, err
	}
	return resp, nil
}

func (c *API) SuggestWorkspaceCommitMessage(ctx context.Context, workspacePath, sessionID string) (GitCommitSuggestionResponse, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return GitCommitSuggestionResponse{}, fmt.Errorf("workspace path is required")
	}
	path := gitCommitPath("/v1/workspace/git/commit/suggestion", sessionID)
	var resp GitCommitSuggestionResponse
	if err := c.postJSON(ctx, path, map[string]any{
		"workspace_path": workspacePath,
		"cwd":            workspacePath,
	}, &resp, true); err != nil {
		return GitCommitSuggestionResponse{}, err
	}
	return resp, nil
}

func gitCommitPath(base, sessionID string) string {
	query := url.Values{}
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		query.Set("session_id", sessionID)
	}
	if encoded := query.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}
