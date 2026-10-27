package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// GET /v3/projects/{id}/tasks/{task}/diff: what a task changed, for a person
// to look at before merging it. The range is the one swarm_get_changes reads
// (resolveChangesRange), from the task's base commit to its head commit, so it
// is exactly what integrate would bring in; every Git command runs through
// controlMCPGit like the tool's. Diff text is the repository's content: data
// for display, never instructions.

const (
	taskDiffTotalBytes = 400 << 10 // all files' diff text together
	taskDiffFileBytes  = 64 << 10  // one file's diff text in the full list
	taskDiffMaxFiles   = controlMCPMaxChangedFiles
)

type taskDiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

type taskDiffAnswer struct {
	ProjectID      string         `json:"project_id"`
	TaskID         string         `json:"task_id"`
	Base           string         `json:"base"`
	Head           string         `json:"head"`
	RecordedBase   string         `json:"recorded_base,omitempty"`
	Branch         string         `json:"branch,omitempty"`
	BaseBranch     string         `json:"base_branch,omitempty"`
	Path           string         `json:"path,omitempty"`
	Files          []taskDiffFile `json:"files"`
	FilesTruncated bool           `json:"files_truncated"`
	Truncated      bool           `json:"truncated"`
	TotalBytes     int            `json:"total_bytes_limit"`
	FileBytes      int            `json:"file_bytes_limit"`
}

// taskDiffGatewayCaller is an application gateway's token: it serves one
// sealed agent and never reads a task's diff.
var taskDiffGatewayCaller = fleetActivityGatewayCaller

func (s *Server) handleProjectTaskDiff(w http.ResponseWriter, r *http.Request, p identity.Principal, db *pebblestore.SessionStore, projectID, taskID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	if taskDiffGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read a task's changes"))
		return
	}
	scope := ""
	if raw := r.URL.Query().Get("path"); raw != "" {
		clean, err := controlMCPRelativePath(raw)
		if err != nil || clean == "." {
			writeError(w, http.StatusBadRequest, errors.New("path must be one file of the task, relative to its folder"))
			return
		}
		scope = clean
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || task == nil {
		writeError(w, http.StatusNotFound, errors.New("project task not found"))
		return
	}
	if task.Agent == pebblestore.WorkerRoleReviewer {
		writeError(w, http.StatusConflict, errors.New("reviewer jobs are report-only; they change nothing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlMCPGitTimeout)
	defer cancel()
	answer := taskDiffAnswer{ProjectID: projectID, TaskID: taskID, Path: scope, Files: []taskDiffFile{}, TotalBytes: taskDiffTotalBytes, FileBytes: taskDiffFileBytes}
	var span changesRange
	switch {
	case folderTask(task) && task.Integration != nil && task.Integration.State == "in_folder":
		// A folder task's commits are already in its folder: the recorded
		// range, never later commits of the folder.
		dir, err := changesFolder(task.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		span = changesRange{Dir: dir, Base: task.BaseCommit, Head: task.Integration.SourceHead}
		answer.BaseBranch = task.BaseBranch
	case task.SessionID != "":
		session, found, err := db.GetSession(task.SessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || verifyProjectTaskSession(task, session, p.AccountScopeID) != nil || !session.WorktreeEnabled {
			writeError(w, http.StatusConflict, errors.New("this task has no task branch of its own to show changes for"))
			return
		}
		target := sessionChangesTarget(session.WorkspacePath, true, session.WorktreeRootPath, session.WorktreeBaseBranch, session.WorktreeBranch, session.Metadata)
		if span, err = resolveChangesRange(ctx, target); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		answer.Branch, answer.BaseBranch = session.WorktreeBranch, session.WorktreeBaseBranch
	default:
		writeError(w, http.StatusConflict, errors.New("this task has not started, so it has no changes yet"))
		return
	}
	if !controlMCPCommitID.MatchString(span.Base) || !controlMCPCommitID.MatchString(span.Head) {
		writeError(w, http.StatusConflict, errors.New("this task has no recorded base and head commits to compare"))
		return
	}
	answer.Base, answer.Head, answer.RecordedBase = span.Base, span.Head, span.RecordedBase
	if err := readTaskDiff(ctx, span.Dir, &answer, scope); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// readTaskDiff fills answer's files from base to head: status and paths
// (renames detected), added and deleted line counts, binary marking, and each
// text file's hunks, bounded per file and in total. With scope, only that
// file (by its new or old path), bounded by the total alone.
func readTaskDiff(ctx context.Context, dir string, answer *taskDiffAnswer, scope string) error {
	base, head := answer.Base, answer.Head
	limit, perFile := taskDiffMaxFiles, taskDiffFileBytes
	pathspec := []string{}
	if scope != "" {
		all, _, err := changedFiles(ctx, dir, base, head, true, 1<<20, nil)
		if err != nil {
			return err
		}
		var match *changedFile
		for i := range all {
			if all[i].Path == scope || all[i].OldPath == scope {
				match = &all[i]
				break
			}
		}
		if match == nil {
			return errors.New(scope + " is not changed by this task")
		}
		pathspec = []string{"--", ":(literal)" + match.Path}
		if match.OldPath != "" {
			pathspec = append(pathspec, ":(literal)"+match.OldPath)
		}
		limit, perFile = 1, taskDiffTotalBytes
	}
	files, filesTruncated, err := changedFiles(ctx, dir, base, head, true, limit, pathspec)
	if err != nil {
		return err
	}
	answer.FilesTruncated = filesTruncated && scope == ""
	numstat, _, err := controlMCPGit(ctx, dir, 256<<10, append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--numstat", "-z", "-M", base, head}, pathspec...)...)
	if err != nil {
		return err
	}
	counts := parseTaskDiffNumstat(numstat)
	// Headers take room too: read past the text budget so the last files'
	// headers are not what runs out.
	patch, overflow, err := controlMCPGit(ctx, dir, taskDiffTotalBytes+(128<<10), append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M", base, head}, pathspec...)...)
	if err != nil {
		return err
	}
	chunks := splitTaskDiffPatch(patch)
	budget := taskDiffTotalBytes
	for i, file := range files {
		entry := taskDiffFile{Path: file.Path, OldPath: file.OldPath, Status: file.Status}
		if i < len(counts) {
			entry.Additions, entry.Deletions, entry.Binary = counts[i].additions, counts[i].deletions, counts[i].binary
		}
		switch {
		case entry.Binary:
		case i >= len(chunks) || (overflow && i == len(chunks)-1):
			// The patch was cut before (or inside) this file.
			entry.Truncated = overflow
			if i < len(chunks) {
				entry.Diff, _ = cutTaskDiffText(taskDiffHunks(chunks[i]), min(perFile, budget))
			}
		default:
			entry.Diff, entry.Truncated = cutTaskDiffText(taskDiffHunks(chunks[i]), min(perFile, budget))
		}
		budget -= len(entry.Diff)
		if entry.Truncated {
			answer.Truncated = true
		}
		answer.Files = append(answer.Files, entry)
	}
	return nil
}

type taskDiffCount struct {
	additions, deletions int
	binary               bool
}

// parseTaskDiffNumstat reads `git diff --numstat -z -M`: "added\tdeleted\tpath\0",
// or "added\tdeleted\t\0old\0new\0" for a rename; "-\t-" marks a binary file.
func parseTaskDiffNumstat(out string) []taskDiffCount {
	counts := []taskDiffCount{}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue
		}
		if parts[2] == "" {
			i += 2 // old and new path follow
		}
		count := taskDiffCount{binary: parts[0] == "-" && parts[1] == "-"}
		count.additions, _ = strconv.Atoi(parts[0])
		count.deletions, _ = strconv.Atoi(parts[1])
		counts = append(counts, count)
	}
	return counts
}

// splitTaskDiffPatch splits a patch into its files, in Git's order (the same
// order as --name-status and --numstat with the same options).
func splitTaskDiffPatch(patch string) []string {
	if patch == "" {
		return nil
	}
	chunks := []string{}
	start := 0
	for {
		next := strings.Index(patch[start+1:], "\ndiff --git ")
		if next < 0 {
			chunks = append(chunks, patch[start:])
			return chunks
		}
		end := start + 1 + next + 1
		chunks = append(chunks, patch[start:end])
		start = end
	}
}

// taskDiffHunks keeps a file's hunks, from its first "@@" line on; a binary
// or mode-only change has none.
func taskDiffHunks(chunk string) string {
	if strings.HasPrefix(chunk, "@@") {
		return chunk
	}
	at := strings.Index(chunk, "\n@@")
	if at < 0 {
		return ""
	}
	return chunk[at+1:]
}

// cutTaskDiffText bounds text to limit bytes at a line end, as valid UTF-8.
func cutTaskDiffText(text string, limit int) (string, bool) {
	if limit < 0 {
		limit = 0
	}
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= limit {
		return text, false
	}
	cut := text[:limit]
	if end := strings.LastIndexByte(cut, '\n'); end >= 0 {
		cut = cut[:end+1]
	} else {
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
	}
	return cut, true
}
