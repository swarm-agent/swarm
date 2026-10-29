package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/providerlimit"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A project task's run (its first run or a follow-up in its session) that
// ends on its provider's usage limit does not fail the task. The task waits
// (status limit_waiting, task state waiting) until the reset the provider
// states, else a bounded backoff, and then the same task session continues
// in its retained worktree: one canonical message and run intent tell the
// agent its run stopped on the limit and to check git status, finish and
// commit. Uncommitted edits stay where they are. Detection, the reset time,
// the backoff and the bound (providerlimit.MaxWaits) are package
// providerlimit's, the same rule the worker and workflow paths use.
//
// The wait is durable on the task record and its index; the timer here only
// wakes it. A restart re-arms every wait at its reset time
// (ResumeProjectTaskLimitWaits). The person's stop or archive cancels it.

// projectTaskLimitNote is the message the continuation gives the agent.
const projectTaskLimitNote = "Your run stopped on the provider's usage limit at %s. The limit has reset and this task continues in the same session and worktree. Check `git status`, finish what you were doing and commit."

// projectTaskLimitAction is a waiting task's next step for the person.
const projectTaskLimitAction = "Waiting for the provider's usage limit to reset; the task continues by itself at %s. stop_task cancels the wait."

func (s *Server) taskLimitNow() time.Time {
	if s != nil && s.taskLimitClock != nil {
		return s.taskLimitClock()
	}
	return time.Now()
}

// projectTaskLimitWait decides, inside the task's update, whether a run of t
// that failed with reason waits for its provider's limit. It reports whether
// t now waits; when it does not, failure is the reason to record
// (providerlimit.Exhausted once the task waited MaxWaits times, else reason).
// A reviewer task (its verdict path handles a limit), a worker's task (the
// worker run waits) and a task that is not running are never waited on.
func projectTaskLimitWait(t *pebblestore.ProjectTaskRecord, runID, provider, reason string, now time.Time) (bool, string) {
	if t == nil || t.Status != "in_progress" || t.Agent == "reviewer" || t.WorkerRunID != "" || !providerlimit.Is(reason) {
		return false, reason
	}
	if t.LimitWaits >= providerlimit.MaxWaits {
		t.LimitRetryAt, t.LimitReason = 0, ""
		return false, providerlimit.Exhausted(t.LimitWaits, reason)
	}
	t.LimitWaits++
	until, stated := providerlimit.ResetAt(reason, now)
	if !stated {
		until = now.Add(providerlimit.Backoff(t.LimitWaits))
	}
	msg := strings.TrimSpace(reason)
	if p := strings.TrimSpace(provider); p != "" {
		msg = "provider " + p + ": " + msg
	}
	t.Status = pebblestore.ProjectTaskStatusLimitWaiting
	t.LimitAt, t.LimitRetryAt, t.LimitRunID = now.UnixMilli(), until.UnixMilli(), runID
	t.LimitReason = providerlimit.Waiting(until, stated, msg)
	t.ActionNeeded = strings.Replace(projectTaskLimitAction, "%s", until.UTC().Format(time.RFC3339), 1)
	return true, ""
}

func taskLimitKey(account, projectID, taskID string) string {
	return account + "\x00" + projectID + "\x00" + taskID
}

// armProjectTaskLimitWait (re)sets the one wake of a waiting task at retryAt.
func (s *Server) armProjectTaskLimitWait(account, projectID, taskID string, retryAt int64) {
	if s == nil || s.isShuttingDown() {
		return
	}
	delay := time.UnixMilli(retryAt).Sub(s.taskLimitNow())
	if delay < 0 {
		delay = 0
	}
	key := taskLimitKey(account, projectID, taskID)
	s.taskLimitMu.Lock()
	defer s.taskLimitMu.Unlock()
	if s.taskLimitTimers == nil {
		s.taskLimitTimers = map[string]*time.Timer{}
	}
	if old := s.taskLimitTimers[key]; old != nil {
		old.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.taskLimitMu.Lock()
		if s.taskLimitTimers[key] == timer {
			delete(s.taskLimitTimers, key)
		}
		s.taskLimitMu.Unlock()
		s.activeRunMu.Lock()
		if s.shuttingDown.Load() {
			s.activeRunMu.Unlock()
			return // the durable wait re-arms at the next start
		}
		s.runWG.Add(1)
		s.activeRunMu.Unlock()
		defer s.runWG.Done()
		if err := s.continueProjectTaskAfterLimit(account, projectID, taskID); err != nil {
			log.Printf("swarmd task limit wait: continue %s: %v", taskID, err)
		}
	})
	s.taskLimitTimers[key] = timer
}

// disarmProjectTaskLimitWait stops a task's wake, if one is set.
func (s *Server) disarmProjectTaskLimitWait(account, projectID, taskID string) {
	key := taskLimitKey(account, projectID, taskID)
	s.taskLimitMu.Lock()
	defer s.taskLimitMu.Unlock()
	if timer := s.taskLimitTimers[key]; timer != nil {
		timer.Stop()
		delete(s.taskLimitTimers, key)
	}
}

// ResumeProjectTaskLimitWaits re-arms every durable limit wait after a
// restart at its own reset time (one already due continues at once). Call it
// once the server is configured, as ResumeLinkedTaskWaits.
func (s *Server) ResumeProjectTaskLimitWaits() {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return
	}
	refs, err := s.sessions.Store().ListProjectTaskLimitWaits()
	if err != nil {
		log.Printf("swarmd task limit wait recovery failed: %v", err)
		return
	}
	for _, ref := range refs {
		s.armProjectTaskLimitWait(ref.AccountScopeID, ref.ProjectID, ref.TaskID, ref.RetryAt)
	}
}

// continueProjectTaskAfterLimit settles a task's limit wait: cancelled when
// the task was archived, unchanged (re-armed) before its reset, else its own
// session continues through one canonical message and run intent. That
// commit also turns the task in_progress and ends its wait
// (prepareTaskExecutionContinuation), so a restart either finds the wait or
// the continuation, never both; the message is idempotent per continuation.
func (s *Server) continueProjectTaskAfterLimit(account, projectID, taskID string) error {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return errors.New("session service unavailable")
	}
	// One continue or cancel at a time: a stop never races a continuation
	// into restarting the task it stopped.
	s.taskLimitOpMu.Lock()
	defer s.taskLimitOpMu.Unlock()
	db := s.sessions.Store()
	task, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil || !found {
		return err
	}
	if task.Status != pebblestore.ProjectTaskStatusLimitWaiting || !pebblestore.ProjectTaskLimitPending(*task) {
		return nil
	}
	if task.Archived {
		_, err := s.cancelProjectTaskLimitWaitLocked(account, projectID, taskID)
		return err
	}
	now := s.taskLimitNow()
	if now.UnixMilli() < task.LimitRetryAt {
		s.armProjectTaskLimitWait(account, projectID, taskID, task.LimitRetryAt)
		return nil
	}
	session, ok, err := s.sessions.GetSession(task.SessionID)
	if err != nil {
		return err
	}
	if !ok || session.AccountScopeID != account || sessionEnded(session) {
		return s.failProjectTaskLimitWait(account, projectID, taskID, "the task's session is no longer available to continue after the provider's limit reset")
	}
	if _, active, err := s.sessions.GetSessionActiveRunIntent(task.SessionID); err != nil {
		return err
	} else if active {
		// A run already continues the work (the person's own message).
		return s.markProjectTaskLimitContinued(account, projectID, taskID, task.SessionID)
	}
	runID := pebblestore.ProjectTaskLimitRerunID(task.LimitRunID, task.LimitWaits)
	stoppedAt := time.UnixMilli(task.LimitAt).UTC().Format(time.RFC3339)
	if task.LimitAt <= 0 {
		stoppedAt = "an earlier time"
	}
	content := strings.Replace(projectTaskLimitNote, "%s", stoppedAt, 1)
	key := "task-limit-continue:" + runID
	sum := sha256.Sum256([]byte(session.ID + "\x00" + runID))
	message := pebblestore.MessageSnapshot{ID: "v3msg_task_limit_" + hex.EncodeToString(sum[:16]), Role: "user", Content: content, CreatedAt: now.UnixMilli(),
		Metadata: map[string]any{"source": "project_task_limit_continue", "project_id": projectID, "task_id": taskID}}
	status, blocked := sessionruntime.RunIntentPendingExecutor, ""
	if sessionTakeoverActive(session) {
		status, blocked = sessionruntime.RunIntentDispatchBlocked, sessionTakeoverBlockedReason
	}
	hashSum := sha256.Sum256([]byte(key + "\x00" + content))
	hash := hex.EncodeToString(hashSum[:])
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: session.UserID, AccountScopeID: account,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: hash, RequestHash: hash,
		Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message,
		RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, SourceMessageID: message.ID, Status: status, BlockedReason: blocked}, NowUnixMs: now.UnixMilli(),
	})
	if err != nil {
		return err
	}
	intent, found, err := s.sessions.GetSessionRunIntent(session.ID, runID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("task limit continuation did not persist a run intent")
	}
	// The continuation's commit turned the task in_progress and ended its
	// wait (prepareTaskExecutionContinuation); only the wake is left.
	s.disarmProjectTaskLimitWait(account, projectID, taskID)
	job := sessionV3ExecutorJob{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: account}, SessionID: session.ID, RunID: runID, EpochID: intent.EpochID, SourceMessageID: intent.SourceMessageID}
	switch intent.Status {
	case sessionruntime.RunIntentPendingExecutor:
		if s.v3SessionExecutor != nil {
			s.v3SessionExecutor.EnqueueRun(job)
		}
	case sessionruntime.RunIntentRunning, sessionruntime.RunIntentDispatchBlocked:
	default:
		// The continuation already ran (a restart between recording it and
		// marking the task): settle its end into the task now.
		return s.reconcileProjectTaskRunLifecycle(job, intent.Status, intent.BlockedReason)
	}
	return nil
}

// markProjectTaskLimitContinued turns a waiting task running again: its
// session's latest run continues the work.
func (s *Server) markProjectTaskLimitContinued(account, projectID, taskID, sessionID string) error {
	_, err := s.sessions.Store().UpdateProjectTask(account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		if t.Status != pebblestore.ProjectTaskStatusLimitWaiting || t.SessionID != sessionID {
			return nil
		}
		t.Status = "in_progress"
		t.LimitRetryAt, t.LimitReason = 0, ""
		t.ActionNeeded = "Continuing after the provider's usage limit reset"
		return nil
	})
	if err == nil {
		s.disarmProjectTaskLimitWait(account, projectID, taskID)
	}
	return err
}

// failProjectTaskLimitWait ends a wait that cannot continue as failed.
func (s *Server) failProjectTaskLimitWait(account, projectID, taskID, reason string) error {
	_, err := s.sessions.Store().UpdateProjectTask(account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		if t.Status != pebblestore.ProjectTaskStatusLimitWaiting {
			return nil
		}
		t.Status = "failed"
		t.LastError = reason
		t.LimitRetryAt, t.LimitReason = 0, ""
		t.ActionNeeded = "Action Needed: Run failed (" + reason + "). Retry task."
		return nil
	})
	if err == nil {
		s.disarmProjectTaskLimitWait(account, projectID, taskID)
	}
	return err
}

// cancelProjectTaskLimitWait is the person's stop (or archive) of a task
// waiting for its provider's limit: the task is stopped, nothing continues
// it, and a message resumes it. It reports whether a wait was cancelled.
func (s *Server) cancelProjectTaskLimitWait(account, projectID, taskID string) (bool, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return false, nil
	}
	s.taskLimitOpMu.Lock()
	defer s.taskLimitOpMu.Unlock()
	return s.cancelProjectTaskLimitWaitLocked(account, projectID, taskID)
}

func (s *Server) cancelProjectTaskLimitWaitLocked(account, projectID, taskID string) (bool, error) {
	db := s.sessions.Store()
	current, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil || !found || current.Status != pebblestore.ProjectTaskStatusLimitWaiting {
		return false, err
	}
	cancelled := false
	_, err = db.UpdateProjectTask(account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		cancelled = pebblestore.CancelProjectTaskLimitWait(t)
		return nil
	})
	if err != nil {
		return false, err
	}
	s.disarmProjectTaskLimitWait(account, projectID, taskID)
	return cancelled, nil
}

// signalTaskLimitWait records task.limit_wait (a warning) when a task starts
// waiting for its provider's limit: once per run that hit the limit and its
// continuations, never once per retry.
func (s *Server) signalTaskLimitWait(account string, task *pebblestore.ProjectTaskRecord) {
	if s == nil || s.signals == nil || task == nil {
		return
	}
	until := time.UnixMilli(task.LimitRetryAt).UTC().Format(time.RFC3339)
	if _, err := s.signals.AppendOnce(pebblestore.Signal{
		Kind: "task.limit_wait", Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd, Account: account,
		Summary:  "Task " + controlMCPTruncate(task.Title, 120) + " (" + task.Agent + ") waits for its provider's usage limit to reset; continues at " + until,
		DedupKey: "task.limit_wait:" + task.ID + ":" + pebblestore.ProjectTaskLimitRunBase(task.LimitRunID),
		Refs:     map[string]string{"project_id": task.ProjectID, "task_id": task.ID, "session_id": task.SessionID, "run_id": task.LimitRunID},
		Attrs:    map[string]string{"retry_at": until, "job_id": taskRunJobID(task.ProjectID, task.ID), "role": task.Agent},
	}); err != nil {
		log.Printf("swarmd signal task.limit_wait not recorded: %v", err)
	}
}
