package v3chat

import (
	"strings"
	"sync"

	"swarm-refactor/swarmtui/internal/client"
)

// Store is the sole mutable owner. State snapshots remain detached and can be
// rendered without holding the store lock.
type Store struct {
	mu    sync.RWMutex
	state State
}

func NewStore() *Store { return &Store{state: NewState()} }

func (s *Store) Dispatch(action Action) State {
	if s == nil {
		return NewState()
	}
	s.mu.Lock()
	s.state = Reduce(s.state, action)
	next := cloneState(s.state)
	s.mu.Unlock()
	return next
}

func (s *Store) Snapshot() State {
	if s == nil {
		return NewState()
	}
	s.mu.RLock()
	next := cloneState(s.state)
	s.mu.RUnlock()
	return next
}

func SelectTitle(state State) string       { return state.Session.Title }
func SelectMessages(state State) []Message { return append([]Message(nil), state.Messages...) }
func SelectRoutedDraft(state State) (RoutedDraft, bool) {
	if state.RoutedDraft == nil {
		return RoutedDraft{}, false
	}
	draft := *state.RoutedDraft
	draft.Metadata = cloneAnyMap(state.RoutedDraft.Metadata)
	return draft, true
}
func SelectPending(state State) []PendingMessage {
	out := make([]PendingMessage, 0, len(state.Pending))
	for _, pending := range state.Pending {
		out = append(out, pending)
	}
	return out
}
func SelectActiveRun(state State) (RunState, bool) {
	if state.CurrentRun == nil || !runStatusActive(state.CurrentRun.Status) {
		return RunState{}, false
	}
	return *state.CurrentRun, true
}
func SelectLatestRun(state State) (RunState, bool) {
	if state.LatestRun == nil {
		return RunState{}, false
	}
	return *state.LatestRun, true
}
func SelectLiveSegments(state State) []LiveSegment {
	out := make([]LiveSegment, 0, len(state.Live))
	for _, segment := range state.Live {
		out = append(out, segment)
	}
	return out
}
func SelectReasoningSegments(state State) []ReasoningSegment {
	out := make([]ReasoningSegment, 0, len(state.Reasoning))
	for _, segment := range state.Reasoning {
		out = append(out, segment)
	}
	return out
}
func SelectLiveTools(state State) []ToolTimelineItem {
	out := make([]ToolTimelineItem, 0, len(state.Tools))
	for _, item := range state.Tools {
		out = append(out, item)
	}
	return out
}
func SelectReconnect(state State) (ConnectionStatus, bool, string) {
	return state.Connection, state.NeedsRehydrate, state.StaleReason
}
func SelectModel(state State) ModelState        { return state.Model }
func SelectUsage(state State) UsageState        { return state.Usage }
func SelectCursor(state State) (string, uint64) { return state.EndpointCursor, state.LastEventSeq }
func SelectPermissions(state State) []PermissionTimelineItem {
	return append([]PermissionTimelineItem(nil), state.Permissions.Records...)
}

func SelectPendingPermissions(state State) []client.PermissionRecord {
	items := SelectPermissions(state)
	out := make([]client.PermissionRecord, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Record.Status), "pending") {
			out = append(out, item.Record)
		}
	}
	return out
}

type PlanHeader struct {
	Active          bool
	StatusLabel     string
	CheckpointLabel string
}

func SelectPlanHeader(state State) PlanHeader {
	plan := state.Plan.ActivePlan
	if !state.Plan.HasActivePlan || plan == nil || plan.Document == nil {
		return PlanHeader{}
	}
	document := plan.Document
	activeID := firstNonEmpty(document.ActiveCheckpointID)
	if activeID == "" && document.ExecutionState != nil {
		activeID = strings.TrimSpace(document.ExecutionState.LastCheckpointID)
	}
	var checkpoint *client.SessionPlanCheckpoint
	for i := range document.Checkpoints {
		if document.Checkpoints[i].ID == activeID {
			checkpoint = &document.Checkpoints[i]
			break
		}
	}
	status := firstNonEmpty(document.Status, plan.Status)
	lastOutcome := ""
	if document.ExecutionState != nil {
		status = firstNonEmpty(document.ExecutionState.Status, status)
		lastOutcome = document.ExecutionState.LastOutcome
	}
	checkpointStatus := firstNonEmpty(valueOrEmpty(checkpoint, func(value *client.SessionPlanCheckpoint) string { return value.Status }), lastOutcome)
	return PlanHeader{
		Active:          true,
		StatusLabel:     planHeaderStatusLabel(status, checkpointStatus, checkpoint, document.Checkpoints),
		CheckpointLabel: planCheckpointLabel(checkpoint),
	}
}

func planHeaderStatusLabel(status, checkpointStatus string, checkpoint *client.SessionPlanCheckpoint, checkpoints []client.SessionPlanCheckpoint) string {
	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	normalizedCheckpoint := strings.ToLower(strings.TrimSpace(checkpointStatus))
	if normalizedStatus == "waiting_review" || normalizedCheckpoint == "needs_review" || checkpoint != nil && checkpoint.Review != nil && strings.EqualFold(checkpoint.Review.Status, "pending") {
		return "Waiting review"
	}
	if normalizedStatus == "completed" || allCheckpointsCompleted(checkpoints) {
		return "Completed"
	}
	if normalizedStatus == "paused" || normalizedCheckpoint == "paused" {
		return "Paused"
	}
	if normalizedStatus == "blocked" || normalizedCheckpoint == "blocked" {
		return "Blocked"
	}
	if normalizedStatus == "failed" || normalizedCheckpoint == "failed" {
		return "Failed"
	}
	return humanizePlanStatus(firstNonEmpty(checkpointStatus, status, "ready"))
}

func allCheckpointsCompleted(checkpoints []client.SessionPlanCheckpoint) bool {
	if len(checkpoints) == 0 {
		return false
	}
	for _, checkpoint := range checkpoints {
		if !strings.EqualFold(strings.TrimSpace(checkpoint.Status), "completed") {
			return false
		}
	}
	return true
}

func planCheckpointLabel(checkpoint *client.SessionPlanCheckpoint) string {
	if checkpoint == nil {
		return ""
	}
	id := strings.TrimSpace(checkpoint.ID)
	title := strings.TrimSpace(checkpoint.Title)
	return strings.TrimSpace(strings.Join([]string{id, title}, " "))
}

func humanizePlanStatus(value string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(value)))
	if len(words) == 0 {
		return "Unknown"
	}
	for i := range words {
		words[i] = strings.ToUpper(words[i][:1]) + strings.ToLower(words[i][1:])
	}
	return strings.Join(words, " ")
}

func valueOrEmpty[T any](value *T, selectValue func(*T) string) string {
	if value == nil {
		return ""
	}
	return selectValue(value)
}
