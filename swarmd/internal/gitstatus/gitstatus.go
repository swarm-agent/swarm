package gitstatus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const CommandTimeout = 2 * time.Second

// ResponseFields is the compact status shape embedded into existing session and
// workspace responses. Keep it summary-only; Snapshot is the detailed Git UI API.
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
	Upstream           string
	DirtyCount         int
	StagedCount        int
	ModifiedCount      int
	UntrackedCount     int
	ConflictCount      int
	AheadCount         int
	BehindCount        int
	StashCount         int
	CommittedFileCount int
	CommittedAdditions int
	CommittedDeletions int
	HasGit             bool
}

type Snapshot struct {
	WorkspacePath  string       `json:"workspace_path"`
	RepoRoot       string       `json:"repo_root,omitempty"`
	GitDir         string       `json:"git_dir,omitempty"`
	HasGit         bool         `json:"has_git"`
	Clean          bool         `json:"clean"`
	Branch         string       `json:"branch,omitempty"`
	HeadOID        string       `json:"head_oid,omitempty"`
	Upstream       string       `json:"upstream,omitempty"`
	AheadCount     int          `json:"ahead_count"`
	BehindCount    int          `json:"behind_count"`
	StashCount     int          `json:"stash_count"`
	DirtyCount     int          `json:"dirty_count"`
	StagedCount    int          `json:"staged_count"`
	ModifiedCount  int          `json:"modified_count"`
	UntrackedCount int          `json:"untracked_count"`
	ConflictCount  int          `json:"conflict_count"`
	Files          []FileStatus `json:"files"`
	Remotes        []Remote     `json:"remotes,omitempty"`
	RecentCommits  []Commit     `json:"recent_commits,omitempty"`
	RefreshedAt    time.Time    `json:"refreshed_at"`
	DurationMS     int64        `json:"duration_ms"`
}

type FileStatus struct {
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

type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

type Commit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author,omitempty"`
	UnixTime  int64  `json:"unix_time,omitempty"`
	Subject   string `json:"subject"`
}

func ForPath(path string) RepoStatus {
	return forPath(path, "")
}

func ForWorktreePath(path, baseBranch string) RepoStatus {
	return forPath(path, baseBranch)
}

func forPath(path, baseBranch string) RepoStatus {
	snapshot, err := SnapshotForPath(context.Background(), path, Options{BaseBranch: baseBranch, IncludeDetails: false})
	if err != nil || !snapshot.HasGit {
		return RepoStatus{Branch: "-"}
	}
	status := RepoStatus{
		Branch:             snapshot.Branch,
		Upstream:           snapshot.Upstream,
		DirtyCount:         snapshot.DirtyCount,
		StagedCount:        snapshot.StagedCount,
		ModifiedCount:      snapshot.ModifiedCount,
		UntrackedCount:     snapshot.UntrackedCount,
		ConflictCount:      snapshot.ConflictCount,
		AheadCount:         snapshot.AheadCount,
		BehindCount:        snapshot.BehindCount,
		StashCount:         snapshot.StashCount,
		CommittedFileCount: 0,
		HasGit:             snapshot.HasGit,
	}
	populateCommittedDiffStats(&status, strings.TrimSpace(path), baseBranch)
	return status
}

type Options struct {
	BaseBranch     string
	RecentLimit    int
	IncludeDetails bool
}

func SnapshotForPath(ctx context.Context, path string, opts Options) (Snapshot, error) {
	started := time.Now()
	target := strings.TrimSpace(path)
	if target == "" {
		return Snapshot{WorkspacePath: target, RefreshedAt: time.Now()}, errors.New("workspace path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()

	watchPaths, err := ResolveWatchPaths(ctx, target)
	if err != nil {
		return Snapshot{WorkspacePath: target, Branch: "-", RefreshedAt: time.Now(), DurationMS: time.Since(started).Milliseconds()}, nil
	}
	statusRaw, err := gitOutputContext(ctx, target, "status", "--porcelain=v2", "--branch", "--show-stash", "-z")
	if err != nil {
		return Snapshot{}, err
	}

	snapshot := ParsePorcelainV2Snapshot(statusRaw)
	snapshot.WorkspacePath = target
	snapshot.RepoRoot = watchPaths.RepoRoot
	snapshot.GitDir = watchPaths.GitDir
	snapshot.RefreshedAt = time.Now()
	if snapshot.Branch == "-" {
		snapshot.Branch = ""
	}
	if opts.BaseBranch != "" {
		status := RepoStatus{AheadCount: snapshot.AheadCount, BehindCount: snapshot.BehindCount}
		populateCommittedDiffStats(&status, target, opts.BaseBranch)
		snapshot.AheadCount = status.AheadCount
		snapshot.BehindCount = status.BehindCount
	}
	if opts.IncludeDetails {
		snapshot.Remotes = listRemotes(ctx, target)
		snapshot.RecentCommits = listRecentCommits(ctx, target, opts.RecentLimit)
	}
	snapshot.DurationMS = time.Since(started).Milliseconds()
	return snapshot, nil
}

func ParsePorcelainV2(raw string) RepoStatus {
	snapshot := ParsePorcelainV2Snapshot([]byte(raw))
	branch := snapshot.Branch
	if strings.TrimSpace(branch) == "" {
		branch = "-"
	}
	return RepoStatus{
		Branch:         branch,
		Upstream:       snapshot.Upstream,
		DirtyCount:     snapshot.DirtyCount,
		StagedCount:    snapshot.StagedCount,
		ModifiedCount:  snapshot.ModifiedCount,
		UntrackedCount: snapshot.UntrackedCount,
		ConflictCount:  snapshot.ConflictCount,
		AheadCount:     snapshot.AheadCount,
		BehindCount:    snapshot.BehindCount,
		StashCount:     snapshot.StashCount,
		HasGit:         snapshot.HasGit,
	}
}

func ParsePorcelainV2Snapshot(raw []byte) Snapshot {
	snapshot := Snapshot{Branch: "-", HasGit: true}
	for _, record := range splitRecords(raw) {
		if len(record) == 0 {
			continue
		}
		line := string(record)
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			snapshot.HeadOID = strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
		case strings.HasPrefix(line, "# branch.head "):
			snapshot.Branch = normalizeBranch(strings.TrimSpace(strings.TrimPrefix(line, "# branch.head ")))
		case strings.HasPrefix(line, "# branch.upstream "):
			snapshot.Upstream = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream "))
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				snapshot.AheadCount = parseCount(fields[2])
				snapshot.BehindCount = parseCount(fields[3])
			}
		case strings.HasPrefix(line, "# stash "):
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				snapshot.StashCount = parseCount(fields[2])
			}
		case strings.HasPrefix(line, "1 "):
			file := parseOrdinary(record)
			addFile(&snapshot, file)
		case strings.HasPrefix(line, "2 "):
			file := parseRename(record)
			addFile(&snapshot, file)
		case strings.HasPrefix(line, "u "):
			file := parseUnmerged(record)
			addFile(&snapshot, file)
		case strings.HasPrefix(line, "? "):
			addFile(&snapshot, FileStatus{Kind: "untracked", Path: strings.TrimPrefix(line, "? "), XY: "??", Untracked: true})
		}
	}
	if strings.TrimSpace(snapshot.Branch) == "" {
		snapshot.Branch = "-"
	}
	snapshot.Clean = snapshot.DirtyCount == 0
	return snapshot
}

func splitRecords(raw []byte) [][]byte {
	if bytes.IndexByte(raw, 0) >= 0 {
		parts := bytes.Split(raw, []byte{0})
		out := make([][]byte, 0, len(parts))
		for i := 0; i < len(parts); i++ {
			part := parts[i]
			if len(part) == 0 {
				continue
			}
			if bytes.HasPrefix(part, []byte("2 ")) && i+1 < len(parts) && len(parts[i+1]) > 0 {
				record := make([]byte, 0, len(part)+1+len(parts[i+1]))
				record = append(record, part...)
				record = append(record, 0)
				record = append(record, parts[i+1]...)
				out = append(out, record)
				i++
				continue
			}
			out = append(out, part)
		}
		return out
	}
	lines := strings.Split(string(raw), "\n")
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, []byte(line))
		}
	}
	return out
}

func normalizeBranch(branch string) string {
	switch branch {
	case "", "HEAD":
		return "-"
	case "(detached)":
		return "detached"
	default:
		return branch
	}
}

func parseOrdinary(record []byte) FileStatus {
	fields := strings.Fields(string(record))
	file := FileStatus{Kind: "ordinary"}
	if len(fields) >= 2 {
		file.XY = fields[1]
		applyXY(&file, file.XY)
	}
	if len(fields) >= 4 {
		file.Submodule = fields[3]
	}
	if len(fields) >= 9 {
		file.Path = strings.Join(fields[8:], " ")
	}
	return file
}

func parseRename(record []byte) FileStatus {
	parts := bytes.SplitN(record, []byte{0}, 2)
	fields := strings.Fields(string(parts[0]))
	file := FileStatus{Kind: "rename"}
	if len(fields) >= 2 {
		file.XY = fields[1]
		applyXY(&file, file.XY)
	}
	if len(fields) >= 4 {
		file.Submodule = fields[3]
	}
	if len(fields) >= 10 {
		file.Path = strings.Join(fields[9:], " ")
	}
	if len(parts) == 2 {
		file.OrigPath = string(parts[1])
	}
	return file
}

func parseUnmerged(record []byte) FileStatus {
	fields := strings.Fields(string(record))
	file := FileStatus{Kind: "unmerged", XY: "UU", Conflict: true}
	if len(fields) >= 2 {
		file.XY = fields[1]
	}
	if len(fields) >= 11 {
		file.Path = strings.Join(fields[10:], " ")
	}
	return file
}

func applyXY(file *FileStatus, xy string) {
	if file == nil || len(xy) < 2 {
		return
	}
	file.Staged = xy[0] != '.'
	file.Modified = xy[1] != '.'
}

func addFile(snapshot *Snapshot, file FileStatus) {
	if snapshot == nil {
		return
	}
	snapshot.DirtyCount++
	if file.Conflict {
		snapshot.ConflictCount++
	}
	if file.Untracked {
		snapshot.UntrackedCount++
	}
	if file.Staged {
		snapshot.StagedCount++
	}
	if file.Modified {
		snapshot.ModifiedCount++
	}
	snapshot.Files = append(snapshot.Files, file)
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
	for _, line := range strings.Split(string(rawDiff), "\n") {
		line = strings.TrimSpace(line)
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

func listRemotes(ctx context.Context, target string) []Remote {
	raw, err := gitOutputContext(ctx, target, "remote", "-v")
	if err != nil {
		return nil
	}
	var remotes []Remote
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		kind := strings.Trim(fields[2], "()")
		key := fields[0] + "\x00" + fields[1] + "\x00" + kind
		if seen[key] {
			continue
		}
		seen[key] = true
		remotes = append(remotes, Remote{Name: fields[0], URL: fields[1], Kind: kind})
	}
	return remotes
}

func listRecentCommits(ctx context.Context, target string, limit int) []Commit {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	format := "%H%x1f%h%x1f%an%x1f%at%x1f%s"
	raw, err := gitOutputContext(ctx, target, "log", "-n", strconv.Itoa(limit), "--date=unix", "--format="+format)
	if err != nil {
		return nil
	}
	var commits []Commit
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\x1f", 5)
		if len(fields) < 5 {
			continue
		}
		unixTime, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		commits = append(commits, Commit{Hash: fields[0], ShortHash: fields[1], Author: fields[2], UnixTime: unixTime, Subject: fields[4]})
	}
	return commits
}

func gitOutput(target string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	return gitOutputContext(ctx, target, args...)
}

func gitOutputContext(ctx context.Context, target string, args ...string) ([]byte, error) {
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs, "--no-optional-locks", "-C", target)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
