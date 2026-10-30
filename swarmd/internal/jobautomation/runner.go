package jobautomation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/providerlimit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// RequestSourceAutomation is the request source of a job an agent step
// started. Such a job never triggers automations itself.
const RequestSourceAutomation = "automation"

const (
	pageSize         = 100
	idleTick         = 30 * time.Second
	maxScanBackoff   = 5 * time.Minute
	maxResultRetries = 30
	maxConcurrent    = 4
)

// Feed is the part of the signal store the consumer reads.
type Feed interface {
	ListAfter(after uint64, limit int, filter pebblestore.SignalFilter) (pebblestore.SignalPage, error)
	Changed() <-chan struct{}
}

// ResultSource builds the result of the job end a signal reports. It returns
// nil for a signal without a result (the job was deleted), and ErrNotReady
// while the result has not settled; lastAttempt asks for what there is.
type ResultSource interface {
	Result(ctx context.Context, sig pebblestore.Signal, lastAttempt bool) (*Result, error)
}

// RunRequest is a run step: the owner's script, run on the host in Folder.
type RunRequest struct {
	Folder   string
	Command  string
	Args     []string
	Env      map[string]string
	Stdin    []byte
	Timeout  time.Duration
	OnOutput func([]byte)
}

// RunOutcome is how a run step's script exited and what it printed (stdout
// and stderr, in order).
type RunOutcome struct {
	ExitCode int
	Output   []byte
}

// RunExecutor runs a run step's script (HostExecutor in the daemon).
type RunExecutor interface {
	Run(ctx context.Context, req RunRequest) (RunOutcome, error)
}

// RefusedError is a step Swarm refused to run (a script that is not owned
// by root, or that group or others can write): nothing ran.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

// AgentRequest is an agent step's job.
type AgentRequest struct {
	Account string
	Folder  string
	Worker  string
	Brief   string
	Result  Result
	// Head is the delivery head: the triggering job's head, moved forward by
	// an earlier agent step that committed on top of it. A coder step starts
	// its worktree there.
	Head           string
	IdempotencyKey string
	// Fixes is the step's fixes option (FixesSecurity or ""): a reviewer
	// started with it may commit small security fixes on a writable branch at
	// the reviewed head.
	Fixes string
	// Checkout is where the agent works: the step's checkout or "" for the
	// agent's default (CheckoutFor once its role is known).
	Checkout string
	// ReviewJob is the job a reviewer reviews: the newest earlier step's job
	// that produced the result or moved the head, "" for the triggering job.
	ReviewJob string
	// Produce is the workflow's result the agent must write (a produce
	// step), nil otherwise.
	Produce *ResultSpec
	// Deliverables is what the delivery can hand over so far.
	Deliverables []Deliverable
}

// AgentOutcome is how the job an agent step started ended. Verdict and
// Findings are set when that job recorded a review (a reviewer job's verdict
// on the job it reviewed); "" otherwise. Head is the job's own head when it
// committed. ReviewUnreadable is set, with Verdict block and no Findings,
// when the recorded review is the parser's malformed-report record (no
// verdict could be read from the reviewer's report).
type AgentOutcome struct {
	JobID    string
	Status   string
	Error    string // The agent's declared blocker or execution failure reason.
	Result   *Result
	Verdict  string
	Findings string
	// ReviewUnreadable is true when the job's review record carries the
	// malformed-review reason (no verdict could be parsed). The verdict is
	// then block with no findings, and a reopen step must not send it.
	ReviewUnreadable bool
	Head             string
	// ResultError is why the job's result (or, for a reviewer allowed to
	// fix, its own checkout) could not be read; "" when it was read. A
	// reviewer allowed to fix fails closed on it with an OK verdict.
	ResultError string
	// LimitUntil (unix ms) is set when the job has not ended but waits for
	// its provider's limit to reset and reruns itself then (a worker run in
	// status waiting); Error is what it waits for. The step waits for the
	// same job until then instead of holding its timeout.
	LimitUntil int64
}

// AgentRunner starts a job for an agent step and waits for it to end.
type AgentRunner interface {
	Start(ctx context.Context, req AgentRequest) (jobID string, err error)
	Wait(ctx context.Context, account, jobID string) (AgentOutcome, error)
}

// ReopenRequest is a reopen step's follow-up into a job's own session.
type ReopenRequest struct {
	Account string
	JobID   string
	Message string
	// RequestID makes the send idempotent across a restart.
	RequestID string
}

// Reopener sends a follow-up into a job's own session exactly as
// swarm_message_job does and returns the follow-up's run id once it is
// accepted. It refuses (RefusedError) a job that is cancelled, and answers
// busy (an error whose Busy reports true, such as BusyError) while the job's
// run or follow-up is still in flight: a job takes one follow-up at a time.
type Reopener interface {
	Reopen(ctx context.Context, req ReopenRequest) (followup string, err error)
}

// ReopenSent is a QueueingReopener's answer once the follow-up is accepted:
// the run that carries it, and whether it was queued for a run already in
// flight (Queued) rather than starting one of its own. A queued message
// reaches the running agent as the person's message with its next tool
// result, or starts the next turn when that run ends without one; either
// way that run's end is the job's next end.
type ReopenSent struct {
	Followup string
	Queued   bool
}

// QueueingReopener is a Reopener that, for a job whose run is in flight,
// queues the follow-up for that run the way the message path does
// (swarm_send_message, message_task) instead of answering busy. It still
// refuses (RefusedError) a job that must not be reopened, and answers busy
// only while no run can take the message yet.
type QueueingReopener interface {
	ReopenOrQueue(ctx context.Context, req ReopenRequest) (ReopenSent, error)
}

// BusyError is a Reopener's answer for a job whose run or follow-up is still
// running: nothing was sent, and the reopen step waits and asks again.
type BusyError struct{ Reason string }

func (e *BusyError) Error() string { return e.Reason }

// Busy reports that the job is still running.
func (e *BusyError) Busy() bool { return true }

// reopenRecheck is the longest a waiting reopen step goes without asking its
// job again; any new signal (a job end among them) wakes it sooner.
const reopenRecheck = 2 * time.Second

// ReopenClosing is the text a reopen step's follow-up ends with.
const ReopenClosing = "Fix these in this worktree, commit, and report what you changed."

// HeadRef is a commit and the checkout it can be fetched from.
type HeadRef struct {
	Commit   string
	Worktree string
}

// HeadVerifier reports, with Git in the folder, whether head descends from
// ancestor (ancestor is head or one of its ancestors).
type HeadVerifier func(ctx context.Context, folder string, ancestor, head HeadRef) (bool, error)

// HeadImporter brings a job's head into the folder as refs/swarm/jobs/<run>
// and returns the ref.
type HeadImporter func(ctx context.Context, folder string, result Result) (string, error)

// LineCounter reports, with Git in the folder, how many lines the commits
// from base to head change in total (added plus removed). A binary change
// counts as more than MaxReviewerFixLines.
type LineCounter func(ctx context.Context, folder string, base, head HeadRef) (int, error)

// Runner is the durable consumer: it reads job ends from the signal feed,
// records a delivery for each matching automation and runs its steps.
type Runner struct {
	store   *Store
	feed    Feed
	account func() string
	results ResultSource
	run     RunExecutor
	agent   AgentRunner
	reopen  Reopener
	head    HeadImporter
	verify  HeadVerifier
	lines   LineCounter
	deliver Deliverer
	emit    func(pebblestore.Signal)
	now     func() time.Time
	backoff func(attempt int) time.Duration
	// recheck is reopenRecheck (a field so a test can shorten it).
	recheck time.Duration

	// reopenMu spans a reopen's send and its durable record, and the
	// consumer's delivery creation, so a follow-up's end is never recorded
	// before the reopen that started it.
	reopenMu sync.Mutex
	mu       sync.Mutex
	active   map[string]bool
	attempts map[uint64]int
	failures int
	wake     chan struct{}
}

// Options wires a Runner.
type Options struct {
	Store   *Store
	Feed    Feed
	Account func() string
	Results ResultSource
	Run     RunExecutor
	Agent   AgentRunner
	Reopen  Reopener
	Head    HeadImporter
	Verify  HeadVerifier
	Lines   LineCounter
	// Deliver hands deliverables over (deliver steps) and finds what
	// produce steps wrote.
	Deliver Deliverer
	Emit    func(pebblestore.Signal)
}

func NewRunner(opts Options) *Runner {
	return &Runner{
		store: opts.Store, feed: opts.Feed, account: opts.Account, results: opts.Results, run: opts.Run, agent: opts.Agent, reopen: opts.Reopen,
		head: opts.Head, verify: opts.Verify, lines: opts.Lines, deliver: opts.Deliver, emit: opts.Emit, now: time.Now, backoff: defaultBackoff, recheck: reopenRecheck,
		active: map[string]bool{}, attempts: map[uint64]int{}, wake: make(chan struct{}, 1),
	}
}

func defaultBackoff(attempt int) time.Duration {
	return min(time.Duration(1<<min(attempt-1, 6))*15*time.Second, 10*time.Minute)
}

// Store is the runner's store.
func (r *Runner) Store() *Store { return r.store }

// Wake asks the runner to look for work now (a re-run, the stop switch off).
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run consumes the feed and runs deliveries until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	for {
		wait := idleTick
		if err := r.Scan(ctx); err != nil && ctx.Err() == nil {
			r.mu.Lock()
			r.failures++
			wait = min(time.Duration(1<<min(r.failures, 8))*5*time.Second, maxScanBackoff)
			r.mu.Unlock()
			if !errors.Is(err, ErrNotReady) {
				log.Printf("swarmd job automations: %v", err)
			}
		} else {
			r.mu.Lock()
			r.failures = 0
			r.mu.Unlock()
		}
		if due := r.dispatch(ctx, false); due > 0 && due < wait {
			wait = due
		}
		var changed <-chan struct{}
		if r.feed != nil {
			changed = r.feed.Changed()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-changed:
			timer.Stop()
		case <-r.wake:
			timer.Stop()
		}
	}
}

// ProcessOnce reads the feed once and runs every delivery that can run now
// to its end, in order, in the caller's goroutine.
func (r *Runner) ProcessOnce(ctx context.Context) error {
	err := r.Scan(ctx)
	r.dispatch(ctx, true)
	return err
}

// ---- consumer --------------------------------------------------------------

// jobEndKinds are the signals that end a job. task.run.* is a run of a task
// one of Swarm's own agents worked (its first run, or a follow-up in its
// session).
var jobEndKinds = map[string]string{
	"task.run.succeeded": KindTask, "task.run.failed": KindTask, "task.run.cancelled": KindTask,
	"worker.run.succeeded": KindWorker, "worker.run.failed": KindWorker, "worker.run.cancelled": KindWorker,
	"media.job.succeeded": KindMedia, "media.job.failed": KindMedia, "media.job.cancelled": KindMedia,
	"design.job.completed": KindDesign, "design.job.failed": KindDesign, "design.job.cancelled": KindDesign,
}

// Scan records deliveries for the job ends after the cursor. A first run
// starts at the feed's end: an automation never runs for jobs that ended
// before automations existed on the machine.
func (r *Runner) Scan(ctx context.Context) error {
	if r.feed == nil || r.store == nil {
		return nil
	}
	account := ""
	if r.account != nil {
		account = r.account()
	}
	if account == "" {
		return nil
	}
	filter := pebblestore.SignalFilter{Account: account, KindPrefixes: []string{"task.run", "worker.run", "media.job", "design.job"}}
	for ctx.Err() == nil {
		cursor, found, err := r.store.Cursor()
		if err != nil {
			return fmt.Errorf("read cursor: %w", err)
		}
		if !found {
			page, err := r.feed.ListAfter(0, 1, filter)
			if err != nil {
				return fmt.Errorf("read feed: %w", err)
			}
			return r.store.SetCursor(page.LatestSeq)
		}
		page, err := r.feed.ListAfter(cursor, pageSize, filter)
		if err != nil {
			return fmt.Errorf("read feed: %w", err)
		}
		for _, sig := range page.Signals {
			if err := r.consume(ctx, account, sig); err != nil {
				return err
			}
			if err := r.store.SetCursor(sig.Seq); err != nil {
				return err
			}
		}
		if len(page.Signals) == 0 && page.NextAfter > cursor {
			if err := r.store.SetCursor(page.NextAfter); err != nil {
				return err
			}
		}
		if len(page.Signals) < pageSize || page.NextAfter <= cursor {
			return nil
		}
	}
	return ctx.Err()
}

// followupOf is the follow-up a worker.run or task.run signal reports (its
// dedup key is worker.run:<run>:<follow-up run> or task.run:<task>:<follow-up
// run>); "" for the job's first run.
// SignalFollowup is followupOf for the daemon's result builders.
func SignalFollowup(sig pebblestore.Signal) string { return followupOf(sig) }

func followupOf(sig pebblestore.Signal) string {
	if !strings.HasPrefix(sig.Kind, "worker.run.") && !strings.HasPrefix(sig.Kind, "task.run.") {
		return ""
	}
	parts := strings.SplitN(sig.DedupKey, ":", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return ""
}

func (r *Runner) consume(ctx context.Context, account string, sig pebblestore.Signal) (err error) {
	if _, ok := jobEndKinds[sig.Kind]; !ok {
		return nil
	}
	// Loop guard: a job an agent step started never triggers automations.
	if sig.Attrs["source"] == RequestSourceAutomation {
		return nil
	}
	// A task job end records its evaluation, the deliveries it matched or
	// none, whichever way it ends here (no workflow, no result, no match),
	// so its progress tells "none" from "not evaluated yet". An error is
	// retried, so it records nothing.
	matched := []string{}
	if jobID := sig.Attrs["job_id"]; strings.HasPrefix(sig.Kind, "task.run.") && IsTaskJobID(jobID) {
		defer func() {
			if err == nil {
				err = r.store.RecordEvaluation(account, sig.Refs["project_id"], jobID, followupOf(sig), sig.Seq, sig.At, matched)
			}
		}()
	}
	autos, err := r.store.List(account)
	if err != nil {
		return err
	}
	candidates := []Automation{}
	for _, a := range autos {
		// An automation applies to job ends after it was created.
		if a.Enabled && a.CreatedAt <= sig.At {
			candidates = append(candidates, a)
		}
	}
	if r.results == nil || (len(candidates) == 0 && !pebblestore.IsWorkflowItemSource(sig.Attrs["source"])) {
		return nil
	}
	r.mu.Lock()
	last := r.attempts[sig.Seq] >= maxResultRetries-1
	r.mu.Unlock()
	result, err := r.results.Result(ctx, sig, last)
	if err != nil {
		r.mu.Lock()
		r.attempts[sig.Seq]++
		tries := r.attempts[sig.Seq]
		r.mu.Unlock()
		if tries < maxResultRetries {
			return fmt.Errorf("build result for signal %d: %w", sig.Seq, err)
		}
		log.Printf("swarmd job automations: no result for signal %d after %d tries: %v", sig.Seq, tries, err)
		return nil
	}
	r.mu.Lock()
	delete(r.attempts, sig.Seq)
	r.mu.Unlock()
	if result == nil || result.RequestSource == RequestSourceAutomation {
		return nil
	}
	result.Followup = followupOf(sig)
	// A follow-up's end is when its signal was recorded: the run's own
	// completion time is its first end.
	if result.FinishedAt == 0 || (result.Followup != "" && sig.At > 0) {
		result.FinishedAt = sig.At
	}
	r.reopenMu.Lock()
	defer r.reopenMu.Unlock()
	triggerOwnsSteps := false
	if result.TriggerID != "" {
		origin, err := r.store.Get(account, result.TriggerID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		triggerOwnsSteps = len(origin.Steps) > 0
	}
	for _, a := range candidates {
		if a.On == nil && triggerOwnsSteps {
			continue
		}
		if !a.Matches(result) {
			// A blocked response to this workflow's own reopen must close
			// its round even when the workflow normally matches successes.
			match := *result
			match.Status = StatusSucceeded
			if result.Followup == "" || !result.EndedBlocked() || !a.Matches(&match) {
				continue
			}
			previous, err := r.store.JobDeliveries(account, result.JobID)
			if err != nil {
				return err
			}
			reopened := false
			for _, p := range previous {
				for _, step := range p.Steps {
					if p.AutomationID == a.ID && step.Type == StepReopen && step.Outcome["followup"] == result.Followup {
						reopened = true
					}
				}
			}
			if !reopened {
				continue
			}
		}
		d, created, err := r.store.CreateDelivery(a, *result)
		if err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		matched = append(matched, d.ID)
		if created && d.State == StateBlocked {
			// Terminal-at-creation deliveries are not dispatched; still publish
			// their blocker and persist the normal completion timestamp.
			r.finish(&d, StateBlocked)
		}
		if !created && d.ID != DeliveryID(result.JobID, result.Followup, a.ID, a.Revision) {
			r.skippedDuplicateHead(d, *result)
		}
	}
	if pebblestore.IsWorkflowItemSource(result.RequestSource) {
		if err := r.store.db.PutJSON(itemEndKey(account, result.JobID), result); err != nil {
			return err
		}
		// A follow-up's success reopens the item, and the items that failed
		// only waiting on it, even after the owning delivery finished.
		if itemEndSucceeded(*result) {
			return r.store.ReviveItems(account, result.RequestSource, result.JobID)
		}
	}
	return nil
}

// skippedDuplicateHead records why a job end created no delivery: the
// automation's delivery existing for the same job and head covers it.
func (r *Runner) skippedDuplicateHead(existing Delivery, result Result) {
	head := existing.TriggerHead()
	log.Printf("swarmd job automations: %s: job %s end %q at head %s creates no delivery (%s): delivery %s (%s) covers that head",
		existing.AutomationName, result.JobID, result.Followup, head, ReasonDuplicateHead, existing.ID, existing.State)
	if r.emit == nil {
		return
	}
	r.emit(pebblestore.Signal{Kind: "automation.delivery.skipped", Severity: pebblestore.SignalSeverityInfo, Account: existing.Account,
		Summary:  truncate(fmt.Sprintf("Automation %s: job %s ended again at head %s, which delivery %s (%s) already covers; no new delivery", existing.AutomationName, result.JobID, head, existing.ID, existing.State), 400),
		DedupKey: "automation.delivery.skipped:" + existing.AutomationID + ":" + result.JobID + ":" + result.Followup,
		Refs:     map[string]string{"job_id": result.JobID, "delivery_id": existing.ID},
		Attrs: map[string]string{"automation": existing.AutomationName, "reason": ReasonDuplicateHead, "head": head, "followup": result.Followup,
			"status": result.Status, "delivery_state": existing.State}})
}

// ---- deliveries ------------------------------------------------------------

// dispatch starts every unfinished delivery that can run now and returns how
// long until the next one waiting on a retry is due (0: none). sync runs
// them in the caller's goroutine.
func (r *Runner) dispatch(ctx context.Context, sync bool) time.Duration {
	if r.store == nil {
		return 0
	}
	list, err := r.store.Unfinished()
	if err != nil {
		log.Printf("swarmd job automations: list deliveries: %v", err)
		return 0
	}
	now := r.now().UnixMilli()
	var due time.Duration
	running := 0
	r.mu.Lock()
	for id := range r.active {
		if r.active[id] {
			running++
		}
	}
	r.mu.Unlock()
	for _, d := range list {
		if d.ItemsReopened != "" {
			continue // its steps finished; only its revived items run
		}
		if wait := nextAttemptWait(d, now); wait > 0 {
			if due == 0 || wait < due {
				due = wait
			}
			continue
		}
		if sync {
			r.runDelivery(ctx, d.Account, d.ID)
			continue
		}
		if running >= maxConcurrent {
			break
		}
		r.mu.Lock()
		busy := r.active[d.ID]
		if !busy {
			r.active[d.ID] = true
			running++
		}
		r.mu.Unlock()
		if busy {
			continue
		}
		go func(account, id string) {
			defer func() {
				r.mu.Lock()
				delete(r.active, id)
				r.mu.Unlock()
				r.Wake()
			}()
			r.runDelivery(ctx, account, id)
		}(d.Account, d.ID)
	}
	return due
}

// nextAttemptWait is how long a delivery waits for its next retry.
func nextAttemptWait(d Delivery, now int64) time.Duration {
	for _, step := range d.Steps {
		if Terminal(step.State) {
			continue
		}
		if (step.State == StatePending || step.State == StateWaiting) && step.NextAttemptAt > now {
			return time.Duration(step.NextAttemptAt-now) * time.Millisecond
		}
		return 0
	}
	return 0
}

// runDelivery runs a delivery's steps in order from the first that is not
// finished, until it ends, waits for a retry or the context ends.
func (r *Runner) runDelivery(ctx context.Context, account, id string) {
	d, err := r.store.GetDelivery(account, id)
	if err != nil || Terminal(d.State) || d.ItemsReopened != "" {
		return
	}
	if d.State == StatePending {
		d.State = StateRunning
		if d.StartedAt == 0 {
			d.StartedAt = r.now().UnixMilli()
		}
		if err := r.store.SaveDelivery(d); err != nil {
			log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
			return
		}
	}
	for i := range d.Steps {
		if ctx.Err() != nil {
			return
		}
		record := &d.Steps[i]
		if Terminal(record.State) {
			continue
		}
		if (record.State == StatePending || record.State == StateWaiting) && record.NextAttemptAt > r.now().UnixMilli() {
			return // waits for its retry or the provider's limit to reset
		}
		stop, err := r.store.StopState()
		if err != nil {
			log.Printf("swarmd job automations: read stop switch: %v", err)
			return
		}
		if stop.Stopped {
			record.State, record.Reason, record.Next, record.FinishedAt = StateStopped, "the automation stop switch is on: no new step starts", "rerun", r.now().UnixMilli()
			r.skipRest(&d, i, "not run: the automation stop switch is on")
			r.finish(&d, StateStopped)
			return
		}
		step, ok := d.step(record.ID)
		if !ok {
			record.State, record.Reason = StateFailed, "the step is missing from the delivery's definition"
			r.finish(&d, StateFailed)
			return
		}
		if held, why := conditionsHold(d, step.If); !held {
			record.State, record.Reason, record.FinishedAt = StateSkipped, why, r.now().UnixMilli()
			if err := r.store.SaveDelivery(d); err != nil {
				log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
			}
			continue
		}
		if r.skipEmptyReview(ctx, d, step, *record) {
			record.State, record.Reason, record.FinishedAt = StateSkipped, "the job made no commits: nothing to review", r.now().UnixMilli()
			if err := r.store.SaveDelivery(d); err != nil {
				log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
				return
			}
			continue
		}
		ok = r.runStep(ctx, &d, i, step)
		if ctx.Err() != nil {
			return
		}
		record = &d.Steps[i]
		if record.State == StatePending || (record.State == StateWaiting && record.NextAttemptAt > 0) {
			return // waits for its retry or the provider's limit to reset
		}
		if !ok && d.BlockedReason != "" {
			r.skipRest(&d, i, "not run: the delivery is blocked")
			r.finish(&d, StateBlocked)
			return
		}
		if !ok && !step.ContinueOnFailure {
			r.skipRest(&d, i, "not run: an earlier step failed")
			r.finish(&d, StateFailed)
			return
		}
	}
	state := StateSucceeded
	for _, step := range d.Steps {
		if step.State == StateFailed {
			state = StateFailed
		}
	}
	// A reopen step that sent the work back means nothing was handed over:
	// the delivery ends sent_back, never succeeded, and the follow-up's end
	// starts a fresh delivery. This includes a step allowed to fail
	// (continue_on_failure, a push) whose failure the reopen sent back.
	if sent := sentBack(d); sent != nil {
		d.SentBack = sent
		state = StateSentBack
	}
	r.finish(&d, state)
}

// sentBack is how the delivery sent its work back: the last reopen step that
// sent a follow-up, its round and the conditions that made it run. nil when
// no reopen step sent one.
func sentBack(d Delivery) *SentBack {
	var out *SentBack
	for _, rec := range d.Steps {
		if rec.Type != StepReopen || rec.State != StateSucceeded {
			continue
		}
		if followup, _ := rec.Outcome["followup"].(string); followup == "" {
			continue
		}
		round := outcomeInt(rec.Outcome["round"])
		if round == 0 {
			round = d.Round + 1
		}
		step, _ := d.step(rec.ID)
		out = &SentBack{Step: rec.ID, Round: round, Reason: sentBackReason(d, step)}
	}
	return out
}

// sentBackReason describes the conditions a reopen step ran on: a verdict
// ("review verdict block") or a failed step with its reason ("push failed:
// exited with code 1"). Without conditions it is the from step's verdict,
// when one was recorded.
func sentBackReason(d Delivery, step Step) string {
	record := func(id string) *StepRecord {
		for i := range d.Steps {
			if d.Steps[i].ID == id {
				return &d.Steps[i]
			}
		}
		return nil
	}
	var parts []string
	for _, c := range step.If {
		rec := record(c.Step)
		if rec == nil {
			continue
		}
		switch {
		case c.Verdict != "":
			parts = append(parts, c.Step+" verdict "+c.Verdict)
		case c.Status == StateFailed:
			why := c.Step + " failed"
			if rec.Reason != "" {
				why += ": " + rec.Reason
			}
			parts = append(parts, why)
		}
	}
	if len(parts) == 0 && step.From != "" {
		if rec := record(step.From); rec != nil {
			if verdict, _ := rec.Outcome["verdict"].(string); verdict != "" {
				parts = append(parts, step.From+" verdict "+verdict)
			}
		}
	}
	return truncate(strings.Join(parts, "; "), 300)
}

// ReopenCause is why the delivery's reopen step stepID sends (or sent) the
// work back, from the conditions it runs on: push_failed when one is a failed
// step after an agent step that passed (the review passed, the push failed:
// rebase and retry), check_failed when it is any other failed step, else
// review_block (a review verdict, or no failed-step condition). failure is
// the failed step and its reason ("push failed: exited with code 1: …"),
// uncapped; "" for a review block. It is the one classification every task
// progress surface reads (pebblestore.ProjectTaskReopen names it).
func ReopenCause(d Delivery, stepID string) (cause, failure string) {
	step, ok := d.step(stepID)
	if !ok {
		return pebblestore.ProjectTaskReopenReviewBlock, ""
	}
	index := func(id string) int {
		for i := range d.Steps {
			if d.Steps[i].ID == id {
				return i
			}
		}
		return -1
	}
	for _, c := range step.If {
		if c.Status != StateFailed {
			continue
		}
		at := index(c.Step)
		if at < 0 || (d.Steps[at].State != StateFailed && d.Steps[at].State != StateStopped) {
			continue
		}
		rec := d.Steps[at]
		failure = c.Step + " failed"
		if reason := strings.TrimSpace(rec.Reason); reason != "" {
			failure += ": " + reason
		}
		for i := at - 1; i >= 0; i-- {
			prior := d.Steps[i]
			if prior.Type != StepAgent || prior.State != StateSucceeded {
				continue
			}
			if verdict, _ := prior.Outcome["verdict"].(string); verdict != VerdictBlock {
				return pebblestore.ProjectTaskReopenPushFailed, failure
			}
			break
		}
		return pebblestore.ProjectTaskReopenCheckFailed, failure
	}
	return pebblestore.ProjectTaskReopenReviewBlock, ""
}

// outcomeInt reads a number from a step outcome, as recorded (int) or as
// read back from the store (float64).
func outcomeInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// skipEmptyReview never infers emptiness from missing Git. Earlier produced
// work has its own review target, so the triggering job's signal cannot skip it.
// A previously started job is resumed, not retroactively skipped.
func (r *Runner) skipEmptyReview(ctx context.Context, d Delivery, step Step, record StepRecord) bool {
	if step.Type != StepAgent || !d.Result.NoCommits || d.Result.Git != nil || d.Head != "" || latestWorkJob(d, step.ID) != "" || record.Attempts != 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(step.Worker), "reviewer") {
		return true
	}
	if roles, ok := r.agent.(interface {
		Role(context.Context, string, string) (string, error)
	}); ok {
		role, err := roles.Role(ctx, d.Account, step.Worker)
		return err == nil && role == "reviewer"
	}
	return false // unresolved roles retain the normal dispatch/error path
}

// conditionsHold reports whether all of a step's conditions hold, and if not,
// why it is skipped. A step that did not run (skipped, stopped) matches no
// status; a missing verdict never matches a verdict condition.
func conditionsHold(d Delivery, conds []Condition) (bool, string) {
	for _, c := range conds {
		var rec *StepRecord
		for i := range d.Steps {
			if d.Steps[i].ID == c.Step {
				rec = &d.Steps[i]
			}
		}
		if rec == nil {
			return false, "condition not met: step " + c.Step + " is not in the delivery"
		}
		if c.Status != "" && rec.State != c.Status {
			return false, "condition not met: step " + c.Step + " is " + rec.State + ", not " + c.Status
		}
		if c.Verdict != "" {
			got, _ := rec.Outcome["verdict"].(string)
			if got == "" {
				return false, "condition not met: step " + c.Step + " recorded no verdict"
			}
			if got != c.Verdict {
				return false, "condition not met: step " + c.Step + " verdict is " + got + ", not " + c.Verdict
			}
		}
	}
	return true, ""
}

func (d Delivery) step(id string) (Step, bool) {
	for _, step := range d.Definition {
		if step.ID == id {
			return step, true
		}
	}
	return Step{}, false
}

func (r *Runner) skipRest(d *Delivery, from int, reason string) {
	for j := from + 1; j < len(d.Steps); j++ {
		if !Terminal(d.Steps[j].State) {
			d.Steps[j].State, d.Steps[j].Reason = StateSkipped, reason
		}
	}
}

func (r *Runner) finish(d *Delivery, state string) {
	d.State, d.FinishedAt = state, r.now().UnixMilli()
	if err := r.store.SaveDelivery(*d); err != nil {
		log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
	}
	severity := pebblestore.SignalSeverityInfo
	if state != StateSucceeded && state != StateSentBack {
		severity = pebblestore.SignalSeverityWarning
	}
	counts := map[string]int{}
	for _, step := range d.Steps {
		counts[step.State]++
	}
	if state == StateBlocked {
		findings := ""
		for _, step := range d.Steps {
			if f, _ := step.Outcome["findings"].(string); f != "" {
				findings = f
			}
		}
		next := "send the findings by hand with swarm_message_job, or after a fix re-run the automation with swarm_deliver_job automation=true"
		if strings.HasPrefix(d.BlockedReason, ReasonReviewUnreadable+":") {
			next = "read the reviewer job's session (swarm_get_job), then send its findings by hand with swarm_message_job or re-run the automation with swarm_deliver_job automation=true"
		}
		attrs := map[string]string{"automation": d.AutomationName, "reason": d.BlockedReason, "round": strconv.Itoa(d.Round), "next": next}
		if findings != "" {
			attrs["findings"] = truncate(findings, 500)
		}
		if d.Head != "" {
			attrs["head"] = d.Head
		}
		r.signal(*d, "automation.delivery.blocked", pebblestore.SignalSeverityWarning,
			fmt.Sprintf("Automation %s blocked job %s after %d rounds: %s", d.AutomationName, d.JobID, d.Round-d.RoundBase, d.BlockedReason), "", attrs, "")
	}
	attrs := map[string]string{
		"automation": d.AutomationName, "state": state, "steps_succeeded": strconv.Itoa(counts[StateSucceeded]), "steps_failed": strconv.Itoa(counts[StateFailed]),
	}
	summary := fmt.Sprintf("Automation %s %s for job %s", d.AutomationName, state, d.JobID)
	if state == StateSentBack && d.SentBack != nil {
		summary = fmt.Sprintf("Automation %s sent job %s back (step %s, round %d)", d.AutomationName, d.JobID, d.SentBack.Step, d.SentBack.Round)
		attrs["sent_back_step"], attrs["round"] = d.SentBack.Step, strconv.Itoa(d.SentBack.Round)
		if d.SentBack.Reason != "" {
			summary += ": " + d.SentBack.Reason
			attrs["reason"] = d.SentBack.Reason
		}
	}
	r.signal(*d, "automation.delivery.finished", severity, summary, "", attrs, "")
}

// runStep runs one attempt of a step and records it. It reports whether the
// step succeeded; a failed attempt with retries left leaves the step pending
// with its next attempt time.
func (r *Runner) runStep(ctx context.Context, d *Delivery, i int, step Step) bool {
	record := &d.Steps[i]
	record.State, record.StartedAt, record.FinishedAt, record.NextAttemptAt = StateRunning, r.now().UnixMilli(), 0, 0
	record.Attempts++
	record.ExitCode, record.Output, record.Reason, record.Next = nil, "", "", ""
	// A stored revision saved before swarm agents moved to their own
	// worktree runs migrated (fresh delivery or rerun of a pinned one).
	step, record.Note = LegacyCheckoutMigration(step)
	if err := r.store.SaveDelivery(*d); err != nil {
		log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
	}
	r.signal(*d, "automation.step.started", pebblestore.SignalSeverityInfo, fmt.Sprintf("Automation %s step %s started", d.AutomationName, step.ID), step.ID,
		map[string]string{"automation": d.AutomationName, "step": step.ID, "type": step.Type, "attempt": strconv.Itoa(record.Attempts)}, "")
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
	var failure stepFailure
	switch step.Type {
	case StepRun:
		failure = r.runScript(stepCtx, d, record, step)
	case StepAgent:
		if step.ForEach != nil {
			failure = r.queueItems(stepCtx, d, record, step)
		} else {
			failure = r.runAgent(stepCtx, d, record, step)
		}
	case StepReopen:
		failure = r.runReopen(stepCtx, d, record, step)
	case StepDeliver:
		failure = r.runDeliver(stepCtx, d, record, step)
	default:
		failure = stepFailure{reason: "unknown step type " + step.Type, next: "fix the script"}
	}
	timedOut := errors.Is(stepCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if ctx.Err() != nil && (!failure.inspectionTransient || record.Attempts >= maxInspectionAttempts) {
		// The daemon is stopping: never exhaust retries because of shutdown.
		// Keep the final attempt running for resume on the next start.
		// Earlier inspection cancellations persist their backoff below.
		return false
	}
	if timedOut && failure.reason == "" {
		failure = stepFailure{reason: fmt.Sprintf("timed out after %d seconds", step.TimeoutSeconds), next: "rerun"}
	}
	record.FinishedAt = r.now().UnixMilli()
	if failure.reason == "" {
		record.State = StateSucceeded
		if err := r.store.SaveDelivery(*d); err != nil {
			log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
		}
		attrs := map[string]string{"automation": d.AutomationName, "step": step.ID, "type": step.Type}
		if record.Output != "" {
			attrs["output"] = record.Output
		}
		r.signal(*d, "automation.step.succeeded", pebblestore.SignalSeverityInfo, fmt.Sprintf("Automation %s step %s succeeded", d.AutomationName, step.ID), step.ID, attrs, "")
		return true
	}
	if failure.limitUntil > 0 {
		// A provider limit is not a failure: the step waits for the reset
		// and runs a new job then, using none of its retries.
		record.State, record.Reason, record.Next, record.NextAttemptAt = StateWaiting, failure.reason, failure.next, failure.limitUntil
		if err := r.store.SaveDelivery(*d); err != nil {
			log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
		}
		r.signal(*d, "automation.step.waiting", pebblestore.SignalSeverityInfo, fmt.Sprintf("Automation %s step %s %s", d.AutomationName, step.ID, failure.reason), step.ID,
			map[string]string{"automation": d.AutomationName, "step": step.ID, "type": step.Type, "reason": failure.reason, "next": failure.next,
				"provider_limit": "true", "retry_at": strconv.FormatInt(record.NextAttemptAt, 10), "attempt": strconv.Itoa(record.Attempts)},
			strconv.Itoa(record.Attempts))
		return false
	}
	record.State, record.Reason, record.Next = StateFailed, failure.reason, failure.next
	record.recordFailure(d.Reruns, record.FinishedAt, failure.reason)
	retry := !failure.refused && record.Attempts-record.LimitWaits <= d.Retries
	if failure.inspectionTransient {
		// A busy machine (or a job with large media) can keep Git
		// inspection over its time budget for a long spell: wait it out
		// with growing backoff (see maxInspectionAttempts) before failing.
		retry = record.Attempts < maxInspectionAttempts
	}
	if retry {
		record.State = StatePending
		record.NextAttemptAt = r.now().Add(r.backoff(record.Attempts)).UnixMilli()
	}
	if err := r.store.SaveDelivery(*d); err != nil {
		log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
	}
	attrs := map[string]string{"automation": d.AutomationName, "step": step.ID, "type": step.Type, "reason": failure.reason, "next": failure.next,
		"attempt": strconv.Itoa(record.Attempts)}
	if record.ExitCode != nil {
		attrs["exit_code"] = strconv.Itoa(*record.ExitCode)
	}
	if retry {
		attrs["will_retry"] = "true"
		attrs["retry_at"] = strconv.FormatInt(record.NextAttemptAt, 10)
	}
	r.signal(*d, "automation.step.failed", pebblestore.SignalSeverityWarning, fmt.Sprintf("Automation %s step %s failed: %s", d.AutomationName, step.ID, failure.reason), step.ID, attrs,
		strconv.Itoa(record.Attempts))
	return false
}

// agentIdempotencyKey is the delivery id plus the step id, so a restart
// never starts an agent step's job twice; an explicit re-run is a new job.
func agentIdempotencyKey(d Delivery, stepID string) string {
	key := d.ID + "." + stepID
	if d.Reruns > 0 {
		key += ".rerun" + strconv.Itoa(d.Reruns)
	}
	return key
}

// maxInspectionAttempts bounds the attempts of a deliver step whose Git
// inspection did not finish (timeout, cancellation, capacity, moving heads).
// With defaultBackoff (15s doubling to a 10-minute cap) the waits between
// ten attempts add up to about 45 minutes, outlasting a slow spell rather
// than failing within the first minute of it.
const maxInspectionAttempts = 10

type stepFailure struct {
	reason, next        string
	refused             bool
	inspectionTransient bool
	// limitUntil (unix ms) is set when the agent's job ended on a provider's
	// usage, rate or session limit: the step waits until then and reruns.
	limitUntil int64
}

// maxProviderLimitWaits bounds how many times one agent step waits for a
// provider's limit to reset before it fails (the bound every limit wait uses).
const maxProviderLimitWaits = providerlimit.MaxWaits

// providerLimitWait classifies an agent job that ended on a provider's usage,
// rate or session limit. It is never a failure or a reviewer's verdict: the
// step waits for the reset the provider states (else a bounded backoff) and
// then starts a new job. ok is false for any other ending.
func (r *Runner) providerLimitWait(record *StepRecord, jobID string, outcome AgentOutcome) (stepFailure, bool) {
	if outcome.LimitUntil > 0 {
		// The job waits for the reset itself and reruns then (it bounds
		// its own waits): the step waits for the same job, using none of
		// its retries, and reads its outcome after the reset.
		detail := truncate(strings.TrimSpace(outcome.Error), 200)
		record.Outcome = map[string]any{"job_id": jobID, "status": outcome.Status, "provider_limit": detail}
		record.LimitWaits++
		reason := detail
		if !strings.HasPrefix(reason, "waiting for the provider's limit to reset") {
			reason = providerlimit.Waiting(time.UnixMilli(outcome.LimitUntil), true, detail)
		}
		return stepFailure{reason: truncate(reason, 300), next: "none: the job runs again after the reset", limitUntil: outcome.LimitUntil}, true
	}
	status := NormalizeStatus(outcome.Status)
	if status == StatusSucceeded || status == StatusCancelled || !providerlimit.Is(outcome.Error) {
		return stepFailure{}, false
	}
	detail := truncate(strings.TrimSpace(outcome.Error), 200)
	record.Outcome = map[string]any{"limited_job_id": jobID, "status": outcome.Status, "provider_limit": detail}
	if record.LimitWaits >= maxProviderLimitWaits {
		return stepFailure{reason: truncate(providerlimit.Exhausted(record.LimitWaits, detail), 300),
			next: "rerun once the provider's limit has reset", refused: true}, true
	}
	record.LimitWaits++
	now := r.now()
	until, stated := providerlimit.ResetAt(outcome.Error, now)
	if !stated {
		until = now.Add(providerlimit.Backoff(record.LimitWaits))
	}
	return stepFailure{reason: truncate(providerlimit.Waiting(until, stated, detail), 300),
		next: "none: the step runs again after the reset", limitUntil: until.UnixMilli()}, true
}

// stepEnv is the environment a run step's script gets. SWARM_HEAD is the
// delivery's head: the job's head, moved forward by an agent step whose job
// committed on top of it.
func stepEnv(d Delivery, ref string) map[string]string {
	env := map[string]string{
		"SWARM_JOB_ID": d.Result.JobID, "SWARM_JOB_STATUS": d.Result.Status, "SWARM_DELIVERY_ID": d.ID,
		"SWARM_HEAD": "", "SWARM_BASE": "", "SWARM_BRANCH": "", "SWARM_JOB_REF": ref,
		"SWARM_ROUND": strconv.Itoa(d.Round),
	}
	if g := d.Result.Git; g != nil {
		env["SWARM_HEAD"], env["SWARM_BASE"], env["SWARM_BRANCH"] = g.Head, g.BaseCommit, g.Branch
	}
	if d.Head != "" {
		env["SWARM_HEAD"] = d.Head
	}
	if d.Spec != nil && d.Spec.Path != "" {
		env["SWARM_RESULT_FILE"] = d.Spec.Path
	}
	return env
}

// headResult is the delivery's result with its git head moved to the
// delivery head, for importing it into the folder.
func headResult(d Delivery) Result {
	out := d.Result
	if out.Git != nil && d.Head != "" {
		g := *out.Git
		g.Head = d.Head
		if d.HeadWorktree != "" {
			g.Worktree = d.HeadWorktree
		}
		out.Git = &g
	}
	return out
}

func (r *Runner) runScript(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	if r.run == nil {
		return stepFailure{reason: "run steps are not available on this daemon", next: "rerun", refused: true}
	}
	if step.Secrets != nil {
		return stepFailure{reason: secretsRemoved, next: "remove secrets from the step", refused: true}
	}
	ref := ""
	if d.Result.Git != nil && d.Result.Git.Head != "" && r.head != nil {
		imported, err := r.head(ctx, d.Folder, headResult(*d))
		if err != nil {
			return stepFailure{reason: truncate("could not bring the job's head into the folder: "+err.Error(), 300), next: "rerun"}
		}
		ref = imported
	}
	// The script gets every deliverable the delivery has now (the work's own
	// and what produce steps wrote).
	stdinResult := d.Result
	stdinResult.Deliverables = d.CurrentDeliverables()
	stdin, err := json.Marshal(stdinResult)
	if err != nil {
		return stepFailure{reason: "encode the result: " + err.Error(), next: "rerun"}
	}
	tail := &tailBuffer{max: MaxLogTailBytes}
	outcome, err := r.run.Run(ctx, RunRequest{
		Folder: d.Folder, Command: step.Command, Args: append([]string(nil), step.Args...), Env: stepEnv(*d, ref), Stdin: stdin,
		Timeout: time.Duration(step.TimeoutSeconds) * time.Second, OnOutput: tail.write,
	})
	if tail.len() == 0 && len(outcome.Output) > 0 {
		tail.write(outcome.Output)
	}
	logTail := tail.bytes()
	if err := r.store.PutStepLog(d.ID, step.ID, logTail); err != nil {
		log.Printf("swarmd job automations: save step log: %v", err)
	}
	record.Output = lastLine(logTail)
	var refused *RefusedError
	if errors.As(err, &refused) {
		return stepFailure{reason: truncate(refused.Reason, 300), next: "rerun", refused: true}
	}
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: truncate("the script could not run: "+err.Error(), 300), next: "rerun"}
	}
	code := outcome.ExitCode
	record.ExitCode = &code
	if code != 0 {
		reason := fmt.Sprintf("exited with code %d", code)
		if record.Output != "" {
			reason += ": " + record.Output
		}
		return stepFailure{reason: truncate(reason, 300), next: "fix the script"}
	}
	return stepFailure{}
}

func (r *Runner) runAgent(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	if r.agent == nil {
		return stepFailure{reason: "agent steps are not available on this daemon", next: "rerun", refused: true}
	}
	jobID, _ := record.Outcome["job_id"].(string)
	if jobID == "" && strings.TrimSpace(d.Head) == "" {
		reviewer := strings.EqualFold(strings.TrimSpace(step.Worker), "reviewer")
		if roles, ok := r.agent.(interface {
			Role(context.Context, string, string) (string, error)
		}); ok && !reviewer {
			role, err := roles.Role(ctx, d.Account, step.Worker)
			reviewer = err == nil && role == "reviewer"
		}
		if reviewer {
			return stepFailure{reason: "no head to review", next: "rerun", refused: true}
		}
	}
	if jobID == "" && step.Fixes != "" {
		// A reviewer allowed to fix branches at the reviewed job's own head;
		// once an earlier agent step moved the delivery head its fixes could
		// never descend from it, so it is not started.
		if d.Result.Git == nil || d.Head == "" || d.Head != d.Result.Git.Head {
			return stepFailure{reason: "fixes need the delivery head to be the reviewed job's own head; an earlier agent step moved it",
				next: "put the fixing review step before any agent step that commits", refused: true}
		}
	}
	if jobID == "" {
		key := agentIdempotencyKey(*d, step.ID)
		if record.LimitWaits > 0 {
			// The job before ended on a provider limit: a new job, once.
			key += ".limit" + strconv.Itoa(record.LimitWaits)
		}
		req := AgentRequest{Account: d.Account, Folder: d.Folder, Worker: step.Worker, Brief: step.Brief, Result: d.Result, Head: d.Head,
			IdempotencyKey: key, Fixes: step.Fixes, Checkout: step.Checkout,
			ReviewJob: latestWorkJob(*d, step.ID), Deliverables: d.CurrentDeliverables()}
		if step.Produce && d.Spec != nil {
			spec := *d.Spec
			req.Produce = &spec
		}
		started, err := r.agent.Start(ctx, req)
		if err != nil {
			var refused *RefusedError
			if errors.As(err, &refused) {
				return stepFailure{reason: truncate(refused.Reason, 300), next: "rerun", refused: true}
			}
			return stepFailure{reason: truncate("could not start the job: "+err.Error(), 300), next: "rerun"}
		}
		jobID = started
		record.Outcome = map[string]any{"job_id": jobID}
		if err := r.store.SaveDelivery(*d); err != nil {
			log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
		}
	}
	outcome, err := r.agent.Wait(ctx, d.Account, jobID)
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: truncate("waiting for job "+jobID+": "+err.Error(), 300), next: "rerun"}
	}
	// A job that ended on a provider limit recorded no review and moves no
	// head: whatever verdict its review record holds is not a reviewer's.
	if limited, ok := r.providerLimitWait(record, jobID, outcome); ok {
		return limited
	}
	out := map[string]any{"job_id": jobID, "status": outcome.Status}
	if res := outcome.Result; res != nil {
		if res.Git != nil {
			out["head"] = res.Git.Head
		}
		if res.Report != "" {
			out["report"] = truncate(res.Report, 4000)
		}
		if len(res.Review) > 0 {
			out["review"] = res.Review
		}
	}
	if outcome.Head != "" {
		out["head"] = outcome.Head
	}
	if outcome.Verdict == VerdictOK || outcome.Verdict == VerdictBlock {
		out["verdict"] = outcome.Verdict
		if outcome.Verdict == VerdictBlock && outcome.ReviewUnreadable {
			// The reviewer's report could not be read: there are no
			// findings, and a reopen step refuses to send anything.
			out["review_unreadable"] = true
		} else if outcome.Findings != "" {
			out["findings"] = truncate(outcome.Findings, maxFindingsBytes)
		}
	}
	record.Outcome = out
	record.Output = jobID
	move := r.moveHead
	if step.Fixes != "" {
		move = func(ctx context.Context, d *Delivery, outcome AgentOutcome, out map[string]any) stepFailure {
			return r.applyReviewerFixes(ctx, d, record, outcome, out)
		}
	}
	if failure := move(ctx, d, outcome, out); failure.reason != "" {
		return failure
	}
	if NormalizeStatus(outcome.Status) != StatusSucceeded {
		reason := outcome.Error
		if strings.TrimSpace(reason) == "" {
			reason = "the job " + jobID + " ended " + outcome.Status
		}
		return stepFailure{reason: reason, next: "rerun"}
	}
	if step.Produce {
		return r.recordProduced(ctx, d, step, outcome, out)
	}
	return stepFailure{}
}

// runReopen sends the step's message (its {{steps.<id>.output}} placeholders
// replaced) and the findings of the step named by from into the triggering
// job's own session as a follow-up and succeeds once it is accepted. With no
// findings (an OK review, or no from) the message is sent alone; with
// neither it sends nothing and fails. It does
// not wait. The follow-up's end is a new job end, whose delivery continues
// the round count. Past max_rounds it sends nothing and blocks the delivery.
// When the from step's review was unreadable it sends nothing, uses no
// round, and blocks the delivery with "review unreadable: <reviewer job>".
func (r *Runner) runReopen(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	if followup, _ := record.Outcome["followup"].(string); followup != "" {
		return stepFailure{} // sent before a restart
	}
	if r.reopen == nil {
		return stepFailure{reason: "reopen steps are not available on this daemon", next: "rerun", refused: true}
	}
	if d.Result.Kind != KindWorker && d.Result.Kind != KindTask {
		return stepFailure{reason: "only a task's or a worker job's session can be reopened; " + d.JobID + " is a " + d.Result.Kind + " job", next: "fix the automation", refused: true}
	}
	if d.Result.Status == StatusCancelled {
		return stepFailure{reason: "the job " + d.JobID + " was cancelled: it is not reopened", next: "swarm_message_job by hand", refused: true}
	}
	findings, reviewer, unreadable := "", "", false
	for _, s := range d.Steps {
		if step.From != "" && s.ID == step.From {
			findings, _ = s.Outcome["findings"].(string)
			reviewer, _ = s.Outcome["job_id"].(string)
			unreadable, _ = s.Outcome["review_unreadable"].(bool)
		}
	}
	if unreadable {
		reason := ReasonReviewUnreadable + ": " + reviewer
		d.BlockedReason = reason
		record.Output = "the reviewer's report could not be read; nothing was sent and no round was used"
		return stepFailure{reason: reason, next: "read the reviewer job's session (swarm_get_job), then swarm_message_job by hand or swarm_deliver_job automation=true", refused: true}
	}
	if strings.TrimSpace(findings) == "" {
		findings = ""
	}
	if findings == "" && step.Message == "" {
		return stepFailure{reason: "step " + step.From + " recorded no findings to send and the reopen step has no message", next: "fix the automation", refused: true}
	}
	// Rounds count per automation, job and reopen step: read how many
	// reopens this step has sent into the job now, so a delivery created
	// before another delivery's reopen never overruns max_rounds.
	if failure := r.roundsExhausted(d, record, step); failure.reason != "" {
		return failure
	}
	var parts []string
	if step.Message != "" {
		parts = append(parts, reopenMessage(*d, step.Message))
	}
	if findings != "" {
		parts = append(parts, findings)
	}
	text := strings.Join(append(parts, ReopenClosing), "\n\n")
	req := ReopenRequest{Account: d.Account, JobID: d.JobID, Message: text, RequestID: agentIdempotencyKey(*d, step.ID)}
	// A QueueingReopener queues the message for a run in flight (sent,
	// queued) like the message path. Otherwise, and while no run can take
	// it yet, the step waits (state waiting, with the reason), within its
	// timeout, and asks again on each new signal or every reopenRecheck. The
	// round is counted once the message is accepted.
	for {
		var changed <-chan struct{}
		if r.feed != nil {
			changed = r.feed.Changed() // before asking, so a job end in between wakes us
		}
		sent, failure, busy := r.sendReopen(ctx, d, record, req, step)
		if sent || failure.reason != "" {
			return failure
		}
		if busy == "" { // the daemon is stopping: resumed on the next start
			return stepFailure{}
		}
		if record.State != StateWaiting || record.Reason != "waiting: "+busy {
			first := record.State != StateWaiting
			record.State, record.Reason = StateWaiting, "waiting: "+busy
			if err := r.store.SaveDelivery(*d); err != nil {
				log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
			}
			if first {
				r.signal(*d, "automation.step.waiting", pebblestore.SignalSeverityInfo, fmt.Sprintf("Automation %s step %s waits for job %s: %s", d.AutomationName, step.ID, d.JobID, busy), step.ID,
					map[string]string{"automation": d.AutomationName, "step": step.ID, "type": step.Type, "reason": busy}, "")
			}
		}
		recheck := time.NewTimer(r.recheck)
		select {
		case <-ctx.Done():
			recheck.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return stepFailure{reason: truncate(fmt.Sprintf("timed out after %d seconds waiting for job %s to settle (%s); nothing was sent", step.TimeoutSeconds, d.JobID, busy), 300),
					next: "swarm_message_job by hand once the job settles, or rerun"}
			}
			return stepFailure{} // the daemon is stopping
		case <-changed:
		case <-recheck.C:
		}
		recheck.Stop()
		if stop, err := r.store.StopState(); err == nil && stop.Stopped {
			return stepFailure{reason: "cancelled while waiting for job " + d.JobID + " to settle: the automation stop switch is on; nothing was sent", next: "rerun", refused: true}
		}
	}
}

// roundsExhausted reads the reopens the automation sent into the job and
// fails the reopen step, blocking the delivery, when another reopen would
// exceed the step's own max_rounds (its reopens only, past its re-run base)
// or the job's overall bound: the sum of the definition's reopen steps'
// max_rounds, over all of them past the delivery's RoundBase. The blocked
// reason names the step that ran out.
func (r *Runner) roundsExhausted(d *Delivery, record *StepRecord, step Step) stepFailure {
	rounds, err := r.store.ReopenRounds(d.Account, d.JobID, d.AutomationID)
	if err != nil {
		return stepFailure{reason: truncate("read the round count: "+err.Error(), 300), next: "rerun"}
	}
	d.Round = max(d.Round, rounds.Total)
	total := d.Round - d.RoundBase
	used := rounds.Step(step.ID)
	if base, ok := d.StepRoundBase[step.ID]; ok {
		used -= base
	} else if d.RoundBase > 0 {
		// Re-run before rounds counted per step: its base is job-wide.
		used = min(used, total)
	}
	next := "swarm_message_job by hand, or swarm_deliver_job automation=true after a fix"
	if used >= step.MaxRounds {
		reason := RoundsExhaustedReason(step.ID)
		d.BlockedReason = reason
		record.Output = fmt.Sprintf("%d of %d reopens of step %s used without an ok verdict; nothing was sent", used, step.MaxRounds, step.ID)
		return stepFailure{reason: reason, next: next, refused: true}
	}
	if bound := jobRoundBound(d.Definition, step); total >= bound {
		d.BlockedReason = ReasonJobRoundsExhausted
		record.Output = fmt.Sprintf("%d of %d reopens over all reopen steps used; step %s sent nothing", total, bound, step.ID)
		return stepFailure{reason: ReasonJobRoundsExhausted, next: next, refused: true}
	}
	return stepFailure{}
}

// jobRoundBound is the job's overall reopen bound: the sum of the reopen
// steps' max_rounds (at least the running step's own).
func jobRoundBound(definition []Step, running Step) int {
	bound := 0
	for _, s := range definition {
		if s.Type == StepReopen {
			bound += s.MaxRounds
		}
	}
	return max(bound, running.MaxRounds)
}

// maxPlaceholderOutputBytes bounds one substituted step output line.
const maxPlaceholderOutputBytes = 500

// reopenMessage replaces each {{steps.<id>.output}} in a reopen step's
// message with the recorded output line of that earlier step (validation
// allows only earlier steps), in one pass: substituted text is never
// expanded again. A step with no recorded output becomes "(no output)".
func reopenMessage(d Delivery, message string) string {
	return stepOutputPlaceholder.ReplaceAllStringFunc(message, func(m string) string {
		id := stepOutputPlaceholder.FindStringSubmatch(m)[1]
		for _, s := range d.Steps {
			if s.ID == id {
				if out := strings.TrimSpace(strings.ReplaceAll(s.Output, "\n", " ")); out != "" {
					return truncate(out, maxPlaceholderOutputBytes)
				}
			}
		}
		return "(no output)"
	})
}

// sendReopen asks the Reopener once. It reports sent with the follow-up
// recorded, a failure, or busy (the job's state) when the job is still
// running and nothing was sent. Neither is set when ctx ended.
//
// The round limit is checked again under reopenMu, right before sending:
// another delivery of the same job may have sent a reopen while this one
// waited, and the count and the send must not interleave.
func (r *Runner) sendReopen(ctx context.Context, d *Delivery, record *StepRecord, req ReopenRequest, step Step) (bool, stepFailure, string) {
	r.reopenMu.Lock()
	defer r.reopenMu.Unlock()
	if failure := r.roundsExhausted(d, record, step); failure.reason != "" {
		return false, failure, ""
	}
	var sent ReopenSent
	var err error
	if queueing, ok := r.reopen.(QueueingReopener); ok {
		sent, err = queueing.ReopenOrQueue(ctx, req)
	} else {
		sent.Followup, err = r.reopen.Reopen(ctx, req)
	}
	followup := sent.Followup
	if err != nil {
		var refused *RefusedError
		if errors.As(err, &refused) {
			return false, stepFailure{reason: truncate(refused.Reason, 300), next: "swarm_message_job by hand", refused: true}, ""
		}
		if ctx.Err() != nil {
			return false, stepFailure{}, ""
		}
		var busy interface{ Busy() bool }
		if errors.As(err, &busy) && busy.Busy() {
			return false, stepFailure{}, truncate(err.Error(), 240)
		}
		return false, stepFailure{reason: truncate("could not send the follow-up: "+err.Error(), 300), next: "rerun"}, ""
	}
	if err := r.store.PutReopen(d.Account, d.JobID, followup, ReopenRecord{DeliveryID: d.ID, AutomationID: d.AutomationID, Step: step.ID, Round: d.Round, RoundBase: d.RoundBase}); err != nil {
		return false, stepFailure{reason: truncate("record the reopen: "+err.Error(), 300), next: "rerun"}, ""
	}
	record.Reason = ""
	record.Outcome = map[string]any{"followup": followup, "round": d.Round + 1}
	record.Output = "reopened " + d.JobID + " (round " + strconv.Itoa(d.Round+1) + ")"
	if sent.Queued {
		// The job was busy: the findings wait in its session for the run in
		// flight, and that run's end starts the next round, whatever head
		// it ends at.
		record.Outcome["queued"] = true
		record.Output = "sent (queued) to " + d.JobID + " for its current run " + followup + " (round " + strconv.Itoa(d.Round+1) + ")"
	}
	return true, stepFailure{}, ""
}

// moveHead moves the delivery head forward to the head of an agent step's
// job that committed on top of it, after Git confirms the job's head
// descends from the current delivery head; a job head that does not fails
// the step and the delivery head stays.
func (r *Runner) moveHead(ctx context.Context, d *Delivery, outcome AgentOutcome, out map[string]any) stepFailure {
	if outcome.Head == "" || d.Head == "" || outcome.Head == d.Head {
		return stepFailure{}
	}
	if NormalizeStatus(outcome.Status) != StatusSucceeded {
		return stepFailure{} // the job's own failure is reported below
	}
	worktree, failure := r.verifyDescends(ctx, d, outcome)
	if failure.reason != "" || ctx.Err() != nil {
		return failure
	}
	out["moved_head"] = map[string]any{"from": d.Head, "to": outcome.Head}
	d.Head, d.HeadWorktree = outcome.Head, worktree
	return stepFailure{}
}

// verifyDescends checks with Git that the job's head descends from the
// delivery head and returns the checkout the job's head can be fetched from.
func (r *Runner) verifyDescends(ctx context.Context, d *Delivery, outcome AgentOutcome) (string, stepFailure) {
	if r.verify == nil {
		return "", stepFailure{reason: "the job " + outcome.JobID + " committed, but this daemon cannot verify its head against the delivery head", next: "rerun", refused: true}
	}
	worktree := ""
	if outcome.Result != nil && outcome.Result.Git != nil {
		worktree = outcome.Result.Git.Worktree
	}
	descends, err := r.verify(ctx, d.Folder, HeadRef{Commit: d.Head, Worktree: d.HeadWorktree}, HeadRef{Commit: outcome.Head, Worktree: worktree})
	if err != nil {
		if ctx.Err() != nil {
			return "", stepFailure{}
		}
		return "", stepFailure{reason: truncate("could not verify the job's head against the delivery head: "+err.Error(), 300), next: "rerun"}
	}
	if !descends {
		return "", stepFailure{reason: "the job " + outcome.JobID + " head " + outcome.Head + " does not descend from the delivery head " + d.Head + ": the delivery head is not moved",
			next: "fix the automation", refused: true}
	}
	return worktree, stepFailure{}
}

// applyReviewerFixes moves the delivery head to the head of a reviewer that
// was allowed to commit security fixes (an agent step with fixes), only when
// the reviewer's verdict is OK, its head descends from the delivery head and
// its commits change at most MaxReviewerFixLines lines. A BLOCK (or missing)
// verdict keeps the head and the step's own outcome; too many changed lines
// fail the step with ReasonFixesTooLarge. The reviewer's fix commits are
// recorded either way.
//
// An OK verdict from a reviewer allowed to fix may mean "I fixed it", so it
// fails closed (ReasonFixesUnreadable, head kept) unless the reviewer's own
// checkout was read: Result.Git must carry its head and base (the reviewed
// head, which must be the delivery head), and the fix commits whenever its
// head moved.
func (r *Runner) applyReviewerFixes(ctx context.Context, d *Delivery, record *StepRecord, outcome AgentOutcome, out map[string]any) stepFailure {
	if NormalizeStatus(outcome.Status) != StatusSucceeded {
		return stepFailure{} // the job's own failure is reported below
	}
	var git *ResultGit
	if outcome.Result != nil {
		git = outcome.Result.Git
	}
	if git != nil && len(git.Commits) > 0 {
		commits := make([]map[string]any, 0, len(git.Commits))
		for _, c := range git.Commits {
			commits = append(commits, map[string]any{"sha": c.SHA, "subject": c.Subject})
		}
		out["fix_commits"] = commits
	}
	if outcome.Verdict != VerdictOK {
		verdict := outcome.Verdict
		if verdict == "" {
			verdict = "missing"
		}
		out["fixes_applied"] = false
		out["fixes_reason"] = "the review verdict is " + verdict + ", not ok: the reviewer's commits do not move the delivery head"
		return stepFailure{}
	}
	unreadable := func(why string) stepFailure {
		out["fixes_applied"] = false
		out["fixes_reason"] = why + ": the delivery head is not moved"
		record.Output = truncate(outcome.JobID+": "+why, 300)
		return stepFailure{reason: ReasonFixesUnreadable, next: "rerun; if it persists, check the reviewer job with swarm_get_job"}
	}
	switch {
	case outcome.ResultError != "":
		return unreadable("the reviewer's checkout could not be read: " + truncate(outcome.ResultError, 200))
	case git == nil || !isCommitID(git.Head):
		return unreadable("the reviewer's result has no head")
	case d.Head == "" || git.BaseCommit != d.Head:
		return unreadable("the reviewer's branch did not start at the delivery head")
	}
	if git.Head == d.Head {
		if len(git.Commits) > 0 {
			return unreadable("the reviewer's head did not move but it lists fix commits")
		}
		out["fixes_applied"] = false
		out["fix_lines"] = 0
		return stepFailure{} // OK with nothing fixed
	}
	if len(git.Commits) == 0 {
		return unreadable("the reviewer's head moved but its fix commits could not be read")
	}
	outcome.Head = git.Head
	worktree, failure := r.verifyDescends(ctx, d, outcome)
	if failure.reason != "" || ctx.Err() != nil {
		return failure
	}
	if r.lines == nil {
		return stepFailure{reason: "the reviewer committed fixes, but this daemon cannot measure them against the delivery head", next: "rerun", refused: true}
	}
	changed, err := r.lines(ctx, d.Folder, HeadRef{Commit: d.Head, Worktree: d.HeadWorktree}, HeadRef{Commit: outcome.Head, Worktree: worktree})
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: truncate("could not measure the reviewer's fixes: "+err.Error(), 300), next: "rerun"}
	}
	out["fix_lines"] = changed
	if changed > MaxReviewerFixLines {
		out["fixes_applied"] = false
		out["fixes_reason"] = fmt.Sprintf("%d changed lines; at most %d: the delivery head is not moved", changed, MaxReviewerFixLines)
		record.Output = fmt.Sprintf("%s: %d changed lines (at most %d)", outcome.JobID, changed, MaxReviewerFixLines)
		return stepFailure{reason: ReasonFixesTooLarge, next: "send the findings with swarm_message_job, or fix by hand and re-run the automation", refused: true}
	}
	out["fixes_applied"] = true
	out["moved_head"] = map[string]any{"from": d.Head, "to": outcome.Head}
	d.Head, d.HeadWorktree = outcome.Head, worktree
	return stepFailure{}
}

func (r *Runner) signal(d Delivery, kind, severity, summary, stepID string, attrs map[string]string, attempt string) {
	if r.emit == nil {
		return
	}
	refs := map[string]string{"job_id": d.JobID, "delivery_id": d.ID}
	if stepID != "" {
		refs["step_id"] = stepID
	}
	dedup := kind + ":" + d.ID + ":" + stepID + ":" + strconv.Itoa(d.Reruns)
	if attempt != "" {
		dedup += ":" + attempt
	}
	if kind == "automation.step.started" {
		dedup += ":" + attrs["attempt"]
	}
	r.emit(pebblestore.Signal{Kind: kind, Severity: severity, Account: d.Account, Summary: truncate(summary, 400), DedupKey: dedup, Refs: refs, Attrs: attrs})
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
}

func (t *tailBuffer) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buf)
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// isCommitID reports whether s is a full SHA-1 or SHA-256 commit id.
func isCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// lastLine is the last non-empty line of a step's output.
func lastLine(out []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return truncate(line, 1000)
		}
	}
	return ""
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
