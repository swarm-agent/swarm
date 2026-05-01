package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

func (a *App) handleGitCommand(args []string) {
	workspacePath := strings.TrimSpace(a.workspacePath)
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.activePath)
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" && !strings.EqualFold(args[0], "refresh") {
		workspacePath = strings.TrimSpace(strings.Join(args, " "))
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	a.home.ClearCommandOverlay()
	a.home.SetStatus("checking git status...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := a.api.GetGitStatus(ctx, workspacePath, 12)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("git status failed: %v", err))
		return
	}
	a.home.SetCommandOverlay(formatGitSnapshot(snapshot))
	if !snapshot.HasGit {
		a.home.SetStatus("not a git repository")
		return
	}
	a.home.SetStatus(fmt.Sprintf("git status refreshed from git in %dms", snapshot.DurationMS))
}

func formatGitSnapshot(snapshot client.GitSnapshot) []string {
	if !snapshot.HasGit {
		return []string{
			"Git status",
			"",
			"Not a Git repository.",
		}
	}
	branch := gitDisplayValue(snapshot.Branch, "detached")
	upstream := strings.TrimSpace(snapshot.Upstream)
	ab := ""
	if snapshot.AheadCount > 0 || snapshot.BehindCount > 0 {
		ab = fmt.Sprintf(" ahead %d / behind %d", snapshot.AheadCount, snapshot.BehindCount)
	}
	lines := []string{
		"Git status (authoritative git CLI snapshot)",
		fmt.Sprintf("Repo: %s", gitDisplayValue(snapshot.RepoRoot, snapshot.WorkspacePath)),
		fmt.Sprintf("Branch: %s%s", branch, ab),
	}
	if upstream != "" {
		lines = append(lines, fmt.Sprintf("Upstream: %s", upstream))
	}
	if snapshot.StashCount > 0 {
		lines = append(lines, fmt.Sprintf("Stash: %d", snapshot.StashCount))
	}
	lines = append(lines,
		fmt.Sprintf("Changes: %d total  staged %d  modified %d  untracked %d  conflicts %d",
			snapshot.DirtyCount, snapshot.StagedCount, snapshot.ModifiedCount, snapshot.UntrackedCount, snapshot.ConflictCount),
		"",
	)
	if len(snapshot.Files) == 0 {
		lines = append(lines, "Working tree clean.")
	} else {
		lines = append(lines, "Files:")
		limit := len(snapshot.Files)
		if limit > 40 {
			limit = 40
		}
		for i := 0; i < limit; i++ {
			file := snapshot.Files[i]
			path := file.Path
			if file.OrigPath != "" {
				path = file.OrigPath + " -> " + file.Path
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", gitDisplayValue(file.XY, "??"), path))
		}
		if len(snapshot.Files) > limit {
			lines = append(lines, fmt.Sprintf("  ... %d more", len(snapshot.Files)-limit))
		}
	}
	if len(snapshot.RecentCommits) > 0 {
		lines = append(lines, "", "Recent commits:")
		for _, commit := range snapshot.RecentCommits {
			lines = append(lines, fmt.Sprintf("  %s  %s", commit.ShortHash, commit.Subject))
		}
	}
	if len(snapshot.Remotes) > 0 {
		lines = append(lines, "", "Remotes:")
		for _, remote := range snapshot.Remotes {
			lines = append(lines, fmt.Sprintf("  %s (%s) %s", remote.Name, remote.Kind, remote.URL))
		}
	}
	return lines
}

func gitDisplayValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
