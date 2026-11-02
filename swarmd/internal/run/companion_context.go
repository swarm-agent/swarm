package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A Companion turn never loads the whole conversation. It is assembled from a
// bounded recent tail of the canonical V3 session plus a bounded, retrieved
// context bank block (pebblestore companion_context.go): current focus,
// decisions, earlier topics relevant to this turn and work pointers whose
// status is read live. Every limit below is fixed, so the reads and prompt
// bytes of a turn do not grow with the Companion's history.
const (
	companionTailMessages      = 40
	companionTailBytes         = 64 * 1024
	companionContextBlockBytes = 12 * 1024
	// companionTurnReserveBytes is held back from every recovery level's
	// history ceiling for what the in-progress turn adds after assembly: its
	// tool calls, tool results and queued notes appended by the provider loop.
	companionTurnReserveBytes  = 16 * 1024
	companionRetrievedMax      = 8
	companionDecisionMax       = 4
	companionWorkMax           = 6
	companionExcerptBytes      = 480
	companionIndexTitleBytes   = 120
	companionIndexSummaryBytes = 600
	// A message with fewer substantive keywords ("yes, do that") is not a new
	// topic: it neither creates a record nor moves the focus.
	companionIndexMinKeywords = 2

	companionContextBlockHeader = "Companion context bank"
)

// CompanionContextBudget is one fixed size of a Companion turn's provider
// history: at most TailMessages recent messages within TailBytes, plus a
// context bank block of at most ContextBlockBytes.
type CompanionContextBudget struct {
	TailMessages      int
	TailBytes         int
	ContextBlockBytes int
}

// companionContextBudgets is the single definition of every Companion
// context size. Level 0 is an ordinary turn. When the provider rejects a
// Companion request as a context overflow, the run is retried once at each
// next level and then fails; it never compacts or scans the whole
// conversation (a Companion has no whole-history compaction). Each recovery
// level is a fixed history ceiling minus companionTurnReserveBytes and its
// block, so the reads and prompt bytes of every attempt are bounded.
var companionContextBudgets = [...]CompanionContextBudget{
	{TailMessages: companionTailMessages, TailBytes: companionTailBytes, ContextBlockBytes: companionContextBlockBytes},
	// 48 KiB history ceiling.
	{TailMessages: 16, TailBytes: 48*1024 - companionTurnReserveBytes - 4*1024, ContextBlockBytes: 4 * 1024},
	// 24 KiB history ceiling.
	{TailMessages: 6, TailBytes: 24*1024 - companionTurnReserveBytes - 2*1024, ContextBlockBytes: 2 * 1024},
}

// CompanionContextBudgetLevels is the number of Companion context sizes: the
// ordinary turn plus CompanionContextBudgetLevels-1 overflow retries.
const CompanionContextBudgetLevels = len(companionContextBudgets)

// CompanionContextBudgetAt returns the budget of level, or false when level
// is outside the fixed ladder (recovery is exhausted).
func CompanionContextBudgetAt(level int) (CompanionContextBudget, bool) {
	if level < 0 || level >= len(companionContextBudgets) {
		return CompanionContextBudget{}, false
	}
	return companionContextBudgets[level], true
}

// CompanionTurnReserveBytes is the per-request reserve for the in-progress
// turn's own tool and event outcomes (see companionTurnReserveBytes).
const CompanionTurnReserveBytes = companionTurnReserveBytes

// ErrCompanionCompactionRefused is returned for any compaction request on a
// Companion session. Compaction summarizes the whole conversation, and a
// Companion's history is unbounded; its context is already bounded per turn
// and overflow is recovered with smaller fixed budgets instead.
var ErrCompanionCompactionRefused = errors.New("companion sessions are not compacted: each turn reads a bounded context, and an overflow is retried with smaller fixed budgets")

// companionPairedTail drops tool results at the start of a tail cut whose
// call is not in the tail. A stored tool record carries its own call and
// result, so only a record that cannot be decoded as one (a bare result) is
// an orphan; reasoning before the first message is dropped too. Assistant
// tool calls are never cut from their results, since the tail is a suffix.
func companionPairedTail(tail []pebblestore.MessageSnapshot) []pebblestore.MessageSnapshot {
	start := 0
	for start < len(tail) {
		role := strings.ToLower(strings.TrimSpace(tail[start].Role))
		if role == "reasoning" || (role == "tool" && !companionSelfPairedToolMessage(tail[start].Content)) {
			start++
			continue
		}
		break
	}
	return tail[start:]
}

func companionSelfPairedToolMessage(content string) bool {
	if record, ok := decodeV3ProviderManagedToolResultRecord(content); ok && strings.TrimSpace(record.ToolName) != "" && strings.TrimSpace(record.CallID) != "" {
		return true
	}
	record, ok := decodeToolHistoryRecord(content)
	return ok && strings.TrimSpace(record.Tool) != "" && strings.TrimSpace(record.CallID) != ""
}

func isCompanionSession(session pebblestore.SessionSnapshot) bool {
	_, marked := session.Metadata[pebblestore.SessionCompanionMetadataKey]
	return marked
}

// IsCompanionSession reports whether session is an account user's Companion.
func IsCompanionSession(session pebblestore.SessionSnapshot) bool {
	return isCompanionSession(session)
}

// companionWatchesMax bounds the active task watches a Companion turn lists.
const companionWatchesMax = 8

// CompanionPersonMessage reports whether message is the person's own words:
// a user-role message that is not a background wake (task, command,
// environment or machine watch outcome), not a note queued by a task
// (message_task) and not a runtime receipt. Only such messages become topics
// or move the Companion's focus, so background results never take over the
// conversation the person is having.
func CompanionPersonMessage(message pebblestore.MessageSnapshot) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || strings.TrimSpace(message.Content) == "" {
		return false
	}
	for _, key := range []string{"wake_id", "task_watch_id", "wake_note_run_id", "redelivered_note_id"} {
		if value, ok := message.Metadata[key]; ok && value != nil && value != "" {
			return false
		}
	}
	if source, _ := message.Metadata[pebblestore.ProjectTaskMessageSourceMetadata].(string); strings.TrimSpace(source) != "" {
		return false
	}
	switch source, _ := message.Metadata["source"].(string); source {
	case pebblestore.SessionWakeSourceBackgroundCommand, pebblestore.SessionWakeSourceTaskWatch, pebblestore.SessionWakeSourceEnvironmentWatch, pebblestore.SessionWakeSourceMachineWatch, "feedback_delivery":
		return false
	}
	return true
}

func companionOneLine(text string, maxBytes int) string {
	return truncateUTF8Bytes(strings.Join(strings.Fields(text), " "), maxBytes)
}

func companionSourceLabel(record pebblestore.CompanionContextRecord) string {
	src := record.Source
	label := fmt.Sprintf("%s %s rev %d; source session %s", record.Kind, record.ID, record.Revision, src.SessionID)
	if src.Kind == pebblestore.CompanionContextSourceMessage {
		label += fmt.Sprintf(" message #%d", src.MessageSeq)
		if len(src.ContentSHA256) >= 12 {
			label += " sha256:" + src.ContentSHA256[:12]
		}
	}
	if record.WorkspacePath != "" {
		label += "; workspace " + record.WorkspacePath
	}
	return "[" + label + "]"
}

func companionLiveWorkStatus(session *pebblestore.SessionSnapshot) string {
	if session == nil {
		return "unavailable"
	}
	life := session.Lifecycle
	if life == nil {
		return "idle"
	}
	status := "idle"
	if life.Active {
		status = "running"
	}
	if phase := strings.TrimSpace(life.Phase); phase != "" {
		status += " (" + phase + ")"
	}
	if stop := strings.TrimSpace(life.StopReason); stop != "" && !life.Active {
		status += ", stopped: " + companionOneLine(stop, 120)
	}
	return status
}

func (s *Service) companionContextStore() *pebblestore.SessionStore {
	if s == nil || s.sessions == nil {
		return nil
	}
	return s.sessions.Store()
}

// recordCompanionRunMessage brings the Companion's state up to the run's
// user message. A message written through the V3 mutation boundary is an
// event of the Companion's log, so the state maintainer folds it in with
// everything before it; a message appended outside V3 (no event) is only
// indexed as a topic and made the focus, as it is not in the log.
func (s *Service) recordCompanionRunMessage(session pebblestore.SessionSnapshot, message pebblestore.MessageSnapshot, viaV3 bool) error {
	store := s.companionContextStore()
	now := time.Now().UnixMilli()
	if viaV3 {
		if _, err := MaintainCompanionState(store, session, CompanionMaintainRunStartEvents, now); err != nil && !errors.Is(err, ErrCompanionNotCurrent) {
			return fmt.Errorf("maintain companion state: %w", err)
		}
		return nil
	}
	if store == nil {
		return errors.New("companion context store unavailable")
	}
	record, indexed, err := indexCompanionTopic(store, session, message, now)
	if err != nil || !indexed {
		return err
	}
	return store.SetCompanionFocus(session.AccountScopeID, session.UserID, pebblestore.CompanionFocus{RecordID: record.ID, Keywords: record.Keywords, MessageSeq: message.GlobalSeq, UpdatedAt: now})
}

func (s *Service) assembleCompanionTurn(session pebblestore.SessionSnapshot, prompt string, roots []string) (companionTurnContext, error) {
	turn, err := assembleCompanionTurnContext(s.companionContextStore(), session, prompt, roots, time.Now().UnixMilli())
	if err != nil {
		return turn, err
	}
	companionReportDebug(session.ID, turn.Report)
	return turn, nil
}
