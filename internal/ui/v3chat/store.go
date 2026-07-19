package v3chat

import "sync"

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
func SelectPending(state State) []PendingMessage {
	out := make([]PendingMessage, 0, len(state.Pending))
	for _, pending := range state.Pending {
		out = append(out, pending)
	}
	return out
}
func SelectActiveRun(state State) (ActiveRun, bool) {
	if state.ActiveRun == nil {
		return ActiveRun{}, false
	}
	return *state.ActiveRun, true
}
func SelectLiveSegments(state State) []LiveSegment {
	out := make([]LiveSegment, 0, len(state.Live))
	for _, segment := range state.Live {
		out = append(out, segment)
	}
	return out
}
func SelectReconnect(state State) (ConnectionStatus, bool, string) {
	return state.Connection, state.NeedsRehydrate, state.StaleReason
}
func SelectModel(state State) ModelState        { return state.Model }
func SelectUsage(state State) UsageState        { return state.Usage }
func SelectCursor(state State) (string, uint64) { return state.EndpointCursor, state.LastEventSeq }
