package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Workflow steps that rebase, fix and rerun without a person
// (docs/automations.md): the Git a rebase step's checks read, the
// fast-forward of a task's own branch to a head a fix step moved its
// delivery to, and the rerun of a task's newest delivery by task id.

// jobAutomationGit runs routed Git in dir, as all daemon Git on a folder.
func jobAutomationGit(ctx context.Context, dir string, args ...string) (string, error) {
	gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
	out, err := sandbox.TimedGitOutput(gitCtx, dir, nil, append([]string{"-c", "safe.directory=" + dir, "-c", "core.hooksPath=" + os.DevNull}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// JobAutomationRebaseGit is the daemon's rebase's Git (jobautomation.GitRebaser):
// routed Git in dir like all daemon Git on a folder, its hooks off (the
// rebaser adds core.hooksPath), env added to the daemon's own environment,
// stdin (nil: none) its standard input: the rebaser passes commit messages
// and blobs there and uses no file outside the folder, because a sandboxed
// project's Git sees only the folder and its Git directory. It returns
// standard output; a non-zero exit is an *exec.ExitError carrying standard
// error.
func JobAutomationRebaseGit(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	gitCtx := sandbox.WithGitCommandTimeout(ctx, 2*time.Minute)
	var full []string
	if len(env) > 0 {
		full = append(os.Environ(), env...)
	}
	return sandbox.TimedGitOutputInput(gitCtx, dir, full, stdin, append([]string{"-c", "safe.directory=" + dir}, args...)...)
}

// validWorkflowBranch refuses a branch name that is not a plain branch:
// empty, an option, or one Git would not accept under refs/heads.
func validWorkflowBranch(branch string) bool {
	if branch == "" || len(branch) > 250 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "@{") {
		return false
	}
	return !strings.ContainsAny(branch, " \t\r\n~^:?*[\\\x00\x7f")
}

// TargetTip is the commit refs/heads/<branch> names in the folder now: the
// tip a rebase step is told to rebase onto.
func (a jobAutomationAgents) TargetTip(ctx context.Context, folder, branch string) (string, error) {
	if !validWorkflowBranch(branch) {
		return "", fmt.Errorf("%q is not a branch name", branch)
	}
	tip, err := jobAutomationGit(ctx, folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("git rev-parse refs/heads/%s: %w", branch, err)
	}
	if !controlMCPCommitID.MatchString(tip) {
		return "", fmt.Errorf("refs/heads/%s does not name a commit", branch)
	}
	return tip, nil
}

// ChangedFiles lists the files the tree at head changes against base, with
// Git in the folder, where both commits must be (jobHeadInFolder).
func (a jobAutomationAgents) ChangedFiles(ctx context.Context, folder string, base, head jobautomation.HeadRef) ([]string, error) {
	for _, ref := range []jobautomation.HeadRef{base, head} {
		if !controlMCPCommitID.MatchString(ref.Commit) {
			return nil, fmt.Errorf("%q is not a commit id", ref.Commit)
		}
		if err := jobHeadInFolder(ctx, folder, ref.Commit); err != nil {
			return nil, err
		}
	}
	names, err := jobAutomationGit(ctx, folder, "diff", "--no-renames", "--no-ext-diff", "--name-only", base.Commit, head.Commit, "--")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only: %w", err)
	}
	files := []string{}
	for _, name := range strings.Split(names, "\n") {
		if name = strings.TrimSpace(name); name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

// AdvanceTaskBranch fast-forwards the triggering task's own branch and
// worktree to the head an agent step moved its delivery to, and records the
// outcome on the task (ProjectTaskRecord.WorkflowBranch). It moves nothing
// unless the task's branch is the delivery's branch, its worktree is its
// own, on that branch, clean and idle, and the move is a fast-forward.
func (a jobAutomationAgents) AdvanceTaskBranch(ctx context.Context, req jobautomation.TaskBranchAdvance) jobautomation.TaskBranchSync {
	s := a.s
	out := jobautomation.TaskBranchSync{Head: req.To.Commit}
	projectID, taskID, ok := parseTaskJobID(req.JobID)
	if !ok || s == nil || s.sessions == nil || s.sessions.Store() == nil {
		out.Reason = "the job is not a project task on this daemon"
		return out
	}
	db := s.sessions.Store()
	task, found, err := db.GetProjectTask(req.Account, projectID, taskID)
	if err != nil || !found || task == nil {
		out.Reason = "the task could not be read"
		if err == nil {
			out.Reason = "the task no longer exists"
		}
		return out
	}
	out.Advanced, out.Reason = a.advanceTaskWorktree(ctx, task, req)
	record := &pebblestore.ProjectTaskWorkflowBranch{DeliveryID: req.DeliveryID, Step: req.Step, From: req.From, Head: req.To.Commit,
		Advanced: out.Advanced, Reason: out.Reason, At: time.Now().UnixMilli()}
	if _, err := db.UpdateProjectTask(req.Account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.WorkflowBranch = record
		return nil
	}); err != nil && out.Reason == "" {
		out.Reason = "the branch moved, but the task's record could not be updated: " + err.Error()
	}
	return out
}

// advanceTaskWorktree is AdvanceTaskBranch's Git: it reports whether the
// task's worktree now stands at the delivery head, else why not.
func (a jobAutomationAgents) advanceTaskWorktree(ctx context.Context, task *pebblestore.ProjectTaskRecord, req jobautomation.TaskBranchAdvance) (bool, string) {
	to := req.To.Commit
	if !controlMCPCommitID.MatchString(to) {
		return false, "the delivery head is not a commit id"
	}
	if task.WorktreeBranch == "" || task.WorktreeBranch != req.Branch {
		return false, fmt.Sprintf("the task works on branch %q, not the delivery's branch %q", task.WorktreeBranch, req.Branch)
	}
	worktree := strings.TrimSpace(task.WorkspacePath)
	source := firstNonEmptyString(task.SourceWorkspace.Path, "")
	if worktree == "" || !filepath.IsAbs(worktree) || (source != "" && filepath.Clean(worktree) == filepath.Clean(source)) {
		return false, "the task has no worktree of its own"
	}
	if task.SessionID != "" {
		if state, found, err := a.s.sessions.Store().GetV3SessionRunState(task.SessionID); err == nil && found && state.Active {
			return false, "the task is running"
		}
	}
	if branch, err := jobAutomationGit(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil || branch != req.Branch {
		return false, fmt.Sprintf("the task's worktree is not on branch %s", req.Branch)
	}
	head, err := jobAutomationGit(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return false, "the task's worktree head could not be read: " + err.Error()
	}
	if head == to {
		return true, ""
	}
	if status, err := jobAutomationGit(ctx, worktree, "status", "--porcelain", "--untracked-files=normal"); err != nil {
		return false, "the task's worktree status could not be read: " + err.Error()
	} else if status != "" {
		return false, "the task's worktree has uncommitted changes"
	}
	if _, err := jobAutomationGit(ctx, worktree, "cat-file", "-e", to+"^{commit}"); err != nil {
		return false, "the delivery head " + to + " is not in the task's repository"
	}
	if req.Rebased {
		// A rebased head does not descend from the branch: it moves only
		// from the head that was rebased (compare-and-swap), its clean
		// worktree with it (reset --keep refuses to lose anything).
		if head != req.From {
			return false, fmt.Sprintf("the task's branch is at %s, not at the head that was rebased (%s): it is not moved", head, req.From)
		}
		if _, err := jobAutomationGit(ctx, worktree, "reset", "--keep", "--quiet", to); err != nil {
			return false, "git reset --keep failed: " + controlMCPTruncate(err.Error(), 200)
		}
		if moved, err := jobAutomationGit(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || moved != to {
			return false, "the task's worktree did not reach the rebased head"
		}
		return true, ""
	}
	if _, err := jobAutomationGit(ctx, worktree, "merge-base", "--is-ancestor", head, to); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, fmt.Sprintf("not a fast-forward: the task's branch is at %s, which the delivery head %s does not descend from", head, to)
		}
		return false, "could not compare the task's branch with the delivery head: " + err.Error()
	}
	if _, err := jobAutomationGit(ctx, worktree, "merge", "--ff-only", "--quiet", to); err != nil {
		return false, "git merge --ff-only failed: " + controlMCPTruncate(err.Error(), 200)
	}
	if moved, err := jobAutomationGit(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || moved != to {
		return false, "the task's worktree did not reach the delivery head"
	}
	return true, ""
}

// RerunTaskDelivery re-runs the newest workflow delivery of a project task,
// found by the task: the documented rerun (POST
// /v3/job-deliveries/{id}/rerun) of that one delivery, from its failed or
// stopped steps. It is manage_projects rerun_delivery's service.
func (s *Server) RerunTaskDelivery(p identity.Principal, projectID, taskID string) (jobautomation.RerunOutcome, error) {
	if !p.Valid() || p.AccountScopeID == "" {
		return jobautomation.RerunOutcome{}, identity.ErrPrincipalRequired
	}
	if !controlMCPSafeID.MatchString(projectID) || !controlMCPSafeID.MatchString(taskID) {
		return jobautomation.RerunOutcome{}, &projectTaskFollowupError{400, errors.New("project_id and task_id are required")}
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		return jobautomation.RerunOutcome{}, errors.New("projects are not available on this daemon")
	}
	if _, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID); err != nil {
		return jobautomation.RerunOutcome{}, err
	} else if !found {
		return jobautomation.RerunOutcome{}, &projectTaskFollowupError{404, errors.New("task not found")}
	}
	store, err := s.jobAutomationStore()
	if err != nil {
		return jobautomation.RerunOutcome{}, err
	}
	newest, err := store.NewestJobDelivery(p.AccountScopeID, taskRunJobID(projectID, taskID))
	if errors.Is(err, jobautomation.ErrNotFound) {
		return jobautomation.RerunOutcome{}, &projectTaskFollowupError{404, errors.New("the task has no workflow delivery to re-run")}
	}
	if err != nil {
		return jobautomation.RerunOutcome{}, err
	}
	return s.rerunJobDelivery(p.AccountScopeID, newest.ID)
}

// jobResultUncommitted reads a result view's uncommitted_changes (the files
// its delivery assessment found uncommitted in the job's worktree).
func jobResultUncommitted(view map[string]any) []string {
	files := []string{}
	for _, item := range controlMCPList(view["uncommitted_changes"]) {
		if name, _ := item.(string); strings.TrimSpace(name) != "" {
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		return nil
	}
	return files
}
