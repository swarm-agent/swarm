package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/outbox"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Job automations (docs/automations.md): when a job ends Swarm records one
// result and runs the steps the person or their AI configured. Swarm gives
// primitives, never a built-in path: with no automation nothing changes and
// the person integrates the job by hand. This file is the daemon side of the
// jobautomation package: how a job end becomes a result, how an agent step
// starts a job, the API under /v3/workflows and /v3/job-deliveries and
// the MCP tools.

const (
	JobAutomationsPath = "/v3/workflows"
	JobDeliveriesPath  = "/v3/job-deliveries"
)

// SetJobAutomations installs the automation runner; nil means the machine
// has none (the routes and tools answer 503).
func (s *Server) SetJobAutomations(runner *jobautomation.Runner) {
	s.jobAutomations = runner
	s.installTaskProgress()
}

func (s *Server) jobAutomationStore() (*jobautomation.Store, error) {
	if s == nil || s.jobAutomations == nil || s.jobAutomations.Store() == nil {
		return nil, errors.New("job automations are not available on this daemon")
	}
	return s.jobAutomations.Store(), nil
}

// jobAutomationCall is a read-only Swarm Control call as the machine owner,
// so a job is read for its automations exactly as swarm_get_job reads it.
func (s *Server) jobAutomationCall(ctx context.Context) (*controlMCPCall, error) {
	if s.jobAutomationCallFn != nil {
		return s.jobAutomationCallFn(ctx)
	}
	return s.outboxCall(ctx)
}

// ---- results ---------------------------------------------------------------

// JobAutomationResults builds the result of each job end for automations.
func (s *Server) JobAutomationResults() jobautomation.ResultSource {
	return jobAutomationResults{s: s}
}

type jobAutomationResults struct{ s *Server }

func (r jobAutomationResults) Result(ctx context.Context, sig pebblestore.Signal, lastAttempt bool) (*jobautomation.Result, error) {
	out, err := r.result(ctx, sig, lastAttempt)
	if err == nil {
		addResultDeliverables(ctx, out)
	}
	return out, err
}

func (r jobAutomationResults) result(ctx context.Context, sig pebblestore.Signal, lastAttempt bool) (*jobautomation.Result, error) {
	switch {
	case strings.HasPrefix(sig.Kind, "worker.run."):
		out, err := r.s.workerJobAutomationResult(ctx, sig.Refs["worker_id"], sig.Refs["run_id"], strings.TrimPrefix(sig.Kind, "worker.run."), lastAttempt)
		if out != nil && sig.Attrs["error"] != "" {
			out.Error = sig.Attrs["error"]
		}
		return out, err
	case strings.HasPrefix(sig.Kind, "media.job."):
		return r.s.mediaJobResultForAutomation(sig)
	case strings.HasPrefix(sig.Kind, "design.job."):
		return r.s.designJobAutomationResult(ctx, sig.Refs["project_id"], sig.Refs["task_id"])
	case strings.HasPrefix(sig.Kind, "task.run."):
		return r.s.taskJobAutomationResult(ctx, sig)
	}
	return nil, nil
}

// workerJobAutomationResult is a worker job's result: the shared result of
// its swarm_get_job view plus its brief, folder, schedule and review.
func (s *Server) workerJobAutomationResult(ctx context.Context, workerID, runID, ended string, lastAttempt bool) (*jobautomation.Result, error) {
	if !controlMCPSafeID.MatchString(workerID) || !controlMCPSafeID.MatchString(runID) {
		return nil, nil
	}
	c, err := s.jobAutomationCall(ctx)
	if err != nil {
		return nil, err
	}
	view, err := c.jobView(controlMCPJobWorker, workerID, runID, 0, false, 0)
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
			return nil, nil // the job was deleted
		}
		return nil, err
	}
	refs, err := c.jobRefs(controlMCPJobWorker, workerID, runID)
	if err != nil {
		return nil, err
	}
	// Follow-ups may settle while the public view still reports in_progress
	// or blocked. Read the same task result used by first-run job views rather
	// than letting that presentation status discard the branch and head.
	if followup, _ := refs.run["followup_status"].(string); followup != "" && refs.projectID != "" {
		result, err := controlMCPTaskResult(c, map[string]any{"project_id": refs.projectID, "task_id": refs.taskID})
		if err != nil {
			return nil, err
		}
		view["result"] = controlMCPMap(result)
	}
	workerName, role := "", ""
	if response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID, nil, nil); err == nil {
		worker := controlMCPMap(response["worker"])
		workerName, _ = worker["name"].(string)
		role, _ = worker["role"].(string)
	}
	if runRole, _ := refs.run["role"].(string); runRole != "" {
		role = runRole
	}
	b := outbox.NewBuilder("automation", 256<<20)
	// A job's workflows start whether or not its end-of-job Git assessment
	// finished in time: an unread Git state is the deliver step's concern
	// (it reassesses and retries), never a reason to hold the trigger back.
	// That holds only once the job's head is known: without a head or
	// commits the result has no branch to deliver, so the job end is
	// retried as for any unsettled assessment.
	unread := !lastAttempt && jobViewGitUnread(view)
	core, err := workerJobResultFromView(ctx, b.GitOutput, lastAttempt || unread, workerID, workerName, runID, view)
	if errors.Is(err, outbox.ErrNotReady) {
		return nil, jobautomation.ErrNotReady
	}
	if err != nil {
		return nil, err
	}
	if unread && core.head == "" && len(controlMCPList(core.result["commits"])) == 0 {
		return nil, jobautomation.ErrNotReady
	}
	out := core.automationResult(ctx, b, role)
	// A resolved head is delivery identity even when the task assessment has
	// not populated its commit list yet. Positively empty lanes retain their
	// existing no-commits handling.
	if out.Git == nil && !out.NoCommits && core.head != "" {
		out.Git = &jobautomation.ResultGit{Head: core.head, Branch: core.branch, BaseCommit: core.base, Worktree: core.repo}
	}
	switch ended {
	case jobautomation.StatusSucceeded, jobautomation.StatusFailed, jobautomation.StatusCancelled:
		out.Status = ended
	}
	out.Folder = c.jobFolder(refs, core.result)
	out.Brief, _ = controlMCPMap(refs.run["input"])["prompt"].(string)
	out.RequestSource, _ = refs.run["request_source"].(string)
	out.ScheduleID, _ = refs.run["automation_id"].(string)
	out.TriggerID, _ = refs.run["workflow_trigger_id"].(string)
	if completed, ok := refs.run["completed_at"].(float64); ok && completed > 0 {
		out.FinishedAt = int64(completed)
	}
	if review := controlMCPMap(view["review"]); len(review) > 0 {
		out.Review = review
	}
	if out.Cost != nil && out.Cost.Model == "" {
		out.Cost.Model, _ = controlMCPMap(view["model"])["model"].(string)
	}
	return out, nil
}

// jobViewGitUnread reports a job view whose delivery assessment could not
// read the Git state (unavailable with a transient inspection reason).
func jobViewGitUnread(view map[string]any) bool {
	result := controlMCPMap(view["result"])
	state, _ := result["delivery_state"].(string)
	code, _ := result["delivery_reason_code"].(string)
	return state == "unavailable" && transientInspectionCode(code)
}

func (s *Server) mediaJobResultForAutomation(sig pebblestore.Signal) (*jobautomation.Result, error) {
	projectID, taskID := sig.Refs["project_id"], sig.Refs["task_id"]
	if !controlMCPSafeID.MatchString(projectID) || !controlMCPSafeID.MatchString(taskID) || s.sessions == nil || s.sessions.Store() == nil {
		return nil, nil
	}
	task, found, err := s.sessions.Store().GetProjectTask(sig.Account, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil {
		return nil, nil
	}
	out := mediaJobAutomationResult(task)
	if ended := strings.TrimPrefix(sig.Kind, "media.job."); ended != "" {
		out.Status = ended
	}
	if out.FinishedAt == 0 {
		out.FinishedAt = sig.At
	}
	return out, nil
}

// designJobAutomationResult is a design job's result: its swarm_get_job view
// (designJobView) and the files it wrote with their hashes.
func (s *Server) designJobAutomationResult(ctx context.Context, projectID, taskID string) (*jobautomation.Result, error) {
	if !controlMCPSafeID.MatchString(projectID) || !controlMCPSafeID.MatchString(taskID) {
		return nil, nil
	}
	c, err := s.jobAutomationCall(ctx)
	if err != nil {
		return nil, err
	}
	view, err := c.designJobView(projectID, taskID, 0)
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	task, err := c.task(projectID, taskID)
	if err != nil {
		return nil, err
	}
	job := controlMCPMap(task["design"])
	status, _ := view["status"].(string)
	out := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: controlMCPJobID(controlMCPJobDesign, projectID, taskID), Kind: jobautomation.KindDesign,
		Role: "designer", RunID: taskID, ProjectID: projectID, JobStatus: status, Status: jobautomation.NormalizeStatus(status)}
	out.Folder, _ = controlMCPMap(task["source_workspace"])["path"].(string)
	if out.Folder == "" {
		out.Folder, _ = task["workspace_path"].(string)
	}
	out.Brief, _ = job["brief"].(string)
	out.Error, _ = view["error"].(string)
	if finished, ok := job["finished_at"].(float64); ok {
		out.FinishedAt = int64(finished)
	}
	model, _ := controlMCPMap(view["model"])["model"].(string)
	totals := controlMCPMap(view["totals"])
	out.Cost = jobResultCost(totals["tokens"], totals["cost_usd"], model)
	if editPath, _ := job["edit_path"].(string); editPath != "" {
		if sha, _ := job["edit_final_sha256"].(string); sha != "" {
			out.Files = append(out.Files, jobautomation.ResultFile{Path: editPath, SHA256: sha, Type: "text/html"})
		}
		return out, nil
	}
	for _, raw := range controlMCPList(job["items"]) {
		item := controlMCPMap(raw)
		path, _ := item["path"].(string)
		sha, _ := item["sha256"].(string)
		if path == "" {
			continue
		}
		out.Files = append(out.Files, jobautomation.ResultFile{Path: path, SHA256: sha, Type: "text/html"})
	}
	return out, nil
}

// ---- agent steps -----------------------------------------------------------

// JobAutomationAgents starts and waits for agent steps' jobs.
func (s *Server) JobAutomationAgents() jobautomation.AgentRunner {
	return jobAutomationAgents{s: s}
}

type jobAutomationAgents struct{ s *Server }

// Role resolves a named worker without deploying or starting it. The runner
// uses this only for positively empty review targets; errors still dispatch
// through Start so unknown workers never silently become skipped reviews.
func (a jobAutomationAgents) Role(ctx context.Context, account, name string) (string, error) {
	if jobautomation.IsBuiltinAgent(name) {
		return name, nil
	}
	c, err := a.s.jobAutomationCall(ctx)
	if err != nil {
		return "", err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || principal.AccountScopeID != account {
		return "", errors.New("agent steps run only for the machine owner's automations")
	}
	resolvedID, err := a.s.workflowRuntimeWorker(c, account, name)
	if err != nil {
		return "", err
	}
	worker, err := c.resolveWorker(resolvedID)
	if err != nil {
		return "", err
	}
	role, _ := worker["role"].(string)
	return role, nil
}

// Start gives the step's worker a job through WorkerExecutionService.Dispatch
// (its environment and workspace checks), with request source automation, so
// that job never triggers automations itself.
func (a jobAutomationAgents) Start(ctx context.Context, req jobautomation.AgentRequest) (string, error) {
	s := a.s
	if jobautomation.IsBuiltinAgent(req.Worker) {
		return s.startBuiltinAgentStep(ctx, req)
	}
	c, err := s.jobAutomationCall(ctx)
	if err != nil {
		return "", err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || principal.AccountScopeID != req.Account {
		return "", &jobautomation.RefusedError{Reason: "agent steps run only for the machine owner's automations"}
	}
	resolvedID, err := s.workflowRuntimeWorker(c, req.Account, req.Worker)
	if err != nil {
		return "", &jobautomation.RefusedError{Reason: err.Error()}
	}
	worker, err := c.resolveWorker(resolvedID)
	if err != nil {
		return "", &jobautomation.RefusedError{Reason: err.Error()}
	}
	workerID, _ := worker["id"].(string)
	role, _ := worker["role"].(string)
	if req.Fixes != "" {
		// Only an agent step that opted in passes fixes, and only to a
		// reviewer; Swarm never lets a reviewer write on its own.
		if req.Fixes != jobautomation.FixesSecurity {
			return "", &jobautomation.RefusedError{Reason: "fixes must be " + jobautomation.FixesSecurity}
		}
		if role != pebblestore.WorkerRoleReviewer {
			return "", &jobautomation.RefusedError{Reason: "fixes is only for a reviewer worker; " + req.Worker + " is a " + firstNonEmptyString(role, "swarm") + " worker"}
		}
	}
	execution, err := s.workerExecutionService()
	if err != nil {
		return "", err
	}
	workspaceID, err := s.resolveWorkerJobWorkspace(principal, "", req.Folder)
	if err != nil {
		return "", &jobautomation.RefusedError{Reason: err.Error()}
	}
	checkout := jobautomation.CheckoutFor(jobautomation.Step{Worker: req.Worker, Fixes: req.Fixes, Checkout: req.Checkout}, role)
	if role != pebblestore.WorkerRoleReviewer && checkout != jobautomation.CheckoutNew {
		return "", &jobautomation.RefusedError{Reason: "checkout " + checkout + " is only for a reviewer; " + req.Worker + " is a " + firstNonEmptyString(role, "swarm") + " worker and works in its own worktree"}
	}
	input := map[string]any{"prompt": agentRequestBrief(req), "project_id": req.Result.ProjectID}
	if req.Fixes != "" {
		input["review_fixes"] = req.Fixes
	}
	if role == pebblestore.WorkerRoleReviewer {
		if strings.TrimSpace(req.Head) == "" {
			return "", &jobautomation.RefusedError{Reason: "no head to review"}
		}
		// same: a new reviewer session in the reviewed job's own worktree,
		// read-only; new: its own checkout at the reviewed head.
		input["review_checkout"] = checkout
		// It reviews the newest work of the delivery: the job an earlier step
		// produced the result in (or moved the head with), else the
		// triggering job.
		reviewJob := firstNonEmptyString(req.ReviewJob, req.Result.JobID)
		if kind := strings.SplitN(reviewJob, "/", 2)[0]; kind != jobautomation.KindWorker && kind != jobautomation.KindTask {
			return "", &jobautomation.RefusedError{Reason: "a reviewer reviews a task's or a worker job's commits; " + reviewJob + " is a " + kind + " job: have an agent step produce the result first"}
		}
		input["review_job"] = reviewJob
		reviewReq := req
		if reviewJob != req.Result.JobID {
			work, err := s.workResult(ctx, req.Account, reviewJob)
			if err != nil || work == nil {
				return "", fmt.Errorf("read the work to review (%s): %v", reviewJob, err)
			}
			reviewReq.Result = *work
		}
		// The reviewer gets the whole picture: the job's brief, its report,
		// the commands it actually ran and its diff.
		input["prompt"] = s.automationReviewerBrief(ctx, reviewReq) + deliverablesNote(req)
	}
	run, err := execution.Dispatch(ctx, principal.AccountScopeID, principal.UserID, pebblestore.WorkerRunAdmission{
		WorkerID: workerID, RequestSource: jobautomation.RequestSourceAutomation, Input: input, IdempotencyKey: req.IdempotencyKey, WorkspaceID: workspaceID,
	})
	if err != nil {
		return "", err
	}
	return controlMCPJobID(controlMCPJobWorker, workerID, run.ID), nil
}

// agentStepBrief is the step's brief with the triggering job's result.
func agentStepBrief(brief string, result jobautomation.Result) string {
	return jobautomation.AgentStepBrief(brief, result)
}

// Wait waits for the job to end: it wakes on each new signal and re-reads the
// run, at most every two seconds, until the run settles or ctx ends.
func (a jobAutomationAgents) Wait(ctx context.Context, account, jobID string) (jobautomation.AgentOutcome, error) {
	s := a.s
	if strings.HasPrefix(jobID, jobautomation.KindTask+"/") {
		return s.waitTaskJob(ctx, account, jobID)
	}
	parts := strings.Split(jobID, "/")
	if len(parts) != 3 || parts[0] != controlMCPJobWorker {
		return jobautomation.AgentOutcome{}, fmt.Errorf("%s is not a worker job", jobID)
	}
	workerID, runID := parts[1], parts[2]
	var changed <-chan struct{}
	for {
		if s.signals != nil {
			changed = s.signals.Changed()
		}
		c, err := s.jobAutomationCall(ctx)
		if err != nil {
			return jobautomation.AgentOutcome{}, err
		}
		response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID+"/runs/"+runID, nil, nil)
		if err != nil {
			return jobautomation.AgentOutcome{}, err
		}
		run := controlMCPMap(response["run"])
		status, _ := run["status"].(string)
		followup, _ := run["followup_status"].(string)
		settled := (status == "succeeded" || status == "failed" || status == "cancelled") && followup != "running" && followup != "queued"
		if status == pebblestore.WorkerRunStatusWaiting {
			// The run waits for its provider's limit to reset and reruns
			// itself then: the step waits for it until the reset rather
			// than inside its timeout. A reset already due is the worker
			// scheduler's to start within seconds, so Wait keeps reading.
			if until := jobAutomationInt64(run["retry_at"]); until > time.Now().UnixMilli() {
				reason, _ := run["queue_reason"].(string)
				return jobautomation.AgentOutcome{JobID: jobID, Status: status, Error: reason, LimitUntil: until}, nil
			}
		}
		if settled {
			if followup == "failed" || followup == "interrupted" {
				status = "failed"
			}
			out := jobautomation.AgentOutcome{JobID: jobID, Status: status}
			out.Error, _ = run["error"].(string)
			result, resultErr := s.workerJobAutomationResult(ctx, workerID, runID, status, true)
			if resultErr == nil {
				out.Result = result
			}
			// A reviewer allowed to fix: its head and fix commits are read
			// from its own checkout with Git, never inferred from a view;
			// any read failure is reported so an OK verdict fails closed.
			if fixGit, fixes, err := s.reviewFixCheckout(ctx, account, workerID, runID); fixes {
				switch {
				case err != nil:
					out.ResultError = err.Error()
				case resultErr != nil:
					out.ResultError = "read the reviewer's result: " + resultErr.Error()
				default:
					if out.Result == nil {
						out.Result = &jobautomation.Result{V: jobautomation.ResultVersion, JobID: jobID, Kind: jobautomation.KindWorker, Role: pebblestore.WorkerRoleReviewer,
							RunID: runID, Status: jobautomation.NormalizeStatus(status)}
					}
					out.Result.Git = fixGit
				}
			}
			if out.Result != nil && out.Result.Git != nil && len(out.Result.Git.Commits) > 0 {
				out.Head = out.Result.Git.Head
			}
			out.Verdict, out.Findings, out.ReviewUnreadable = s.automationAgentReview(account, workerID, runID)
			return out, nil
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jobautomation.AgentOutcome{}, ctx.Err()
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		// Coalesce a burst of signals from the running job.
		select {
		case <-ctx.Done():
			return jobautomation.AgentOutcome{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// jobAutomationInt64 reads a whole number from a decoded JSON value; 0 when
// it is absent or not a number.
func jobAutomationInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// maxReviewFixCommits bounds the fix commits read from a reviewer's checkout;
// fixes are at most MaxReviewerFixLines lines, so more commits than this are
// refused rather than listed in part.
const maxReviewFixCommits = 100

// reviewFixCheckout reads, with Git, the checkout of a reviewer an automation
// step allowed to commit security fixes (its run's input review_fixes): its
// agent/review-<run> branch, HEAD (which must descend from the reviewed head)
// and the commits from the reviewed head to HEAD. fixes is false for every
// other job, which is not read. Git runs routed like all daemon Git.
func (s *Server) reviewFixCheckout(ctx context.Context, account, workerID, runID string) (*jobautomation.ResultGit, bool, error) {
	if s.sessions == nil || s.sessions.Store() == nil {
		return nil, false, nil
	}
	run, found, err := s.sessions.Store().WorkerStore().GetWorkerRun(account, workerID, runID)
	if err != nil || !found {
		return nil, false, nil
	}
	if fixes, _ := run.Input["review_fixes"].(string); fixes != jobautomation.FixesSecurity || run.Role != pebblestore.WorkerRoleReviewer {
		return nil, false, nil
	}
	target, _ := run.Input["captured_review_target"].(map[string]any)
	return s.readReviewFixCheckout(ctx, run.SessionID, runID, target)
}

func (s *Server) taskReviewFixCheckout(ctx context.Context, account string, task *pebblestore.ProjectTaskRecord) (*jobautomation.ResultGit, bool, error) {
	if task.AccountID != account || task.Agent != "reviewer" || task.ReviewFixes != jobautomation.FixesSecurity {
		return nil, false, nil
	}
	return s.readReviewFixCheckout(ctx, task.SessionID, task.ID, task.ReviewTarget)
}

// readReviewFixCheckout is shared by worker and native reviewer tasks.
func (s *Server) readReviewFixCheckout(ctx context.Context, sessionID, runID string, target map[string]any) (*jobautomation.ResultGit, bool, error) {
	reviewed, _ := target["head"].(string)
	if !controlMCPCommitID.MatchString(reviewed) {
		return nil, true, errors.New("the reviewer's captured review head is missing")
	}
	session, found, err := s.sessions.Store().GetSession(sessionID)
	if err != nil {
		return nil, true, fmt.Errorf("read the reviewer's session: %w", err)
	}
	branch := "agent/review-" + runID
	if !found || !session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) == "" || session.WorktreeBranch != branch {
		return nil, true, errors.New("the reviewer's fix checkout is not recorded on its session")
	}
	dir := session.WorktreeRootPath
	git := func(args ...string) (string, error) {
		gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
		out, err := sandbox.TimedGitOutput(gitCtx, dir, nil, append([]string{"-c", "safe.directory=" + dir}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	current, err := git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || current != branch {
		return nil, true, fmt.Errorf("the reviewer's checkout is not on %s", branch)
	}
	head, err := git("rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil || !controlMCPCommitID.MatchString(head) {
		return nil, true, errors.New("the reviewer's checkout head could not be read")
	}
	if _, err := git("merge-base", "--is-ancestor", reviewed, head); err != nil {
		return nil, true, errors.New("the reviewer's head does not descend from the reviewed head")
	}
	log, err := git("log", "--no-decorate", "--format=%H %s", fmt.Sprintf("--max-count=%d", maxReviewFixCommits+1), reviewed+".."+head, "--")
	if err != nil {
		return nil, true, fmt.Errorf("list the reviewer's fix commits: %w", err)
	}
	out := &jobautomation.ResultGit{Branch: branch, BaseCommit: reviewed, Head: head, Worktree: dir, Files: []string{}}
	for _, line := range strings.Split(log, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sha, subject, _ := strings.Cut(line, " ")
		out.Commits = append(out.Commits, jobautomation.ResultCommit{SHA: sha, Subject: subject})
	}
	if len(out.Commits) > maxReviewFixCommits {
		return nil, true, fmt.Errorf("the reviewer made more than %d fix commits", maxReviewFixCommits)
	}
	if (head == reviewed) != (len(out.Commits) == 0) {
		return nil, true, errors.New("the reviewer's fix commits do not match its head")
	}
	return out, true, nil
}

// automationAgentReview is the verdict and findings of the review an agent
// step's job recorded: a reviewer job's store.JobReview is recorded against
// the job it reviewed (its input review_job) with ReviewerJobID set to the
// reviewer job, so it is read there, never from the reviewer's own result.
// A job that recorded no review has no verdict. Findings are the record's
// verbatim FINDINGS section (bounded at 16,000 bytes when it was recorded);
// a readable BLOCK record without findings gives its reason. A record whose
// reason is the parser's malformed-report reason (no verdict could be read)
// is unreadable: block, no findings, unreadable true, so no reopen sends the
// reason to the coder as findings. The job's task summary (result.Report,
// cut at 4,000 characters) is never a source of findings.
func (s *Server) automationAgentReview(account, workerID, runID string) (verdict, findings string, unreadable bool) {
	if s.sessions == nil || s.sessions.Store() == nil {
		return "", "", false
	}
	ws := s.sessions.Store().WorkerStore()
	run, found, err := ws.GetWorkerRun(account, workerID, runID)
	if err != nil || !found {
		return "", "", false
	}
	target, _ := run.Input["review_job"].(string)
	parts := strings.Split(target, "/")
	if len(parts) != 3 || (parts[0] != controlMCPJobWorker && parts[0] != jobautomation.KindTask) {
		return "", "", false
	}
	reviewed := parts[1]
	if parts[0] == jobautomation.KindTask {
		reviewed = pebblestore.TaskReviewWorker(parts[1])
	}
	review, err := ws.GetJobReviewByReviewer(account, reviewed, parts[2], controlMCPJobID(controlMCPJobWorker, workerID, runID))
	if err != nil || review == nil || (review.Verdict != jobautomation.VerdictOK && review.Verdict != jobautomation.VerdictBlock) {
		return "", "", false
	}
	return normalizedAutomationReview(review)
}

// normalizedAutomationReview keeps native and custom reviewer outcomes identical.
func normalizedAutomationReview(review *pebblestore.JobReview) (verdict, findings string, unreadable bool) {
	if review.Verdict == jobautomation.VerdictBlock && review.Reason == pebblestore.JobReviewMalformedReason {
		return jobautomation.VerdictBlock, "", true
	}
	findings = strings.TrimSpace(review.Findings)
	if strings.EqualFold(strings.TrimRight(findings, "."), "none") {
		findings = ""
	}
	if findings == "" && review.Verdict == jobautomation.VerdictBlock {
		findings = review.Reason
	}
	return review.Verdict, findings, false
}

// ---- reopen steps ------------------------------------------------------------

// JobAutomationReopener sends reopen steps' follow-ups.
func (s *Server) JobAutomationReopener() jobautomation.Reopener {
	return jobAutomationReopener{s: s}
}

type jobAutomationReopener struct{ s *Server }

// jobAutomationWriteCall is the Swarm Control call a reopen step sends its
// follow-up with: the machine owner, allowed to read jobs and write to their
// sessions only.
func (s *Server) jobAutomationWriteCall(ctx context.Context) (*controlMCPCall, error) {
	return s.jobAutomationOwnerCall(ctx, "job-automation-reopen", "Swarm job automation reopen (internal)", "swarm:read", "sessions:read", "sessions:write", "automations:read")
}

// jobAutomationOwnerCall is a Swarm Control call as the machine owner with
// exactly scopes: each internal workflow action gets only what it needs.
func (s *Server) jobAutomationOwnerCall(ctx context.Context, tokenID, tokenName string, scopes ...string) (*controlMCPCall, error) {
	if s.jobAutomationCallFn != nil {
		return s.jobAutomationCallFn(ctx)
	}
	if s.identitySessions == nil {
		return nil, errors.New("identity unavailable")
	}
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil || !isCompleteProductActor(actor) {
		return nil, errors.New("the machine owner's account is not set up")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	req.Host, req.RemoteAddr = "127.0.0.1", "127.0.0.1:0"
	req = requestWithActorContext(req, actor)
	req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{
		ID: tokenID, Name: tokenName, Scopes: scopes,
		AccountScopeID: actor.AccountScopeID, UserID: actor.UserID,
	})
	return &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}, nil
}

// Reopen sends the follow-up into the job's own session through
// swarm_message_job's own path (controlMCPMessageJob): the same session and
// worktree, never a new job. A cancelled job is refused with the reason; a
// job whose run or follow-up is still in flight is answered busy
// (jobautomation.BusyError), so the reopen step waits for it.
func (a jobAutomationReopener) Reopen(ctx context.Context, req jobautomation.ReopenRequest) (string, error) {
	if strings.HasPrefix(req.JobID, jobautomation.KindTask+"/") {
		return a.s.reopenTask(ctx, req)
	}
	c, err := a.s.jobAutomationWriteCall(ctx)
	if err != nil {
		return "", err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || principal.AccountScopeID != req.Account {
		return "", &jobautomation.RefusedError{Reason: "reopen steps run only for the machine owner's automations"}
	}
	args := map[string]any{"job_id": req.JobID, "message": req.Message, "client_request_id": req.RequestID}
	kind, w, r, err := controlMCPParseJobID(args)
	if err != nil || kind != controlMCPJobWorker {
		return "", &jobautomation.RefusedError{Reason: "only a worker job's session can be reopened: " + req.JobID}
	}
	state, _, err := c.jobState(kind, w, r)
	if err != nil {
		return "", err
	}
	status, _ := state["status"].(string)
	switch status {
	case "cancelled":
		return "", &jobautomation.RefusedError{Reason: "the job " + req.JobID + " was cancelled: it is not reopened"}
	case "in_progress", "queued", "planning", "pending_approval", "needs_input", "waiting_on_approval":
		return "", &jobautomation.BusyError{Reason: "the job " + req.JobID + " is still running (" + status + ")"}
	}
	if active, _ := state["run_active"].(bool); active {
		return "", &jobautomation.BusyError{Reason: "the job " + req.JobID + " is still running a follow-up"}
	}
	out, err := controlMCPMessageJob(c, args)
	if err != nil {
		var refused controlMCPToolError
		if errors.As(err, &refused) {
			// A follow-up that started after the busy check above, before
			// this send: the job is busy again, not refusing.
			if strings.Contains(refused.message, controlMCPFollowupBusy) {
				return "", &jobautomation.BusyError{Reason: "the job " + req.JobID + " is still running a follow-up: " + refused.message}
			}
			return "", &jobautomation.RefusedError{Reason: refused.message}
		}
		return "", err
	}
	sent := controlMCPMap(controlMCPMap(out)["sent"])
	followup, _ := sent["run_id"].(string)
	if queued, _ := sent["queued"].(bool); queued && followup == "" {
		// A run started in the job's session after the busy check above: the
		// message was queued for it, so that run carries the findings.
		followup, _ = sent["queued_for_run_id"].(string)
	}
	if followup == "" {
		return "", errors.New("the follow-up was accepted without a run id")
	}
	return followup, nil
}

// ---- job head --------------------------------------------------------------

// ImportJobRef checks a job's exact head is in the folder (jobHeadInFolder) and
// names it refs/swarm/jobs/<run-id>, so a run step can push or inspect it.
// Git runs routed like all daemon Git on a folder.
func ImportJobRef(ctx context.Context, folder string, result jobautomation.Result) (string, error) {
	g := result.Git
	if g == nil || g.Head == "" {
		return "", nil
	}
	if !controlMCPCommitID.MatchString(g.Head) || !controlMCPSafeID.MatchString(result.RunID) {
		return "", errors.New("the job's head or run id is not usable as a ref")
	}
	if err := jobHeadInFolder(ctx, folder, g.Head); err != nil {
		return "", err
	}
	ref := "refs/swarm/jobs/" + result.RunID
	gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
	if _, err := sandbox.TimedGitOutput(gitCtx, folder, nil, "-c", "safe.directory="+folder, "update-ref", ref, g.Head); err != nil {
		return "", fmt.Errorf("name the job's head %s: %w", ref, err)
	}
	return ref, nil
}

// VerifyJobHead reports whether head descends from ancestor, with Git in the
// folder: both commits must be there (jobHeadInFolder; jobs commit in their
// worktrees of the folder) and git merge-base --is-ancestor decides. Git runs routed like all daemon
// Git on a folder.
func VerifyJobHead(ctx context.Context, folder string, ancestor, head jobautomation.HeadRef) (bool, error) {
	for _, ref := range []jobautomation.HeadRef{ancestor, head} {
		if !controlMCPCommitID.MatchString(ref.Commit) {
			return false, fmt.Errorf("%q is not a commit id", ref.Commit)
		}
		if err := jobHeadInFolder(ctx, folder, ref.Commit); err != nil {
			return false, err
		}
	}
	gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
	_, err := sandbox.TimedGitOutput(gitCtx, folder, nil, "-c", "safe.directory="+folder, "merge-base", "--is-ancestor", ancestor.Commit, head.Commit)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base: %w", err)
}

// JobHeadChangedLines is how many lines the commits from base to head change
// in total (added plus removed), with Git in the folder, where both commits
// must be (jobHeadInFolder). A binary file counts
// as more than the reviewer fix limit: its size in lines is unknown. Git runs
// routed like all daemon Git on a folder.
func JobHeadChangedLines(ctx context.Context, folder string, base, head jobautomation.HeadRef) (int, error) {
	for _, ref := range []jobautomation.HeadRef{base, head} {
		if !controlMCPCommitID.MatchString(ref.Commit) {
			return 0, fmt.Errorf("%q is not a commit id", ref.Commit)
		}
		if err := jobHeadInFolder(ctx, folder, ref.Commit); err != nil {
			return 0, err
		}
	}
	gitCtx := sandbox.WithGitCommandTimeout(ctx, time.Minute)
	out, err := sandbox.TimedGitOutput(gitCtx, folder, nil, "-c", "safe.directory="+folder, "diff", "--numstat", "--no-renames", "--no-ext-diff", "--no-textconv", base.Commit, head.Commit, "--")
	if err != nil {
		return 0, fmt.Errorf("git diff --numstat: %w", err)
	}
	return countNumstatLines(string(out))
}

// countNumstatLines sums git diff --numstat's added and removed columns; a
// binary file ("-") counts as one more line than the reviewer fix limit.
func countNumstatLines(numstat string) (int, error) {
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return 0, fmt.Errorf("unexpected git diff --numstat line %q", controlMCPTruncate(line, 120))
		}
		if fields[0] == "-" || fields[1] == "-" {
			total += jobautomation.MaxReviewerFixLines + 1
			continue
		}
		added, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, fmt.Errorf("unexpected git diff --numstat line %q", controlMCPTruncate(line, 120))
		}
		removed, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("unexpected git diff --numstat line %q", controlMCPTruncate(line, 120))
		}
		total += added + removed
	}
	return total, nil
}

// jobAutomationFixesError refuses fixes, or checkout same, on an agent step
// whose worker is not a reviewer. A role name is checked by NormalizeSteps; a worker id or name
// is resolved here against the account's workers, and one that does not
// resolve to a reviewer is refused: fixes are never granted on a guess.
func (s *Server) jobAutomationFixesError(account string, steps []jobautomation.Step) error {
	for _, step := range steps {
		if step.Type != jobautomation.StepAgent || (step.Fixes == "" && step.Checkout != jobautomation.CheckoutSame) || jobautomation.IsBuiltinAgent(step.Worker) {
			continue
		}
		refuse := jobautomation.NonReviewerFixesError
		if step.Fixes == "" {
			refuse = jobautomation.NonReviewerCheckoutError
		}
		if s.sessions == nil || s.sessions.Store() == nil {
			return errors.New("workers are not available to check the fixes option")
		}
		ws := s.sessions.Store().WorkerStore()
		worker, found, err := ws.GetWorker(account, step.Worker)
		if err != nil {
			return err
		}
		cursor := ""
		for page := 0; !found && page < 50; page++ {
			list, err := ws.ListWorkers(account, pebblestore.ListWorkersQuery{Limit: 200, After: cursor})
			if err != nil {
				return err
			}
			for _, candidate := range list.Workers {
				if strings.EqualFold(candidate.Name, step.Worker) {
					worker, found = candidate, true
					break
				}
			}
			if list.NextCursor == "" || list.NextCursor == cursor {
				break
			}
			cursor = list.NextCursor
		}
		if !found {
			return refuse(step.ID, step.Worker, "unknown")
		}
		if worker.Role != pebblestore.WorkerRoleReviewer {
			return refuse(step.ID, step.Worker, firstNonEmptyString(worker.Role, "swarm"))
		}
	}
	return nil
}

// ---- views -----------------------------------------------------------------

func jobAutomationView(a jobautomation.Automation) map[string]any {
	state := "active"
	if a.Proposed {
		state = "proposed"
	} else if !a.Enabled {
		state = "disabled"
	}
	view := map[string]any{
		"inputs": a.Inputs, "with": a.With,
		"id": a.ID, "name": a.Name, "scope": a.Scope, "when": a.When, "enabled": a.Enabled, "retries": a.Retries, "result": a.Result,
		"state": state, "steps": a.Steps, "revision": a.Revision, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
	}
	if a.On != nil {
		view["on"] = a.On
		if a.Enabled && !a.Proposed && a.On.Schedule != nil {
			if next := a.NextScheduleRun(time.Now()); !next.IsZero() {
				view["next_run_at"] = next.UnixMilli()
			}
		}
	}
	if migrations := jobAutomationStepMigrations(a.Steps); len(migrations) > 0 {
		view["step_migrations"] = migrations
	}
	if a.Proposed && a.BaseID != "" {
		// Propose binds the base by exact name; AcceptProposal checks that
		// same name and base revision before replacing the active definition.
		view["replaces"] = map[string]any{"id": a.BaseID, "revision": a.BaseRevision, "name": a.Name}
	}
	return view
}

// jobAutomationStepMigrations lists the stored steps that run differently
// from how they are stored (jobautomation.LegacyCheckoutMigration): each
// step's stored and effective checkout and a note, so the owner knows to
// resave the workflow.
func jobAutomationStepMigrations(steps []jobautomation.Step) []map[string]any {
	var out []map[string]any
	for _, step := range steps {
		migrated, note := jobautomation.LegacyCheckoutMigration(step)
		if note == "" {
			continue
		}
		out = append(out, map[string]any{"step": step.ID, "checkout": step.Checkout, "effective_checkout": migrated.Checkout,
			"note": note + "; resave the workflow with checkout " + migrated.Checkout})
	}
	return out
}

// jobDeliveryView is a delivery without its full result (the result is
// read with the job). withLogs adds each step's log tail.
func jobDeliveryView(d jobautomation.Delivery) map[string]any {
	steps := make([]map[string]any, 0, len(d.Steps))
	for _, step := range d.Steps {
		view := map[string]any{"id": step.ID, "type": step.Type, "state": step.State, "attempts": step.Attempts}
		for key, value := range map[string]int64{"started_at": step.StartedAt, "finished_at": step.FinishedAt, "next_attempt_at": step.NextAttemptAt} {
			if value > 0 {
				view[key] = value
			}
		}
		if step.ExitCode != nil {
			view["exit_code"] = *step.ExitCode
		}
		for key, value := range map[string]string{"output": step.Output, "reason": step.Reason, "next": step.Next, "log_tail": step.LogTail, "note": step.Note} {
			if value != "" {
				view[key] = value
			}
		}
		if len(step.Outcome) > 0 {
			view["outcome"] = step.Outcome
		}
		if len(step.Failures) > 0 {
			view["failures"] = step.Failures
		}
		if step.Items != nil {
			items := make([]map[string]any, 0, len(step.Items))
			for _, item := range step.Items {
				items = append(items, map[string]any{"index": item.Index, "label": item.Label, "state": item.State, "job_id": item.JobID, "error": item.Error})
			}
			view["items"] = items
		}
		steps = append(steps, view)
	}
	out := map[string]any{
		"delivery_id": d.ID, "automation_id": d.AutomationID, "automation": d.AutomationName, "automation_revision": d.AutomationRevision,
		"job_id": d.JobID, "round": d.Round, "state": d.State, "steps": steps, "created_at": d.CreatedAt,
	}
	if d.Followup != "" {
		out["followup"] = d.Followup
	}
	if d.StartedAt > 0 {
		out["started_at"] = d.StartedAt
	}
	if d.FinishedAt > 0 {
		out["finished_at"] = d.FinishedAt
	}
	if d.Reruns > 0 {
		out["reruns"] = d.Reruns
	}
	if d.Head != "" {
		out["head"] = d.Head
	}
	if d.BlockedReason != "" {
		out["blocked_reason"] = d.BlockedReason
	}
	if sb := d.SentBack; sb != nil {
		sent := map[string]any{"step": sb.Step, "round": sb.Round}
		if sb.Reason != "" {
			sent["reason"] = sb.Reason
		}
		out["sent_back"] = sent
	}
	// The reviewer fix commits the delivery head moved to (an agent step
	// with fixes whose commits were applied).
	fixes := []any{}
	for _, step := range d.Steps {
		if applied, _ := step.Outcome["fixes_applied"].(bool); !applied {
			continue
		}
		switch commits := step.Outcome["fix_commits"].(type) {
		case []any:
			fixes = append(fixes, commits...)
		case []map[string]any:
			for _, c := range commits {
				fixes = append(fixes, c)
			}
		}
	}
	if len(fixes) > 0 {
		out["fix_commits"] = fixes
	}
	return out
}

// ---- HTTP ------------------------------------------------------------------

type jobAutomationBody struct {
	Inputs    map[string]jobautomation.Input `json:"inputs,omitempty"`
	With      map[string]string              `json:"with,omitempty"`
	On        *jobautomation.Trigger         `json:"on,omitempty"`
	RotateKey bool                           `json:"rotate_key,omitempty"`
	Project   string                         `json:"project,omitempty"`
	Workspace string                         `json:"workspace,omitempty"`
	Name      string                         `json:"name"`
	Scope     jobautomation.Scope            `json:"scope"`
	When      string                         `json:"when"`
	Enabled   *bool                          `json:"enabled"`
	Retries   int                            `json:"retries"`
	Steps     []jobautomation.Step           `json:"steps"`
	// Result is what the work must hand over (optional).
	Result *jobautomation.ResultSpec `json:"result"`
}

func (b jobAutomationBody) automation() jobautomation.Automation {
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	return jobautomation.Automation{Inputs: b.Inputs, With: b.With, On: b.On, Name: b.Name, Scope: b.Scope, When: b.When, Enabled: enabled, Retries: b.Retries, Steps: b.Steps, Result: b.Result}
}

// errRunStepNeedsOwner refuses a run step from a caller without the machine
// owner's authority.
var errRunStepNeedsOwner = errors.New("a run step runs its command on the host as the daemon's user, outside the sandbox and without tool approvals, so only the machine's owner may save one: the local owner, or a key with the admin scope (a full AI key). This key may save agent and reopen steps")

// runStepOwnerError refuses steps containing a run step unless the caller
// is the machine's owner: a request without a scoped token (the local owner,
// as on every other route) or a token with the admin scope. A write-level AI
// key or a remote device token (automations:write without admin) is refused:
// a run step's command and args are free-form, so saving one is code
// execution on the host.
func runStepOwnerError(r *http.Request, steps []jobautomation.Step) error {
	if !jobautomation.HasRunStep(steps) {
		return nil
	}
	if record, ok := ScopedTokenFromRequest(r); ok && !record.HasScope("admin") {
		return errRunStepNeedsOwner
	}
	return nil
}

func jobAutomationHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobautomation.ErrProposalConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, errRunStepNeedsOwner):
		writeError(w, http.StatusForbidden, err)
	case jobautomation.IsValidationError(err):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, jobautomation.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		var refusal controlMCPToolError
		if errors.As(err, &refusal) {
			status := refusal.status
			if status == 0 {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
	}
}

// handleJobAutomations serves /v3/workflows, /v3/workflows/stop
// and /v3/workflows/{id}.
func (s *Server) handleJobAutomations(w http.ResponseWriter, r *http.Request) {
	if s.serveWorkflowEvent(w, r) {
		return
	}
	// Decode export separately so JSON-only exports need read scope, while
	// file output still requires write scope before any filesystem access.
	if strings.Trim(strings.TrimPrefix(r.URL.Path, JobAutomationsPath), "/") == "export" && r.Method == http.MethodPost {
		principal, ok := PrincipalFromRequest(r)
		if !ok || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		if !s.requireScopeAny(w, r, "automations:read", "automations:write") {
			return
		}
		s.handleWorkflowTransfer(w, r, "export")
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if read && !s.requireScopeAny(w, r, "automations:read", "automations:write") {
		return
	}
	if !read && !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := s.jobAutomationStore(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, JobAutomationsPath), "/")
	switch {
	case r.Method == http.MethodPost && (rest == "propose" || strings.HasSuffix(rest, "/enable") || strings.HasSuffix(rest, "/disable")):
		var args map[string]any
		if err := decodeJSONStrict(w, r, 256*1024, &args); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if args == nil {
			writeError(w, http.StatusBadRequest, errors.New("object body required"))
			return
		}
		if rest == "propose" {
			args["action"] = "propose"
		} else {
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				writeError(w, http.StatusBadRequest, errors.New("invalid workflow id"))
				return
			}
			args["id"], args["action"] = parts[0], parts[1]
		}
		if _, exists := args["machine"]; exists {
			writeError(w, http.StatusBadRequest, errors.New("nested machine routing is not allowed"))
			return
		}
		if rotate, _ := args["rotate_key"].(bool); rotate {
			writeError(w, http.StatusForbidden, errors.New("key rotation is not available for proposals"))
			return
		}
		p, _ := PrincipalFromRequest(r)
		out, err := s.ManageWorkflows(r.Context(), p, p.UserID, args)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case strings.HasSuffix(rest, "/accept") && r.Method == http.MethodPost:
		s.handleWorkflowAccept(w, r, strings.TrimSuffix(rest, "/accept"))
	case (rest == "history" && r.Method == http.MethodGet) || (rest == "restore" && r.Method == http.MethodPost):
		s.handleWorkflowHistory(w, r, rest)
	case rest == "import" && r.Method == http.MethodPost:
		s.handleWorkflowTransfer(w, r, rest)
	case rest == "" && r.Method == http.MethodGet:
		opts, err := workflowListOptions(r.URL.Query().Get)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		out, err := s.ListWorkflows(workflowCallerFromRequest(r), opts)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		view := out.(map[string]any)
		view["ok"] = true
		writeJSON(w, http.StatusOK, view)
	case rest == "" && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		var body jobAutomationBody
		if err := decodeJSONStrict(w, r, 256*1024, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, created, _, err := s.SaveWorkflow(workflowCallerFromRequest(r), body, "set", false)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		key, err := s.saveTriggerKey(saved, body.RotateKey)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		view := map[string]any{"ok": true, "automation": jobAutomationView(saved), "created": created}
		if key != "" {
			view["trigger_key"] = key
		}
		writeJSON(w, status, view)
	case rest == "stop" && r.Method == http.MethodGet:
		stop, err := s.StopWorkflows(workflowCallerFromRequest(r), nil)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stop": stop})
	case rest == "stop" && r.Method == http.MethodPost:
		var body struct {
			Stopped *bool `json:"stopped"`
		}
		if err := decodeJSONStrict(w, r, 4096, &body); err != nil || body.Stopped == nil {
			writeError(w, http.StatusBadRequest, errors.New("stopped (true or false) is required"))
			return
		}
		stop, err := s.StopWorkflows(workflowCallerFromRequest(r), body.Stopped)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stop": stop})
	case rest != "" && !strings.Contains(rest, "/") && r.Method == http.MethodGet:
		a, err := s.GetWorkflow(workflowCallerFromRequest(r), rest)
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		view := jobAutomationView(a)
		if a.On != nil {
			repository, _ := s.jobAutomationStore()
			events, err := repository.TriggerEvents(a)
			if err != nil {
				jobAutomationHTTPError(w, err)
				return
			}
			view["events"] = events

		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "automation": view})
	case !strings.Contains(rest, "/") && r.Method == http.MethodDelete:
		out, err := s.DeleteWorkflow(workflowCallerFromRequest(r), map[string]any{"id": rest, "name": r.URL.Query().Get("name")})
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		view := out.(map[string]any)
		view["ok"] = true
		writeJSON(w, http.StatusOK, view)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown job automation route"))
	}
}

func (s *Server) setJobAutomationsStopped(stopped bool, by string) (jobautomation.StopState, error) {
	store, err := s.jobAutomationStore()
	if err != nil {
		return jobautomation.StopState{}, err
	}
	stop, err := store.SetStopped(stopped, by)
	if err == nil && !stopped {
		s.jobAutomations.Wake()
	}
	return stop, err
}

// handleJobDeliveries serves /v3/job-deliveries, /v3/job-deliveries/{id} and
// POST /v3/job-deliveries/{id}/rerun.
func (s *Server) handleJobDeliveries(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if read && !s.requireScopeAny(w, r, "automations:read", "automations:write") {
		return
	}
	if !read && !s.requireScope(w, r, "automations:write") {
		return
	}
	store, err := s.jobAutomationStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	account := principal.AccountScopeID
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, JobDeliveriesPath), "/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		query := r.URL.Query()
		limit, _ := strconv.Atoi(query.Get("limit"))
		list, err := store.ListDeliveries(account, jobautomation.DeliveryFilter{JobID: query.Get("job_id"), AutomationID: query.Get("automation_id"), State: query.Get("state"), Limit: min(max(limit, 0), 200)})
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		out := []map[string]any{}
		for _, d := range list {
			out = append(out, jobDeliveryView(d))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deliveries": out})
	case len(parts) == 1 && rest != "" && r.Method == http.MethodGet:
		d, err := store.GetDelivery(account, parts[0])
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		view := jobDeliveryView(store.WithLogs(d))
		view["result"] = d.Result
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delivery": view})
	case len(parts) == 2 && parts[1] == "rerun" && r.Method == http.MethodPost:
		d, rerun, err := s.rerunJobDelivery(account, parts[0])
		if err != nil {
			jobAutomationHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rerun": rerun, "delivery": jobDeliveryView(d)})
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown job delivery route"))
	}
}

func (s *Server) rerunJobDelivery(account, id string) (jobautomation.Delivery, bool, error) {
	store, err := s.jobAutomationStore()
	if err != nil {
		return jobautomation.Delivery{}, false, err
	}
	d, rerun, err := store.Rerun(account, id)
	if err == nil && rerun {
		s.jobAutomations.Wake()
	}
	return d, rerun, err
}

// ---- MCP tools ---------------------------------------------------------------

func (c *controlMCPCall) jobAutomationAccount(scope string) (*jobautomation.Store, string, error) {
	if err := c.requireTokenScope(scope); err != nil {
		return nil, "", err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || !principal.Valid() || c.server == nil {
		return nil, "", controlMCPToolFailure("automations need an authenticated account")
	}
	store, err := c.server.jobAutomationStore()
	if err != nil {
		return nil, "", controlMCPToolFailure("%s", err.Error())
	}
	return store, principal.AccountScopeID, nil
}

// controlMCPAutomationSteps reads the steps argument (the same shape as the
// API's steps) and validates them.
func controlMCPAutomationSteps(raw any) ([]jobautomation.Step, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, controlMCPToolFailure("steps could not be read")
	}
	var steps []jobautomation.Step
	if err := json.Unmarshal(encoded, &steps); err != nil {
		return nil, controlMCPToolFailure("steps must be a list of {id, type: run|agent|reopen|deliver, if, command, args, worker, brief, fixes, checkout, produce, from, message, max_rounds, to, path, what, timeout_seconds, continue_on_failure}: %s", err.Error())
	}
	if err := jobautomation.NormalizeSteps(steps); err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	if err := jobautomation.ValidateBriefPlaceholders(steps); err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	return steps, nil
}

// automationSteps reads and validates the steps argument, including that
// fixes names a reviewer worker of the caller's account and that only the
// machine's owner sets a run step. swarm_set_workflow and both checks of a
// then (before the job starts, and when it is set) go through it.
func (c *controlMCPCall) automationSteps(raw any, account string) ([]jobautomation.Step, error) {
	steps, err := controlMCPAutomationSteps(raw)
	if err != nil {
		return nil, err
	}
	if err := runStepOwnerError(c.request, steps); err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	if c.server != nil {
		if err := c.server.jobAutomationFixesError(account, steps); err != nil {
			return nil, controlMCPToolFailure("%s", err.Error())
		}
	}
	return steps, nil
}

func (s *Server) saveWorkflowArgs(caller WorkflowCaller, args map[string]any, skipUnchanged bool, source string) (any, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var body jobAutomationBody
	if err := json.Unmarshal(encoded, &body); err != nil {
		return nil, controlMCPToolFailure("invalid workflow: %s", err)
	}
	saved, created, unchanged, err := s.SaveWorkflow(caller, body, source, skipUnchanged)
	if err != nil {
		return nil, err
	}
	if unchanged {
		return map[string]any{"automation": jobAutomationView(saved), "created": false, "unchanged": true}, nil
	}
	return workflowSaveView(saved, created), nil
}

func controlMCPSetAutomation(c *controlMCPCall, args map[string]any) (any, error) {
	out, err := c.workflowDispatch(http.MethodPost, JobAutomationsPath, nil, args)
	if err != nil {
		return nil, err
	}
	out["next"] = workflowSaveNext
	if key, _ := out["trigger_key"].(string); key != "" {
		out["next"] = workflowSaveNext + " The trigger key is shown only now; store it securely."
	}
	return out, nil
}

// ListWorkflows lists the account's workflows: by default each definition
// with its last deliveries one compact line each (WorkflowListOptions selects
// bounded detail or one delivery).
func (s *Server) ListWorkflows(caller WorkflowCaller, opts WorkflowListOptions) (any, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	if opts.Detail != "" && opts.Detail != workflowDetailDeliveries {
		return nil, controlMCPToolFailure("detail must be %q (or omitted for one line per delivery)", workflowDetailDeliveries)
	}
	if opts.DeliveryID != "" {
		d, err := store.GetDelivery(account, opts.DeliveryID)
		if errors.Is(err, jobautomation.ErrNotFound) {
			return nil, workflowHistoryNotFound("no delivery %q", opts.DeliveryID)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"delivery": workflowDeliveryDetail(d)}, nil
	}
	list, err := store.List(account)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, a := range list {
		view := jobAutomationView(a)
		if a.On != nil {
			events, err := store.TriggerEvents(a)
			if err != nil {
				return nil, err
			}
			view["events"] = events

		}
		if err := workflowHistorySummary(store, account, a.ID, view); err != nil {
			return nil, err
		}
		latest, err := store.ListDeliveries(account, jobautomation.DeliveryFilter{AutomationID: a.ID, Limit: workflowListDeliveries})
		if err != nil {
			return nil, err
		}
		deliveries := []map[string]any{}
		for _, d := range latest {
			if opts.Detail == workflowDetailDeliveries {
				deliveries = append(deliveries, workflowDeliveryDetail(d))
			} else {
				deliveries = append(deliveries, workflowDeliveryLine(d))
			}
		}
		view["latest_deliveries"] = deliveries
		out = append(out, view)
	}
	stop, err := store.StopState()
	if err != nil {
		return nil, err
	}
	return map[string]any{"automations": out, "stopped": stop.Stopped}, nil
}

func (s *Server) DeleteWorkflow(caller WorkflowCaller, args map[string]any) (any, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	id := controlMCPString(args, "id")
	if id == "" {
		a, err := store.FindByName(account, controlMCPString(args, "name"))
		if err != nil {
			return nil, controlMCPToolFailure("no workflow named %q (swarm_list_workflows lists them)", controlMCPString(args, "name"))
		}
		id = a.ID
	}
	if a, err := store.Get(account, id); err != nil {
		return nil, err
	} else if a.On != nil && a.On.Event.Source == "webhook" {
		values, err := s.triggerSecrets()
		if err != nil {
			return nil, err
		}
		if err := values.DeleteWorkflowTriggerKey(account, id); err != nil {
			return nil, err
		}
	}
	if err := store.DeleteChange(account, id, jobautomation.Change{Source: "delete", Who: caller.Who}); err != nil {
		if errors.Is(err, jobautomation.ErrNotFound) {
			return nil, workflowHistoryNotFound("no automation %q", id)
		}
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func controlMCPStopAutomations(c *controlMCPCall, args map[string]any) (any, error) {
	out, err := c.workflowDispatch(http.MethodPost, JobAutomationsPath+"/stop", nil, args)
	if err != nil {
		return nil, err
	}
	stopped, _ := controlMCPMap(out["stop"])["stopped"].(bool)
	next := "no new automation step starts on this machine until you turn it off; running steps finish"
	if !stopped {
		next = "automations run again; deliveries the switch stopped re-run with swarm_deliver_job (automation: true)"
	}
	return map[string]any{"stopped": stopped, "next": next}, nil
}

// controlMCPThen creates the automation of a `then` argument: the steps,
// scoped to one job or one schedule's jobs.
func (c *controlMCPCall) controlMCPThen(raw any, name, folder string, scope jobautomation.Scope) (map[string]any, error) {
	then := controlMCPMap(raw)
	if then == nil {
		return nil, controlMCPToolFailure("then must be {steps, when, retries}")
	}
	if err := c.requireTokenScope("automations:write"); err != nil {
		return nil, err
	}
	scope.Folder = folder
	out, err := c.server.saveWorkflowArgs(workflowCallerFromRequest(c.request), map[string]any{"name": name, "scope": scope, "when": controlMCPString(then, "when"), "retries": controlMCPInt(then, "retries", 0), "steps": then["steps"]}, false, "set")
	if err != nil {
		if jobautomation.IsValidationError(err) {
			return nil, controlMCPToolFailure("then: %s", err.Error())
		}
		return nil, err
	}
	return controlMCPMap(out.(map[string]any)["automation"]), nil
}

// controlMCPValidateThen checks a `then` argument before a job starts.
func controlMCPValidateThen(c *controlMCPCall, raw any) error {
	if raw == nil {
		return nil
	}
	_, account, err := c.jobAutomationAccount("automations:write")
	if err != nil {
		return err
	}
	_, err = c.automationSteps(controlMCPMap(raw)["steps"], account)
	return err
}

// jobDeliveries is the automations part of swarm_get_job: the job's
// deliveries and their steps. ok is false when the machine has none.
func (c *controlMCPCall) jobDeliveries(jobID string) ([]map[string]any, bool) {
	if c.server == nil || c.server.jobAutomations == nil {
		return nil, false
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return nil, false
	}
	list, err := c.server.jobAutomations.Store().JobDeliveries(principal.AccountScopeID, jobID)
	if err != nil || len(list) == 0 {
		return nil, false
	}
	out := []map[string]any{}
	for _, d := range list {
		out = append(out, jobDeliveryView(d))
	}
	return out, true
}

// rerunJobAutomations is swarm_deliver_job with automation: true. Per
// automation it re-runs only the failed or stopped steps of the job's newest
// delivery, and only while that delivery's head is still the job's current
// head; older deliveries are never re-run (POST
// /v3/job-deliveries/<id>/rerun re-runs one named delivery explicitly).
func (c *controlMCPCall) rerunJobAutomations(kind, a, b string) (any, error) {
	jobID := controlMCPJobID(kind, a, b)
	store, account, err := c.jobAutomationAccount("automations:write")
	if err != nil {
		return nil, err
	}
	list, err := store.JobDeliveries(account, jobID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, controlMCPToolFailure("job %s has no workflow deliveries: set one with swarm_set_workflow, or deliver the job by hand (swarm_deliver_job without automation)", jobID)
	}
	head, err := c.jobCurrentHead(kind, a, b)
	if err != nil {
		return nil, controlMCPToolFailure("could not read job %s's current head, so nothing was re-run: %s", jobID, controlMCPTruncate(err.Error(), 300))
	}
	outcomes, err := store.RerunNewest(account, jobID, head)
	if err != nil {
		return nil, err
	}
	rerun := []map[string]any{}
	unchanged := []map[string]any{}
	stale := false
	for _, outcome := range outcomes {
		d := outcome.Delivery
		if outcome.Rerun {
			rerun = append(rerun, jobDeliveryView(d))
			continue
		}
		entry := map[string]any{"delivery_id": d.ID, "automation": d.AutomationName, "state": d.State, "reason": outcome.Reason}
		if h := d.TriggerHead(); h != "" {
			entry["head"] = h
		}
		stale = stale || outcome.Reason == jobautomation.ReasonStaleHead
		unchanged = append(unchanged, entry)
	}
	if len(rerun) > 0 && c.server.jobAutomations != nil {
		c.server.jobAutomations.Wake()
	}
	out := map[string]any{"job_id": jobID, "rerun": rerun, "unchanged": unchanged}
	if head != "" {
		out["job_head"] = head
	}
	switch {
	case len(rerun) > 0:
		out["next"] = "the failed or stopped steps of the newest delivery run again; swarm_get_job shows their progress"
	case stale:
		out["next"] = "nothing was re-run: the newest delivery is for an older head than the job's current head, and older deliveries are never re-run (POST /v3/job-deliveries/<id>/rerun re-runs one named delivery)"
	default:
		out["next"] = "the newest delivery had no failed or stopped step to re-run"
	}
	return out, nil
}

// jobCurrentHead is the commit a job's branch is at now, read from its
// swarm_get_job view; "" for a job without commits (or a media or design
// job, which has none).
func (c *controlMCPCall) jobCurrentHead(kind, a, b string) (string, error) {
	if kind == jobautomation.KindTask {
		p, ok := PrincipalFromRequest(c.request)
		if !ok || c.server == nil {
			return "", errors.New("no principal to read the task")
		}
		result, err := c.server.taskJobResult(c.request.Context(), p.AccountScopeID, a, b, "")
		if err != nil || result == nil || result.Git == nil {
			return "", err
		}
		return result.Git.Head, nil
	}
	if kind != controlMCPJobWorker {
		return "", nil
	}
	view, err := c.jobView(kind, a, b, 0, false, 0)
	if err != nil {
		return "", err
	}
	core, err := workerJobResultFromView(c.request.Context(), outbox.NewBuilder("automation", 256<<20).GitOutput, true, a, "", b, view)
	if err != nil {
		return "", err
	}
	return core.head, nil
}

// controlMCPAutomationStepsSchema is the JSON schema of an automation's steps.
func controlMCPAutomationStepsSchema() map[string]any {
	return map[string]any{"type": "array", "minItems": 0, "maxItems": jobautomation.MaxSteps, "description": "Ordered steps; empty only when on is set. A failed step stops the rest unless continue_on_failure; a step whose if does not hold is skipped and the rest still run.",
		"items": map[string]any{"type": "object", "required": []string{"type"}, "properties": map[string]any{
			"id":   controlMCPStringProp("Step id (lowercase, digits, - or _). Default step-N.", 0, 64),
			"type": map[string]any{"type": "string", "enum": []string{jobautomation.StepRun, jobautomation.StepAgent, jobautomation.StepReopen, jobautomation.StepDeliver}},
			"if": map[string]any{"type": "array", "maxItems": jobautomation.MaxStepConditions, "description": "Conditions that must all hold for the step to run, else it is skipped: {step, status: succeeded|failed} or, for an agent step, {step, verdict: ok|block} (the verdict of the review its job recorded; no verdict never matches).",
				"items": map[string]any{"type": "object", "required": []string{"step"}, "properties": map[string]any{
					"step":    controlMCPStringProp("An earlier step's id.", 1, 64),
					"status":  map[string]any{"type": "string", "enum": []string{jobautomation.StateSucceeded, jobautomation.StateFailed}},
					"verdict": map[string]any{"type": "string", "enum": []string{jobautomation.VerdictOK, jobautomation.VerdictBlock}},
				}}},
			"from":                controlMCPStringProp("reopen: the earlier agent step whose findings are sent into the triggering task's or job's own session (as swarm_send_message or swarm_message_job does). Optional when message is set.", 0, 64),
			"message":             controlMCPStringProp("reopen: text placed before the findings, or sent alone when from recorded none (an ok review) or from is omitted. {{steps.<id>.output}} is replaced by that earlier step's recorded output line.", 0, 4000),
			"max_rounds":          map[string]any{"type": "integer", "minimum": 1, "maximum": jobautomation.MaxRounds, "description": "reopen: how many times this reopen step may reopen the job (each reopen step counts its own rounds; the job as a whole allows the sum of its reopen steps' max_rounds); past it the delivery ends blocked (rounds_exhausted: step <id>). Default 2."},
			"command":             controlMCPStringProp("run: the machine owner's script, as an absolute path or a path relative to the folder (e.g. deliver/push.sh). It runs on the host as the daemon's user, in the folder, with the machine's own credentials, and only if the file (symlinks resolved) is owned by root and not writable by group or others; a relative path must resolve to a singly linked file inside the folder (prefer an absolute path in a directory only root can write). It gets the job's result JSON on stdin and SWARM_* variables; its last output line is the step's output. Only the machine's owner may set a run step (a full AI key); a write key is refused.", 0, 1024),
			"args":                map[string]any{"type": "array", "maxItems": jobautomation.MaxStepArgs, "items": map[string]any{"type": "string"}},
			"worker":              controlMCPStringProp("agent: one of Swarm's own agents (swarm, coder, finder, designer, reviewer), run as a task; reviewer is Swarm's native reviewer at the triggering job's exact head, read-only unless fixes is security. Any worker, including a custom reviewer, can be named by its worker id; no worker is deployed automatically.", 0, 200),
			"for_each":            tool.WorkflowForEachSchema(),
			"brief":               controlMCPStringProp("agent: the brief; the triggering task's or job's result is attached.", 0, 20000),
			"fixes":               map[string]any{"type": "string", "enum": []string{jobautomation.FixesSecurity}, "description": "agent, reviewer or a reviewer worker named by its id: \"security\" lets the reviewer commit small security-only fixes on a branch at the reviewed head instead of blocking for them. Its commits move the delivery head only with an OK verdict and at most " + strconv.Itoa(jobautomation.MaxReviewerFixLines) + " changed lines (else the step fails: reviewer fixes too large). Without it a reviewer is read-only."},
			"checkout":            map[string]any{"type": "string", "enum": []string{jobautomation.CheckoutSame, jobautomation.CheckoutNew, jobautomation.CheckoutNone}, "description": "agent: where the step's agent works. same: a new session in the worktree of the job it follows, read-only (a reviewer only; the default for a reviewer). new: its own worktree at the job's head (swarm, the coder, a reviewer with fixes, any other worker). none: the folder itself (finder and designer, as their tasks always do). Omit it for the agent's default."},
			"produce":             map[string]any{"type": "boolean", "description": "agent: the agent writes the workflow's result file (result: {kind: file, path, describe}); Swarm checks it exists, records its sha256 and adds it to what the delivery hands over. Not for finder or reviewer (they do not write files). A reviewer started after it reviews the produced work."},
			"to":                  map[string]any{"type": "string", "enum": []string{jobautomation.DeliverFolder, jobautomation.DeliverMerge, jobautomation.DeliverPublish, jobautomation.DeliverOutbox}, "description": "deliver: where the deliverables go. folder: copied into path, a directory inside the folder (never overwriting a different file). merge: the work's branch merged into the folder's branch (swarm_integrate_task or swarm_deliver_job). publish: a pull request through the folder's publish rule. outbox: the machine's bucket (SWARM_OUTBOX_URL), with a manifest at machines/<machine>/deliveries/<delivery>/<step>.json."},
			"path":                controlMCPStringProp("deliver to folder: the directory inside the folder (relative, e.g. reports).", 0, 512),
			"what":                map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{"type": "string", "maxLength": 512}, "description": "deliver: the deliverables by name, as the work's result lists them (branch, report, a file path). Default: the workflow's result; without one, everything that fits the target (files, media and the report for folder and outbox; the branch for merge and publish)."},
			"timeout_seconds":     map[string]any{"type": "integer", "minimum": 1, "maximum": jobautomation.MaxStepTimeoutSeconds, "description": "Default 600."},
			"continue_on_failure": map[string]any{"type": "boolean", "description": "Default false: a failed step stops the rest."},
		}}}
}

func controlMCPThenSchema() map[string]any {
	return map[string]any{"type": "object", "description": "Optional steps to run when the job ends (a workflow scoped to this job). Without it nothing runs: integrate by hand.",
		"required": []string{"steps"}, "properties": map[string]any{
			"steps":   controlMCPAutomationStepsSchema(),
			"when":    map[string]any{"type": "string", "enum": []string{jobautomation.WhenSucceeded, jobautomation.WhenFailed, jobautomation.WhenAlways}, "description": "Default succeeded."},
			"retries": map[string]any{"type": "integer", "minimum": 0, "maximum": jobautomation.MaxRetries, "description": "Automatic retries of a failed step, with backoff. Default 0."},
		}}
}

func controlMCPAutomationTools() []controlMCPTool {
	return append(append(workflowTransferTools(), workflowHistoryTools()...), []controlMCPTool{
		{
			Name: "swarm_set_workflow", Title: "Set a workflow",
			Description: "Create or update (by name) a workflow: what Swarm does when work ends in a folder. It starts when a task of Swarm's own agents ends (swarm_run_task: swarm, coder, finder, designer; its first run and each follow-up in its session) or when a worker job ends. Its ordered steps are each: another agent working on the result (agent: swarm, coder, finder or designer as a task in the folder, a coder starting at the work's head; or Swarm's native reviewer task at that head, or a custom worker id), a follow-up that sends its message and/or an agent step's findings back into the work's own session so the same agent fixes its own work (reopen, bounded by max_rounds), handing the work's result over (deliver: to a directory of the folder, merged into the folder's branch, a pull request, or the machine's outbox bucket), or the machine owner's root-owned script run on the host in the folder (run: push, deploy, alert). Every result lists its deliverables (branch, files with sha256, media, report); result names the one the work must hand over, and an agent step with produce: true writes it when the work did not (research turned into a report). A step's if runs it only when earlier steps' status or review verdict match. Scope it to every task and job in the folder, one agent's tasks (role), one worker's jobs, one job or one schedule's jobs; when succeeded, failed or always. swarm_list_workflows shows exactly what each one does. Swarm runs only these steps and reports what happened; with no workflow the work just ends with its branch and the person integrates it by hand. Changes retain history readable with swarm_workflow_history and reversible with swarm_restore_workflow. Offer it; do not set it up unasked. for_each queues one worker job per object in a committed JSON array with bounded parallelism and optional after_field dependencies on id_field (default id). " + tool.WorkflowTriggerDescription,
			InputSchema: controlMCPObject([]string{"name", "steps"}, map[string]any{
				"inputs": tool.WorkflowInputsSchema(), "with": tool.WorkflowWithSchema(),
				"on":         tool.WorkflowTriggerSchema(),
				"rotate_key": map[string]any{"type": "boolean", "description": "Replace the webhook signing key; the save result shows it only once."},
				"name":       controlMCPStringProp("Unique name; setting an existing name updates it (its revision rises).", 1, 200),
				"project":    controlMCPStringProp("Project (id or name) whose folder the workflow watches, when scope.folder is not given.", 0, 256),
				"workspace":  controlMCPStringProp("Which of the project's folders (absolute path), when it has several.", 0, 4096),
				"scope": map[string]any{"type": "object", "properties": map[string]any{
					"folder":   controlMCPStringProp("Absolute path of the project folder.", 0, 4096),
					"role":     map[string]any{"type": "string", "enum": []string{"swarm", "coder", "finder", "designer"}, "description": "Only the tasks this one of Swarm's own agents finished in the folder."},
					"worker":   controlMCPStringProp("Only this worker's jobs (worker id).", 0, 200),
					"job":      controlMCPStringProp("Only this task or job (task/<project>/<task> or a job id).", 0, 700),
					"schedule": controlMCPStringProp("Only this schedule's jobs (schedule id).", 0, 200),
				}},
				"when":    map[string]any{"type": "string", "enum": []string{jobautomation.WhenSucceeded, jobautomation.WhenFailed, jobautomation.WhenAlways}, "description": "Default succeeded."},
				"retries": map[string]any{"type": "integer", "minimum": 0, "maximum": jobautomation.MaxRetries, "description": "Automatic retries of a failed step, with backoff. Default 0."},
				"enabled": map[string]any{"type": "boolean", "description": "Default true."},
				"result": map[string]any{"type": "object", "required": []string{"kind"}, "description": "What the work must hand over (optional): a file (path, relative to the folder) or media file, the report, or the branch, and describe what it must contain. A deliver step hands it over by default; when the work did not produce it, an agent step with produce: true writes it.", "properties": map[string]any{
					"kind":     map[string]any{"type": "string", "enum": []string{jobautomation.DeliverableFile, jobautomation.DeliverableMedia, jobautomation.DeliverableReport, jobautomation.DeliverableBranch}},
					"path":     controlMCPStringProp("file or media: its path relative to the folder (e.g. reports/audit.md).", 0, 512),
					"describe": controlMCPStringProp("What the result must be or contain, for the agent that produces it and the reviewer that checks it.", 0, 4000),
				}},
				"steps": controlMCPAutomationStepsSchema(),
			}),
			Annotations: controlMCPWrites, call: controlMCPSetAutomation,
		},
		{
			Name: "swarm_list_workflows", Title: "List workflows",
			Description: "This account's workflows, each exactly as Swarm runs it (scope, when, steps, revision, enabled), with its history count and last change (time, who; swarm_workflow_history reads revisions and swarm_restore_workflow restores them), its last 3 deliveries one line each (delivery_id, job, state, round, head, blocked reason, and each step's state and verdict; no findings), and whether the machine-wide stop switch is on. For step detail (attempts, exit code, output, findings cut to 1500 characters) pass delivery_id for one delivery, or detail: \"deliveries\" for the last 3 of every workflow.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"delivery_id": controlMCPStringProp("One delivery (a delivery_id from the listing) in bounded detail, instead of the list.", 0, 200),
				"detail":      map[string]any{"type": "string", "enum": []string{workflowDetailDeliveries}, "description": "deliveries: each workflow's last 3 deliveries with their steps' outcomes (bounded), instead of one line each."},
			}), Annotations: controlMCPReadOnly, call: controlMCPListAutomations,
		},
		{
			Name: "swarm_delete_workflow", Title: "Delete a workflow",
			Description: "Delete a workflow by id or name. Its history and past deliveries stay readable; swarm_restore_workflow recreates a retained revision. One already running finishes.",
			InputSchema: controlMCPObject(nil, map[string]any{"id": controlMCPStringProp("", 0, 200), "name": controlMCPStringProp("", 0, 200)}),
			Annotations: controlMCPDestructive, call: controlMCPDeleteAutomation,
		},
		{
			Name: "swarm_stop_workflows", Title: "Stop or resume all workflows",
			Description: "The machine-wide stop switch: while on, no new workflow step starts (running steps finish; the next step is recorded as stopped). Turn it off to resume; swarm_deliver_job with automation: true re-runs stopped steps.",
			InputSchema: controlMCPObject([]string{"stopped"}, map[string]any{"stopped": map[string]any{"type": "boolean"}}),
			Annotations: controlMCPWrites, call: controlMCPStopAutomations,
		},
	}...)
}
