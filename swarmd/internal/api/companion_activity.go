package api

import (
	"sort"
	"strings"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The Companion stays responsive while its work runs, through the canonical
// V3 mechanisms rather than a second event store:
//
//   - The person's message posted while a Companion run is active is a queued
//     note (acceptSessionsV3QueuedNote): stored durably and idempotently by
//     client_request_id, delivered to the running model after its next tool
//     result (never inside it), receipted once (feedback_delivery), and, when
//     the run ends without delivering it, carried by the next turn.
//   - Delegated work is followed with task watches (manage_projects
//     watch_tasks): durable, nonblocking, firing their outcomes as their own
//     wake turns, after the active run ends when one is running.
//   - Ordering, replay and reconnect are the session's durable V3 events and
//     /v3/sync cursors.
//
// What this file adds is the Companion's honest read of that state
// (CompanionActivity) and its bounded provider context on the V3 executor,
// where the focus follows only the person's own messages.

const (
	// companionActivityNoteWindow bounds the tail scanned for undelivered
	// queued input (the window the executor itself scans for receipts).
	companionActivityNoteWindow = sessionV3QueuedNoteHistoryWindow
	// companionActivityQueuedMax bounds the queued inputs listed.
	companionActivityQueuedMax = 20

	// CompanionInputQueued: the input is queued for the active run and
	// reaches it after its current tool call or step, or starts the next turn.
	CompanionInputQueued = "queued"
	// CompanionInputWaitingNextTurn: the run it was queued for was stopped;
	// it is kept in history and is read by the Companion's next turn.
	CompanionInputWaitingNextTurn = "waiting_next_turn"
)

// CompanionActivity is the read-only state of the Companion's foreground and
// background work, read from the authoritative records at request time.
type CompanionActivity struct {
	Run CompanionRunActivity `json:"run"`
	// QueuedInputs are the person's messages accepted during a run that no
	// provider step has received yet, oldest first.
	QueuedInputs     []CompanionQueuedInput `json:"queued_inputs"`
	QueuedInputsMore bool                   `json:"queued_inputs_more,omitempty"`
	// PendingPermissions counts tool approvals the run is waiting on.
	PendingPermissions int `json:"pending_permissions"`
	// TaskWatches are the delegated tasks the Companion follows, with exact
	// machine/project/task identity.
	TaskWatches []pebblestore.V3TaskWatchSummary `json:"task_watches"`
	ReadAt      int64                            `json:"read_at"`
}

// CompanionRunActivity is the Companion's current run, if any. A device
// command or other tool call in progress is not interrupted by new input;
// the input waits for it (Phase stays running).
type CompanionRunActivity struct {
	Active     bool   `json:"active"`
	RunID      string `json:"run_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Phase      string `json:"phase,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

type CompanionQueuedInput struct {
	MessageID      string `json:"message_id"`
	MessageSeq     uint64 `json:"message_seq"`
	CreatedAt      int64  `json:"created_at,omitempty"`
	QueuedForRunID string `json:"queued_for_run_id,omitempty"`
	DeliveryStatus string `json:"delivery_status"`
}

// companionActivity reads the Companion session's live work state. Every read
// is bounded: one active run intent, a fixed message tail, the session's
// watches and one pending-permission count.
func (s *Server) companionActivity(session pebblestore.SessionSnapshot) (*CompanionActivity, error) {
	out := &CompanionActivity{QueuedInputs: []CompanionQueuedInput{}, TaskWatches: []pebblestore.V3TaskWatchSummary{}, ReadAt: time.Now().UnixMilli()}
	activeRunID := ""
	intent, found, err := s.sessions.GetSessionActiveRunIntent(session.ID)
	if err != nil {
		return nil, err
	}
	if found && (intent.Status == sessionruntime.RunIntentPendingExecutor || intent.Status == sessionruntime.RunIntentRunning) {
		activeRunID = intent.RunID
		out.Run = CompanionRunActivity{Active: true, RunID: intent.RunID, Status: intent.Status, UpdatedAt: intent.UpdatedAt}
	}
	if life := session.Lifecycle; life != nil {
		if out.Run.RunID == "" || out.Run.RunID == life.RunID {
			out.Run.Phase, out.Run.UpdatedAt = life.Phase, max(out.Run.UpdatedAt, life.UpdatedAt)
			if !life.Active {
				out.Run.StopReason = life.StopReason
			}
			if out.Run.RunID == "" {
				out.Run.RunID = life.RunID
			}
		}
	}
	messages, err := s.sessions.ListSessionMessageTail(session.ID, companionActivityNoteWindow)
	if err != nil {
		return nil, err
	}
	out.QueuedInputs, out.QueuedInputsMore = companionUndeliveredInputs(messages, activeRunID)
	watches, err := s.sessions.Store().ListV3SessionTaskWatches(session.ID)
	if err != nil {
		return nil, err
	}
	out.TaskWatches = pebblestore.V3TaskWatchSummaries(watches)
	if out.TaskWatches == nil {
		out.TaskWatches = []pebblestore.V3TaskWatchSummary{}
	}
	if s.perm != nil {
		if out.PendingPermissions, err = s.perm.PendingCount(session.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// companionUndeliveredInputs returns the person's queued notes in messages
// that have no delivery receipt, oldest first, at most
// companionActivityQueuedMax. A note queued for the active run (or for a run
// that already handed it to the active follow-up turn) is queued; one whose
// run ended without starting a turn waits for the next turn.
func companionUndeliveredInputs(messages []pebblestore.MessageSnapshot, activeRunID string) ([]CompanionQueuedInput, bool) {
	receipted := make(map[string]bool)
	for _, message := range messages {
		if message.Role == "system" && message.Metadata["source"] == "feedback_delivery" {
			if id, _ := message.Metadata["message_id"].(string); id != "" {
				receipted[id] = true
			}
		}
	}
	out := []CompanionQueuedInput{}
	more := false
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].GlobalSeq < messages[j].GlobalSeq })
	for _, message := range messages {
		if !runruntime.IsQueuedNote(message) || !runruntime.CompanionPersonMessage(message) || receipted[message.ID] {
			continue
		}
		if len(out) == companionActivityQueuedMax {
			more = true
			break
		}
		runID, _ := message.Metadata[runruntime.QueuedNoteRunMetadataKey].(string)
		status := CompanionInputWaitingNextTurn
		if activeRunID != "" {
			// The active run is the one it was queued for, or the follow-up
			// that carries every undelivered note in its context.
			status = CompanionInputQueued
		}
		out = append(out, CompanionQueuedInput{MessageID: message.ID, MessageSeq: message.GlobalSeq, CreatedAt: message.CreatedAt, QueuedForRunID: strings.TrimSpace(runID), DeliveryStatus: status})
	}
	return out, more
}

// sessionV3CompanionContextMessages is the Companion's provider history on the
// V3 executor: its bounded tail and context bank instead of the full epoch
// transcript. ok is false for any other session and for checkpoint or resume
// runs, which keep their canonical context.
func (e *sessionV3Executor) sessionV3CompanionContextMessages(job sessionV3ExecutorJob, epochID string) ([]pebblestore.MessageSnapshot, bool, error) {
	if job.ResumeContext || strings.TrimSpace(job.CheckpointID) != "" || strings.TrimSpace(job.PlanID) != "" {
		return nil, false, nil
	}
	session, found, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !found || !runruntime.IsCompanionSession(session) {
		return nil, false, err
	}
	var fromSeq uint64
	epoch, found, err := e.server.sessions.GetExecutionEpoch(job.SessionID, epochID)
	if err != nil {
		return nil, false, err
	}
	if found && epoch.Boundary.Reason == pebblestore.ExecutionEpochReasonContextCleared {
		fromSeq = epoch.FirstRootSeq
	}
	scope, err := e.resolveSessionV3WorkspaceScope(session, job.Principal)
	if err != nil {
		return nil, false, err
	}
	roots := append([]string(nil), scope.Roots...)
	if len(roots) == 0 && strings.TrimSpace(scope.PrimaryPath) != "" {
		roots = []string{scope.PrimaryPath}
	}
	messages, err := runruntime.CompanionV3ContextMessages(e.server.sessions.Store(), session, roots, fromSeq, time.Now().UnixMilli())
	if err != nil {
		return nil, false, err
	}
	return messages, true, nil
}
