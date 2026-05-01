package client

import (
	"context"
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
