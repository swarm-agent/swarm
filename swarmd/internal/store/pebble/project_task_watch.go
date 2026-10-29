package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// A task watch is how a conversation follows delegated tasks without pending.
// manage_projects wait_tasks (alias watch_tasks) records one durable watch on
// the conversation and returns at once; the run that registered it goes on
// and ends its turn normally. When the watched tasks settle the daemon starts
// a new turn with their outcomes, or, while a run is active, delivers them
// right after that run ends. A person's message never ends a watch: it ends
// when it fires, when the AI cancels it, when the person stops it, or when the
// conversation is archived or deleted.
//
// Watches are their own records (not run intent state), so the conversation is
// never put into a waiting state and its run state stays idle or working.
type V3SessionTaskWatch struct {
	WatchID        string `json:"watch_id"`
	SessionID      string `json:"session_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	// OriginRunID is the run that registered the watch (or, for a migrated
	// wait, the run that was asleep on it).
	OriginRunID string `json:"origin_run_id,omitempty"`
	EpochID     string `json:"epoch_id,omitempty"`
	// Wait is what is watched: project, linked machine, until rule and the
	// pinned task attempts (followed under until=settled).
	Wait V3ProjectTaskWait `json:"wait"`
	// Titles name Wait.Tasks (same order). Linked-machine titles are
	// untrusted data from that machine.
	Titles    []string `json:"titles,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// V3TaskWatchSummary is the client view of an active watch: what the
// conversation is watching and where. Titles from linked machines are
// untrusted display data.
type V3TaskWatchSummary struct {
	WatchID   string   `json:"watch_id"`
	ProjectID string   `json:"project_id"`
	Machine   string   `json:"machine,omitempty"`
	Until     string   `json:"until,omitempty"`
	TaskIDs   []string `json:"task_ids"`
	Titles    []string `json:"titles,omitempty"`
	CreatedAt int64    `json:"created_at,omitempty"`
}

// Watch errors. Deferred means the outcomes are ready but a run is active:
// the watch stays and fires right after that run ends.
var (
	ErrProjectTaskWatchDeferred  = errors.New("project task watch outcomes wait for the active run to end")
	ErrProjectTaskAlreadyWatched = errors.New("every selected task is already watched by this conversation")
	ErrProjectTaskWatchNotFound  = errors.New("no task watch on this conversation matches")
)

// Watch transcript notice values (metadata task_wait_notice) and end causes.
const (
	ProjectTaskWaitNoticeWatching = "watching"
	ProjectTaskWaitEndedByAgent   = "agent"
)

const v3TaskWatchPrefix = "v3/task_watch/"

func keyV3SessionTaskWatchPrefix(sessionID string) string {
	return v3TaskWatchPrefix + keyPart(sessionID) + "/"
}

// KeyV3SessionTaskWatch is the durable record of one watch.
func KeyV3SessionTaskWatch(sessionID, watchID string) string {
	return keyV3SessionTaskWatchPrefix(sessionID) + keyPart(watchID)
}

// ProjectTaskWatchID is a watch's identity: deterministic for one run, one
// machine/project and one task set, so a retried registration is the same
// watch.
func ProjectTaskWatchID(sessionID, runID, machine, projectID, until string, taskIDs []string) string {
	ids := append([]string(nil), taskIDs...)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join([]string{sessionID, runID, machine, projectID, until, strings.Join(ids, "\x00")}, "\x01")))
	return "watch-" + hex.EncodeToString(sum[:12])
}

// ProjectTaskWatchResumeID is the deterministic run a fired watch starts.
func ProjectTaskWatchResumeID(watchID string) string {
	return ProjectTaskWaitResumeID(watchID)
}

func (w V3SessionTaskWatch) summary() V3TaskWatchSummary {
	ids := make([]string, 0, len(w.Wait.Tasks))
	for _, target := range w.Wait.Tasks {
		ids = append(ids, target.TaskID)
	}
	return V3TaskWatchSummary{WatchID: w.WatchID, ProjectID: w.Wait.ProjectID, Machine: w.Wait.Machine, Until: w.Wait.Until, TaskIDs: ids, Titles: projectTaskWaitNoticeNames(w.Titles), CreatedAt: w.CreatedAt}
}

// V3TaskWatchSummaries is the client view of watches, oldest first.
func V3TaskWatchSummaries(watches []V3SessionTaskWatch) []V3TaskWatchSummary {
	out := make([]V3TaskWatchSummary, 0, len(watches))
	for _, w := range watches {
		out = append(out, w.summary())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// owner is the run-intent view of a watch the outcome builders read.
func (w V3SessionTaskWatch) owner() V3SessionRunIntent {
	wait := w.Wait
	return V3SessionRunIntent{SessionID: w.SessionID, UserID: w.UserID, AccountScopeID: w.AccountScopeID, RunID: w.OriginRunID, EpochID: w.EpochID, TaskWait: &wait}
}

func listV3SessionTaskWatchesFromReader(reader pebble.Reader, sessionID string) ([]V3SessionTaskWatch, error) {
	var out []V3SessionTaskWatch
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: keyV3SessionTaskWatchPrefix(sessionID), Limit: 256}, func(_ string, raw []byte) (bool, error) {
		var watch V3SessionTaskWatch
		if err := json.Unmarshal(raw, &watch); err != nil {
			return false, err
		}
		out = append(out, watch)
		return true, nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, err
}

// ListV3SessionTaskWatches lists a conversation's active watches, oldest first.
func (s *SessionStore) ListV3SessionTaskWatches(sessionID string) ([]V3SessionTaskWatch, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return listV3SessionTaskWatchesFromReader(s.store.db, sessionID)
}

// GetV3SessionTaskWatch reads one watch.
func (s *SessionStore) GetV3SessionTaskWatch(sessionID, watchID string) (V3SessionTaskWatch, bool, error) {
	var watch V3SessionTaskWatch
	ok, err := s.store.GetJSON(KeyV3SessionTaskWatch(strings.TrimSpace(sessionID), strings.TrimSpace(watchID)), &watch)
	return watch, ok, err
}

// ListV3TaskWatchesPaged lists every conversation's watches (startup recovery
// and task publication). afterKey is the last key of the previous page.
func (s *SessionStore) ListV3TaskWatchesPaged(afterKey string, limit int) ([]V3SessionTaskWatch, string, error) {
	if limit <= 0 {
		limit = 64
	}
	start := ""
	if afterKey != "" {
		start = afterKey + "\x00"
	}
	var out []V3SessionTaskWatch
	next := ""
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: v3TaskWatchPrefix, StartKey: start, Limit: limit}, func(key string, raw []byte) (bool, error) {
		next = key
		var watch V3SessionTaskWatch
		if err := json.Unmarshal(raw, &watch); err != nil {
			return false, err
		}
		out = append(out, watch)
		return true, nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) < limit {
		next = ""
	}
	return out, next, nil
}

// withV3TaskWatches adds a conversation's active watches to its run state, so
// a snapshot alone tells clients what it watches.
func withV3TaskWatches(reader pebble.Reader, state V3SessionRunState) (V3SessionRunState, error) {
	watches, err := listV3SessionTaskWatchesFromReader(reader, state.SessionID)
	if err != nil || len(watches) == 0 {
		return state, err
	}
	state.TaskWatches = V3TaskWatchSummaries(watches)
	return state, nil
}

// projectTaskWatchCount says "N task(s)" in words.
func projectTaskWatchCount(n int) string {
	if n == 1 {
		return "1 task"
	}
	return fmt.Sprintf("%d tasks", n)
}

// ProjectTaskWatchLine is the transcript line shown when a watch starts.
func ProjectTaskWatchLine(titles []string, machine string) string {
	where := ""
	if machine = strings.TrimSpace(machine); machine != "" {
		where = " on " + machine
	}
	return fmt.Sprintf("Watching %s%s: %s.", projectTaskWatchCount(len(titles)), where, strings.Join(projectTaskWaitNoticeNames(titles), ", "))
}

// ProjectTaskWatchNotice is what wait_tasks tells the AI: the watch started,
// so it answers the person now and ends its turn normally.
func ProjectTaskWatchNotice(titles []string, machine string) string {
	where := ""
	if machine = strings.TrimSpace(machine); machine != "" {
		where = " on " + machine
	}
	return fmt.Sprintf("Watching %d task(s)%s: %s. Reply to the person now and end your turn; a new turn starts with the outcomes when they settle.", len(titles), where, strings.Join(projectTaskWaitNoticeNames(titles), ", "))
}

// projectTaskWatchEndLine is the transcript line when a watch ends.
func projectTaskWatchEndLine(endedBy string, titles []string) string {
	names := strings.Join(projectTaskWaitNoticeNames(titles), ", ")
	if endedBy == ProjectTaskWaitEndedByTasks {
		return "Tasks settled: " + names + "."
	}
	return "Stopped watching: " + names + "."
}

func projectTaskWatchNoticeMetadata(watch V3SessionTaskWatch, notice string) map[string]any {
	metadata := map[string]any{
		"source":           ProjectTaskWaitNoticeSource,
		"task_wait_notice": notice,
		"task_watch_id":    watch.WatchID,
		"project_id":       watch.Wait.ProjectID,
		"task_count":       len(watch.Wait.Tasks),
		"task_titles":      projectTaskWaitNoticeNames(watch.Titles),
	}
	if watch.Wait.Machine != "" {
		// Linked-machine titles come from that machine.
		metadata["machine"], metadata["untrusted_content"] = watch.Wait.Machine, true
	}
	return metadata
}

// prepareProjectTaskWatchRegistration turns a validated wait into a watch:
// tasks this conversation already watches are skipped (no duplicate wakes),
// and the start line is the mutation's message. The run intent is untouched.
func (s *SessionStore) prepareProjectTaskWatchRegistration(input *V3SessionMutationInput, owner V3SessionRunIntent, wait *V3ProjectTaskWait, titles []string) error {
	existing, err := s.ListV3SessionTaskWatches(input.SessionID)
	if err != nil {
		return err
	}
	watched := map[string]bool{}
	for _, w := range existing {
		if w.Wait.Machine != wait.Machine || w.Wait.ProjectID != wait.ProjectID {
			continue
		}
		for _, target := range w.Wait.Tasks {
			watched[target.TaskID] = true
		}
	}
	tasks := make([]V3ProjectTaskWaitTarget, 0, len(wait.Tasks))
	names := make([]string, 0, len(wait.Tasks))
	ids := make([]string, 0, len(wait.Tasks))
	for i, target := range wait.Tasks {
		if watched[target.TaskID] {
			continue
		}
		tasks = append(tasks, target)
		names = append(names, titles[i])
		ids = append(ids, target.TaskID)
	}
	if len(tasks) == 0 {
		return ErrProjectTaskAlreadyWatched
	}
	wait.Tasks = tasks
	wait.Notice = ProjectTaskWatchLine(names, wait.Machine)
	now := input.NowUnixMs
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	watch := V3SessionTaskWatch{
		WatchID: ProjectTaskWatchID(input.SessionID, owner.RunID, wait.Machine, wait.ProjectID, wait.Until, ids), SessionID: input.SessionID,
		UserID: input.UserID, AccountScopeID: input.AccountScopeID, OriginRunID: owner.RunID, EpochID: owner.EpochID,
		Wait: *wait, Titles: names, CreatedAt: now,
	}
	input.Kind, input.EventType, input.RunIntent = V3SessionMutationAppendMessage, "session.task_watch.started", nil
	input.Message = &MessageSnapshot{ID: watch.WatchID + "-watch-notice", Role: "system", Content: wait.Notice, Metadata: projectTaskWatchNoticeMetadata(watch, ProjectTaskWaitNoticeWatching)}
	input.taskWatchSet = []V3SessionTaskWatch{watch}
	input.TaskWait.RegisteredWatchID, input.TaskWait.RegisteredTaskIDs, input.TaskWait.RegisteredTitles = watch.WatchID, ids, names
	after := V3TaskWatchSummaries(append(existing, watch))
	input.taskWatchesAfter = &after
	input.taskWaitNotice = true
	return nil
}

// prepareProjectTaskWatchWake fires a watch whose outcomes are ready: on an
// idle conversation it starts the outcome turn and retires the watch in the
// same batch; while a run is active it refuses with ErrProjectTaskWatchDeferred
// and the watch fires right after that run (the executor re-checks then).
func (s *SessionStore) prepareProjectTaskWatchWake(input *V3SessionMutationInput, parent SessionSnapshot) error {
	op := input.TaskWait
	watch, found, err := s.GetV3SessionTaskWatch(input.SessionID, op.WatchID)
	if err != nil {
		return err
	}
	if !found || watch.AccountScopeID != input.AccountScopeID || watch.UserID != input.UserID || parent.AccountScopeID != input.AccountScopeID || parent.UserID != input.UserID {
		return ErrProjectTaskWaitStale
	}
	if op.Machine != "" && op.Machine != watch.Wait.Machine {
		return ErrProjectTaskWaitStale
	}
	if tomb, ok, err := s.GetV3SessionTombstone(input.SessionID); err != nil {
		return err
	} else if ok && (tomb.Archived || tomb.Deleted) {
		return ErrProjectTaskWaitStale
	}
	var raw []byte
	if watch.Wait.Machine != "" {
		raw, err = projectTaskWatchRemoteOutcome(op, watch.Wait)
	} else {
		if watch.Wait.ProjectID != ProjectConversationID(parent) {
			return ErrProjectTaskWaitStale
		}
		raw, err = s.projectTaskWatchLocalOutcome(input, watch.owner())
	}
	if err != nil {
		return err
	}
	state, found, err := s.GetV3SessionRunState(input.SessionID)
	if err != nil {
		return err
	}
	if found && state.Active {
		return ErrProjectTaskWatchDeferred
	}
	epochID := watch.EpochID
	if epoch, ok, err := s.GetActiveExecutionEpoch(input.SessionID); err != nil {
		return err
	} else if ok {
		epochID = epoch.EpochID
	}
	runID := ProjectTaskWatchResumeID(watch.WatchID)
	messageID := runID + "-result"
	input.RunIntent = &V3SessionRunIntent{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, RunID: runID, SourceMessageID: messageID, Status: V3RunIntentPendingExecutor, EpochID: epochID, ResumeContext: true}
	input.Kind, input.EventType = V3SessionMutationAppendMessage, "session.run.tasks_ready"
	input.Message = &MessageSnapshot{ID: messageID, Role: "system", Content: "Delegated project task outcomes:\n" + string(raw), Metadata: map[string]any{"task_watch_id": watch.WatchID, "task_wait_owner_run_id": watch.OriginRunID}}
	input.taskWatchDelete = []V3SessionTaskWatch{watch}
	remaining, err := s.ListV3SessionTaskWatches(input.SessionID)
	if err != nil {
		return err
	}
	after := V3TaskWatchSummaries(withoutTaskWatch(remaining, watch.WatchID))
	input.taskWatchesAfter = &after
	if input.taskWaitEnded != nil {
		*input.taskWaitEnded = &projectTaskWaitEnd{watch: &watch, endedBy: ProjectTaskWaitEndedByTasks}
	}
	return nil
}

func withoutTaskWatch(watches []V3SessionTaskWatch, watchID string) []V3SessionTaskWatch {
	out := make([]V3SessionTaskWatch, 0, len(watches))
	for _, w := range watches {
		if w.WatchID != watchID {
			out = append(out, w)
		}
	}
	return out
}

// prepareProjectTaskWatchCancel ends watches: the AI's cancel (wait_tasks
// cancel=true or cancel_watch) or the person's stop. TaskIDs narrows it to
// those tasks (a watch left with none ends); WatchID, Machine and ProjectID
// narrow which watches. Nothing matching is an error, never a silent success.
func (s *SessionStore) prepareProjectTaskWatchCancel(input *V3SessionMutationInput, parent SessionSnapshot) error {
	op := input.TaskWait
	if parent.AccountScopeID != input.AccountScopeID || parent.UserID != input.UserID {
		return ErrProjectTaskWaitStale
	}
	existing, err := s.ListV3SessionTaskWatches(input.SessionID)
	if err != nil {
		return err
	}
	cancelIDs := map[string]bool{}
	for _, id := range op.TaskIDs {
		cancelIDs[strings.TrimSpace(id)] = true
	}
	var sets, deletes, kept []V3SessionTaskWatch
	var removedTitles, removedIDs []string
	for _, w := range existing {
		if (op.WatchID != "" && w.WatchID != op.WatchID) || (op.Machine != "" && w.Wait.Machine != op.Machine) || (op.ProjectID != "" && w.Wait.ProjectID != op.ProjectID) {
			kept = append(kept, w)
			continue
		}
		next := w
		next.Wait.Tasks, next.Titles = nil, nil
		removed := false
		for i, target := range w.Wait.Tasks {
			title := target.TaskID
			if i < len(w.Titles) {
				title = w.Titles[i]
			}
			if len(cancelIDs) == 0 || cancelIDs[target.TaskID] {
				removed = true
				removedTitles, removedIDs = append(removedTitles, title), append(removedIDs, target.TaskID)
				continue
			}
			next.Wait.Tasks, next.Titles = append(next.Wait.Tasks, target), append(next.Titles, title)
		}
		switch {
		case !removed:
			kept = append(kept, w)
		case len(next.Wait.Tasks) == 0:
			deletes = append(deletes, w)
		default:
			next.Wait.Notice = ProjectTaskWatchLine(next.Titles, next.Wait.Machine)
			sets, kept = append(sets, next), append(kept, next)
		}
	}
	if len(removedIDs) == 0 {
		return ErrProjectTaskWatchNotFound
	}
	endedBy := op.CancelledBy
	if endedBy == "" {
		endedBy = ProjectTaskWaitEndedByAgent
	}
	sort.Strings(removedIDs)
	sum := sha256.Sum256([]byte(input.SessionID + "\x00" + input.ClientRequestID + "\x00" + strings.Join(removedIDs, "\x00")))
	metadata := map[string]any{"source": ProjectTaskWaitNoticeSource, "task_wait_notice": ProjectTaskWaitNoticeEnded, "ended_by": endedBy, "task_count": len(removedIDs), "task_titles": projectTaskWaitNoticeNames(removedTitles)}
	input.Kind, input.EventType, input.RunIntent = V3SessionMutationAppendMessage, "session.task_watch.cancelled", nil
	input.Message = &MessageSnapshot{ID: "watch-cancel-" + hex.EncodeToString(sum[:12]), Role: "system", Content: projectTaskWatchEndLine(endedBy, removedTitles), Metadata: metadata}
	input.taskWatchSet, input.taskWatchDelete = sets, deletes
	after := V3TaskWatchSummaries(kept)
	input.taskWatchesAfter = &after
	input.taskWaitNotice = true
	op.CancelledTaskIDs = removedIDs
	return nil
}

// prepareProjectTaskWaitMigration converts a run asleep on a task wait (the
// retired pending model) into a watch and ends that run as completed, so no
// conversation stays pending after an upgrade.
func (s *SessionStore) prepareProjectTaskWaitMigration(input *V3SessionMutationInput, parent SessionSnapshot) error {
	op := input.TaskWait
	owner, found, err := s.GetV3SessionRunIntent(input.SessionID, op.RunID)
	if err != nil {
		return err
	}
	if !found || owner.Status != V3RunIntentWaitingTasks || owner.TaskWait == nil || owner.AccountScopeID != input.AccountScopeID || owner.UserID != input.UserID || parent.AccountScopeID != input.AccountScopeID || parent.UserID != input.UserID {
		return ErrProjectTaskWaitStale
	}
	wait := *owner.TaskWait
	wait.Tasks = append([]V3ProjectTaskWaitTarget(nil), owner.TaskWait.Tasks...)
	if wait.Until == "" {
		wait.Until = ProjectTaskWaitUntilSettled
	}
	titles := make([]string, 0, len(wait.Tasks))
	ids := make([]string, 0, len(wait.Tasks))
	for _, target := range wait.Tasks {
		title := ""
		if wait.Machine == "" {
			if task, ok, err := s.GetProjectTask(input.AccountScopeID, wait.ProjectID, target.TaskID); err != nil {
				return err
			} else if ok {
				title = task.Title
			}
		}
		titles = append(titles, projectTaskWaitNoticeTitle(title, target.TaskID))
		ids = append(ids, target.TaskID)
	}
	wait.Notice = ProjectTaskWatchLine(titles, wait.Machine)
	now := input.NowUnixMs
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	watch := V3SessionTaskWatch{
		WatchID: ProjectTaskWatchID(input.SessionID, owner.RunID, wait.Machine, wait.ProjectID, wait.Until, ids), SessionID: input.SessionID,
		UserID: owner.UserID, AccountScopeID: owner.AccountScopeID, OriginRunID: owner.RunID, EpochID: owner.EpochID,
		Wait: wait, Titles: titles, CreatedAt: now,
	}
	next := owner
	next.Status, next.TaskWait, next.BlockedReason = V3RunIntentCompleted, nil, "task wait converted to a background watch"
	input.Kind, input.EventType, input.Message = V3SessionMutationRecordRunIntent, "session.task_watch.migrated", nil
	input.RunIntent = &next
	input.taskWatchSet = []V3SessionTaskWatch{watch}
	existing, err := s.ListV3SessionTaskWatches(input.SessionID)
	if err != nil {
		return err
	}
	after := V3TaskWatchSummaries(append(withoutTaskWatch(existing, watch.WatchID), watch))
	input.taskWatchesAfter = &after
	return nil
}

// setProjectTaskWatchesInBatch writes the watch changes prepared for this
// mutation, and drops every watch of a conversation being archived or deleted.
func (s *SessionStore) setProjectTaskWatchesInBatch(batch *pebble.Batch, input V3SessionMutationInput) error {
	if input.Kind == V3SessionMutationArchiveSession || input.Kind == V3SessionMutationDeleteSession {
		return s.deleteSessionTaskWatchesInBatch(batch, input.SessionID)
	}
	for _, watch := range input.taskWatchDelete {
		if err := batch.Delete([]byte(KeyV3SessionTaskWatch(watch.SessionID, watch.WatchID)), nil); err != nil {
			return err
		}
	}
	for _, watch := range input.taskWatchSet {
		raw, err := json.Marshal(watch)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3SessionTaskWatch(watch.SessionID, watch.WatchID)), raw, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) deleteSessionTaskWatchesInBatch(batch *pebble.Batch, sessionID string) error {
	watches, err := s.ListV3SessionTaskWatches(sessionID)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if err := batch.Delete([]byte(KeyV3SessionTaskWatch(watch.SessionID, watch.WatchID)), nil); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileProjectTaskWaits fires the local watches whose tasks settled. It
// runs on committed task publication, once on startup and after a run ends.
// The durable watch records are the recovery authority, not callbacks. Each
// page is bounded; a watch not ready, or deferred by an active run, stays.
func (s *SessionStore) ReconcileProjectTaskWaits(account, project string, publish func(V3SessionMutationResult)) error {
	after := ""
	for {
		watches, next, err := s.ListV3TaskWatchesPaged(after, 64)
		if err != nil {
			return err
		}
		for _, watch := range watches {
			// Linked-machine watches resolve only from what that machine reports.
			if watch.Wait.Machine != "" || (account != "" && watch.AccountScopeID != account) || (project != "" && watch.Wait.ProjectID != project) {
				continue
			}
			if err := s.fireProjectTaskWatch(watch, publish); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// ReconcileSessionTaskWatches fires one conversation's ready local watches
// (after its run ended, a deferred watch is delivered now).
func (s *SessionStore) ReconcileSessionTaskWatches(sessionID string, publish func(V3SessionMutationResult)) error {
	watches, err := s.ListV3SessionTaskWatches(sessionID)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if watch.Wait.Machine != "" {
			continue
		}
		if err := s.fireProjectTaskWatch(watch, publish); err != nil {
			return err
		}
	}
	return nil
}

// fireProjectTaskWatch tries one local watch. Only a real failure is an error.
func (s *SessionStore) fireProjectTaskWatch(watch V3SessionTaskWatch, publish func(V3SessionMutationResult)) error {
	key := "task-watch-wake:" + watch.WatchID
	result, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: watch.SessionID, UserID: watch.UserID, AccountScopeID: watch.AccountScopeID, Kind: V3SessionMutationAppendMessage, EventType: "session.run.tasks_ready", ClientRequestID: key, PayloadHash: key, TaskWait: &V3ProjectTaskWaitMutation{WatchID: watch.WatchID, Wake: true}})
	if errors.Is(err, ErrProjectTaskWaitNotReady) || errors.Is(err, ErrProjectTaskWaitStale) || errors.Is(err, ErrProjectTaskWatchDeferred) {
		return nil
	}
	if err != nil {
		// Another run may have started between the state read and the
		// commit; the watch stays and fires after that run.
		if state, ok, readErr := s.GetV3SessionRunState(watch.SessionID); readErr == nil && ok && state.Active {
			return nil
		}
		return fmt.Errorf("reconcile project task watch: %w", err)
	}
	if publish != nil {
		publish(result)
	}
	return nil
}

// MigrateProjectTaskWaitsToWatches converts every run still asleep on a task
// wait into a watch and ends that run, so the conversation is idle. It runs
// once on startup before watches are reconciled; it is idempotent.
func (s *SessionStore) MigrateProjectTaskWaitsToWatches() (int, error) {
	converted := 0
	after := ""
	for {
		waits, next, err := s.ListV3SessionRunIntentsByStatusPaged(V3RunIntentWaitingTasks, after, 64)
		if err != nil {
			return converted, err
		}
		for _, owner := range waits {
			if owner.TaskWait == nil {
				continue
			}
			key := "task-wait-migrate:" + owner.RunID
			_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: owner.SessionID, UserID: owner.UserID, AccountScopeID: owner.AccountScopeID, Kind: V3SessionMutationRecordRunIntent, EventType: "session.task_watch.migrated", ClientRequestID: key, PayloadHash: key, TaskWait: &V3ProjectTaskWaitMutation{RunID: owner.RunID, Migrate: true}})
			if errors.Is(err, ErrProjectTaskWaitStale) {
				continue
			}
			if err != nil {
				return converted, fmt.Errorf("convert task wait %s to a watch: %w", owner.RunID, err)
			}
			converted++
		}
		if next == "" {
			return converted, nil
		}
		after = next
	}
}
