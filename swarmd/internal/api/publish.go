package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/outbox"
	"swarm/packages/swarmd/internal/publish"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Jobs in a folder whose owner set a publish command on the machine
// (`swarmctl workspace publish`) are delivered as pull requests: Swarm runs
// the owner's command with the job's branch and head, and records the pull
// request it prints, instead of merging into the folder. With `auto`, a job
// that finishes with commits is delivered without anyone calling
// swarm_deliver_job. Merging happens on the forge, under its own rules.

// SetPublishConfig names the owner's publish rules file (SWARM_PUBLISH_CONFIG).
func (s *Server) SetPublishConfig(path string) {
	s.publishConfig = strings.TrimSpace(path)
}

// publishRule is the publish rule of a folder, read fresh so the owner's
// change applies without a restart.
func (s *Server) publishRule(folder string) (publish.Rule, bool, error) {
	if s == nil || s.publishConfig == "" || strings.TrimSpace(folder) == "" {
		return publish.Rule{}, false, nil
	}
	load := s.loadPublishConfig
	if load == nil {
		load = publish.Load
	}
	rules, err := load(s.publishConfig)
	if err != nil {
		return publish.Rule{}, false, err
	}
	rule, ok := publish.For(rules, folder)
	return rule, ok, nil
}

// jobResult is the result part of a job's view, read as an AI receives it.
func controlMCPJobResultJSON(view map[string]any) map[string]any {
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil
	}
	var decoded map[string]any
	if json.Unmarshal(encoded, &decoded) != nil {
		return nil
	}
	return controlMCPMap(decoded["result"])
}

// publishJob opens or updates the job's pull request with the folder's
// command. It refuses a job without commits and records the pull request on
// the job's task and in the activity feed.
func (c *controlMCPCall) publishJob(kind, a, b string, refs controlMCPJobRefs, rule publish.Rule, result map[string]any, explicit bool) (map[string]any, error) {
	jobID := controlMCPJobID(kind, a, b)
	if result["delivery_state"] == "report_only" {
		return nil, errors.New("report-only jobs cannot be published")
	}
	if refs.run["role"] == "reviewer" {
		return nil, errors.New("reviewer jobs are report-only")
	}
	if reviewGated(rule) {
		if err := c.requireLandAdmission(kind, a, b, rule, result); err != nil {
			return map[string]any{"job_id": jobID, "delivery_state": "refused", "delivery_reason": err.Error()}, nil
		}
	}
	if rule.Mode == "land" {
		principal, ok := PrincipalFromRequest(c.request)
		if !ok {
			return nil, errors.New("principal required")
		}
		q, err := c.server.landingQueue()
		if err != nil {
			return nil, err
		}
		entry, pos, err := q.Get(principal.AccountScopeID, jobID)
		if err != nil {
			return nil, err
		}
		if entry.State == "landed" || entry.State == "landing" || entry.State == "queued" {
			return landDeliveryReceipt(entry, pos), nil
		}
		if !explicit && (entry.State == "sent_back" || entry.State == "landing_stopped") {
			if controlMCPString(refs.run, "followup_status") != "completed" || !entry.CanRetryFix(controlMCPString(result, "head")) {
				return landDeliveryReceipt(entry, pos), nil
			}
		}
	}
	if state, _ := result["delivery_state"].(string); state == "empty" {
		return nil, controlMCPToolFailure("job %s committed nothing: there is no pull request to open (its report is the result)", jobID)
	}
	branch, _ := result["worktree_branch"].(string)
	head, _ := result["head"].(string)
	base, _ := result["base_branch"].(string)
	if head == "" || branch == "" {
		return nil, controlMCPToolFailure("job %s has no branch and head commit yet; wait until it is ready for review (swarm_get_job)", jobID)
	}
	report, _ := result["report"].(string)
	title := ""
	if task, err := c.task(refs.projectID, refs.taskID); err == nil {
		title, _ = task["title"].(string)
	}
	if strings.TrimSpace(title) == "" {
		title = "Swarm job " + jobID
	}
	if rule.Mode == "land" {
		return c.enqueueLand(rule, publish.Request{Folder: rule.Folder, JobID: jobID, Branch: branch, Head: head, BaseBranch: base, Title: title, Body: report}, explicit)
	}
	res, err := publish.Run(c.request.Context(), rule, publish.Request{
		Folder: rule.Folder, JobID: jobID, Branch: branch, Head: head, BaseBranch: base, Title: title, Body: report,
	})
	if err != nil {
		c.signalPublish(kind, a, b, refs, "job.publish_failed", pebblestore.SignalSeverityWarning, "Opening the job's pull request failed: "+controlMCPTruncate(err.Error(), 200), map[string]string{"head": head})
		return nil, controlMCPToolFailure("%s", controlMCPTruncate(err.Error(), 400))
	}
	if principal, ok := PrincipalFromRequest(c.request); ok && c.server != nil && c.server.sessions != nil && c.server.sessions.Store() != nil {
		_, _ = updateProjectTaskWithRetry(c.server.sessions.Store(), principal.AccountScopeID, refs.projectID, refs.taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Integration = &pebblestore.ProjectTaskIntegration{State: "pull_request", SourceBranch: branch, SourceHead: head, TargetBranch: base, TargetWorkspacePath: rule.Folder, PullRequestURL: res.URL}
			return nil
		})
	}
	if err := c.signalPublishDurable(kind, a, b, refs, "job.pull_request", pebblestore.SignalSeverityInfo, "Job delivered as a pull request", map[string]string{"pull_request_url": res.URL, "head": head}); err != nil {
		return nil, err
	}
	return map[string]any{
		"job_id":       jobID,
		"pull_request": res.URL,
		"head":         head,
		"next":         "the folder publishes jobs as pull requests: the result is on the forge, merged by its own rules; a follow-up (swarm_message_job) and another delivery update the same pull request",
	}, nil
}

func (c *controlMCPCall) signalPublish(kind, a, b string, refs controlMCPJobRefs, signalKind, severity, summary string, attrs map[string]string) {
	if err := c.signalPublishDurable(kind, a, b, refs, signalKind, severity, summary, attrs); err != nil {
		log.Printf("swarmd signal %s not recorded: %v", signalKind, err)
	}
}

func (c *controlMCPCall) signalPublishDurable(kind, a, b string, refs controlMCPJobRefs, signalKind, severity, summary string, attrs map[string]string) error {
	if c.server == nil || c.server.signals == nil {
		return fmt.Errorf("publish signals unavailable")
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return fmt.Errorf("publish signal principal required")
	}
	if err := c.recordJobActivity(kind, a, b, refs, signalKind, attrs["head"], attrs["notification_id"]); err != nil {
		return err
	}
	sigRefs := map[string]string{"project_id": refs.projectID, "task_id": refs.taskID}
	if kind == controlMCPJobWorker {
		sigRefs["worker_id"], sigRefs["run_id"] = a, b
	}
	attrs["job_id"] = controlMCPJobID(kind, a, b)
	dedup := signalKind + ":" + attrs["job_id"] + ":" + attrs["head"] + ":" + attrs["send_backs"]
	if id := attrs["notification_id"]; id != "" {
		dedup = signalKind + ":" + attrs["job_id"] + ":transition:" + id
	}
	if _, err := c.server.signals.AppendOnce(pebblestore.Signal{
		Kind: signalKind, Severity: severity, Source: pebblestore.SignalSourceSwarmd, Account: principal.AccountScopeID,
		Summary: summary, DedupKey: dedup, Refs: sigRefs, Attrs: attrs,
	}); err != nil {
		return err
	}
	return nil
}

// AutoPublisher delivers finished jobs of folders with an auto publish rule.
type AutoPublisher struct {
	server       *Server
	feed         outbox.Feed
	state        *pebblestore.Store
	mu           sync.Mutex
	reviewCursor string
	attempts     map[uint64]int
}

const (
	autoPublishPendingKey = "publish_meta/pending"
	autoPublishCursorKey  = "publish_meta/cursor"
	autoPublishMaxRetries = 20
	autoPublishRetryDelay = 90 * time.Second // at most about 30 minutes of assessment waits
)

// NewAutoPublisher reads the machine's signal feed from the last delivered
// job onwards.
func (s *Server) NewAutoPublisher(feed outbox.Feed, state *pebblestore.Store) *AutoPublisher {
	return &AutoPublisher{server: s, feed: feed, state: state, attempts: map[uint64]int{}}
}

// Run delivers until ctx ends. It is idle on machines without publish rules.
func (p *AutoPublisher) Run(ctx context.Context) {
	// Landing can take minutes; never block ingestion of other finished jobs.
	landingDone := make(chan struct{})
	go func() {
		defer close(landingDone)
		for ctx.Err() == nil {
			if err := p.landPending(ctx); err != nil && ctx.Err() == nil {
				log.Printf("landing queue: %v", err)
			}
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	defer func() { <-landingDone }()
	for {
		if err := p.once(ctx); err != nil && ctx.Err() == nil {
			log.Printf("auto publish ingestion: %v", err)
		}
		// Retry deadlines belong to individual signals, not the feed.
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-p.feed.Appended():
			timer.Stop()
		}
	}
}

func (p *AutoPublisher) once(ctx context.Context) error {
	consumeErr := p.consume(ctx, p.deliver)
	var recoveryErr error
	if p.server.publishConfig != "" {
		recoveryErr = p.recoverReviews(ctx, p.deliver)
	}
	// New completion ingestion takes priority over stale recovery targets.
	return errors.Join(consumeErr, recoveryErr)
}

type autoPublishPending struct {
	Signal      pebblestore.Signal `json:"signal"`
	Attempts    int                `json:"attempts"`
	NextAttempt time.Time          `json:"next_attempt,omitempty"`
}

// consume advances only after admission or durable retry handoff. One broken
// job must not hold every later completion behind its backoff.
func (p *AutoPublisher) consume(ctx context.Context, deliver func(context.Context, pebblestore.Signal) error) error {
	if p.server.publishConfig == "" {
		return nil
	}
	var cursor uint64
	if _, err := p.state.GetJSON(autoPublishCursorKey, &cursor); err != nil {
		return err
	}
	if cursor == 0 {
		// Start at the present: jobs that ended before publishing was set up
		// are delivered by hand if wanted.
		page, err := p.feed.ListAfter(0, 1, pebblestore.SignalFilter{})
		if err != nil {
			return err
		}
		return p.state.PutJSON(autoPublishCursorKey, max(page.LatestSeq, 1))
	}
	var pending []autoPublishPending
	if _, err := p.state.GetJSON(autoPublishPendingKey, &pending); err != nil {
		return err
	}
	var failures []error
	attempt := func(sig pebblestore.Signal) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := deliver(ctx, sig)
		if err != nil {
			failures = append(failures, err)
		}
		return err
	}
	// Ingest new completions before retrying old work. Each durable retry has
	// its own deadline; unrelated feed activity must not exhaust its budget.
	retries := len(pending)
	account := p.server.OutboxAccount()
	retryPending := func() error {
		remaining := make([]autoPublishPending, 0, len(pending))
		for _, item := range pending[:retries] {
			if time.Now().Before(item.NextAttempt) {
				remaining = append(remaining, item)
				continue
			}
			if item.Signal.Account != account {
				remaining = append(remaining, item)
				continue
			}
			p.mu.Lock()
			p.attempts[item.Signal.Seq] = item.Attempts
			p.mu.Unlock()
			if err := attempt(item.Signal); err != nil {
				p.mu.Lock()
				item.Attempts = p.attempts[item.Signal.Seq]
				item.NextAttempt = time.Now().Add(autoPublishRetryDelay)
				p.mu.Unlock()
				remaining = append(remaining, item)
			}
		}
		pending = append(remaining, pending[retries:]...)
		return p.state.PutJSON(autoPublishPendingKey, pending)
	}
	for ctx.Err() == nil {
		page, err := p.feed.ListAfter(cursor, 100, pebblestore.SignalFilter{Account: account})
		if err != nil {
			return err
		}
		for _, sig := range page.Signals {
			if autoPublishSignal(sig.Kind, sig.Attrs["verdict"]) || (sig.Kind == "job.reviewed" && sig.Attrs["verdict"] == "block") {
				// A crash after retry handoff but before cursor persistence can
				// leave the same signal in both places. Do not duplicate it.
				deferred := false
				for _, item := range pending {
					if item.Signal.Seq == sig.Seq {
						deferred = true
						break
					}
				}
				if !deferred {
					if err := attempt(sig); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						p.mu.Lock()
						tries := p.attempts[sig.Seq]
						p.mu.Unlock()
						pending = append(pending, autoPublishPending{Signal: sig, Attempts: tries, NextAttempt: time.Now().Add(autoPublishRetryDelay)})
						if err := p.state.PutJSON(autoPublishPendingKey, pending); err != nil {
							return err
						}
					}
				}
			}
			cursor = sig.Seq
			if err := p.state.PutJSON(autoPublishCursorKey, cursor); err != nil {
				return err
			}
		}
		if page.NextAfter > cursor && len(page.Signals) == 0 {
			cursor = page.NextAfter
			if err := p.state.PutJSON(autoPublishCursorKey, cursor); err != nil {
				return err
			}
		}
		if len(page.Signals) < 100 {
			if err := retryPending(); err != nil {
				return err
			}
			return errors.Join(failures...)
		}
	}
	return ctx.Err()
}

const (
	autoPublishSkip = iota
	autoPublishWait
	autoPublishDeliver
)

type autoPublishRunState struct {
	active, recovering, settled, workerBlocked bool
}

// autoPublishNext is what a job asks of the auto publisher: skip it when it
// committed nothing (delivery empty, or settled with its head still at its
// base even when the delivery assessment could not read its lane), wait while
// it is not settled or its commits are not assessed yet, else deliver it.
func autoPublishNext(status string, result map[string]any, run *autoPublishRunState) int {
	if status == "blocked" {
		if run == nil || run.active || run.recovering || !run.settled || !run.workerBlocked {
			return autoPublishWait
		}
		return autoPublishSkip
	}
	if state, _ := result["delivery_state"].(string); state == "empty" || state == "report_only" {
		return autoPublishSkip
	}
	head, _ := result["head"].(string)
	base, _ := result["base_commit"].(string)
	if status == "needs_review" && head != "" && head == base {
		return autoPublishSkip
	}
	switch status {
	case "in_progress", "running":
		return autoPublishWait
	case "needs_review":
		if !controlMCPJobHasCommits(result) {
			return autoPublishWait
		}
		return autoPublishDeliver
	default:
		return autoPublishSkip
	}
}

// errAutoPublishWait asks for a retry: the job's commits are not assessed yet.
var errAutoPublishWait = errors.New("job not settled")

func (s *Server) pipelineCall(ctx context.Context) (*controlMCPCall, error) {
	return s.internalCall(ctx, "sessions:read", "sessions:write", "projects:read", "projects:write", "swarm:read", "agents:read", "automations:read", "automations:write", "signals:read")
}

func (p *AutoPublisher) logDecision(sig pebblestore.Signal, decision, reason string) {
	log.Printf("auto publish signal=%d job=%s %s reason=%q", sig.Seq,
		controlMCPJobID(controlMCPJobWorker, sig.Refs["worker_id"], sig.Refs["run_id"]),
		decision, controlMCPTruncate(reason, 200))
}

// skip acknowledges only this signal, never the job: later completion signals
// must read the current job again, including after a publisher restart.
func (p *AutoPublisher) skip(sig pebblestore.Signal, reason string) error {
	p.mu.Lock()
	delete(p.attempts, sig.Seq)
	p.mu.Unlock()
	p.logDecision(sig, "skip", reason)
	return nil
}

func (p *AutoPublisher) deliver(ctx context.Context, sig pebblestore.Signal) error {
	workerID, runID := sig.Refs["worker_id"], sig.Refs["run_id"]
	if !controlMCPSafeID.MatchString(workerID) || !controlMCPSafeID.MatchString(runID) {
		return p.skip(sig, "invalid worker or run id")
	}
	c, err := p.server.pipelineCall(ctx)
	if err != nil {
		return p.retry(sig, err) // owner initialization may still be in progress
	}
	view, err := c.jobView(controlMCPJobWorker, workerID, runID, 0, false, 0)
	if err != nil {
		return p.retry(sig, err)
	}
	result := controlMCPJobResultJSON(view)
	status, _ := view["status"].(string)
	if result["delivery_state"] == "report_only" {
		return p.skip(sig, "report-only job")
	}
	refs, err := c.jobRefs(controlMCPJobWorker, workerID, runID)
	if err != nil {
		return p.retry(sig, err)
	}
	run := &autoPublishRunState{}
	run.active, _ = view["run_active"].(bool)
	run.recovering = controlMCPString(view, "sandbox_cleanup_error") != "" || controlMCPString(refs.run, "sandbox_cleanup_error") != ""
	switch controlMCPString(refs.run, "status") {
	case "succeeded", "failed", "cancelled":
		run.settled = true
	}
	if status == "blocked" && refs.sessionID != "" {
		lifecycle, ok, err := p.server.sessions.GetLifecycle(refs.sessionID)
		if err != nil {
			return p.retry(sig, err)
		}
		// task_progress blocked preserves its requested phase and reason at
		// lifecycle finish. A snapshot without an end is not a final outcome.
		run.workerBlocked = ok && lifecycle.EndedAt > 0 && lifecycle.Phase == "blocked" && strings.TrimSpace(lifecycle.StopReason) != ""
		run.active = run.active || (ok && lifecycle.Active)
	}
	if autoPublishNext(status, result, run) == autoPublishSkip {
		if result["delivery_state"] == "unavailable" {
			return p.skip(sig, fmt.Sprintf("status %q: assessment unavailable (%s): %s", status, controlMCPString(result, "delivery_reason_code"), controlMCPString(result, "delivery_reason")))
		}
		return p.skip(sig, fmt.Sprintf("status %q: job is not deliverable", status))
	}
	if refs.run["role"] == "reviewer" {
		return p.skip(sig, "reviewer job")
	}
	rule, ok, err := p.server.publishRule(c.jobFolder(refs, result))
	if err != nil {
		return p.retry(sig, err)
	}
	if !ok || !rule.Auto {
		return p.skip(sig, "no automatic publish rule")
	}
	if sig.Kind == "job.reviewed" && !reviewGated(rule) {
		return p.skip(sig, "rule does not require review")
	}
	// Recovery waits must not be acknowledged as missing review: without a
	// final worker outcome this completion still needs a durable retry.
	if status == "blocked" && autoPublishNext(status, result, run) == autoPublishWait {
		return p.retry(sig, errAutoPublishWait)
	}
	if rule.Pipeline {
		// Follow-up may already have made the coder active when the process
		// dies before the receipt. Repair before the status gate can skip it.
		principal, _ := PrincipalFromRequest(c.request)
		var pending ReviewPipeline
		if _, err := p.state.GetJSON(pipelineKey(principal.AccountScopeID, controlMCPJobID(controlMCPJobWorker, workerID, runID)), &pending); err != nil {
			return err
		}
		if err := c.emitPipeline(workerID, runID, refs, pending); err != nil {
			return err
		}
		switch autoPublishNext(status, result, run) {
		case autoPublishSkip:
			return p.skip(sig, "job has no deliverable commits")
		case autoPublishWait:
			return p.retry(sig, errAutoPublishWait)
		}
		ready, err := c.advancePipeline(workerID, runID, refs, rule, result)
		if err != nil {
			var receiptErr pipelineReceiptError
			if errors.As(err, &receiptErr) {
				return err
			}
			principal, _ := PrincipalFromRequest(c.request)
			key := pipelineKey(principal.AccountScopeID, controlMCPJobID(controlMCPJobWorker, workerID, runID))
			var chain ReviewPipeline
			if _, readErr := p.state.GetJSON(key, &chain); readErr != nil {
				return readErr
			}
			chain.Head = controlMCPString(result, "head")
			chain.Next = "review_failed"
			chain.Findings = err.Error()
			if writeErr := p.state.PutJSON(key, chain); writeErr != nil {
				return writeErr
			}
			return c.emitPipeline(workerID, runID, refs, chain)
		}
		if !ready {
			return p.skip(sig, "review pipeline pending; awaiting review signal")
		}
	}
	// A missing review must not hold the signal cursor: a later durable
	// job.reviewed signal will retry delivery against the then-current head.
	if reviewGated(rule) {
		if err := c.requireLandAdmission(controlMCPJobWorker, workerID, runID, rule, result); err != nil {
			var missing *missingLandReview
			if errors.As(err, &missing) {
				return p.skip(sig, err.Error())
			}
			return p.retry(sig, err)
		}
	}
	switch autoPublishNext(status, result, run) {
	case autoPublishSkip:
		return p.skip(sig, "job has no deliverable commits")
	case autoPublishWait:
		return p.retry(sig, errAutoPublishWait)
	}
	if _, err := c.publishJob(controlMCPJobWorker, workerID, runID, refs, rule, result, false); err != nil {
		return p.retry(sig, err)
	}
	p.mu.Lock()
	delete(p.attempts, sig.Seq)
	p.mu.Unlock()
	p.logDecision(sig, "deliver", "job admitted for publishing")
	return nil
}

// retry defers a job until delivered or its bounded retry budget is exhausted.
func (p *AutoPublisher) retry(sig pebblestore.Signal, err error) error {
	p.mu.Lock()
	p.attempts[sig.Seq]++
	tries := p.attempts[sig.Seq]
	p.mu.Unlock()
	if tries < autoPublishMaxRetries {
		p.logDecision(sig, "wait", fmt.Sprintf("attempt %d: %v", tries, err))
		return fmt.Errorf("auto publish %s: %w", sig.Refs["run_id"], err)
	}
	p.logDecision(sig, "give up", err.Error())
	p.mu.Lock()
	delete(p.attempts, sig.Seq)
	p.mu.Unlock()
	if p.server.signals != nil {
		_, signalErr := p.server.signals.Append(pebblestore.Signal{
			Kind: "job.publish_failed", Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd, Account: sig.Account,
			Summary:  "Automatic job delivery gave up: " + controlMCPTruncate(err.Error(), 200),
			DedupKey: "job.publish_failed:" + sig.Refs["run_id"], Refs: sig.Refs,
			Attrs: map[string]string{"job_id": controlMCPJobID(controlMCPJobWorker, sig.Refs["worker_id"], sig.Refs["run_id"])},
		})
		if signalErr != nil {
			p.mu.Lock()
			p.attempts[sig.Seq] = tries
			p.mu.Unlock()
			return signalErr
		}
	}
	return nil
}

// internalCall is a Swarm Control call as the machine owner with the given
// scopes, through the same routes and checks an AI's call takes.
func (s *Server) internalCall(ctx context.Context, scopes ...string) (*controlMCPCall, error) {
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
		ID: "auto-publish", Name: "Swarm auto publish (internal)", Scopes: scopes,
		AccountScopeID: actor.AccountScopeID, UserID: actor.UserID,
	})
	return &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}, nil
}

// jobFolder is the registered folder a job ran in: its task's source
// workspace. The job's result names the worktree it worked in, which is not
// the folder a publish rule is set for.
func (c *controlMCPCall) jobFolder(refs controlMCPJobRefs, result map[string]any) string {
	if refs.projectID != "" && refs.taskID != "" {
		if task, err := c.task(refs.projectID, refs.taskID); err == nil {
			if path, _ := controlMCPMap(task["source_workspace"])["path"].(string); strings.TrimSpace(path) != "" {
				return path
			}
		}
	}
	path, _ := result["workspace_path"].(string)
	return path
}

// controlMCPJobHasCommits reports a job result with a head commit and at
// least one commit on its branch: enough to publish, whether or not Swarm's
// delivery assessment has settled.
func controlMCPJobHasCommits(result map[string]any) bool {
	head, _ := result["head"].(string)
	return strings.TrimSpace(head) != "" && len(controlMCPList(result["commits"])) > 0
}
