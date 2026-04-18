package gitstatus

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const CommandTimeout = 2 * time.Second

type ResponseFields struct {
	GitBranch             string `json:"git_branch,omitempty"`
	GitHasGit             bool   `json:"git_has_git,omitempty"`
	GitClean              bool   `json:"git_clean,omitempty"`
	GitDirtyCount         int    `json:"git_dirty_count,omitempty"`
	GitStagedCount        int    `json:"git_staged_count,omitempty"`
	GitModifiedCount      int    `json:"git_modified_count,omitempty"`
	GitUntrackedCount     int    `json:"git_untracked_count,omitempty"`
	GitConflictCount      int    `json:"git_conflict_count,omitempty"`
	GitAheadCount         int    `json:"git_ahead_count,omitempty"`
	GitBehindCount        int    `json:"git_behind_count,omitempty"`
	GitCommittedFileCount int    `json:"git_committed_file_count,omitempty"`
	GitCommittedAdditions int    `json:"git_committed_additions,omitempty"`
	GitCommittedDeletions int    `json:"git_committed_deletions,omitempty"`
}

type RepoStatus struct {
	Branch             string
	DirtyCount         int
	StagedCount        int
	ModifiedCount      int
	UntrackedCount     int
	ConflictCount      int
	AheadCount         int
	BehindCount        int
	CommittedFileCount int
	CommittedAdditions int
	CommittedDeletions int
	HasGit             bool
}

func ForPath(path string) RepoStatus {
	return forPath(path, "")
}

func ForWorktreePath(path, baseBranch string) RepoStatus {
	return forPath(path, baseBranch)
}

func forPath(path, baseBranch string) RepoStatus {
	target := strings.TrimSpace(path)
	if target == "" {
		return RepoStatus{Branch: "-"}
	}

	raw, err := gitOutput(target, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return RepoStatus{Branch: "-"}
	}
	status := ParsePorcelainV2(string(raw))
	if !status.HasGit {
		return status
	}
	populateCommittedDiffStats(&status, target, baseBranch)
	return status
}

func ParsePorcelainV2(raw string) RepoStatus {
	status := RepoStatus{Branch: "-", HasGit: true}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			switch branch {
			case "", "HEAD":
				status.Branch = "-"
			case "(detached)":
				status.Branch = "detached"
			default:
				status.Branch = branch
			}
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				status.AheadCount = parseCount(fields[2])
				status.BehindCount = parseCount(fields[3])
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			status.DirtyCount++
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				accumulateXY(&status, fields[1])
			}
		case strings.HasPrefix(line, "u "):
			status.DirtyCount++
			status.ConflictCount++
		case strings.HasPrefix(line, "? "):
			status.DirtyCount++
			status.UntrackedCount++
		}
	}
	if strings.TrimSpace(status.Branch) == "" {
		status.Branch = "-"
	}
	return status
}

func accumulateXY(status *RepoStatus, xy string) {
	if status == nil || len(xy) < 2 {
		return
	}
	if xy[0] != '.' {
		status.StagedCount++
	}
	if xy[1] != '.' {
		status.ModifiedCount++
	}
}

func parseCount(value string) int {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "+")
	trimmed = strings.TrimPrefix(trimmed, "-")
	count, err := strconv.Atoi(trimmed)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func populateCommittedDiffStats(status *RepoStatus, target, baseBranch string) {
	if status == nil {
		return
	}
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		return
	}

	rawCounts, err := gitOutput(target, "rev-list", "--left-right", "--count", baseBranch+"...HEAD")
	if err == nil {
		fields := strings.Fields(strings.TrimSpace(string(rawCounts)))
		if len(fields) >= 2 {
			status.BehindCount = parseCount(fields[0])
			status.AheadCount = parseCount(fields[1])
		}
	}

	rawDiff, err := gitOutput(target, "diff", "--numstat", baseBranch+"...HEAD")
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(rawDiff)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		status.CommittedFileCount++
		if fields[0] != "-" {
			status.CommittedAdditions += parseCount(fields[0])
		}
		if fields[1] != "-" {
			status.CommittedDeletions += parseCount(fields[1])
		}
	}
}

func gitOutput(target string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs, "--no-optional-locks", "-C", target)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	return cmd.Output()
}
