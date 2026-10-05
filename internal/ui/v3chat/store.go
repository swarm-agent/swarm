package v3chat

import (
	"strings"
	"sync"

	"swarm-refactor/swarmtui/internal/client"
)

// Store is the sole mutable owner. State snapshots remain detached and can be
// rendered without holding the store lock.
type Store struct {
	mu              sync.RWMutex
	state           State
	revision        uint64
	contentRevision uint64
	sessionRevision uint64
}

func NewStore() *Store {
	return &Store{
		state:           NewState(),
		revision:        1,
		contentRevision: 1,
		sessionRevision: 1,
	}
}

func (s *Store) Dispatch(action Action) State {
	next, _ := s.DispatchWithDomain(action)
	return next
}

// DispatchWithDomain reduces action, updates revisions accordingly, and returns
// a detached next state plus the modified change domain(s).
func (s *Store) DispatchWithDomain(action Action) (State, ChangeDomain) {
	if s == nil {
		return NewState(), DomainNone
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next, domain := ReduceWithDomain(s.state, action)
	if domain != DomainNone {
		s.revision++
		if domain.HasContentChange() {
			s.contentRevision++
		}
		if domain&DomainSession != 0 {
			s.sessionRevision++
		}
		s.state = next
	}
	return cloneState(s.state), domain
}

func (s *Store) Revisions() (rev, contentRev, sessionRev uint64) {
	if s == nil {
		return 0, 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision, s.contentRevision, s.sessionRevision
}

func (s *Store) ContentRevision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contentRevision
}

func (s *Store) Revision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *Store) SelectActiveRun() (RunState, bool) {
	if s == nil {
		return RunState{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SelectActiveRun(s.state)
}

func (s *Store) SelectPendingPermissions() []client.PermissionRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SelectPendingPermissions(s.state)
}

func (s *Store) SelectPendingWorkerReviews() []client.PermissionRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SelectPendingWorkerReviews(s.state)
}

func (s *Store) SelectModel() ModelState {
	if s == nil {
		return ModelState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Model
}

func (s *Store) SelectSessionID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.state.Session.ID)
}

func (s *Store) SelectTitle() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.state.Session.Title)
}

func (s *Store) Read(fn func(state *State)) {
	if s == nil || fn == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state)
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
		if strings.EqualFold(strings.TrimSpace(item.Record.Status), "pending") && !isWorkerReviewPermission(item.Record) {
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
