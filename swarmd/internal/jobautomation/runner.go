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

// RunRequest is a run step for the folder's sandbox.
type RunRequest struct {
	Folder   string
	Command  string
	Args     []string
	Env      map[string]string
	Stdin    []byte
	Secrets  []string
	Timeout  time.Duration
	OnOutput func([]byte)
}

// RunOutcome is how a run step's script exited and what it printed (stdout
// and stderr, in order).
type RunOutcome struct {
	ExitCode int
	Output   []byte
}

// RunExecutor runs a run step's script in the folder's sandbox.
type RunExecutor interface {
	Run(ctx context.Context, req RunRequest) (RunOutcome, error)
}

// RefusedError is a step Swarm refused to run (no sandbox, no secret
// gateway, a script outside the folder): nothing ran.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

// AgentRequest is an agent step's job.
type AgentRequest struct {
	Account        string
	Folder         string
	Worker         string
	Brief          string
	Result         Result
	IdempotencyKey string
	// Fixes is the step's fixes option (FixesSecurity or ""): a reviewer
	// started with it may commit small security fixes on a writable branch at
	// the reviewed head.
	Fixes string
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
// accepted. It refuses (RefusedError) a job that is cancelled or still
// running.
type Reopener interface {
	Reopen(ctx context.Context, req ReopenRequest) (followup string, err error)
}

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
	emit    func(pebblestore.Signal)
	now     func() time.Time
	backoff func(attempt int) time.Duration

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
	Emit    func(pebblestore.Signal)
}

func NewRunner(opts Options) *Runner {
	return &Runner{
		store: opts.Store, feed: opts.Feed, account: opts.Account, results: opts.Results, run: opts.Run, agent: opts.Agent, reopen: opts.Reopen,
		head: opts.Head, verify: opts.Verify, lines: opts.Lines, emit: opts.Emit, now: time.Now, backoff: defaultBackoff,
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

// jobEndKinds are the signals that end a job.
var jobEndKinds = map[string]string{
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
	filter := pebblestore.SignalFilter{Account: account, KindPrefixes: []string{"worker.run", "media.job", "design.job"}}
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

// followupOf is the follow-up a worker.run signal reports (its dedup key is
// worker.run:<run>:<follow-up run>); "" for the job's first run.
func followupOf(sig pebblestore.Signal) string {
	if !strings.HasPrefix(sig.Kind, "worker.run.") {
		return ""
	}
	parts := strings.SplitN(sig.DedupKey, ":", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return ""
}

func (r *Runner) consume(ctx context.Context, account string, sig pebblestore.Signal) error {
	if _, ok := jobEndKinds[sig.Kind]; !ok {
		return nil
	}
	// Loop guard: a job an agent step started never triggers automations.
	if sig.Attrs["source"] == RequestSourceAutomation {
		return nil
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
	if len(candidates) == 0 || r.results == nil {
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
	for _, a := range candidates {
		if !a.Matches(result) {
			continue
		}
		if _, _, err := r.store.CreateDelivery(a, *result); err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
	}
	return nil
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
		if step.State == StatePending && step.NextAttemptAt > now {
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
	if err != nil || Terminal(d.State) {
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
		if record.State == StatePending && record.NextAttemptAt > r.now().UnixMilli() {
			return // waits for its retry
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
		ok = r.runStep(ctx, &d, i, step)
		if ctx.Err() != nil {
			return
		}
		record = &d.Steps[i]
		if record.State == StatePending {
			return // waits for its retry
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
	r.finish(&d, state)
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
	if state != StateSucceeded {
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
	r.signal(*d, "automation.delivery.finished", severity, fmt.Sprintf("Automation %s %s for job %s", d.AutomationName, state, d.JobID), "", map[string]string{
		"automation": d.AutomationName, "state": state, "steps_succeeded": strconv.Itoa(counts[StateSucceeded]), "steps_failed": strconv.Itoa(counts[StateFailed]),
	}, "")
}

// runStep runs one attempt of a step and records it. It reports whether the
// step succeeded; a failed attempt with retries left leaves the step pending
// with its next attempt time.
func (r *Runner) runStep(ctx context.Context, d *Delivery, i int, step Step) bool {
	record := &d.Steps[i]
	record.State, record.StartedAt, record.FinishedAt, record.NextAttemptAt = StateRunning, r.now().UnixMilli(), 0, 0
	record.Attempts++
	record.ExitCode, record.Output, record.Reason, record.Next = nil, "", "", ""
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
		failure = r.runAgent(stepCtx, d, record, step)
	case StepReopen:
		failure = r.runReopen(stepCtx, d, record, step)
	default:
		failure = stepFailure{reason: "unknown step type " + step.Type, next: "fix the script"}
	}
	timedOut := errors.Is(stepCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if ctx.Err() != nil {
		// The daemon is stopping: the step stays running and is resumed on
		// the next start.
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
	record.State, record.Reason, record.Next = StateFailed, failure.reason, failure.next
	retry := !failure.refused && record.Attempts <= d.Retries
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

type stepFailure struct {
	reason, next string
	refused      bool
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
	ref := ""
	if d.Result.Git != nil && d.Result.Git.Head != "" && r.head != nil {
		imported, err := r.head(ctx, d.Folder, headResult(*d))
		if err != nil {
			return stepFailure{reason: truncate("could not bring the job's head into the folder: "+err.Error(), 300), next: "rerun"}
		}
		ref = imported
	}
	stdin, err := json.Marshal(d.Result)
	if err != nil {
		return stepFailure{reason: "encode the result: " + err.Error(), next: "rerun"}
	}
	tail := &tailBuffer{max: MaxLogTailBytes}
	outcome, err := r.run.Run(ctx, RunRequest{
		Folder: d.Folder, Command: step.Command, Args: append([]string(nil), step.Args...), Env: stepEnv(*d, ref), Stdin: stdin,
		Secrets: append([]string(nil), step.Secrets...), Timeout: time.Duration(step.TimeoutSeconds) * time.Second, OnOutput: tail.write,
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
		started, err := r.agent.Start(ctx, AgentRequest{Account: d.Account, Folder: d.Folder, Worker: step.Worker, Brief: step.Brief, Result: d.Result,
			IdempotencyKey: agentIdempotencyKey(*d, step.ID), Fixes: step.Fixes})
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
		return stepFailure{reason: "the job " + jobID + " ended " + outcome.Status, next: "rerun"}
	}
	return stepFailure{}
}

// runReopen sends the findings of the step named by from into the triggering
// job's own session as a follow-up and succeeds once it is accepted; it does
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
	if d.Result.Kind != KindWorker {
		return stepFailure{reason: "only a worker job's session can be reopened; " + d.JobID + " is a " + d.Result.Kind + " job", next: "fix the automation", refused: true}
	}
	if d.Result.Status == StatusCancelled {
		return stepFailure{reason: "the job " + d.JobID + " was cancelled: it is not reopened", next: "swarm_message_job by hand", refused: true}
	}
	findings, reviewer, unreadable := "", "", false
	for _, s := range d.Steps {
		if s.ID == step.From {
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
		return stepFailure{reason: "step " + step.From + " recorded no findings to send", next: "fix the automation", refused: true}
	}
	used := d.Round - d.RoundBase
	if used >= step.MaxRounds {
		d.BlockedReason = ReasonRoundsExhausted
		record.Output = fmt.Sprintf("%d of %d reopens used without an ok verdict; nothing was sent", used, step.MaxRounds)
		return stepFailure{reason: ReasonRoundsExhausted, next: "swarm_message_job by hand, or swarm_deliver_job automation=true after a fix", refused: true}
	}
	text := findings + "\n\n" + ReopenClosing
	if step.Message != "" {
		text = step.Message + "\n\n" + text
	}
	r.reopenMu.Lock()
	defer r.reopenMu.Unlock()
	followup, err := r.reopen.Reopen(ctx, ReopenRequest{Account: d.Account, JobID: d.JobID, Message: text, RequestID: agentIdempotencyKey(*d, step.ID)})
	if err != nil {
		var refused *RefusedError
		if errors.As(err, &refused) {
			return stepFailure{reason: truncate(refused.Reason, 300), next: "swarm_message_job by hand", refused: true}
		}
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: truncate("could not send the follow-up: "+err.Error(), 300), next: "rerun"}
	}
	if err := r.store.PutReopen(d.Account, d.JobID, followup, ReopenRecord{DeliveryID: d.ID, AutomationID: d.AutomationID, Round: d.Round, RoundBase: d.RoundBase}); err != nil {
		return stepFailure{reason: truncate("record the reopen: "+err.Error(), 300), next: "rerun"}
	}
	record.Outcome = map[string]any{"followup": followup, "round": d.Round + 1}
	record.Output = "reopened " + d.JobID + " (round " + strconv.Itoa(d.Round+1) + ")"
	return stepFailure{}
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
