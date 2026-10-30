package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Stalled tasks (docs/task-progress.md, "Stalled tasks").
//
// The invariant: an unfinished task either makes progress, recovers by
// itself, or produces exactly one clear alert naming its state. A periodic
// check reads only Swarm's own records (the task, its session's run state and
// last event, its workflow deliveries, pending approvals, background
// commands) and, for the one shape that needs it, the task's worktree with
// Git. It never calls a provider.
//
// Two known shapes recover once by themselves: a review loop that used its
// automatic rounds while the reviewer still blocks gets one extra findings
// turn, and a run that ended with uncommitted changes and nothing committed
// gets one commit nudge per head. Every other stall, and a recovery that did
// not work, raises one task.stalled signal per stall episode (the signal
// feed, so swarm-watch and its Telegram relay hear it), wakes the Orchestrator
// conversation that delegated the task with the same text, and marks the
// task so task_state_reason names the stall everywhere the task is read.
// When the task moves again: task.unstalled, and the episode ends.
//
// The check follows only work that can still move. A task whose records last
// changed longer ago than the horizon (stall_horizon_hours, default 24), and a
// worker-run mirror whose worker is gone, archived or no longer admits runs,
// is old work: never recovered (a recovery would re-run a legacy worker run),
// never alerted, and its task state is left as its own records say. The
// first check of a daemon process sends at most one summary signal naming how
// many old unfinished tasks it left alone, never one per task.

const (
	// taskStallTick is how often the check runs.
	taskStallTick = time.Minute
	// taskStallRecoverGrace is how long a task's records must stay unchanged
	// before a known shape is recovered (or, once recovered, alerted): long
	// enough for a job end to be evaluated and a workflow to start.
	taskStallRecoverGrace = 2 * time.Minute
	// taskStallGitWindow bounds which finished tasks get the uncommitted
	// check: those whose records changed within it.
	taskStallGitWindow = 24 * time.Hour
	// taskStallGitPerTick bounds Git inspections per check.
	taskStallGitPerTick = 20
	taskStallGitTimeout = 15 * time.Second
	// taskStallMaxProjects bounds the projects read per check.
	taskStallMaxProjects = 1000
)

const (
	taskStallRecoveryReviewExtra = "review_extra"
	taskStallRecoveryCommitNudge = "commit_nudge"
)

// taskStallGit is what the uncommitted check reads from a task's worktree.
type taskStallGit struct {
	Head    string
	Dirty   bool
	Commits int
}

// taskStallVerdict is the check's answer for one task.
type taskStallVerdict struct {
	// Episode names the task's progress as observed: a different episode is
	// movement.
	Episode string
	// Kind and Reason are set when the task is stalled (or a known shape is
	// due for recovery).
	Kind   string
	Reason string
	// Recover names a known shape due for its automatic recovery.
	Recover string
	Head    string
	// Findings and AutomationID describe an exhausted review.
	Findings     string
	AutomationID string
	DeliveryID   string
	// RunID is the task session's latest run.
	RunID string
	// LastChange is when the task's records last changed (Unix ms).
	LastChange int64
	// Recent is the later of LastChange and any armed wait's deadline (a
	// provider-limit reset): the time the horizon is measured from, so a
	// long wait that is due still counts as work that can move.
	Recent int64
}

// RunTaskStallDetector runs the stall check for the machine owner's account
// until ctx ends: once at start (after a restart nothing is re-sent: every
// alert and recovery is keyed durably) and then every taskStallTick.
func (s *Server) RunTaskStallDetector(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(taskStallTick)
	defer ticker.Stop()
	for {
		if account := s.OutboxAccount(); account != "" {
			s.CheckStalledTasks(ctx, account, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckStalledTasks runs one stall check over account's unarchived project
// tasks at now.
func (s *Server) CheckStalledTasks(ctx context.Context, account string, now time.Time) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || strings.TrimSpace(account) == "" {
		return
	}
	db := s.sessions.Store()
	limits, err := db.GetLimits(account)
	if err != nil {
		log.Printf("swarmd stall check: read limits: %v", err)
		return
	}
	projects, err := db.ListProjects(account, taskStallMaxProjects)
	if err != nil {
		log.Printf("swarmd stall check: list projects: %v", err)
		return
	}
	budget := taskStallGitPerTick
	skipped, complete := 0, true
	for _, project := range projects {
		tasks, err := db.ListProjectTasksByArchive(account, project.ID, false)
		if err != nil {
			log.Printf("swarmd stall check: list tasks of %s: %v", project.ID, err)
			complete = false
			continue
		}
		for _, task := range tasks {
			if ctx.Err() != nil {
				return
			}
			if s.checkTaskStall(ctx, account, task, now, limits, &budget) {
				skipped++
			}
		}
	}
	if complete {
		s.summarizeOldTasks(account, skipped, limits)
	}
}

// taskStallHorizon is how long after its last change a task is still work
// the check follows.
func taskStallHorizon(limits pebblestore.Limits) time.Duration {
	hours := limits.StallHorizonHours
	if hours <= 0 {
		hours = pebblestore.DefaultStallHorizonHours
	}
	return time.Duration(hours) * time.Hour
}

// summarizeOldTasks sends, once per daemon process (its first complete
// check), the one summary of the old unfinished tasks the check left alone.
// The dedup key carries the count and horizon, so a restart over the same
// old work sends nothing again.
func (s *Server) summarizeOldTasks(account string, skipped int, limits pebblestore.Limits) {
	s.taskStallGitMu.Lock()
	done := s.taskStallSummarized
	s.taskStallSummarized = true
	s.taskStallGitMu.Unlock()
	if done || skipped == 0 || s.signals == nil {
		return
	}
	horizon := taskStallHorizon(limits)
	hours := strconv.Itoa(int(horizon / time.Hour))
	noun := "tasks were"
	if skipped == 1 {
		noun = "task was"
	}
	if _, err := s.signals.AppendOnce(pebblestore.Signal{
		Kind: "task.stall_check_skipped", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd, Account: account,
		Summary:  strconv.Itoa(skipped) + " old unfinished " + noun + " not checked for stalls on " + s.taskStallMachine() + " (older than " + hours + " h, or a retired worker's run)",
		DedupKey: "task.stall_check_skipped:" + strconv.Itoa(skipped) + ":" + hours,
		Attrs:    map[string]string{"count": strconv.Itoa(skipped), "horizon_hours": hours, "machine": s.taskStallMachine()},
	}); err != nil {
		log.Printf("swarmd signal task.stall_check_skipped not recorded: %v", err)
	}
}

// checkTaskStall checks one task. old reports that the task is unfinished
// old work the check left alone (counted in the one summary).
func (s *Server) checkTaskStall(ctx context.Context, account string, task pebblestore.ProjectTaskRecord, now time.Time, limits pebblestore.Limits, gitBudget *int) (old bool) {
	db := s.sessions.Store()
	rec, found, err := db.GetTaskStall(account, task.ID)
	if err != nil {
		log.Printf("swarmd stall check: read stall of %s: %v", task.ID, err)
		return false
	}
	if !taskStallCandidate(task) {
		if found {
			s.endTaskStall(account, task, rec)
		}
		return false
	}
	if legacy, ok := s.taskStallLegacyWorkerRun(account, task); !ok {
		return false // the worker could not be read: claim nothing
	} else if legacy {
		s.leaveOldTask(account, task, rec, found)
		return taskStatusClass(task.Status) != "finished"
	}
	v, ok := s.taskStallVerdict(ctx, account, task, now, limits, gitBudget)
	if !ok {
		return false // a record could not be read: claim nothing
	}
	if v.Recent > 0 && now.Sub(time.UnixMilli(v.Recent)) > taskStallHorizon(limits) {
		s.leaveOldTask(account, task, rec, found)
		return taskStatusClass(task.Status) != "finished"
	}
	if found && rec.Episode != v.Episode {
		s.endTaskStall(account, task, rec)
		found = false
	}
	if v.Kind == "" {
		if found {
			s.endTaskStall(account, task, rec)
		}
		return false
	}
	if !found {
		rec = pebblestore.TaskStallRecord{ProjectID: task.ProjectID, TaskID: task.ID, Episode: v.Episode, Kind: v.Kind, Reason: v.Reason, Since: v.LastChange}
	}
	if rec.Alerted {
		return false // one alert per episode
	}
	if v.Recover != "" {
		sent, failure := s.recoverTaskStall(ctx, account, task, v)
		if sent {
			// The recovery is a new turn: the task moves, and the next check
			// sees a new episode. Nothing is alerted.
			return false
		}
		if failure != "" {
			v.Reason += "; automatic recovery failed: " + failure
		}
	}
	rec.Kind, rec.Reason = v.Kind, v.Reason
	if err := db.PutTaskStall(account, rec); err != nil {
		log.Printf("swarmd stall check: record stall of %s: %v", task.ID, err)
		return false
	}
	s.alertTaskStall(ctx, account, task, v, rec, now)
	return false
}

// taskStallLegacyWorkerRun reports whether task mirrors a worker run whose
// worker no longer runs jobs here: gone, archived, deleted, or not active.
// Such a task belongs to the retired worker pipeline; a recovery turn would
// re-run its old worker run. ok is false when the worker could not be read.
func (s *Server) taskStallLegacyWorkerRun(account string, task pebblestore.ProjectTaskRecord) (legacy, ok bool) {
	if strings.TrimSpace(task.WorkerRunID) == "" {
		return false, true
	}
	workerID := strings.TrimSpace(task.WorkerID)
	if workerID == "" {
		return true, true
	}
	w, found, err := pebblestore.NewWorkerStore(s.sessions.Store().Underlying()).GetWorker(account, workerID)
	if err != nil {
		log.Printf("swarmd stall check: read worker of %s: %v", task.ID, err)
		return false, false
	}
	return !found || w.LifecycleState != pebblestore.WorkerLifecycleStateActive, true
}

// leaveOldTask leaves old work alone. A stall episode the check recorded for
// it earlier (before the horizon applied) is closed quietly: its marker is
// cleared without a task.unstalled signal, so nothing is announced for work
// that cannot move and the task reads as its own records say.
func (s *Server) leaveOldTask(account string, task pebblestore.ProjectTaskRecord, rec pebblestore.TaskStallRecord, found bool) {
	db := s.sessions.Store()
	if task.Stall != nil {
		if _, err := db.SetProjectTaskStall(account, task.ProjectID, task.ID, task.UpdatedAt, nil); err != nil {
			log.Printf("swarmd stall check: clear mark of %s: %v", task.ID, err)
			return
		}
	}
	if found {
		if err := db.DeleteTaskStall(account, rec.TaskID); err != nil {
			log.Printf("swarmd stall check: end stall of %s: %v", task.ID, err)
		}
	}
}

// taskStallCandidate reports whether task is one the check follows: an
// unarchived agent task with its own session (media and design jobs have
// their own pipelines and signals).
func taskStallCandidate(task pebblestore.ProjectTaskRecord) bool {
	return !task.Archived && strings.TrimSpace(task.SessionID) != "" && task.Design == nil && !mediaTaskAgent(task.Agent)
}

// taskStallVerdict decides whether task is stalled at now. ok is false when a
// record it needs could not be read.
func (s *Server) taskStallVerdict(ctx context.Context, account string, task pebblestore.ProjectTaskRecord, now time.Time, limits pebblestore.Limits, gitBudget *int) (taskStallVerdict, bool) {
	db := s.sessions.Store()
	nowMs := now.UnixMilli()
	stallAfter := time.Duration(limits.StallAfterMinutes) * time.Minute
	runSilent := time.Duration(limits.RunSilentMinutes) * time.Minute

	state, stateFound, err := db.GetV3SessionRunState(task.SessionID)
	if err != nil {
		return taskStallVerdict{}, false
	}
	projection, _, err := db.GetV3SessionProjection(task.SessionID)
	if err != nil {
		return taskStallVerdict{}, false
	}
	deliveries, eval := s.taskProgressRecords(account, task)
	latest := latestDeliveries(deliveries)
	progress := computeTaskProgress(task, deliveries, eval, nowMs)
	markTaskProgressAttention(&progress, task)
	var pending []pebblestore.PermissionRecord
	if s.perm != nil && stateFound && state.Active {
		if pending, err = s.perm.ListPending(task.SessionID, 5); err != nil {
			return taskStallVerdict{}, false
		}
	}

	// The episode: everything whose change is movement.
	fp := []string{task.ID, strconv.FormatInt(task.UpdatedAt, 10), task.Status}
	if stateFound {
		fp = append(fp, state.RunID, state.Status, strconv.FormatUint(state.EventSeq, 10))
	}
	fp = append(fp, strconv.FormatUint(projection.LastEventSeq, 10))
	lastChange := max(task.UpdatedAt, state.UpdatedAt, state.StartedAt, state.CompletedAt, projection.UpdatedAt)
	if eval != nil {
		fp = append(fp, "eval", eval.Followup, strconv.FormatBool(eval.Evaluated()))
		lastChange = max(lastChange, eval.EndedAt)
	}
	for _, d := range latest {
		fp = append(fp, d.ID, d.State, strconv.Itoa(d.Round))
		lastChange = max(lastChange, d.CreatedAt, d.StartedAt, d.FinishedAt)
		for _, step := range d.Steps {
			fp = append(fp, step.ID, step.State, strconv.Itoa(step.Attempts), strconv.FormatInt(step.NextAttemptAt, 10))
			lastChange = max(lastChange, step.StartedAt, step.FinishedAt)
		}
	}
	for _, p := range pending {
		fp = append(fp, "perm", p.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(fp, "\x00")))
	recent := max(lastChange, task.LimitRetryAt)
	for _, d := range latest {
		for _, step := range d.Steps {
			recent = max(recent, step.NextAttemptAt)
		}
	}
	v := taskStallVerdict{Episode: hex.EncodeToString(sum[:8]), LastChange: lastChange, Recent: recent, RunID: state.RunID}
	idle := time.Duration(nowMs-lastChange) * time.Millisecond
	if lastChange <= 0 {
		idle = 0
	}
	stalled := func(kind, reason string) (taskStallVerdict, bool) {
		v.Kind, v.Reason = kind, reason
		return v, true
	}

	switch progress.TaskState {
	case pebblestore.ProjectTaskStateLanded, pebblestore.ProjectTaskStateStopped:
		return v, true
	}

	// Armed waits: an idle conversation watching its delegated tasks (the
	// watch starts its next turn; a run left by the retired pending wait is
	// converted to a watch at startup) and a background command (it reports
	// back when it ends). An active run is judged on its own below.
	if stateFound && state.Status == pebblestore.V3RunIntentWaitingTasks {
		return v, true
	}
	if !stateFound || !state.Active {
		watches, err := db.ListV3SessionTaskWatches(task.SessionID)
		if err != nil {
			return taskStallVerdict{}, false
		}
		if len(watches) > 0 {
			return v, true
		}
	}
	if s.taskSessionBackgroundRunning(task.SessionID) {
		return v, true
	}
	if stateFound && state.Status == pebblestore.V3RunIntentDispatchBlocked {
		if idle < stallAfter {
			return v, true
		}
		return stalled(pebblestore.ProjectTaskStallHeld, s.taskStallHeldReason(state.BlockedReason, idle))
	}
	// An active run: queued for the executor or running.
	if stateFound && state.Active {
		if len(pending) > 0 {
			oldest := pending[0]
			for _, p := range pending[1:] {
				if p.PermissionRequested > 0 && (oldest.PermissionRequested == 0 || p.PermissionRequested < oldest.PermissionRequested) {
					oldest = p
				}
			}
			since := oldest.PermissionRequested
			if since <= 0 {
				since = lastChange
			}
			if time.Duration(nowMs-since)*time.Millisecond >= stallAfter {
				return stalled(pebblestore.ProjectTaskStallApproval, "waiting on an approval for "+firstNonEmptyString(oldest.ToolName, "a tool")+" since "+taskStallClock(since))
			}
			return v, true
		}
		switch state.Status {
		case pebblestore.V3RunIntentPendingExecutor:
			if idle < stallAfter {
				return v, true
			}
			return stalled(pebblestore.ProjectTaskStallHeld, s.taskStallHeldReason(state.BlockedReason, idle))
		}
		if s.taskSessionOnTool(task.SessionID, projection.LastEventSeq) {
			return v, true // a tool call is in flight; tools have their own timeouts
		}
		silentSince := max(projection.UpdatedAt, state.UpdatedAt, state.StartedAt)
		if silentSince > 0 && time.Duration(nowMs-silentSince)*time.Millisecond >= runSilent {
			return stalled(pebblestore.ProjectTaskStallRunSilent, "a run with no events for "+taskStallMinutes(time.Duration(nowMs-silentSince)*time.Millisecond))
		}
		return v, true
	}

	// No active run.
	switch task.Status {
	case "pending_approval":
		if idle >= stallAfter {
			return stalled(pebblestore.ProjectTaskStallApproval, "waiting on Start tasks approval for "+taskStallMinutes(idle))
		}
		return v, true
	case "queued", "pending":
		if idle >= stallAfter {
			return stalled(pebblestore.ProjectTaskStallHeld, s.taskStallHeldReason("", idle))
		}
		return v, true
	case pebblestore.ProjectTaskStatusLimitWaiting:
		// The task's own run stopped on its provider's limit and its session
		// continues at LimitRetryAt (project_task_limit_wait.go): armed until
		// that reset plus the stall window, like a workflow step's wait.
		if task.LimitRetryAt > 0 && nowMs >= task.LimitRetryAt+stallAfter.Milliseconds() {
			return stalled(pebblestore.ProjectTaskStallProviderLimit, "waiting on a provider limit until "+taskStallClock(task.LimitRetryAt)+" (passed); the task has not continued")
		}
		return v, true
	}
	for _, d := range latest {
		if jobautomation.Terminal(d.State) {
			continue
		}
		// A workflow is taking the task through its steps. A provider-limit
		// wait is armed until its reset (plus the stall window); any other
		// running or waiting step is progress the workflow owns (an agent
		// step's own task is checked by this same rule, a run step has its
		// timeout).
		for _, step := range d.Steps {
			if step.State == jobautomation.StateWaiting && step.NextAttemptAt > 0 && nowMs >= step.NextAttemptAt+stallAfter.Milliseconds() {
				return stalled(pebblestore.ProjectTaskStallProviderLimit, "waiting on a provider limit until "+taskStallClock(step.NextAttemptAt)+" (passed); workflow "+d.AutomationName+" step "+step.ID+" has not run again")
			}
		}
		return v, true
	}
	class := taskStatusClass(task.Status)
	// Review exhausted: the newest delivery blocked because its reopen step
	// ran out of rounds, and the reviewer still blocks.
	if d, ok := taskStallExhaustedReview(latest); ok && class != progressWorking && class != progressWaiting {
		if idle < taskStallRecoverGrace {
			return v, true
		}
		v.Findings, v.AutomationID, v.DeliveryID = taskStallFindings(d), d.AutomationID, d.ID
		v.Kind, v.Reason = pebblestore.ProjectTaskStallReviewExhausted, "review exhausted: workflow "+d.AutomationName+" used its automatic rounds and the reviewer still blocks"
		if done, err := db.HasTaskStallRecovery(account, task.ID, taskStallRecoveryReviewExtra); err != nil {
			return taskStallVerdict{}, false
		} else if !done {
			v.Recover = taskStallRecoveryReviewExtra
		} else {
			v.Reason += " after one extra round"
		}
		return v, true
	}
	// Ended uncommitted: the run ended, nothing is committed and the
	// worktree has changes, so no review or next step can start.
	nothingCommitted := progress.Integration == nil || (progress.Integration.State != progressIntegrated && progress.Integration.Commits == 0)
	if class == "finished" && nothingCommitted && task.Agent != pebblestore.WorkerRoleReviewer && strings.TrimSpace(task.WorkspacePath) != "" {
		if idle >= taskStallRecoverGrace && idle < taskStallGitWindow {
			if git, ok := s.taskStallGitState(ctx, task, v.Episode, gitBudget); ok && git.Dirty && git.Commits == 0 {
				v.Kind, v.Head = pebblestore.ProjectTaskStallEndedUncommitted, firstNonEmptyString(git.Head, task.BaseCommit, "unknown")
				v.Reason = "ended uncommitted: the run ended with uncommitted changes and nothing committed"
				if _, done, err := db.GetTaskStallRecovery(account, task.ID, taskStallRecoveryCommitNudge, v.Head); err != nil {
					return taskStallVerdict{}, false
				} else if !done {
					v.Recover = taskStallRecoveryCommitNudge
				} else {
					v.Reason += " (after one commit nudge)"
				}
				return v, true
			}
		}
	}
	// Anything else unfinished with no run and no next step.
	switch progress.TaskState {
	case pebblestore.ProjectTaskStateRunning, pebblestore.ProjectTaskStateWaiting:
		if idle >= stallAfter {
			what := "no run and no next step for " + taskStallMinutes(idle)
			if progress.Summary != "" {
				what += " (" + progress.Summary + ")"
			}
			return stalled(pebblestore.ProjectTaskStallNoNextStep, what)
		}
	}
	return v, true
}

// taskStallExhaustedReview is the newest finished delivery when it ended
// blocked because a reopen step ran out of rounds.
func taskStallExhaustedReview(latest []jobautomation.Delivery) (jobautomation.Delivery, bool) {
	if len(latest) == 0 {
		return jobautomation.Delivery{}, false
	}
	sorted := append([]jobautomation.Delivery(nil), latest...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt > sorted[j].CreatedAt })
	d := sorted[0]
	if d.State != jobautomation.StateBlocked || !strings.HasPrefix(d.BlockedReason, jobautomation.ReasonRoundsExhausted) {
		return jobautomation.Delivery{}, false
	}
	return d, true
}

// taskStallFindings are the findings the exhausted reopen step would have
// sent: those of its from step, else the latest any step recorded.
func taskStallFindings(d jobautomation.Delivery) string {
	from := ""
	for i, step := range d.Steps {
		if step.Type == jobautomation.StepReopen && i < len(d.Definition) && d.Definition[i].From != "" {
			from = d.Definition[i].From
		}
	}
	findings := ""
	for _, step := range d.Steps {
		text, _ := step.Outcome["findings"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if from == "" || step.ID == from {
			findings = text
		}
	}
	return strings.TrimSpace(findings)
}

// taskStallHeldReason says what holds a queued run: the update drain, an
// admission refusal, or nothing known.
func (s *Server) taskStallHeldReason(blocked string, idle time.Duration) string {
	if manager := s.ExecutionCapacity(); manager != nil {
		if status := manager.DrainStatus(); status.Draining {
			return "held by an update drain since " + taskStallClock(status.Since.UnixMilli())
		}
	}
	if blocked = strings.TrimSpace(blocked); blocked != "" {
		return "held by admission: " + clipOutcome(blocked) + " (" + taskStallMinutes(idle) + ")"
	}
	return "queued, not started for " + taskStallMinutes(idle)
}

// taskSessionBackgroundRunning reports whether the task's session has a
// background command still running (it reports back when it ends).
func (s *Server) taskSessionBackgroundRunning(sessionID string) bool {
	running, ok := s.backgroundCommands.(interface{ SessionBackgroundCommandsRunning(string) bool })
	return ok && running.SessionBackgroundCommandsRunning(sessionID)
}

// taskSessionOnTool reports whether the session's last event started (or
// streams) a tool call that has not ended.
func (s *Server) taskSessionOnTool(sessionID string, lastSeq uint64) bool {
	if lastSeq == 0 {
		return false
	}
	event, found, err := s.sessions.Store().GetV3SessionEvent(sessionID, lastSeq)
	if err != nil || !found {
		return false
	}
	return event.EventType == "session.tool.started" || event.EventType == "session.tool.delta"
}

// taskStallGitState reads the task's worktree with Git (head, dirty,
// commits), at most once per episode and within the per-check budget.
func (s *Server) taskStallGitState(ctx context.Context, task pebblestore.ProjectTaskRecord, episode string, budget *int) (taskStallGit, bool) {
	key := task.ID + ":" + episode
	s.taskStallGitMu.Lock()
	if cached, ok := s.taskStallGitCache[key]; ok {
		s.taskStallGitMu.Unlock()
		return cached, true
	}
	s.taskStallGitMu.Unlock()
	if budget != nil {
		if *budget <= 0 {
			return taskStallGit{}, false
		}
		*budget--
	}
	read := s.taskStallGitFn
	if read == nil {
		read = s.inspectTaskStallGit
	}
	gitCtx, cancel := context.WithTimeout(ctx, taskStallGitTimeout)
	defer cancel()
	out, ok := read(gitCtx, task)
	if !ok {
		return out, false
	}
	s.taskStallGitMu.Lock()
	if s.taskStallGitCache == nil || len(s.taskStallGitCache) > 10_000 {
		s.taskStallGitCache = map[string]taskStallGit{}
	}
	s.taskStallGitCache[key] = out
	s.taskStallGitMu.Unlock()
	return out, true
}

// inspectTaskStallGit reads the task's captured lane through the same
// delivery assessment task reads use.
func (s *Server) inspectTaskStallGit(ctx context.Context, task pebblestore.ProjectTaskRecord) (taskStallGit, bool) {
	state := inspectTaskGitStateContext(ctx, task, s.sessions.Store())
	a := state.deliveryAssessment
	if a == nil || a.State == "unavailable" || ctx.Err() != nil {
		return taskStallGit{}, false
	}
	return taskStallGit{Head: a.SourceOID, Dirty: a.SourceDirty, Commits: max(a.CandidateCommits, state.unintegratedCommits)}, true
}

// recoverTaskStall sends a known shape's one automatic turn into the task's
// own session (the review loop's reopen path). sent reports that it was
// accepted; failure says why it could not be.
func (s *Server) recoverTaskStall(ctx context.Context, account string, task pebblestore.ProjectTaskRecord, v taskStallVerdict) (bool, string) {
	db := s.sessions.Store()
	if strings.TrimSpace(task.WorkerRunID) != "" {
		// A worker run's job is owned by its worker; a recovery turn here
		// would re-run that worker run. The person decides instead.
		return false, "a worker's run is never re-run by the stall check"
	}
	var id, message string
	switch v.Recover {
	case taskStallRecoveryReviewExtra:
		if v.Findings == "" {
			return false, "the reviewer's findings could not be read"
		}
		id = firstNonEmptyString(v.AutomationID, "workflow")
		message = "The review loop used its automatic rounds and the reviewer still blocks. One extra round: address these findings.\n\n" + v.Findings + "\n\n" + jobautomation.ReopenClosing
	case taskStallRecoveryCommitNudge:
		id = v.Head
		message = "Your run ended with uncommitted changes in your worktree and nothing is committed, so no review or next step can start. Finish the work, run the tests, commit, or say why not."
	default:
		return false, ""
	}
	if _, done, err := db.GetTaskStallRecovery(account, task.ID, v.Recover, id); err != nil {
		return false, err.Error()
	} else if done {
		return false, ""
	}
	requestID := "task-stall:" + v.Recover + ":" + task.ID + ":" + id
	followup, err := s.reopenTask(ctx, jobautomation.ReopenRequest{Account: account, JobID: taskRunJobID(task.ProjectID, task.ID), Message: message, RequestID: requestID})
	if err != nil {
		log.Printf("swarmd stall check: %s for task %s: %v", v.Recover, task.ID, err)
		return false, clipOutcome(err.Error())
	}
	if err := db.PutTaskStallRecovery(account, task.ID, pebblestore.TaskStallRecovery{Kind: v.Recover, ID: id, RunID: v.RunID, Followup: followup, At: time.Now().UnixMilli()}); err != nil {
		log.Printf("swarmd stall check: record %s for task %s: %v", v.Recover, task.ID, err)
	}
	return true, ""
}

// alertTaskStall raises the episode's one task.stalled signal, wakes the
// delegating Orchestrator with the same text and marks the task. Each step
// is keyed by the episode, so a restart in between repeats none of them.
func (s *Server) alertTaskStall(ctx context.Context, account string, task pebblestore.ProjectTaskRecord, v taskStallVerdict, rec pebblestore.TaskStallRecord, now time.Time) {
	db := s.sessions.Store()
	machine := s.taskStallMachine()
	summary := "Task \"" + controlMCPTruncate(task.Title, 80) + "\" on " + machine + " is stalled: " + v.Reason
	if s.signals != nil {
		// AppendOnce answers the episode's first signal when one was already
		// recorded (a restart between the signal and the episode record): its
		// summary is the episode's text, so the wake below replays exactly.
		sig, err := s.signals.AppendOnce(pebblestore.Signal{
			Kind: "task.stalled", Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd, Account: account,
			Summary:  summary,
			DedupKey: "task.stalled:" + task.ID + ":" + rec.Episode,
			Refs:     map[string]string{"project_id": task.ProjectID, "task_id": task.ID, "session_id": task.SessionID},
			Attrs:    map[string]string{"stall": v.Kind, "machine": machine, "title": controlMCPTruncate(task.Title, 120), "since": taskStallClock(rec.Since)},
		})
		if err != nil {
			log.Printf("swarmd signal task.stalled not recorded: %v", err)
			return
		}
		if strings.TrimSpace(sig.Summary) != "" {
			summary = sig.Summary
		}
	}
	text := summary + "."
	if v.Kind == pebblestore.ProjectTaskStallReviewExhausted {
		text += " Review is exhausted: the reviewer still blocks after the automatic rounds and one extra round; it needs your decision."
		if v.Findings != "" {
			text += "\n\nThe reviewer's last findings:\n\n" + v.Findings
		}
	}
	text += "\n\n(task_id " + task.ID + ", project_id " + task.ProjectID + ")"
	s.wakeTaskDelegator(ctx, task, rec.Episode, v.Kind, text)
	if _, err := db.SetProjectTaskStall(account, task.ProjectID, task.ID, task.UpdatedAt, &pebblestore.ProjectTaskStall{Kind: v.Kind, Reason: v.Reason, Since: rec.Since, TaskUpdatedAt: task.UpdatedAt}); err != nil {
		log.Printf("swarmd stall check: mark task %s: %v", task.ID, err)
	}
	rec.Alerted, rec.AlertedAt = true, now.UnixMilli()
	if err := db.PutTaskStall(account, rec); err != nil {
		log.Printf("swarmd stall check: record alert of %s: %v", task.ID, err)
	}
}

// wakeTaskDelegator sends text into the Orchestrator conversation that
// delegated the task, through the session messages route as the machine
// owner: an idle conversation starts a turn, a running one gets it as a
// queued note (its task watches stay). The request id is the
// episode's, so it is sent once.
func (s *Server) wakeTaskDelegator(ctx context.Context, task pebblestore.ProjectTaskRecord, episode, kind, text string) {
	origin := strings.TrimSpace(task.OriginSessionID)
	if origin == "" || origin == task.SessionID {
		return
	}
	c, err := s.jobAutomationWriteCall(ctx)
	if err != nil {
		log.Printf("swarmd stall check: wake delegator of %s: %v", task.ID, err)
		return
	}
	body := map[string]any{
		"client_request_id": "task-stall:wake:" + task.ID + ":" + episode,
		"role":              "user",
		"content":           text,
		"metadata":          map[string]any{"source": "task_stall", "task_id": task.ID, "stall": kind},
	}
	if _, err := c.dispatch(http.MethodPost, controlMCPSessionPath(origin, "messages"), nil, body); err != nil {
		log.Printf("swarmd stall check: wake delegator of %s: %v", task.ID, err)
	}
}

// endTaskStall ends a task's stall episode: task.unstalled when it was
// alerted, the marker cleared and the record removed.
func (s *Server) endTaskStall(account string, task pebblestore.ProjectTaskRecord, rec pebblestore.TaskStallRecord) {
	db := s.sessions.Store()
	if rec.Alerted && s.signals != nil {
		if _, err := s.signals.AppendOnce(pebblestore.Signal{
			Kind: "task.unstalled", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd, Account: account,
			Summary:  "Task \"" + controlMCPTruncate(task.Title, 80) + "\" on " + s.taskStallMachine() + " moves again (was stalled: " + controlMCPTruncate(rec.Reason, 120) + ")",
			DedupKey: "task.unstalled:" + task.ID + ":" + rec.Episode,
			Refs:     map[string]string{"project_id": task.ProjectID, "task_id": task.ID, "session_id": task.SessionID},
			Attrs:    map[string]string{"stall": rec.Kind, "machine": s.taskStallMachine()},
		}); err != nil {
			log.Printf("swarmd signal task.unstalled not recorded: %v", err)
			return
		}
	}
	if task.Stall != nil {
		if _, err := db.SetProjectTaskStall(account, task.ProjectID, task.ID, task.UpdatedAt, nil); err != nil {
			log.Printf("swarmd stall check: clear mark of %s: %v", task.ID, err)
		}
	}
	if err := db.DeleteTaskStall(account, task.ID); err != nil {
		log.Printf("swarmd stall check: end stall of %s: %v", task.ID, err)
	}
}

// taskStallMachine is this machine's name for alerts.
func (s *Server) taskStallMachine() string {
	if cfg, err := s.loadStartupConfig(); err == nil && strings.TrimSpace(cfg.SwarmName) != "" {
		return strings.TrimSpace(cfg.SwarmName)
	}
	return "this machine"
}

func taskStallClock(ms int64) string {
	if ms <= 0 {
		return "an unknown time"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func taskStallMinutes(d time.Duration) string {
	return fmt.Sprintf("%d min", int(d/time.Minute))
}
