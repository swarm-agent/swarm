package run

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// A finished job's checkout is removed once its work is in the folder's
// target branch (worktree.Service.RemoveLandedWorktree); its branch, result
// and history stay. This happens right after Swarm lands the work and in a
// periodic sweep that also finds work merged outside Swarm and checkouts left
// from before.
const (
	landedWorktreeSweepDelay    = 2 * time.Minute
	landedWorktreeSweepInterval = 15 * time.Minute
	// A kept checkout (unmerged, dirty, in use) is looked at again after this.
	landedWorktreeRecheck = time.Hour
	// A failed removal is retried with backoff, at most this many times.
	landedWorktreeMaxAttempts    = 5
	landedWorktreeFailureBackoff = 10 * time.Minute

	LandedWorktreeTriggerLanded = "landed"
	LandedWorktreeTriggerSweep  = "sweep"
)

type landedWorktreeRemover interface {
	RemoveLandedWorktree(worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error)
}

type landedWorktreeSweep struct {
	// slot serializes one checkout's evaluation and record writes (a
	// one-place semaphore). The sweep holds it per checkout, never for a whole
	// pass; a removal right after a landing waits for it at most
	// landedLandingWait and otherwise leaves the checkout to the sweep, so a
	// finished integration never waits behind a sweep.
	once      sync.Once
	slot      chan struct{}
	nextCheck map[string]time.Time
	// claims holds, by "session:<id>" and "path:<path>", when new work
	// claimed a checkout (ClaimSessionWorktree). Read and written under
	// slot only. It lives with the daemon: the work that claims a checkout
	// is prepared within one request of the same process.
	claims map[string]time.Time
	// landingWaitForTest replaces landedLandingWait.
	landingWaitForTest time.Duration
	// removerForTest replaces the worktree service's Git removal.
	removerForTest landedWorktreeRemover
	nowForTest     func() time.Time
}

// LandedWorktreeOutcome reports one evaluation: Removed with what was
// removed, or Kept with why the checkout stays.
type LandedWorktreeOutcome struct {
	SessionID string
	Removed   bool
	Removal   worktree.LandedWorktreeRemoval
	Kept      string
}

// landedLandingWait bounds how long a removal right after a landing waits
// for another evaluation to finish.
const landedLandingWait = 10 * time.Second

// acquire takes the evaluation slot, waiting at most wait (forever when wait
// is negative); it reports whether it got it.
func (l *landedWorktreeSweep) acquire(wait time.Duration) bool {
	l.once.Do(func() { l.slot = make(chan struct{}, 1) })
	if wait < 0 {
		l.slot <- struct{}{}
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case l.slot <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (l *landedWorktreeSweep) release() { <-l.slot }

func (s *WorkerExecutionService) landedNow() time.Time {
	if s.landed.nowForTest != nil {
		return s.landed.nowForTest()
	}
	return time.Now()
}

func (s *WorkerExecutionService) landedRemover() landedWorktreeRemover {
	if s.landed.removerForTest != nil {
		return s.landed.removerForTest
	}
	return s.host.trees
}

// runLandedWorktreeSweep runs the periodic sweep until ctx ends (the worker
// scheduler's lifetime, see WorkerScheduler.Run).
func (s *WorkerExecutionService) runLandedWorktreeSweep(ctx context.Context, delay, interval time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, err := s.SweepLandedWorktrees(ctx)
		message := ""
		if err != nil {
			message = err.Error()
		}
		// Report a changed failure once, never every pass.
		if message != "" && message != lastError && ctx.Err() == nil {
			log.Printf("landed worktree sweep: %s", message)
		}
		lastError = message
		timer.Reset(interval)
	}
}

// RemoveLandedWorktree removes the checkout of a finished job's session right
// after Swarm landed its work (an integration, a workflow merge or delivery).
// Every rule of the sweep applies; a kept checkout is not an error.
func (s *WorkerExecutionService) RemoveLandedWorktree(sessionID string) (LandedWorktreeOutcome, error) {
	if s == nil || s.host == nil {
		return LandedWorktreeOutcome{SessionID: sessionID}, errors.New("worker execution authorities unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	wait := landedLandingWait
	if s.landed.landingWaitForTest > 0 {
		wait = s.landed.landingWaitForTest
	}
	if !s.landed.acquire(wait) {
		// The work already landed; the sweep looks at this checkout later.
		return LandedWorktreeOutcome{SessionID: sessionID, Kept: "another checkout is being evaluated; the sweep removes this one later"}, nil
	}
	defer s.landed.release()
	accounts := map[string]bool{}
	if snap, found, err := s.host.runs.sessions.Store().GetSession(sessionID); err != nil {
		return LandedWorktreeOutcome{SessionID: sessionID}, err
	} else if found {
		accounts[snap.AccountScopeID] = true
	}
	inUse, err := s.landedWorktreesInUse(accounts)
	if err != nil {
		return LandedWorktreeOutcome{SessionID: sessionID}, err
	}
	return s.removeLandedSessionWorktree(sessionID, LandedWorktreeTriggerLanded, inUse)
}

// SweepLandedWorktrees evaluates every finished job checkout it can find: the
// sessions of finished worker runs and the owners of managed worktrees on
// disk. It reports how many checkouts it removed. Once ctx ends (the daemon
// stops) it stops between candidates, never in the middle of one checkout's
// evaluation or removal, so a stop never waits for a whole pass.
func (s *WorkerExecutionService) SweepLandedWorktrees(ctx context.Context) (int, error) {
	if s == nil || s.host == nil {
		return 0, errors.New("worker execution authorities unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	accounts := map[string]bool{}
	candidates, err := s.landedWorktreeCandidates(accounts)
	var failures []error
	if err != nil {
		failures = append(failures, err)
	}
	removed := 0
	var inUse map[string]string
	var inUseAt time.Time
	for _, sessionID := range candidates {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		outcome, done, err := s.sweepLandedCandidate(sessionID, accounts, &inUse, &inUseAt)
		if err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", sessionID, err))
			if !done {
				// Without the full picture of what is in use nothing
				// more is removed this pass.
				break
			}
		}
		if outcome.Removed {
			removed++
		}
	}
	return removed, errors.Join(failures...)
}

// landedInUseMaxAge bounds how old the picture of checkouts in use may be
// when a sweep evaluates a checkout; past it the picture is read again.
const landedInUseMaxAge = 30 * time.Second

// sweepLandedCandidate evaluates one candidate under the per-checkout lock.
// done is false when the picture of checkouts in use could not be read.
func (s *WorkerExecutionService) sweepLandedCandidate(sessionID string, accounts map[string]bool, inUse *map[string]string, inUseAt *time.Time) (LandedWorktreeOutcome, bool, error) {
	s.landed.acquire(-1)
	defer s.landed.release()
	now := s.landedNow()
	if s.landed.nextCheck == nil {
		s.landed.nextCheck = map[string]time.Time{}
	}
	if next, ok := s.landed.nextCheck[sessionID]; ok && now.Before(next) {
		return LandedWorktreeOutcome{SessionID: sessionID}, true, nil
	}
	if *inUse == nil || time.Since(*inUseAt) > landedInUseMaxAge {
		fresh, err := s.landedWorktreesInUse(accounts)
		if err != nil {
			return LandedWorktreeOutcome{SessionID: sessionID}, false, err
		}
		*inUse, *inUseAt = fresh, time.Now()
	}
	outcome, err := s.removeLandedSessionWorktree(sessionID, LandedWorktreeTriggerSweep, *inUse)
	if outcome.Removed {
		delete(s.landed.nextCheck, sessionID)
	} else {
		s.landed.nextCheck[sessionID] = now.Add(landedWorktreeRecheck)
	}
	return outcome, true, err
}

// landedWorktreeCandidates lists sessions that may own a finished job's
// checkout, without repeats, and adds their accounts to accounts.
func (s *WorkerExecutionService) landedWorktreeCandidates(accounts map[string]bool) ([]string, error) {
	db := s.host.runs.sessions.Store()
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	var failures []error
	ws := db.WorkerStore()
	for cursor := ""; ; {
		page, err := ws.ListWorkersForScheduling(cursor, 25)
		if err != nil {
			failures = append(failures, err)
			break
		}
		for _, w := range page.Workers {
			accounts[w.AccountScopeID] = true
			for after := ""; ; {
				runs, next, err := ws.ListWorkerRuns(w.AccountScopeID, w.ID, 100, after)
				if err != nil {
					failures = append(failures, err)
					break
				}
				for _, r := range runs {
					if store.AutomationV2Terminal(r.Status) {
						add(r.SessionID)
					}
				}
				if next == "" {
					break
				}
				after = next
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	// Checkouts on disk whose durable owner is known (tasks of Swarm's own
	// agents and older runs).
	root, err := appstorage.WorktreesRoot()
	if err == nil {
		repos, readErr := os.ReadDir(root)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			failures = append(failures, readErr)
		}
		for _, repo := range repos {
			if !repo.IsDir() || repo.Type()&os.ModeSymlink != 0 || strings.HasPrefix(repo.Name(), ".") {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(root, repo.Name()))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				claim, found, err := db.GetWorktreeOwnership(filepath.Join(root, repo.Name(), entry.Name()))
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if found {
					accounts[claim.AccountScopeID] = true
					add(claim.OwnerSessionID)
				}
			}
		}
	}
	return out, errors.Join(failures...)
}

// landedWorktreesInUse maps each path a live session or an unfinished job
// works in to that session (or job), so no other job's checkout is removed
// from under it: active sessions (their worktree, folder and grants),
// unfinished worker runs (their sessions and any path in their input, such as
// a review's lane) and unfinished tasks of accounts (their checkout and
// review lane).
func (s *WorkerExecutionService) landedWorktreesInUse(accounts map[string]bool) (map[string]string, error) {
	db := s.host.runs.sessions.Store()
	inUse := map[string]string{}
	addSession := func(snap store.SessionSnapshot, user string) {
		for _, path := range []string{snap.WorktreeRootPath, snap.WorkspacePath} {
			if path = strings.TrimSpace(path); path != "" {
				inUse[filepath.Clean(path)] = user
			}
		}
		for _, grant := range snap.WorkspaceGrants {
			if path := strings.TrimSpace(grant.Path); path != "" {
				inUse[filepath.Clean(path)] = user
			}
		}
	}
	// Every lifecycle record is read: a page limit would silently miss
	// active sessions once a machine has many finished ones.
	active, err := db.ListEveryActiveSessionLifecycle()
	if err != nil {
		return nil, err
	}
	for _, lc := range active {
		if lc.AccountScopeID != "" {
			accounts[lc.AccountScopeID] = true
		}
		snap, found, err := db.GetSession(lc.SessionID)
		if err != nil {
			return nil, err
		}
		if found {
			addSession(snap, lc.SessionID)
		}
	}
	ws := db.WorkerStore()
	for cursor := ""; ; {
		page, err := ws.ListWorkersForScheduling(cursor, 25)
		if err != nil {
			return nil, err
		}
		for _, w := range page.Workers {
			accounts[w.AccountScopeID] = true
			pending, err := ws.UnfinishedWorkerRuns(w.AccountScopeID, w.ID, "")
			if err != nil {
				return nil, err
			}
			for _, r := range pending {
				user := "worker run " + r.ID
				if r.SessionID != "" {
					snap, found, err := db.GetSession(r.SessionID)
					if err != nil {
						return nil, err
					}
					if found {
						addSession(snap, r.SessionID)
					}
				}
				collectAbsolutePaths(r.Input, func(path string) { inUse[path] = user })
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	for account := range accounts {
		if account == "" {
			continue
		}
		projects, err := db.ListProjects(account, 1000)
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			// Archived tasks run nothing.
			tasks, err := db.ListProjectTasksByArchive(account, project.ID, false)
			if err != nil {
				return nil, err
			}
			for _, task := range tasks {
				if projectTaskFinishedForWorktreeRemoval(task.Status) {
					continue
				}
				user := "task " + task.ID
				if path := strings.TrimSpace(task.WorkspacePath); path != "" {
					inUse[filepath.Clean(path)] = user
				}
				collectAbsolutePaths(task.ReviewTarget, func(path string) { inUse[path] = user })
			}
		}
	}
	return inUse, nil
}

func collectAbsolutePaths(value any, add func(string)) {
	switch v := value.(type) {
	case string:
		if v = strings.TrimSpace(v); filepath.IsAbs(v) {
			add(filepath.Clean(v))
		}
	case map[string]any:
		for _, item := range v {
			collectAbsolutePaths(item, add)
		}
	case []any:
		for _, item := range v {
			collectAbsolutePaths(item, add)
		}
	}
}

// projectTaskFinishedForWorktreeRemoval: the task's run is over. Queued,
// planning, running, waiting (on a provider limit included), stopped-but-open
// and approval states keep the checkout.
func projectTaskFinishedForWorktreeRemoval(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "needs_review", "rejected", "superseded", "accepted":
		return true
	}
	return false
}

func (s *WorkerExecutionService) removeLandedSessionWorktree(sessionID, trigger string, inUse map[string]string) (LandedWorktreeOutcome, error) {
	out := LandedWorktreeOutcome{SessionID: sessionID}
	kept := func(reason string) (LandedWorktreeOutcome, error) {
		out.Kept = reason
		return out, nil
	}
	db := s.host.runs.sessions.Store()
	snap, found, err := db.GetSession(sessionID)
	if err != nil {
		return out, err
	}
	if !found {
		return kept("session not found")
	}
	path := strings.TrimSpace(snap.WorktreeRootPath)
	source := mapString(snap.Metadata, "swarm_v3_source_workspace_path")
	if !snap.WorktreeEnabled || path == "" || strings.TrimSpace(snap.WorktreeBranch) == "" || strings.TrimSpace(snap.WorktreeBaseBranch) == "" || source == "" {
		return kept("session has no checkout of its own on a branch with a target")
	}
	path = filepath.Clean(path)
	if path == filepath.Clean(source) {
		return kept("the checkout is the folder itself")
	}
	if mapString(snap.Metadata, "swarm_v3_worktree_owner_session_id") != sessionID {
		return kept("the session does not own the checkout")
	}
	if claim, ok, err := db.GetWorktreeOwnership(path); err != nil {
		return out, err
	} else if ok && (claim.OwnerSessionID != sessionID || claim.ClaimantSessionID != "" || claim.OperationState != "") {
		return kept("the checkout is claimed by another session or a recovery")
	}
	if user, ok := inUse[path]; ok && user != sessionID {
		return kept("the checkout is in use by " + user)
	}
	if intent, active, err := db.GetV3SessionActiveRunIntent(sessionID); err != nil {
		return out, err
	} else if active {
		return kept("the session's run is " + string(intent.Status))
	}
	if lc, ok, err := db.GetSessionLifecycle(sessionID); err != nil {
		return out, err
	} else if ok && lc.Active {
		return kept("the session's run is active")
	}
	run, task, err := s.landedWorktreeOwners(snap)
	if err != nil {
		return out, err
	}
	if run == nil && task == nil {
		return kept("the checkout is not a worker run's or task's")
	}
	if run != nil && !store.AutomationV2Terminal(run.Status) {
		return kept("the run is " + run.Status)
	}
	// A follow-up is queued (PrepareFollowup recreated the checkout and has
	// not appended its run yet) or running in the checkout.
	if run != nil && run.FollowupActive() {
		return kept("the run's follow-up is " + run.FollowupStatus)
	}
	if task != nil && !projectTaskFinishedForWorktreeRemoval(task.Status) {
		return kept("the task is " + task.Status)
	}
	now := s.landedNow()
	// A checkout claimed or recreated for new work (ClaimSessionWorktree) is
	// kept while that work starts: a follow-up is marked only when its run is
	// appended, a reopened task only after its reopen checks.
	if s.landed.claimedForNewWork(sessionID, path, now) {
		return kept("the checkout is reserved for new work")
	}
	for _, prior := range []*store.WorktreeRemoval{landedRunRemoval(run), landedTaskRemoval(task)} {
		if prior != nil && prior.State == store.WorktreeRemovalRestored && prior.Path == path && now.Sub(time.UnixMilli(prior.At)) < landedRestoreGrace {
			return kept("the checkout is reserved for new work (recreated)")
		}
	}
	for _, prior := range []*store.WorktreeRemoval{landedRunRemoval(run), landedTaskRemoval(task)} {
		if prior == nil || prior.State != store.WorktreeRemovalFailed || prior.Path != path {
			continue
		}
		if prior.Attempts >= landedWorktreeMaxAttempts {
			return kept(fmt.Sprintf("removal failed %d times (%s); remove the checkout by hand", prior.Attempts, prior.LastError))
		}
		if prior.NextAttemptAt > now.UnixMilli() {
			return kept("a failed removal is retried later")
		}
	}
	removal, err := s.landedRemover().RemoveLandedWorktree(worktree.LandedWorktree{Source: source, Path: path, Branch: snap.WorktreeBranch, TargetBranch: snap.WorktreeBaseBranch})
	switch {
	case errors.Is(err, worktree.ErrLandedWorktreeGone):
		return kept("the checkout is already gone")
	case worktree.IsLandedWorktreeKept(err):
		return kept(strings.TrimPrefix(err.Error(), "worktree kept: "))
	case err != nil:
		if recordErr := s.recordLandedWorktreeFailure(snap, run, task, path, trigger, err, now); recordErr != nil {
			return out, errors.Join(err, recordErr)
		}
		return out, err
	}
	out.Removed, out.Removal = true, removal
	return out, s.recordLandedWorktreeRemoval(snap, run, task, removal, trigger, now)
}

func landedRunRemoval(r *store.WorkerRunRecord) *store.WorktreeRemoval {
	if r == nil {
		return nil
	}
	return r.WorktreeRemoval
}

func landedTaskRemoval(t *store.ProjectTaskRecord) *store.WorktreeRemoval {
	if t == nil {
		return nil
	}
	return t.WorktreeRemoval
}

// landedWorktreeOwners reads the worker run and the task a session works
// for, when they are this session's.
func (s *WorkerExecutionService) landedWorktreeOwners(snap store.SessionSnapshot) (*store.WorkerRunRecord, *store.ProjectTaskRecord, error) {
	db := s.host.runs.sessions.Store()
	var run *store.WorkerRunRecord
	if runID, workerID := mapString(snap.Metadata, "worker_execution_run_id"), mapString(snap.Metadata, "worker_id"); runID != "" && workerID != "" {
		r, found, err := db.WorkerStore().GetWorkerRun(snap.AccountScopeID, workerID, runID)
		if err != nil {
			return nil, nil, err
		}
		if found && r.SessionID == snap.ID {
			run = &r
		}
	}
	var task *store.ProjectTaskRecord
	if projectID, taskID := mapString(snap.Metadata, "project_id"), mapString(snap.Metadata, "task_id"); projectID != "" && taskID != "" {
		t, found, err := db.GetProjectTask(snap.AccountScopeID, projectID, taskID)
		if err != nil {
			return nil, nil, err
		}
		if found && t != nil && (t.SessionID == snap.ID || filepath.Clean(strings.TrimSpace(t.WorkspacePath)) == filepath.Clean(snap.WorktreeRootPath)) {
			task = t
		}
	}
	if task == nil && run != nil && run.ProjectID != "" {
		// A worker job's task (reconcileRunTask's default id).
		t, found, err := db.GetProjectTask(snap.AccountScopeID, run.ProjectID, "task_"+run.ID)
		if err != nil {
			return nil, nil, err
		}
		if found && t != nil && t.WorkerRunID == run.ID {
			task = t
		}
	}
	return run, task, nil
}

func landedWorktreeRefs(snap store.SessionSnapshot, run *store.WorkerRunRecord, task *store.ProjectTaskRecord) map[string]string {
	refs := map[string]string{"session_id": snap.ID}
	if run != nil {
		refs["worker_id"], refs["run_id"] = run.WorkerID, run.ID
	}
	if task != nil {
		refs["project_id"], refs["task_id"] = task.ProjectID, task.ID
	}
	return refs
}

func landedWorktreeSubject(run *store.WorkerRunRecord, task *store.ProjectTaskRecord) string {
	switch {
	case run != nil:
		return "run " + run.ID
	case task != nil:
		return "task " + task.ID
	}
	return "job"
}

// recordLandedWorktreeRemoval writes the removal into the run's and the
// task's history and raises one worktree.removed activity event.
func (s *WorkerExecutionService) recordLandedWorktreeRemoval(snap store.SessionSnapshot, run *store.WorkerRunRecord, task *store.ProjectTaskRecord, removal worktree.LandedWorktreeRemoval, trigger string, now time.Time) error {
	record := store.WorktreeRemoval{State: store.WorktreeRemovalRemoved, Path: removal.Path, Branch: removal.Branch, Head: removal.Head, TargetBranch: removal.TargetBranch, Trigger: trigger, At: now.UnixMilli()}
	var failures []error
	if err := s.writeLandedWorktreeRecord(snap, run, task, record); err != nil {
		failures = append(failures, err)
	}
	subject := landedWorktreeSubject(run, task)
	_, err := s.host.runs.sessions.Store().AppendSignalOnce(store.Signal{
		Kind: "worktree.removed", Severity: store.SignalSeverityInfo, Account: snap.AccountScopeID,
		Summary:  fmt.Sprintf("Removed the checkout of finished %s: its branch %s is in %s", subject, removal.Branch, removal.TargetBranch),
		DedupKey: "worktree.removed:" + snap.ID + ":" + removal.Head + ":" + fmt.Sprint(record.At),
		Refs:     landedWorktreeRefs(snap, run, task),
		Attrs:    map[string]string{"path": removal.Path, "branch": removal.Branch, "head": removal.Head, "target_branch": removal.TargetBranch, "trigger": trigger},
	})
	if err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// recordLandedWorktreeFailure keeps the checkout and records the failure with
// a backoff; the first failure of a checkout is reported once as activity.
func (s *WorkerExecutionService) recordLandedWorktreeFailure(snap store.SessionSnapshot, run *store.WorkerRunRecord, task *store.ProjectTaskRecord, path, trigger string, cause error, now time.Time) error {
	attempts := 0
	for _, prior := range []*store.WorktreeRemoval{landedRunRemoval(run), landedTaskRemoval(task)} {
		if prior != nil && prior.State == store.WorktreeRemovalFailed && prior.Path == path && prior.Attempts > attempts {
			attempts = prior.Attempts
		}
	}
	attempts++
	record := store.WorktreeRemoval{State: store.WorktreeRemovalFailed, Path: path, Branch: snap.WorktreeBranch, TargetBranch: snap.WorktreeBaseBranch, Trigger: trigger, At: now.UnixMilli(), Attempts: attempts, LastError: cause.Error()}
	if attempts < landedWorktreeMaxAttempts {
		record.NextAttemptAt = now.Add(landedWorktreeFailureBackoff << min(attempts-1, 6)).UnixMilli()
	}
	var failures []error
	if err := s.writeLandedWorktreeRecord(snap, run, task, record); err != nil {
		failures = append(failures, err)
	}
	if attempts == 1 || attempts == landedWorktreeMaxAttempts {
		_, err := s.host.runs.sessions.Store().AppendSignalOnce(store.Signal{
			Kind: "worktree.remove_failed", Severity: store.SignalSeverityWarning, Account: snap.AccountScopeID,
			Summary:  fmt.Sprintf("Could not remove the checkout of finished %s (attempt %d of %d): %s", landedWorktreeSubject(run, task), attempts, landedWorktreeMaxAttempts, cause.Error()),
			DedupKey: fmt.Sprintf("worktree.remove_failed:%s:%s:%d", snap.ID, path, attempts),
			Refs:     landedWorktreeRefs(snap, run, task),
			Attrs:    map[string]string{"path": path, "branch": snap.WorktreeBranch, "target_branch": snap.WorktreeBaseBranch, "trigger": trigger},
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *WorkerExecutionService) writeLandedWorktreeRecord(snap store.SessionSnapshot, run *store.WorkerRunRecord, task *store.ProjectTaskRecord, record store.WorktreeRemoval) error {
	db := s.host.runs.sessions.Store()
	var failures []error
	if run != nil {
		// One locked read-modify-write of the record field only, so a
		// concurrent ReserveWorkerFollowup is never overwritten.
		if err := db.WorkerStore().SetWorkerRunWorktreeRemoval(run.AccountScopeID, run.WorkerID, run.ID, record); err != nil && !errors.Is(err, store.ErrWorkerNotFound) {
			failures = append(failures, fmt.Errorf("record worktree removal on run %s: %w", run.ID, err))
		}
	}
	if task != nil {
		_, err := db.UpdateProjectTask(snap.AccountScopeID, task.ProjectID, task.ID, func(t *store.ProjectTaskRecord) error {
			copy := record
			t.WorktreeRemoval = &copy
			return nil
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("record worktree removal on task %s: %w", task.ID, err))
		}
	}
	return errors.Join(failures...)
}

// landedRestoreGrace is how long a recreated checkout is kept before the
// ordinary rules (an active session, run, follow-up or task) alone decide.
// The work it was recreated for marks itself active well within it.
const landedRestoreGrace = 30 * time.Minute

// claimedForNewWork reports whether a checkout (by its owning session or its
// path) was claimed for new work within landedRestoreGrace. Called under the
// evaluation slot.
func (l *landedWorktreeSweep) claimedForNewWork(sessionID, path string, now time.Time) bool {
	for _, key := range []string{"session:" + sessionID, "path:" + path} {
		if at, ok := l.claims[key]; ok {
			if now.Sub(at) < landedRestoreGrace {
				return true
			}
			delete(l.claims, key)
		}
	}
	return false
}

// ClaimSessionWorktree is the one entry for new work about to use a
// session's checkout again (a follow-up, a task reopen, a review reading a
// job in place). It always takes the evaluation slot, so it waits for any
// landed removal or sweep evaluation in flight (which holds the slot from its
// state checks to its Git removal and record). Under the slot it re-reads the
// session's removal record, recreates the checkout from its kept branch when
// it was removed (recording it as restored), and records a claim on the
// checkout even when nothing was removed. A claimed checkout is kept by every
// later evaluation for landedRestoreGrace, until the new work marks itself
// active (a reserved follow-up, an appended run intent, an in_progress task).
// It reports whether the checkout was recreated.
func (s *WorkerExecutionService) ClaimSessionWorktree(sessionID string) (bool, error) {
	if s == nil || s.host == nil {
		return false, errors.New("worker execution authorities unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	// A session no evaluation could ever remove a checkout of (no checkout
	// of its own, or no worker run or task behind it; see
	// removeLandedSessionWorktree) needs no claim and never waits for the slot.
	if snap, found, err := s.host.runs.sessions.Store().GetSession(sessionID); err != nil {
		return false, err
	} else if !found || !landedRemovalCandidate(snap) {
		return false, nil
	}
	s.landed.acquire(-1)
	defer s.landed.release()
	snap, found, err := s.host.runs.sessions.Store().GetSession(sessionID)
	if err != nil {
		return false, err
	}
	if !found || !landedRemovalCandidate(snap) {
		return false, nil
	}
	path := filepath.Clean(strings.TrimSpace(snap.WorktreeRootPath))
	// The claim is recorded first: whatever happens below, no evaluation
	// after this one removes the checkout while the new work starts.
	now := s.landedNow()
	if s.landed.claims == nil {
		s.landed.claims = map[string]time.Time{}
	}
	s.landed.claims["session:"+sessionID] = now
	s.landed.claims["path:"+path] = now
	run, task, err := s.landedWorktreeOwners(snap)
	if err != nil {
		return false, err
	}
	var removed *store.WorktreeRemoval
	for _, prior := range []*store.WorktreeRemoval{landedRunRemoval(run), landedTaskRemoval(task)} {
		if prior != nil && prior.State == store.WorktreeRemovalRemoved && prior.Path == path {
			removed = prior
			break
		}
	}
	if removed == nil {
		return false, nil
	}
	source := mapString(snap.Metadata, "swarm_v3_source_workspace_path")
	recreated, err := s.host.trees.RestoreRemovedWorktree(source, path, snap.WorktreeBranch, removed.Head)
	if err != nil {
		return false, err
	}
	record := store.WorktreeRemoval{State: store.WorktreeRemovalRestored, Path: path, Branch: snap.WorktreeBranch, Head: removed.Head, TargetBranch: removed.TargetBranch, Trigger: removed.Trigger, At: s.landedNow().UnixMilli()}
	if err := s.writeLandedWorktreeRecord(snap, run, task, record); err != nil {
		return recreated, fmt.Errorf("record the recreated checkout: %w", err)
	}
	return recreated, nil
}

// landedRemovalCandidate reports whether removeLandedSessionWorktree could
// ever remove this session's checkout: a worktree of its own, owned by the
// session, behind a worker run or a project task (landedWorktreeOwners).
func landedRemovalCandidate(snap store.SessionSnapshot) bool {
	if !snap.WorktreeEnabled || strings.TrimSpace(snap.WorktreeRootPath) == "" || mapString(snap.Metadata, "swarm_v3_worktree_owner_session_id") != snap.ID {
		return false
	}
	workerRun := mapString(snap.Metadata, "worker_execution_run_id") != "" && mapString(snap.Metadata, "worker_id") != ""
	projectTask := mapString(snap.Metadata, "project_id") != "" && mapString(snap.Metadata, "task_id") != ""
	return workerRun || projectTask
}

// PrepareSessionMessage is the one preparation a message needs before it may
// start a run in a session (POST /v3/sessions/{id}/messages). A worker job's
// session goes through PrepareFollowup, which claims its checkout; any other
// session (a project task's, a person continuing a finished Coder session)
// claims its checkout through ClaimSessionWorktree: it waits for a removal
// being decided, recreates the checkout from its kept branch if it was
// removed, and keeps it from later evaluations until the run is active. A
// checkout that cannot be recreated refuses the message (ErrWorkerConflict)
// before anything is appended.
func (s *WorkerExecutionService) PrepareSessionMessage(account, user, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if strings.HasPrefix(sessionID, "worker-execution-") {
		return s.PrepareFollowup(account, user, sessionID)
	}
	if refused, err := s.taskMessageWillBeRefused(sessionID); err != nil {
		return err
	} else if refused {
		// The run admission refuses it (completed, integrated, rejected...);
		// nothing is claimed or recreated for a message that never runs.
		return nil
	}
	if _, err := s.ClaimSessionWorktree(sessionID); err != nil {
		return fmt.Errorf("%w: the session's checkout was removed after its work landed and cannot be recreated from its branch: %v", store.ErrWorkerConflict, err)
	}
	return nil
}

// taskMessageWillBeRefused reports whether the session's task refuses a
// message: the same task rules the run admission applies
// (store.TaskContinuationStateRefusal, plus a task that is not this
// session's). A session without a task is never refused here.
func (s *WorkerExecutionService) taskMessageWillBeRefused(sessionID string) (bool, error) {
	db := s.host.runs.sessions.Store()
	snap, found, err := db.GetSession(sessionID)
	if err != nil || !found {
		return false, err
	}
	projectID := mapString(snap.Metadata, "project_id")
	taskID := mapString(snap.Metadata, "task_id")
	if taskID == "" {
		taskID = mapString(snap.Metadata, "project_task_id")
	}
	if taskID == "" {
		return false, nil
	}
	task, found, err := db.GetProjectTask(snap.AccountScopeID, projectID, taskID)
	if err != nil {
		return false, err
	}
	if !found || task == nil || task.SessionID != snap.ID {
		return true, nil
	}
	return store.TaskContinuationStateRefusal(task) != nil, nil
}

// restoreRemovedRunWorktree claims a run's checkout for its follow-up,
// recreating it from its kept branch when it was removed. It never trusts
// the caller's copy of the run: the record is re-read under the slot.
func (s *WorkerExecutionService) restoreRemovedRunWorktree(snap store.SessionSnapshot, _ store.WorkerRunRecord) error {
	if !snap.WorktreeEnabled {
		return nil
	}
	if _, err := s.ClaimSessionWorktree(snap.ID); err != nil {
		return fmt.Errorf("%w: the job's checkout was removed after its work landed and cannot be recreated from its branch: %v", store.ErrWorkerConflict, err)
	}
	return nil
}
