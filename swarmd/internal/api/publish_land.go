package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environment"
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
		s.landQueue.SetParallel(s.landingParallel)
	}
	return s.landQueue, nil
}
func (c *controlMCPCall) enqueueLand(rule publish.Rule, req publish.Request, retryStopped bool, completedRunID ...string) (map[string]any, error) {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return nil, errors.New("principal required")
	}
	q, err := c.server.landingQueue()
	if err != nil {
		return nil, err
	}
	retryRunID := ""
	if len(completedRunID) > 0 {
		retryRunID = completedRunID[0]
	}
	entry, pos, err := q.EnqueueDelivery(publish.Entry{Account: principal.AccountScopeID, Request: req, RetryFollowupRunID: retryRunID}, retryStopped)
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
	sem := make(chan struct{}, p.server.landingLimit())
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
	c, err := p.server.pipelineCall(ctx)
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
	// Both automatic and explicit deliveries execute as the daemon owner.
	// internalCall installs that principal on its request, not on the original
	// caller context. Carry it through admission, preparation, checks and Git.
	ctx = c.request.Context()
	return q.Process(ctx, account, folder, publish.QueueHooks{
		Recover: func(ctx context.Context, e publish.Entry) (bool, error) {
			base, err := publish.Update(ctx, rule, e.Request.BaseBranch)
			if err != nil {
				return false, err
			}
			m := sandbox.Default()
			if m == nil || !m.Active() {
				return false, errors.New("landing requires an active sandbox")
			}
			scope, err := m.CommandScope(rule.Folder)
			if err != nil {
				return false, err
			}
			res, err := m.ExecDaemon(ctx, scope, sandbox.ExecRequest{Command: []string{"git", "merge-base", "--is-ancestor", e.Request.Head, base.Commit}, WorkingDir: rule.Folder, Timeout: time.Minute, MaxTimeout: time.Minute, MaxOutput: 4096})
			if err != nil {
				return false, err
			}
			if res.ExitCode > 1 {
				return false, errors.New("cannot reconcile landing target")
			}
			return res.ExitCode == 0, nil
		},
		Stack: func(ctx context.Context, e *publish.Entry, previous *publish.Entry) error {
			e.DeferChecks = true
			if previous != nil {
				e.StackBase = &previous.Request
			}
			return c.prepareLand(ctx, rule, e)
		},
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
			if e.RunChecks != nil {
				if err := e.RunChecks(); err != nil {
					return err
				}
			} else if err := c.prepareLand(ctx, rule, e); err != nil {
				return err
			}
			if ignoreLandProof(rule, e) {
				return nil
			}
			return c.verifyLandProof(ctx, rule, e)
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
			if e.State == "review_pending" {
				_, err := c.advancePipeline(a, b, refs, rule, map[string]any{"head": e.Request.Head, "base_commit": e.Request.BaseCommit})
				if err != nil {
					return err
				}
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
			payload := map[string]string{"head": e.Commit, "send_backs": fmt.Sprint(e.SendBacks), "notification_id": e.NotificationID}
			if e.Review != nil && e.Review.PatchIdentical {
				payload["review_acceptance"] = "patch-identical"
				payload["reviewed_head"], payload["rebased_head"] = e.Review.ReviewedHead, e.Review.RebasedHead
			}
			return c.signalPublishDurable(kind, a, b, refs, signal, severity, summary, payload)
		},
	})
}

// SetLandingMaxConcurrent configures startup-only box-wide check admission.
func (s *Server) SetLandingMaxConcurrent(limit int) error {
	if limit < 0 {
		return errors.New("landing_max_concurrent must be non-negative")
	}
	s.landQueueMu.Lock()
	defer s.landQueueMu.Unlock()
	if s.landingSlots != nil {
		return errors.New("landing admission already started")
	}
	s.landingMaxConcurrent = limit
	return nil
}

func (s *Server) landingLimit() int {
	s.landQueueMu.Lock()
	defer s.landQueueMu.Unlock()
	return s.landingLimitLocked()
}

func (s *Server) landingLimitLocked() int {
	if s.landingMaxConcurrent > 0 {
		return s.landingMaxConcurrent
	}
	return max(1, runtime.NumCPU()/4)
}

// admitLanding shares the run admission lock with drain and restart claims.
// Queue.Process retains the durable entry while waiting, and releases only after
// checks, Git and durable effects finish, before admitting the next entry.
func (s *Server) admitLanding(ctx context.Context, e publish.Entry) (func(), error) {
	manager := s.ExecutionCapacity()
	if manager == nil {
		return nil, errors.New("landing admission unavailable")
	}
	s.landQueueMu.Lock()
	if s.landingSlots == nil {
		s.landingSlots = make(chan struct{}, s.landingLimitLocked())
	}
	slots := s.landingSlots
	s.landQueueMu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: e.Account, SessionID: "landing:" + e.Request.JobID, RunID: "landing:" + e.Request.JobID})
	if err != nil {
		<-slots
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = lease.Release(); <-slots }) }, nil
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
	err = prepareLandWorktreeWithExecutors(ctx, rule, e, path, env.Definition.Checks, env.Definition.CheckTimeoutMinutes,
		func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			return m.ExecDaemon(ctx, scope, req)
		}, c.server.landingEnvironmentProvider, environment.Spec{Folder: rule.Folder, Checkout: path, Definition: environment.Definition{BaseImage: env.Definition.BaseImage, Setup: env.Definition.Setup, Env: env.Definition.Env}}, publish.Update, func(head string) error {
			return c.requireLandReview(kind, a, b, head)
		})
	if err != nil {
		return err
	}
	// Only serialized landing imports job objects into the folder. Fetch an
	// exact validated head, so a moving clone HEAD cannot change what is landed.
	res, err := m.ExecDaemon(ctx, scope, sandbox.ExecRequest{Command: []string{"git", "fetch", "--no-tags", "--", path, e.Request.Head}, WorkingDir: rule.Folder, Timeout: 2 * time.Minute, MaxTimeout: 2 * time.Minute, MaxOutput: 4096})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return errors.New("cannot fetch prepared job commit for landing")
	}
	return nil
}

func prepareLandWorktreeWithExecutors(ctx context.Context, rule publish.Rule, e *publish.Entry, path string, checks []string, checkTimeoutMinutes int,
	exec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error), environments environment.Provider, spec environment.Spec, update func(context.Context, publish.Rule, string) (publish.Base, error), reviewChecks ...func(string) error) (retErr error) {
	currentBranch, err := exec(ctx, sandbox.ExecRequest{Command: []string{"git", "branch", "--show-current"}, WorkingDir: path, Timeout: time.Minute, MaxTimeout: time.Minute, MaxOutput: 4096})
	if err != nil {
		return err
	}
	if currentBranch.ExitCode != 0 || strings.TrimSpace(currentBranch.Stdout) != e.Request.Branch {
		return errors.New("job branch changed before landing")
	}
	var base publish.Base
	if e.StackBase != nil {
		base = publish.Base{Commit: e.StackBase.Head, Branch: e.StackBase.BaseBranch}
	} else {
		base, err = update(ctx, rule, e.Request.BaseBranch)
	}
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
	// The updated base is in the folder, not in the worker's independent repo.
	if rule.Folder != "" {
		if _, code, err := git("fetch", "--no-tags", "--", rule.Folder, base.Commit); err != nil || code != 0 {
			return errors.New("cannot fetch landing base into job clone")
		}
	}
	dirty, code, err := git("status", "--porcelain")
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(dirty) != "" {
		return errors.New("job worktree is not clean")
	}
	logEquivalence := func(reason, oldBase, reviewedHead, rebasedHead string) {
		// Quote the reason to keep executor errors on a single diagnostic line.
		log.Printf("landing review equivalence: job=%s reason=%q oldBase=%s reviewedHead=%s newBase=%s rebasedHead=%s", e.Request.JobID, reason, oldBase, reviewedHead, base.Commit, rebasedHead)
	}
	var reviewedHead, reviewedPatch, oldBase string
	reviewBase := base.Commit
	if e.DeferChecks && e.Request.BaseCommit != "" {
		reviewBase = e.Request.BaseCommit
	}
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
			rejectEquivalence := func(reason string) error {
				logEquivalence(reason, e.Review.ReviewedBase, e.Review.ReviewedHead, current)
				return fmt.Errorf("%w (reason: %s)", reviewRequired(current), reason)
			}
			if current != e.Review.RebasedHead {
				return rejectEquivalence("rebased head mismatch")
			}
			patch, err := patchID(current, reviewBase)
			if err != nil {
				logEquivalence(fmt.Sprintf("cannot verify current patch identity: %v", err), e.Review.ReviewedBase, e.Review.ReviewedHead, current)
				return err
			}
			approvedBase := reviewBase
			if e.DeferChecks && e.Review.ReviewedBase != "" {
				approvedBase = e.Review.ReviewedBase
			}
			approvedPatch, err := patchID(e.Review.ReviewedHead, approvedBase)
			if err != nil {
				logEquivalence(fmt.Sprintf("cannot verify approved patch identity: %v", err), approvedBase, e.Review.ReviewedHead, current)
				return err
			}
			if patch != e.Review.PatchID {
				return rejectEquivalence("patch differs from recorded equivalent patch")
			}
			if approvedPatch != patch {
				return rejectEquivalence("patch differs from approved patch")
			}
			reviewedHead = e.Review.ReviewedHead
		}
		if err := reviewChecks[0](reviewedHead); err != nil {
			return err
		}
		reviewedPatch, err = patchID(current, reviewBase)
		if err != nil {
			return err
		}
		if e.Review == nil {
			e.Review = &publish.ReviewEquivalence{ReviewedHead: reviewedHead, RebasedHead: current, PatchID: reviewedPatch}
		}
		if e.ValidateReview != nil {
			if err := e.ValidateReview(current, reviewedPatch, reviewBase); err != nil {
				return err
			}
		}
		if e.Checkpoint != nil {
			if err := e.Checkpoint(); err != nil {
				return err
			}
		}
	}
	if reviewGated(rule) {
		raw, exit, readErr := git("merge-base", reviewedHead, reviewBase)
		if readErr != nil || exit != 0 {
			return errors.New("cannot read range-diff base")
		}
		oldBase, err = publish.ParseHeadOutput(raw)
		if err != nil {
			return err
		}
	}
	if e.DeferChecks && e.Request.BaseCommit != "" {
		_, code, err = git("rebase", "--onto", base.Commit, e.Request.BaseCommit)
	} else {
		_, code, err = git("rebase", base.Commit)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		// A conflicted attempt must never leave transferable approval behind.
		if e.Review != nil {
			logEquivalence("rebase conflicted", oldBase, reviewedHead, "unavailable")
		}
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
	deferReview := func(reason string) error {
		logEquivalence(reason, oldBase, reviewedHead, parsedHead)
		e.Request.Head, e.Request.BaseCommit, e.Request.BaseBranch = parsedHead, base.Commit, base.Branch
		e.Review = nil
		if !rule.Pipeline {
			return fmt.Errorf("%w (reason: %s)", reviewRequired(parsedHead), reason)
		}
		return &publish.RebaseReviewError{Reason: fmt.Sprintf("Rebase-scoped review: review only the conflict resolution and confirm the range-diff against the previously reviewed commits (%s..%s versus %s..%s) (reason: %s).", oldBase, reviewedHead, base.Commit, parsedHead, reason)}
	}
	if reviewGated(rule) && parsedHead != reviewedHead {
		// Compare ordered per-commit stable patch IDs, not range-diff's context
		// similarity heuristic or just the aggregate patch. Bound and fail closed
		// on truncated output; empty commits and merges require a fresh review.
		ids := func(base, head string) (string, error) {
			res, err := exec(ctx, sandbox.ExecRequest{Command: []string{"bash", "-o", "pipefail", "-c", `set -e; git rev-list --reverse "$1..$2" | while read -r commit; do git show --format= --no-ext-diff --no-textconv --binary "$commit" | git patch-id --stable | cut -d ' ' -f 1; echo end; done`, "patch-ids", base, head}, WorkingDir: path, Timeout: 2 * time.Minute, MaxTimeout: 2 * time.Minute, MaxOutput: 4096})
			if err != nil {
				return "", err
			}
			if res.ExitCode != 0 || len(res.Stdout) >= 4096 || strings.TrimSpace(res.Stdout) == "" {
				return "", errors.New("cannot verify commit patch identities")
			}
			fields := strings.Fields(res.Stdout)
			if len(fields)%2 != 0 {
				return "", errors.New("empty or unsupported commit patch")
			}
			for i := 0; i < len(fields); i += 2 {
				if _, err := publish.ParseHeadOutput(fields[i]); err != nil || fields[i+1] != "end" {
					return "", errors.New("invalid commit patch identity")
				}
			}
			return res.Stdout, nil
		}
		before, err := ids(oldBase, reviewedHead)
		if err != nil {
			return deferReview(fmt.Sprintf("cannot verify commit patch identities: %v", err))
		}
		after, err := ids(base.Commit, parsedHead)
		if err != nil {
			return deferReview(fmt.Sprintf("cannot verify commit patch identities: %v", err))
		}
		if before != after {
			return deferReview(fmt.Sprintf("commit patch IDs differ: before=%d commits, after=%d commits", len(strings.Fields(before))/2, len(strings.Fields(after))/2))
		}
	}
	if reviewGated(rule) && parsedHead != reviewedHead {
		patch, err := patchID(parsedHead, base.Commit)
		if err != nil {
			return deferReview(fmt.Sprintf("cannot verify aggregate patch identity: %v", err))
		}
		if patch != reviewedPatch {
			return deferReview("aggregate patch id differs")
		}
	}
	if e.DeferChecks {
		e.Request.Head, e.Request.BaseCommit, e.Request.BaseBranch = parsedHead, base.Commit, base.Branch
	}
	if reviewGated(rule) {
		e.Review.RebasedHead = parsedHead
		e.Review.ReviewedBase, e.Review.RebasedBase = oldBase, base.Commit
		e.Review.PatchIdentical = parsedHead != reviewedHead
		if e.Checkpoint != nil {
			if err := e.Checkpoint(); err != nil {
				return err
			}
		}
	}
	e.Request.Head = parsedHead
	e.Request.BaseCommit = base.Commit
	e.Request.BaseBranch = base.Branch
	runChecks := func() (retErr error) {
		e.Checks = nil
		if environments == nil {
			return errors.New("landing environment provider unavailable")
		}
		spec.Commit = parsedHead
		spec.BaseCommit = base.Commit
		spec.Account = e.Account
		spec.Checks = checks
		spec.CheckTimeout = 20 * time.Minute
		if checkTimeoutMinutes > 0 {
			spec.CheckTimeout = time.Duration(checkTimeoutMinutes) * time.Minute
		}
		e.EnvironmentNote = ""
		spec.Fallback = func(note string) { e.EnvironmentNote = environment.FallbackNote(note) }
		spec.Selected = func(name string) { e.Environment = name }
		checkEnvironment, err := environments.Acquire(ctx, spec)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, checkEnvironment.Release()) }()
		checkExec := checkEnvironment.Exec
		limit := 20 * time.Minute
		if checkTimeoutMinutes > 0 {
			limit = time.Duration(checkTimeoutMinutes) * time.Minute
		}
		// ExecRequest.Timeout is enforced by the supervisor after CPU admission.
		// Keep only caller cancellation on ctx: a command deadline here would also
		// charge queueing (and container acquisition) to the check's budget.
		for i, command := range checks {
			var output []byte
			req := sandbox.ExecRequest{Command: []string{"bash", "-lc", command}, WorkingDir: path, Timeout: limit, MaxTimeout: 60 * time.Minute, MaxOutput: publish.CheckOutputLimit, OnOutput: func(data []byte) {
				if len(data) >= publish.CheckOutputLimit {
					output = append(output[:0], data[len(data)-publish.CheckOutputLimit:]...)
					return
				}
				if overflow := len(output) + len(data) - publish.CheckOutputLimit; overflow > 0 {
					copy(output, output[overflow:])
					output = output[:len(output)-overflow]
				}
				output = append(output, data...)
			}}
			res, err := checkExec(ctx, req)
			var died *sandbox.ErrSandboxDied
			if errors.As(err, &died) && ctx.Err() == nil {
				output = output[:0]
				res, err = checkExec(ctx, req)
			}
			if errors.As(err, &died) {
				return fmt.Errorf("landing check infrastructure failure: %w", err)
			}
			excerpt := publish.CheckOutputExcerpt(string(output))
			if err != nil {
				if errors.Is(err, provider.ErrOperationTimedOut) && ctx.Err() == nil {
					e.Checks = append(e.Checks, publish.CheckResult{ExitCode: -1, Output: excerpt})
					return publish.ChecksSendBack(base, checks[:i+1], true, excerpt)
				}
				return err
			}
			e.Checks = append(e.Checks, publish.CheckResult{ExitCode: res.ExitCode, Output: excerpt, DurationMS: res.DurationMS})
			if res.ExitCode != 0 {
				return publish.ChecksSendBack(base, checks[:i+1], false, excerpt)
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
	if e.DeferChecks {
		e.RunChecks = runChecks
		return nil
	}
	return runChecks()
}

// SetLandingParallel configures the startup-only candidate window.
func (s *Server) SetLandingParallel(n int) error {
	if n < 1 {
		return errors.New("landing_parallel must be positive")
	}
	s.landQueueMu.Lock()
	defer s.landQueueMu.Unlock()
	if s.landQueue != nil {
		return errors.New("landing queue already started")
	}
	s.landingParallel = n
	return nil
}

// SetEnvironmentProvider configures landing execution at startup only.
func (s *Server) SetEnvironmentProvider(name string, waitSeconds ...int) error {
	if name != "local" && name != "runners" && name != "auto" && name != "gcp-cloudbuild" {
		return fmt.Errorf("invalid environment_provider %q (valid values: local, runners, auto, gcp-cloudbuild)", name)
	}
	wait := 120 * time.Second
	if len(waitSeconds) > 0 && waitSeconds[0] >= 0 {
		wait = time.Duration(waitSeconds[0]) * time.Second
	}
	s.landingEnvironmentProvider = landingProvider{server: s, name: name, wait: wait}
	return nil
}
