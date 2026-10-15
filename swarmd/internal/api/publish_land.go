package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) landingQueue() (*publish.Queue, error) {
	s.landQueueMu.Lock()
	defer s.landQueueMu.Unlock()
	if s.sessions == nil || s.sessions.Store() == nil {
		return nil, errors.New("landing store unavailable")
	}
	if s.landQueue == nil {
		s.landQueue = publish.NewQueue(s.sessions.Store().Underlying())
	}
	return s.landQueue, nil
}
func (c *controlMCPCall) enqueueLand(rule publish.Rule, req publish.Request, retryStopped bool) (map[string]any, error) {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return nil, errors.New("principal required")
	}
	q, err := c.server.landingQueue()
	if err != nil {
		return nil, err
	}
	entry, pos, err := q.EnqueueDelivery(publish.Entry{Account: principal.AccountScopeID, Request: req}, retryStopped)
	if err != nil {
		return nil, err
	}
	kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": req.JobID})
	if err != nil {
		return nil, err
	}
	refs, err := c.jobRefs(kind, a, b)
	if err != nil {
		return nil, err
	}
	if err := c.recordJobActivity(kind, a, b, refs, "job."+entry.State, entry.Commit, entry.NotificationID); err != nil {
		return nil, err
	}
	// The durable entry, not this request goroutine, owns recovery.
	return landDeliveryReceipt(entry, pos), nil
}
func landDeliveryReceipt(entry publish.Entry, pos int) map[string]any {
	out := map[string]any{"job_id": entry.Request.JobID, "delivery_state": entry.State, "queue_position": pos}
	if entry.Reason != "" {
		out["delivery_reason"] = entry.Reason
	}
	if entry.Commit != "" {
		out["commit"] = entry.Commit
	}
	return out
}

func (p *AutoPublisher) landPending(ctx context.Context) error {
	q, err := p.server.landingQueue()
	if err != nil {
		return err
	}
	account := p.server.OutboxAccount()
	var entries []publish.Entry
	// Use the queue's public pending-folder index rather than inspecting keys.
	entries, err = q.Pending(account)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(entries))
	// Bound folder fan-out; each Process drains exactly one folder's FIFO.
	sem := make(chan struct{}, 4)
	for _, entry := range entries {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := p.processLand(ctx, q, account, folder); err != nil {
				errorsCh <- err
			}
		}(entry.Request.Folder)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		return err
	}
	return nil
}
func (p *AutoPublisher) processLand(ctx context.Context, q *publish.Queue, account, folder string) error {
	c, err := p.server.internalCall(ctx, "sessions:read", "sessions:write", "projects:read", "projects:write", "swarm:read", "agents:read", "automations:read", "signals:read")
	if err != nil {
		return err
	}
	rule, ok, err := p.server.publishRule(folder)
	if err != nil {
		return err
	}
	if !ok || rule.Mode != "land" {
		return errors.New("queued folder no longer has a land rule")
	}
	return q.Process(ctx, account, folder, publish.QueueHooks{
		Admit: p.server.admitLanding,
		Queued: func(ctx context.Context, e publish.Entry) error {
			kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": e.Request.JobID})
			if err != nil {
				return err
			}
			refs, err := c.jobRefs(kind, a, b)
			if err != nil {
				return err
			}
			return c.recordJobActivity(kind, a, b, refs, "job.queued", e.Commit, e.NotificationID)
		},
		Prepare: func(ctx context.Context, e *publish.Entry) error {
			kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": e.Request.JobID})
			if err != nil {
				return err
			}
			refs, err := c.jobRefs(kind, a, b)
			if err != nil {
				return err
			}
			if err := c.recordJobActivity(kind, a, b, refs, "job.landing", "", e.NotificationID); err != nil {
				return err
			}
			return c.prepareLand(ctx, rule, e)
		},
		Land: func(ctx context.Context, e publish.Entry) (publish.LandResult, error) {
			return publish.Land(ctx, rule, e.Request)
		},
		SendBack: func(ctx context.Context, e publish.Entry) error {
			requestID := e.FollowUpID
			if requestID == "" {
				return errors.New("landing follow-up has no durable request ID")
			}
			_, err := controlMCPMessageJob(c, map[string]any{"job_id": e.Request.JobID, "message": "Landing rejected this job: " + e.Reason, "client_request_id": requestID})
			return err
		},
		Finished: func(ctx context.Context, e publish.Entry) error {
			kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": e.Request.JobID})
			if err != nil {
				return err
			}
			refs, err := c.jobRefs(kind, a, b)
			if err != nil {
				return err
			}
			severity := pebblestore.SignalSeverityInfo
			if e.State == "landing_stopped" || e.State == "land_failed" {
				severity = pebblestore.SignalSeverityWarning
			}
			signal := "job." + e.State
			if e.State == "landed" {
				signal = "job.landed"
			}
			summary := "Job delivery: " + e.State
			if e.State == "landed" {
				summary += " " + e.Commit
			}
			return c.signalPublishDurable(kind, a, b, refs, signal, severity, summary, map[string]string{"head": e.Commit, "send_backs": fmt.Sprint(e.SendBacks), "notification_id": e.NotificationID})
		},
	})
}

// admitLanding shares the run admission lock with drain and restart claims.
// Queue.Process retains the durable entry while waiting, and releases only after
// checks, Git and durable effects finish, before admitting the next entry.
func (s *Server) admitLanding(ctx context.Context, e publish.Entry) (func(), error) {
	manager := s.ExecutionCapacity()
	if manager == nil {
		return nil, errors.New("landing admission unavailable")
	}
	lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: e.Account, SessionID: "landing:" + e.Request.JobID, RunID: "landing:" + e.Request.JobID})
	if err != nil {
		return nil, err
	}
	return func() { _ = lease.Release() }, nil
}

func (c *controlMCPCall) prepareLand(ctx context.Context, rule publish.Rule, e *publish.Entry) error {
	kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": e.Request.JobID})
	if err != nil {
		return err
	}
	view, err := c.jobView(kind, a, b, 0, false, 0)
	if err != nil {
		return err
	}
	refs, err := c.jobRefs(kind, a, b)
	if err != nil {
		return err
	}
	if refs.run["role"] == "reviewer" || controlMCPJobResultJSON(view)["delivery_state"] == "report_only" {
		return errors.New("reviewer/report-only jobs cannot enter land preparation")
	}
	// jobView uses jobState, which includes the session's active follow-up.
	// Check readiness before reading a result that does not exist yet or
	// touching the sandbox/worktree while the worker is still writing it.
	status, _ := view["status"].(string)
	if running, _ := view["run_active"].(bool); running || !controlMCPJobSettled[status] {
		return publish.ErrNotSettled
	}
	m := sandbox.Default()
	if m == nil || !m.Active() {
		return errors.New("landing requires an active sandbox")
	}
	result := controlMCPJobResultJSON(view)
	path, _ := result["workspace_path"].(string)
	if path == "" {
		return errors.New("job worktree is unavailable")
	}
	scope, err := m.CommandScope(path)
	if err != nil {
		return err
	}
	if scope.Root != rule.Folder {
		return errors.New("job sandbox does not belong to publish folder")
	}
	if c.server.sandboxEnvironments == nil {
		return errors.New("folder environment store unavailable")
	}
	env, found, err := c.server.sandboxEnvironments.Get(rule.Folder)
	if err != nil {
		return err
	}
	if found && env.Account != e.Account {
		return errors.New("environment belongs to another account")
	}
	if reviewGated(rule) {
		ws := c.server.sessions.Store().WorkerStore()
		e.ValidateReview = func(head, patch, base string) error {
			reviews, err := ws.JobReviews(e.Account, a, b)
			if err != nil {
				return err
			}
			if err := rejectBlockingPatch(head, patch, reviews, func(reviewHead string) (string, error) {
				res, err := m.ExecDaemon(ctx, scope, sandbox.ExecRequest{Command: []string{"bash", "-o", "pipefail", "-c", `git diff --no-ext-diff --no-textconv --binary "$1...$2" | git patch-id --stable`, "patch-id", base, reviewHead}, WorkingDir: path, Timeout: 2 * time.Minute, MaxTimeout: 2 * time.Minute, MaxOutput: 4096})
				if err != nil {
					return "", err
				}
				fields := strings.Fields(res.Stdout)
				if res.ExitCode != 0 || len(fields) != 2 {
					return "", errors.New("cannot verify blocking review patch")
				}
				return fields[0], nil
			}); err != nil {
				return err
			}
			if e.Review != nil {
				r, err := ws.CurrentValidJobReview(e.Account, a, b, e.Review.ReviewedHead)
				if err != nil {
					return err
				}
				if r == nil || r.Verdict != "ok" || (e.Review.ReviewID != "" && e.Review.ReviewID != r.ReviewerJobID) {
					return reviewRequired(head)
				}
				e.Review.ReviewID = r.ReviewerJobID
			}
			return nil
		}
	}
	return prepareLandWorktreeWithExecutors(ctx, rule, e, path, env.Definition.Checks, env.Definition.CheckTimeoutMinutes,
		func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			return m.ExecDaemon(ctx, scope, req)
		}, func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			return m.Exec(ctx, scope, req)
		}, publish.Update, func(head string) error {
			return c.requireLandReview(kind, a, b, head)
		})
}

// prepareLandWorktree keeps preparation and its verified command boundary testable.
func prepareLandWorktree(ctx context.Context, rule publish.Rule, e *publish.Entry, path string, checks []string, checkTimeoutMinutes int,
	exec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error), update func(context.Context, publish.Rule, string) (publish.Base, error), reviewChecks ...func(string) error) error {
	return prepareLandWorktreeWithExecutors(ctx, rule, e, path, checks, checkTimeoutMinutes, exec, exec, update, reviewChecks...)
}

func prepareLandWorktreeWithExecutors(ctx context.Context, rule publish.Rule, e *publish.Entry, path string, checks []string, checkTimeoutMinutes int,
	exec, checkExec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error), update func(context.Context, publish.Rule, string) (publish.Base, error), reviewChecks ...func(string) error) error {
	currentBranch, err := exec(ctx, sandbox.ExecRequest{Command: []string{"git", "branch", "--show-current"}, WorkingDir: path, Timeout: time.Minute, MaxTimeout: time.Minute, MaxOutput: 4096})
	if err != nil {
		return err
	}
	if currentBranch.ExitCode != 0 || strings.TrimSpace(currentBranch.Stdout) != e.Request.Branch {
		return errors.New("job branch changed before landing")
	}
	base, err := update(ctx, rule, e.Request.BaseBranch)
	if err != nil {
		return errors.New("base update failed")
	}
	git := func(args ...string) (string, int, error) {
		// Exec routes Git explicitly inside the verified sandbox; no host fallback.
		res, err := exec(ctx, sandbox.ExecRequest{Command: append([]string{"git"}, args...), WorkingDir: path, Timeout: 2 * time.Minute, MaxTimeout: 2 * time.Minute, MaxOutput: 4096})
		if err != nil {
			return "", -1, err
		}
		return res.Stdout, res.ExitCode, nil
	}
	dirty, code, err := git("status", "--porcelain")
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(dirty) != "" {
		return errors.New("job worktree is not clean")
	}
	var reviewedHead, reviewedPatch, oldBase string
	patchID := func(head, base string) (string, error) {
		// Fixed shell program; refs are separate positional arguments. Compute
		// the whole job's patch without truncating diff bytes through tool output.
		res, err := exec(ctx, sandbox.ExecRequest{Command: []string{"bash", "-o", "pipefail", "-c", `git diff --no-ext-diff --no-textconv --binary "$1...$2" | git patch-id --stable`, "patch-id", base, head}, WorkingDir: path, Timeout: 2 * time.Minute, MaxTimeout: 2 * time.Minute, MaxOutput: 4096})
		if err != nil {
			return "", err
		}
		fields := strings.Fields(res.Stdout)
		if res.ExitCode != 0 || len(fields) != 2 {
			return "", errors.New("cannot verify rebased patch identity")
		}
		if _, err := publish.ParseHeadOutput(fields[0]); err != nil {
			return "", err
		}
		return fields[0], nil
	}
	if reviewGated(rule) {
		raw, exit, readErr := git("rev-parse", "HEAD")
		if readErr != nil || exit != 0 {
			return errors.New("cannot read review head")
		}
		reviewedHead, err = publish.ParseHeadOutput(raw)
		if err != nil {
			return err
		}
		if len(reviewChecks) != 1 {
			return reviewRequired(reviewedHead)
		}
		current := reviewedHead
		// Exact-head authority supersedes an older equivalence checkpoint: the
		// landing base may have absorbed part of this already-approved job.
		if exactErr := reviewChecks[0](current); exactErr == nil {
			e.Review = nil
		} else {
			var missing *missingLandReview
			if !errors.As(exactErr, &missing) || e.Review == nil {
				return exactErr
			}
		}
		if e.Review != nil {
			patch, err := patchID(current, base.Commit)
			if err != nil {
				return err
			}
			approvedPatch, err := patchID(e.Review.ReviewedHead, base.Commit)
			if err != nil {
				return err
			}
			if patch != e.Review.PatchID || approvedPatch != patch {
				return reviewRequired(current)
			}
			reviewedHead = e.Review.ReviewedHead
		}
		if err := reviewChecks[0](reviewedHead); err != nil {
			return err
		}
		reviewedPatch, err = patchID(current, base.Commit)
		if err != nil {
			return err
		}
		if e.Review == nil {
			e.Review = &publish.ReviewEquivalence{ReviewedHead: reviewedHead, RebasedHead: current, PatchID: reviewedPatch}
		}
		if e.ValidateReview != nil {
			if err := e.ValidateReview(current, reviewedPatch, base.Commit); err != nil {
				return err
			}
		}
		if e.Checkpoint != nil {
			if err := e.Checkpoint(); err != nil {
				return err
			}
		}
	}
	if rule.Pipeline {
		raw, exit, readErr := git("merge-base", reviewedHead, base.Commit)
		if readErr != nil || exit != 0 {
			return errors.New("cannot read range-diff base")
		}
		oldBase, err = publish.ParseHeadOutput(raw)
		if err != nil {
			return err
		}
	}
	_, code, err = git("rebase", base.Commit)
	if err != nil {
		return err
	}
	if code != 0 {
		// A conflicted attempt must never leave transferable approval behind.
		e.Review = nil
		if e.Checkpoint != nil {
			if err := e.Checkpoint(); err != nil {
				return err
			}
		}
		files, _, readErr := git("diff", "--name-only", "--diff-filter=U")
		if readErr != nil {
			return readErr
		}
		_, abortCode, abortErr := git("rebase", "--abort")
		if abortErr != nil || abortCode != 0 {
			return errors.New("cannot abort conflicted rebase")
		}
		back := publish.RebaseConflictSendBack(base, files)
		if reviewGated(rule) {
			return &publish.SendBackError{Reason: back.Error() + "; after committing the resolution, give the reviewer `review_job` for a new OK review"}
		}
		return back
	}
	head, code, err := git("rev-parse", "HEAD")
	if err != nil || code != 0 {
		return errors.New("cannot read rebased head")
	}
	parsedHead, err := publish.ParseHeadOutput(head)
	if err != nil {
		return err
	}
	if rule.Pipeline && parsedHead != reviewedHead {
		// Let Git produce only correspondence headers, never a truncated patch.
		diff, exit, readErr := git("range-diff", "--no-color", "--no-patch", oldBase+".."+reviewedHead, base.Commit+".."+parsedHead)
		if readErr != nil || exit != 0 || !unchangedRangeDiff(diff) {
			return reviewRequired(parsedHead)
		}
	}
	if reviewGated(rule) && parsedHead != reviewedHead {
		patch, err := patchID(parsedHead, base.Commit)
		if err != nil {
			return err
		}
		if patch != reviewedPatch {
			return reviewRequired(parsedHead)
		}
	}
	if reviewGated(rule) {
		e.Review.RebasedHead = parsedHead
		if e.Checkpoint != nil {
			if err := e.Checkpoint(); err != nil {
				return err
			}
		}
	}
	e.Request.Head = parsedHead
	e.Request.BaseCommit = base.Commit
	e.Request.BaseBranch = base.Branch
	e.Checks = nil
	limit := 20 * time.Minute
	if checkTimeoutMinutes > 0 {
		limit = time.Duration(checkTimeoutMinutes) * time.Minute
	}
	// ExecRequest.Timeout is enforced by the supervisor after CPU admission.
	// Keep only caller cancellation on ctx: a command deadline here would also
	// charge queueing (and container acquisition) to the check's budget.
	for i, command := range checks {
		var tail string
		req := sandbox.ExecRequest{Command: []string{"bash", "-lc", command}, WorkingDir: path, Timeout: limit, MaxTimeout: 60 * time.Minute, MaxOutput: 4096, OnOutput: func(data []byte) {
			tail += string(data)
			if len(tail) > 4096 {
				tail = tail[len(tail)-4096:]
			}
		}}
		res, err := checkExec(ctx, req)
		var died *sandbox.ErrSandboxDied
		if errors.As(err, &died) && ctx.Err() == nil {
			tail = ""
			res, err = checkExec(ctx, req)
		}
		if errors.As(err, &died) {
			return fmt.Errorf("landing check infrastructure failure: %w", err)
		}
		if err != nil {
			if errors.Is(err, provider.ErrOperationTimedOut) && ctx.Err() == nil {
				e.Checks = append(e.Checks, publish.CheckResult{ExitCode: -1, Output: tail})
				return publish.ChecksSendBack(base, checks[:i+1], true, tail)
			}
			return err
		}
		e.Checks = append(e.Checks, publish.CheckResult{ExitCode: res.ExitCode, Output: tail})
		if res.ExitCode != 0 {
			return publish.ChecksSendBack(base, checks[:i+1], false, tail)
		}
	}
	finalHead, code, err := git("rev-parse", "HEAD")
	parsedFinalHead, parseErr := publish.ParseHeadOutput(finalHead)
	if err != nil || code != 0 || parseErr != nil || parsedFinalHead != e.Request.Head {
		return errors.New("checks changed the rebased head")
	}
	dirty, code, err = git("status", "--porcelain")
	if err != nil || code != 0 || strings.TrimSpace(dirty) != "" {
		return errors.New("checks left uncommitted changes")
	}
	if reviewGated(rule) {
		// Checks may take minutes; BLOCK on any equivalent patch wins.
		if e.ValidateReview != nil {
			if err := e.ValidateReview(parsedHead, reviewedPatch, base.Commit); err != nil {
				return err
			}
		}
		return reviewChecks[0](reviewedHead)
	}
	return nil
}
