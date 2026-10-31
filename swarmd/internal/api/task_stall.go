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
	// taskStallMaxTasks bounds the unarchived tasks of one project the check
	// reads; a larger project is reported and left out of the check.
	taskStallMaxTasks = 10000
)

const (
	taskStallRecoveryReviewExtra = "review_extra"
	taskStallRecoveryCommitNudge = "commit_nudge"
	// taskStallRecoveryReopenRetry re-sends, once per delivery, the reopen
	// of a workflow delivery that sent the task back when no run carried it
	// for the stall window.
	taskStallRecoveryReopenRetry = "reopen_retry"
)

// taskStallGit is what the uncommitted check reads from a task's worktree.
type taskStallGit struct {
	Head    string
	Dirty   bool
	Commits int
}

// taskStallGitEntry is a task's cached worktree read and the episode it was
// read in.
type taskStallGitEntry struct {
	episode string
	git     taskStallGit
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
	// ReopenStep and Message are the sent_back delivery's reopen step and
	// the follow-up it sent, re-sent by the reopen retry.
	ReopenStep string
	Message    string
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
//
// Tasks are listed by their briefs (pebblestore.ProjectTaskBrief), never as
// whole records: the candidate filter, the worker check, the records the
// verdict reads besides the task (run state, projection, deliveries, job
// evaluation, pending approvals) and the horizon need only the brief. A
// task's full record is read only when it is still work the check follows
// (taskStallInputs.leftAlone is false); old history costs its brief.
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
		var tasks []pebblestore.ProjectTaskBrief
		err := db.ListProjectTaskBriefs(account, project.ID, false, func(task pebblestore.ProjectTaskBrief) bool {
			tasks = append(tasks, task)
			return len(tasks) <= taskStallMaxTasks
		})
		if err == nil && len(tasks) > taskStallMaxTasks {
			err = fmt.Errorf("more than %d unarchived tasks", taskStallMaxTasks)
		}
		if err != nil {
			log.Printf("swarmd stall check: list tasks of %s: %v", project.ID, err)
			complete = false
			continue
		}
		// Oldest created first, as the board lists them.
		sort.Slice(tasks, func(i, j int) bool {
			if tasks[i].CreatedAt == tasks[j].CreatedAt {
				return tasks[i].ID < tasks[j].ID
			}
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		})
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
func (s *Server) checkTaskStall(ctx context.Context, account string, brief pebblestore.ProjectTaskBrief, now time.Time, limits pebblestore.Limits, gitBudget *int) (old bool) {
	db := s.sessions.Store()
	rec, found, err := db.GetTaskStall(account, brief.ID)
	if err != nil {
		log.Printf("swarmd stall check: read stall of %s: %v", brief.ID, err)
		return false
	}
	if !taskStallCandidate(brief) {
		if found {
			s.endTaskStall(account, brief, rec)
		}
		return false
	}
	if legacy, ok := s.taskStallLegacyWorkerRun(account, brief); !ok {
		return false // the worker could not be read: claim nothing
	} else if legacy {
		s.leaveOldTask(account, brief, rec, found)
		return taskStatusClass(brief.Status) != "finished"
	}
	in, ok := s.readTaskStallInputs(account, brief)
	if !ok {
		return false // a record could not be read: claim nothing
	}
	if in.leftAlone(brief, now, limits) {
		s.leaveOldTask(account, brief, rec, found)
		return taskStatusClass(brief.Status) != "finished"
	}
	// Work the check follows: its full record, hydrated as the board reads it.
	full, exists, err := db.GetProjectTask(account, brief.ProjectID, brief.ID)
	if err != nil || !exists || full == nil {
		if err != nil {
			log.Printf("swarmd stall check: read task %s: %v", brief.ID, err)
		}
		return false
	}
	task := *full
	brief = pebblestore.ProjectTaskBriefOf(task)
	v, ok := s.taskStallVerdict(ctx, account, task, in, now, limits, gitBudget)
	if !ok {
		return false // a record could not be read: claim nothing
	}
	if v.Recent > 0 && now.Sub(time.UnixMilli(v.Recent)) > taskStallHorizon(limits) {
		s.leaveOldTask(account, brief, rec, found)
		return taskStatusClass(task.Status) != "finished"
	}
	if found && rec.Episode != v.Episode {
		s.endTaskStall(account, brief, rec)
		found = false
	}
	if v.Kind == "" {
		if found {
			s.endTaskStall(account, brief, rec)
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
func (s *Server) taskStallLegacyWorkerRun(account string, task pebblestore.ProjectTaskBrief) (legacy, ok bool) {
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
func (s *Server) leaveOldTask(account string, task pebblestore.ProjectTaskBrief, rec pebblestore.TaskStallRecord, found bool) {
	db := s.sessions.Store()
	if task.Stalled {
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
func taskStallCandidate(task pebblestore.ProjectTaskBrief) bool {
	return !task.Archived && strings.TrimSpace(task.SessionID) != "" && !task.Design && !mediaTaskAgent(task.Agent)
}

// taskStallInputs are the records the verdict reads besides the task
// itself, read from its brief.
type taskStallInputs struct {
	state      pebblestore.V3SessionRunState
	stateFound bool
	projection pebblestore.V3SessionProjection
	deliveries []jobautomation.Delivery
	latest     []jobautomation.Delivery
	eval       *jobautomation.JobEvaluation
	pending    []pebblestore.PermissionRecord
	// lastChange and recent are the verdict's LastChange and Recent for
	// the brief's UpdatedAt and LimitRetryAt (times).
	lastChange, recent int64
}

// readTaskStallInputs reads task's inputs. ok is false when a record they
// need could not be read.
func (s *Server) readTaskStallInputs(account string, task pebblestore.ProjectTaskBrief) (taskStallInputs, bool) {
	db := s.sessions.Store()
	var in taskStallInputs
	var err error
	if in.state, in.stateFound, err = db.GetV3SessionRunState(task.SessionID); err != nil {
		return taskStallInputs{}, false
	}
	if in.projection, _, err = db.GetV3SessionProjection(task.SessionID); err != nil {
		return taskStallInputs{}, false
	}
	in.deliveries, in.eval = s.taskStallProgressRecords(account, task)
	in.latest = latestDeliveries(in.deliveries)
	if s.perm != nil && in.stateFound && in.state.Active {
		if in.pending, err = s.perm.ListPending(task.SessionID, 5); err != nil {
			return taskStallInputs{}, false
		}
	}
	in.lastChange, in.recent = in.times(task.UpdatedAt, task.LimitRetryAt)
	return in, true
}

// times is when the task's records last changed (LastChange) and the later
// of that and any armed wait's deadline (Recent).
func (in taskStallInputs) times(updatedAt, limitRetryAt int64) (lastChange, recent int64) {
	lastChange = max(updatedAt, in.state.UpdatedAt, in.state.StartedAt, in.state.CompletedAt, in.projection.UpdatedAt)
	if in.eval != nil {
		lastChange = max(lastChange, in.eval.EndedAt)
	}
	for _, d := range in.latest {
		lastChange = max(lastChange, d.CreatedAt, d.StartedAt, d.FinishedAt)
		for _, step := range d.Steps {
			lastChange = max(lastChange, step.StartedAt, step.FinishedAt)
		}
	}
	recent = max(lastChange, limitRetryAt)
	for _, d := range in.latest {
		for _, step := range d.Steps {
			recent = max(recent, step.NextAttemptAt)
		}
	}
	return lastChange, recent
}

// leftAlone reports that task is old work (its records last changed, or
// its armed wait was due, longer ago than the horizon) that the verdict
// would reach without a worktree read: what the full verdict would decide,
// decided without its full record. A finished task that ended within the
// worktree window still takes the full verdict, so the per-check Git budget
// and the worktree cache are spent exactly as before.
func (in taskStallInputs) leftAlone(task pebblestore.ProjectTaskBrief, now time.Time, limits pebblestore.Limits) bool {
	if in.recent <= 0 || now.Sub(time.UnixMilli(in.recent)) <= taskStallHorizon(limits) {
		return false
	}
	idle := time.Duration(now.UnixMilli()-in.lastChange) * time.Millisecond
	if in.lastChange <= 0 {
		idle = 0
	}
	gitDue := taskStatusClass(task.Status) == "finished" && task.Agent != pebblestore.WorkerRoleReviewer && strings.TrimSpace(task.WorkspacePath) != "" &&
		idle >= taskStallRecoverGrace && idle < taskStallGitWindow
	return !gitDue
}

// taskStallVerdict decides whether task is stalled at now, from its inputs.
// ok is false when a record it needs could not be read.
func (s *Server) taskStallVerdict(ctx context.Context, account string, task pebblestore.ProjectTaskRecord, in taskStallInputs, now time.Time, limits pebblestore.Limits, gitBudget *int) (taskStallVerdict, bool) {
	db := s.sessions.Store()
	nowMs := now.UnixMilli()
	stallAfter := time.Duration(limits.StallAfterMinutes) * time.Minute
	runSilent := time.Duration(limits.RunSilentMinutes) * time.Minute

	state, stateFound, projection := in.state, in.stateFound, in.projection
	deliveries, eval, latest, pending := in.deliveries, in.eval, in.latest, in.pending
	progress := computeTaskProgress(task, deliveries, eval, nowMs)
	markTaskProgressAttention(&progress, task)

	// The episode: everything whose change is movement.
	fp := []string{task.ID, strconv.FormatInt(task.UpdatedAt, 10), task.Status}
	if stateFound {
		fp = append(fp, state.RunID, state.Status, strconv.FormatUint(state.EventSeq, 10))
	}
	fp = append(fp, strconv.FormatUint(projection.LastEventSeq, 10))
	if eval != nil {
		fp = append(fp, "eval", eval.Followup, strconv.FormatBool(eval.Evaluated()))
	}
	for _, d := range latest {
		fp = append(fp, d.ID, d.State, strconv.Itoa(d.Round))
		for _, step := range d.Steps {
			fp = append(fp, step.ID, step.State, strconv.Itoa(step.Attempts), strconv.FormatInt(step.NextAttemptAt, 10))
		}
	}
	for _, p := range pending {
		fp = append(fp, "perm", p.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(fp, "\x00")))
	lastChange, recent := in.times(task.UpdatedAt, task.LimitRetryAt)
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
	// Sent back with no run: the newest delivery sent the work back into the
	// task's session, yet no run has carried it for the stall window (the
	// follow-up's run never started, or ended leaving no next step). Its
	// reopen is re-sent once; after that the stall is reported.
	if d, ok := taskStallSentBack(latest); ok && idle >= stallAfter {
		v.AutomationID, v.DeliveryID, v.ReopenStep = d.AutomationID, d.ID, d.SentBack.Step
		v.Kind = pebblestore.ProjectTaskStallNoNextStep
		v.Reason = fmt.Sprintf("sent back by workflow %s (step %s, round %d) but no run for %s", d.AutomationName, d.SentBack.Step, d.SentBack.Round, taskStallMinutes(idle))
		if _, done, err := db.GetTaskStallRecovery(account, task.ID, taskStallRecoveryReopenRetry, d.ID); err != nil {
			return taskStallVerdict{}, false
		} else if done {
			v.Reason += " after one retry of the reopen"
		} else if text, ok := jobautomation.ReopenText(d, d.SentBack.Step); ok {
			v.Recover, v.Message = taskStallRecoveryReopenRetry, text
		} else {
			v.Reason += "; its follow-up could not be rebuilt"
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

// taskStallProgressRecords is taskProgressRecords for the check, which reads
// every task every minute: each workflow's newest delivery in full and, of
// older ones, only ordering, states, times and job ends
// (jobautomation.Store.JobProgressDeliveries), so a long delivery history is
// not decoded whole per task per tick. Everything the check reads from
// progress (state, summary, integration, idle) is the same as from the full
// history; only the wording of older history entries can differ.
func (s *Server) taskStallProgressRecords(account string, task pebblestore.ProjectTaskBrief) ([]jobautomation.Delivery, *jobautomation.JobEvaluation) {
	var deliveries []jobautomation.Delivery
	var eval *jobautomation.JobEvaluation
	if store, err := s.jobAutomationStore(); err == nil && (taskStatusClass(task.Status) != progressWaiting || task.Status == pebblestore.ProjectTaskStatusLimitWaiting) {
		jobID := taskProgressJobIDOf(task)
		if list, err := store.JobProgressDeliveries(account, jobID); err == nil {
			deliveries = list
		} else {
			log.Printf("swarmd stall check: read deliveries of %s: %v", jobID, err)
		}
		if e, found, err := store.JobEvaluation(account, jobID); err == nil && found {
			eval = &e
		} else if err != nil {
			log.Printf("swarmd stall check: read evaluation of %s: %v", jobID, err)
		}
	}
	return deliveries, eval
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

// taskStallSentBack is the newest finished delivery when it ended sent_back
// with the reopen step that sent the work back.
func taskStallSentBack(latest []jobautomation.Delivery) (jobautomation.Delivery, bool) {
	if len(latest) == 0 {
		return jobautomation.Delivery{}, false
	}
	sorted := append([]jobautomation.Delivery(nil), latest...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt > sorted[j].CreatedAt })
	d := sorted[0]
	if d.State != jobautomation.StateSentBack || d.SentBack == nil || d.SentBack.Step == "" {
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
	// One entry per task, for its current episode: a new episode replaces
	// the old one instead of adding to the map.
	key := task.ID
	s.taskStallGitMu.Lock()
	if cached, ok := s.taskStallGitCache[key]; ok && cached.episode == episode {
		s.taskStallGitMu.Unlock()
		return cached.git, true
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
		s.taskStallGitCache = map[string]taskStallGitEntry{}
	}
	s.taskStallGitCache[key] = taskStallGitEntry{episode: episode, git: out}
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
	case taskStallRecoveryReopenRetry:
		if v.Message == "" || v.DeliveryID == "" || v.ReopenStep == "" {
			return false, "the workflow's follow-up could not be rebuilt"
		}
		id, message = v.DeliveryID, v.Message
	default:
		return false, ""
	}
	if _, done, err := db.GetTaskStallRecovery(account, task.ID, v.Recover, id); err != nil {
		return false, err.Error()
	} else if done {
		return false, ""
	}
	requestID := "task-stall:" + v.Recover + ":" + task.ID + ":" + id
	jobID := taskRunJobID(task.ProjectID, task.ID)
	retry := v.Recover == taskStallRecoveryReopenRetry
	followup, err := s.reopenTask(ctx, jobautomation.ReopenRequest{Account: account, JobID: jobID, Message: message, RequestID: requestID, Recovery: retry})
	if err != nil {
		log.Printf("swarmd stall check: %s for task %s: %v", v.Recover, task.ID, err)
		return false, clipOutcome(err.Error())
	}
	if retry {
		// The retry counts only once a run carries it, and the delivery's
		// reopen then names it, so its end continues the delivery's round.
		started, err := jobAutomationReopener{s: s}.FollowupRunStarted(ctx, account, jobID, followup)
		if err != nil || !started {
			return false, "the workflow's follow-up was re-sent but no run started on the task's session"
		}
		if store, err := s.jobAutomationStore(); err != nil {
			log.Printf("swarmd stall check: reopen retry for task %s: %v", task.ID, err)
		} else if err := store.RecordReopenRetry(account, v.DeliveryID, v.ReopenStep, followup); err != nil {
			log.Printf("swarmd stall check: record reopen retry of %s for task %s: %v", v.DeliveryID, task.ID, err)
		}
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
func (s *Server) endTaskStall(account string, task pebblestore.ProjectTaskBrief, rec pebblestore.TaskStallRecord) {
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
	if task.Stalled {
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
