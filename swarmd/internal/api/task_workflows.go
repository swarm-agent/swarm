package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Workflows and Swarm's own agents (swarm, coder, finder, designer).
//
// A task one of them finished is a workflow job end like a worker job's
// (task.run.* signals, job id task/<project>/<task>); a workflow's agent step
// can run one of them as a task; and a reopen step sends findings back into
// the task's own session, where the agent fixes its own work in its own
// worktree.

// workflowTaskStart is set only by a workflow's agent step, inside the daemon,
// when it creates a task: the workflow that started it (so the task's end
// never triggers workflows itself) and the commit a coder's worktree starts
// at (the delivery head). An API client cannot set either.
type workflowTaskStart struct {
	AutomationID   string
	StartCommit    string
	ReviewJob      string
	ReviewCheckout string
	ReviewFixes    string
}

type workflowTaskStartKey struct{}

func withWorkflowTaskStart(ctx context.Context, start workflowTaskStart) context.Context {
	return context.WithValue(ctx, workflowTaskStartKey{}, start)
}

func workflowTaskStartFrom(ctx context.Context) (workflowTaskStart, bool) {
	start, ok := ctx.Value(workflowTaskStartKey{}).(workflowTaskStart)
	return start, ok
}

// parseTaskJobID splits task/<project>/<task>.
func parseTaskJobID(jobID string) (projectID, taskID string, ok bool) {
	parts := strings.Split(jobID, "/")
	if len(parts) != 3 || parts[0] != jobautomation.KindTask || !controlMCPSafeID.MatchString(parts[1]) || !controlMCPSafeID.MatchString(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// maxTaskResultCommits bounds the commits a task's result lists.
const maxTaskResultCommits = 200

// taskJobAutomationResult is the result of a task.run.* signal: the task's
// swarm_task_result view plus its commits read with Git from its worktree.
func (s *Server) taskJobAutomationResult(ctx context.Context, sig pebblestore.Signal) (*jobautomation.Result, error) {
	ended := strings.TrimPrefix(sig.Kind, "task.run.")
	out, err := s.taskJobResult(ctx, sig.Account, sig.Refs["project_id"], sig.Refs["task_id"], ended)
	if err != nil || out == nil {
		return out, err
	}
	if out.FinishedAt == 0 {
		out.FinishedAt = sig.At
	}
	return out, nil
}

// taskJobResult builds a task's result. ended is the run's end
// (succeeded, failed or cancelled), or "" to take it from the task's status.
func (s *Server) taskJobResult(ctx context.Context, account, projectID, taskID, ended string) (*jobautomation.Result, error) {
	if !controlMCPSafeID.MatchString(projectID) || !controlMCPSafeID.MatchString(taskID) || s.sessions == nil || s.sessions.Store() == nil {
		return nil, nil
	}
	task, found, err := s.sessions.Store().GetProjectTask(account, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil {
		return nil, nil // the task was deleted
	}
	c, err := s.jobAutomationCall(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := controlMCPTaskResult(c, map[string]any{"project_id": projectID, "task_id": taskID})
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	view := controlMCPMap(raw)
	status := controlMCPString(view, "status")
	out := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: taskRunJobID(projectID, taskID), Kind: jobautomation.KindTask,
		Role: task.Agent, RunID: taskID, ProjectID: projectID, JobStatus: status, Brief: task.Description}
	switch ended {
	case jobautomation.StatusSucceeded, jobautomation.StatusFailed, jobautomation.StatusCancelled:
		out.Status = ended
	default:
		out.Status = jobautomation.NormalizeStatus(status)
	}
	// A run that ended on a declared blocker or a failed task is not a success.
	if out.Status == jobautomation.StatusSucceeded && (status == "failed" || status == "blocked" || status == "rejected") {
		out.Status = jobautomation.StatusFailed
	}
	out.Folder = firstNonEmptyString(task.SourceWorkspace.Path, task.WorkspacePath)
	out.Report = controlMCPString(view, "report")
	if review, err := s.taskReview(task); err != nil {
		return nil, err
	} else if review != nil {
		out.Review = map[string]any{"verdict": review.Verdict, "findings": review.Findings, "reason": review.Reason, "head": review.Head}
		if review.FixHead != "" {
			out.Review["fix_head"] = review.FixHead
		}
	}
	out.Error = controlMCPString(view, "last_error")
	if task.AutomationID != "" {
		out.RequestSource = jobautomation.RequestSourceAutomation
	}
	if cost := controlMCPMap(view["cost"]); len(cost) > 0 {
		out.Cost = jobResultCost(cost["total_tokens"], cost["estimated_cost_usd"], cost["model"])
	}
	if task.ReviewFixes == jobautomation.FixesSecurity {
		out.Git, _, err = s.taskReviewFixCheckout(ctx, account, task)
		return out, err
	}
	head, base := controlMCPString(view, "head"), controlMCPString(view, "base_commit")
	worktree := strings.TrimSpace(task.WorkspacePath)
	if !controlMCPCommitID.MatchString(head) || !controlMCPCommitID.MatchString(base) || worktree == "" || (filepath.Clean(worktree) == filepath.Clean(out.Folder) && controlMCPString(view, "delivery_state") != "in_folder") {
		return out, nil // no commits in a worktree of its own
	}
	g, err := taskResultGit(ctx, worktree, base, head)
	if err != nil {
		return nil, err
	}
	g.Branch, g.BaseBranch = task.WorktreeBranch, task.BaseBranch
	if len(g.Commits) > 0 {
		out.Git = g
	}
	return out, nil
}

// taskResultGit lists a task's commits (full ids) and changed files from base
// to head with Git in its worktree. Git runs routed like all daemon Git.
func taskResultGit(ctx context.Context, worktree, base, head string) (*jobautomation.ResultGit, error) {
	git := func(args ...string) (string, error) {
		gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
		out, err := sandbox.TimedGitOutput(gitCtx, worktree, nil, append([]string{"-c", "safe.directory=" + worktree}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	g := &jobautomation.ResultGit{BaseCommit: base, Head: head, Worktree: worktree, Files: []string{}}
	if head == base {
		return g, nil
	}
	log, err := git("log", "--no-decorate", "--format=%H %s", fmt.Sprintf("--max-count=%d", maxTaskResultCommits), base+".."+head, "--")
	if err != nil {
		return nil, fmt.Errorf("list the task's commits: %w", err)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sha, subject, _ := strings.Cut(line, " ")
		g.Commits = append(g.Commits, jobautomation.ResultCommit{SHA: sha, Subject: subject})
	}
	names, err := git("diff", "--no-renames", "--name-only", base, head, "--")
	if err != nil {
		return nil, fmt.Errorf("list the task's files: %w", err)
	}
	for _, name := range strings.Split(names, "\n") {
		if name = strings.TrimSpace(name); name != "" {
			g.Files = append(g.Files, name)
		}
	}
	return g, nil
}

// startBuiltinAgentStep runs an agent step that names one of Swarm's own
// agents as a task in the folder, in the triggering job's project, exactly as
// swarm_run_task would: the step's brief with the triggering job's result. A
// coder's worktree starts at the delivery head, so its commits descend from
// the work it follows. The task records the workflow that started it, so its
// end never triggers workflows.
func (s *Server) startBuiltinAgentStep(ctx context.Context, req jobautomation.AgentRequest) (string, error) {
	if err := jobautomation.NormalizeSteps([]jobautomation.Step{{ID: "agent", Type: jobautomation.StepAgent, Worker: req.Worker, Brief: req.Brief, Fixes: req.Fixes, Checkout: req.Checkout}}); err != nil {
		return "", &jobautomation.RefusedError{Reason: err.Error()}
	}
	if req.Result.ProjectID == "" {
		return "", &jobautomation.RefusedError{Reason: "job " + req.Result.JobID + " has no project, so " + req.Worker + " cannot be given a task for it"}
	}
	if want := jobautomation.CheckoutFor(jobautomation.Step{Worker: req.Worker}, ""); !strings.EqualFold(req.Worker, "reviewer") && req.Checkout != "" && req.Checkout != want {
		return "", &jobautomation.RefusedError{Reason: req.Worker + " works with checkout " + want + ", not " + req.Checkout}
	}
	c, err := s.jobAutomationWriteCall(ctx)
	if err != nil {
		return "", err
	}
	p, ok := PrincipalFromRequest(c.request)
	if !ok || p.AccountScopeID != req.Account {
		return "", &jobautomation.RefusedError{Reason: "agent steps run only for the machine owner's automations"}
	}
	brief := agentRequestBrief(req)
	if strings.EqualFold(req.Worker, "reviewer") {
		reviewReq := req
		if req.ReviewJob != "" && req.ReviewJob != req.Result.JobID {
			work, err := s.workResult(ctx, req.Account, req.ReviewJob)
			if err != nil || work == nil {
				return "", fmt.Errorf("read review job %s: %v", req.ReviewJob, err)
			}
			reviewReq.Result = *work
		}
		brief = s.automationReviewerBrief(ctx, reviewReq) + deliverablesNote(req)
	}
	title := strings.TrimLeft(strings.TrimSpace(strings.SplitN(strings.TrimSpace(req.Brief), "\n", 2)[0]), "#- ")
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	if title == "" {
		title = req.Worker + " step"
	}
	start := workflowTaskStart{AutomationID: "workflow:" + req.IdempotencyKey}
	if strings.EqualFold(req.Worker, "coder") && req.Result.Git != nil && controlMCPCommitID.MatchString(req.Head) {
		start.StartCommit = req.Head
	}
	if strings.EqualFold(req.Worker, "reviewer") {
		start.ReviewJob, start.StartCommit = firstNonEmptyString(req.ReviewJob, req.Result.JobID), req.Head
		start.ReviewFixes = req.Fixes
		start.ReviewCheckout = jobautomation.CheckoutFor(jobautomation.Step{Worker: req.Worker, Checkout: req.Checkout, Fixes: req.Fixes}, "")
	}
	sum := sha256.Sum256([]byte(req.Account + "\x00" + req.IdempotencyKey))
	taskID := "task_wf_" + hex.EncodeToString(sum[:12])
	task, err := s.CreateProjectTask(withWorkflowTaskStart(ctx, start), p, req.Result.ProjectID, tool.ProjectTaskCreateInput{
		ID: taskID, Title: title, Description: brief, Agent: strings.ToLower(req.Worker), WorkspacePath: req.Folder,
		AutoApprove: true, ClientRequestID: "workflow-" + taskID,
	})
	if err != nil {
		return "", err
	}
	return taskRunJobID(req.Result.ProjectID, task.ID), nil
}

// waitTaskJob waits until a task an agent step started settles (see
// controlMCPTaskSettled), waking on each new signal and re-reading the task
// at most every minute, then returns its outcome, including a native
// reviewer's durable parsed verdict.
func (s *Server) waitTaskJob(ctx context.Context, account, jobID string) (jobautomation.AgentOutcome, error) {
	projectID, taskID, ok := parseTaskJobID(jobID)
	if !ok {
		return jobautomation.AgentOutcome{}, fmt.Errorf("%s is not a task", jobID)
	}
	for {
		var changed <-chan struct{}
		if s.signals != nil {
			changed = s.signals.Changed()
		}
		task, found, err := s.sessions.Store().GetProjectTask(account, projectID, taskID)
		if err != nil {
			return jobautomation.AgentOutcome{}, err
		}
		if !found || task == nil {
			return jobautomation.AgentOutcome{}, fmt.Errorf("task %s is gone", jobID)
		}
		active := false
		if task.SessionID != "" {
			if state, found, err := s.sessions.Store().GetV3SessionRunState(task.SessionID); err == nil && found {
				active = state.Active
			}
		}
		if controlMCPTaskSettled[task.Status] && !active {
			out := jobautomation.AgentOutcome{JobID: jobID, Status: jobautomation.NormalizeStatus(task.Status)}
			switch task.Status {
			case "failed", "blocked", "rejected", "needs_input", "pending_approval":
				out.Status = jobautomation.StatusFailed
			case "superseded":
				out.Status = jobautomation.StatusCancelled
			}
			result, err := s.taskJobResult(ctx, account, projectID, taskID, out.Status)
			if err != nil {
				out.ResultError = err.Error()
			} else {
				out.Result = result
			}
			if out.Result != nil && out.Result.Git != nil && len(out.Result.Git.Commits) > 0 {
				out.Head = out.Result.Git.Head
			}
			if task.Agent == "reviewer" {
				review, err := s.repairTaskReview(task)
				if err != nil {
					if errors.Is(err, run.ErrTaskReviewCleanupPending) {
						goto waitForChange
					}
					return out, err
				}
				if review != nil {
					out.Verdict, out.Findings, out.ReviewUnreadable = normalizedAutomationReview(review)
				}
			}
			return out, nil
		}
	waitForChange:
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jobautomation.AgentOutcome{}, ctx.Err()
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		// Coalesce a burst of signals from the running task.
		select {
		case <-ctx.Done():
			return jobautomation.AgentOutcome{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// reopenTask sends a reopen step's follow-up into the task's own session,
// exactly as swarm_send_message does: the same session and worktree, never a
// new task. A cancelled task is refused; a task whose run is still going is
// answered busy, so the reopen step waits for it. It returns the follow-up's
// run id, whose end is the task's next job end (task.run.*).
func (s *Server) reopenTask(ctx context.Context, req jobautomation.ReopenRequest) (string, error) {
	projectID, taskID, ok := parseTaskJobID(req.JobID)
	if !ok {
		return "", &jobautomation.RefusedError{Reason: req.JobID + " is not a task"}
	}
	c, err := s.jobAutomationWriteCall(ctx)
	if err != nil {
		return "", err
	}
	p, ok := PrincipalFromRequest(c.request)
	if !ok || p.AccountScopeID != req.Account {
		return "", &jobautomation.RefusedError{Reason: "reopen steps run only for the machine owner's automations"}
	}
	task, found, err := s.sessions.Store().GetProjectTask(req.Account, projectID, taskID)
	if err != nil {
		return "", err
	}
	if !found || task == nil || task.SessionID == "" {
		return "", &jobautomation.RefusedError{Reason: "the task " + req.JobID + " has no session to reopen"}
	}
	switch task.Status {
	case "cancelled", "superseded", "rejected":
		return "", &jobautomation.RefusedError{Reason: "the task " + req.JobID + " was " + task.Status + ": it is not reopened"}
	case "in_progress", "queued", "planning", "pending_approval":
		return "", &jobautomation.BusyError{Reason: "the task " + req.JobID + " is still running (" + task.Status + ")"}
	}
	if state, found, err := s.sessions.Store().GetV3SessionRunState(task.SessionID); err != nil {
		return "", err
	} else if found && state.Active {
		return "", &jobautomation.BusyError{Reason: "the task " + req.JobID + " is still running a follow-up"}
	}
	run, err := c.sendMessage(task.SessionID, req.Message, req.RequestID)
	if err != nil {
		var refused controlMCPToolError
		if errors.As(err, &refused) && refused.status == http.StatusConflict {
			return "", &jobautomation.BusyError{Reason: "the task " + req.JobID + " is still running: " + refused.message}
		}
		return "", err
	}
	followup, _ := run["run_id"].(string)
	if followup == "" {
		return "", errors.New("the follow-up was accepted without a run id")
	}
	return followup, nil
}
