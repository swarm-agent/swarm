package api

import (
	"strings"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type gitStatusResponseFields = gitstatus.ResponseFields

func gitStatusResponseForPath(path string) gitStatusResponseFields {
	status := gitstatus.ForPath(path)
	branch := strings.TrimSpace(status.Branch)
	if branch == "-" {
		branch = ""
	}
	return gitStatusResponseFields{
		GitBranch:             branch,
		GitHasGit:             status.HasGit,
		GitClean:              status.HasGit && status.DirtyCount == 0,
		GitDirtyCount:         status.DirtyCount,
		GitStagedCount:        status.StagedCount,
		GitModifiedCount:      status.ModifiedCount,
		GitUntrackedCount:     status.UntrackedCount,
		GitConflictCount:      status.ConflictCount,
		GitAheadCount:         status.AheadCount,
		GitBehindCount:        status.BehindCount,
		GitCommittedFileCount: status.CommittedFileCount,
		GitCommittedAdditions: status.CommittedAdditions,
		GitCommittedDeletions: status.CommittedDeletions,
	}
}

func gitStatusResponseForSession(session pebblestore.SessionSnapshot) gitStatusResponseFields {
	if session.WorktreeEnabled {
		return gitStatusResponseForWorktree(session.WorkspacePath, session.WorktreeBaseBranch)
	}
	if fields, ok := metadataGitStatusResponseFields(session.Metadata); ok {
		return fields
	}
	return gitStatusResponseFields{}
}

func gitStatusResponseForWorktree(path, baseBranch string) gitStatusResponseFields {
	status := gitstatus.ForWorktreePath(path, baseBranch)
	branch := strings.TrimSpace(status.Branch)
	if branch == "-" {
		branch = ""
	}
	return gitStatusResponseFields{
		GitBranch:             branch,
		GitHasGit:             status.HasGit,
		GitClean:              status.HasGit && status.DirtyCount == 0,
		GitDirtyCount:         status.DirtyCount,
		GitStagedCount:        status.StagedCount,
		GitModifiedCount:      status.ModifiedCount,
		GitUntrackedCount:     status.UntrackedCount,
		GitConflictCount:      status.ConflictCount,
		GitAheadCount:         status.AheadCount,
		GitBehindCount:        status.BehindCount,
		GitCommittedFileCount: status.CommittedFileCount,
		GitCommittedAdditions: status.CommittedAdditions,
		GitCommittedDeletions: status.CommittedDeletions,
	}
}

func metadataGitStatusResponseFields(metadata map[string]any) (gitStatusResponseFields, bool) {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return gitStatusResponseFields{}, false
	}
	statusMeta, _ := gitMeta["status"].(map[string]any)
	if statusMeta == nil {
		return gitStatusResponseFields{}, false
	}
	branch := strings.TrimSpace(metadataMapString(statusMeta, "branch"))
	if branch == "-" {
		branch = ""
	}
	return gitStatusResponseFields{
		GitBranch:             branch,
		GitHasGit:             metadataMapBool(statusMeta, "has_git"),
		GitClean:              metadataMapBool(statusMeta, "clean"),
		GitDirtyCount:         metadataMapInt(statusMeta, "dirty_count"),
		GitStagedCount:        metadataMapInt(statusMeta, "staged_count"),
		GitModifiedCount:      metadataMapInt(statusMeta, "modified_count"),
		GitUntrackedCount:     metadataMapInt(statusMeta, "untracked_count"),
		GitConflictCount:      metadataMapInt(statusMeta, "conflict_count"),
		GitAheadCount:         metadataMapInt(statusMeta, "ahead_count"),
		GitBehindCount:        metadataMapInt(statusMeta, "behind_count"),
		GitCommittedFileCount: metadataMapInt(statusMeta, "committed_file_count"),
		GitCommittedAdditions: metadataMapInt(statusMeta, "committed_additions"),
		GitCommittedDeletions: metadataMapInt(statusMeta, "committed_deletions"),
	}, true
}

func metadataGitCommitDetected(metadata map[string]any) bool {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return false
	}
	return metadataMapBool(gitMeta, "commit_detected")
}

func metadataGitCommitCount(metadata map[string]any) int {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return 0
	}
	return metadataMapInt(gitMeta, "commit_count")
}

func gitCommitDetectedForSession(session pebblestore.SessionSnapshot, fields gitStatusResponseFields) bool {
	if session.WorktreeEnabled {
		return fields.GitAheadCount > 0
	}
	return metadataGitCommitDetected(session.Metadata)
}

func gitCommitCountForSession(session pebblestore.SessionSnapshot, fields gitStatusResponseFields) int {
	if session.WorktreeEnabled {
		return fields.GitAheadCount
	}
	return metadataGitCommitCount(session.Metadata)
}

func metadataMapString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func metadataMapBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func metadataMapInt(payload map[string]any, key string) int {
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	default:
		return 0
	}
}
