package api

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
)

const (
	v3LiveMaxPendingBytesPerStream     = 64 << 10
	v3LiveMaxPendingBytesPerSubscriber = 256 << 10
	v3LiveWriterMaxFramesPerTurn       = 16
	v3LiveWriterMaxBytesPerTurn        = 64 << 10
)

type v3LiveSessionKey struct {
	AccountScopeID string
	SessionID      string
}

type v3LivePatchKey struct {
	SessionID string
	RunID     string
	StreamID  string
}

type v3LiveSlowConsumer struct {
	Reason string
}

type v3LivePendingPatch struct {
	Patch V3RealtimeLivePatch
	Text  bytes.Buffer
	Bytes int
}

type v3LiveSubscriber struct {
	id string

	mu sync.Mutex

	pendingByKey map[v3LivePatchKey]*v3LivePendingPatch
	readyKeys    []v3LivePatchKey
	queuedKeys   map[v3LivePatchKey]struct{}
	// sessionKeys is protected by v3LiveHub.mu, not by mu.
	sessionKeys  map[v3LiveSessionKey]struct{}
	pendingBytes int

	notify chan struct{}
	slow   chan v3LiveSlowConsumer
	closed bool
}

type v3LiveHub struct {
	mu sync.RWMutex

	nextSub   uint64
	subs      map[string]*v3LiveSubscriber
	bySession map[v3LiveSessionKey]map[string]*v3LiveSubscriber
}

func newV3LiveHub() *v3LiveHub {
	return &v3LiveHub{
		subs:      make(map[string]*v3LiveSubscriber),
		bySession: make(map[v3LiveSessionKey]map[string]*v3LiveSubscriber),
	}
}

func (h *v3LiveHub) subscribe() *v3LiveSubscriber {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSub++
	sub := &v3LiveSubscriber{
		id:           fmt.Sprintf("v3live_%d", h.nextSub),
		pendingByKey: make(map[v3LivePatchKey]*v3LivePendingPatch),
		queuedKeys:   make(map[v3LivePatchKey]struct{}),
		sessionKeys:  make(map[v3LiveSessionKey]struct{}),
		notify:       make(chan struct{}, 1),
		slow:         make(chan v3LiveSlowConsumer, 1),
	}
	h.subs[sub.id] = sub
	return sub
}

func (h *v3LiveHub) unsubscribe(sub *v3LiveSubscriber) {
	if h == nil || sub == nil {
		return
	}
	sub.mu.Lock()
	sub.closed = true
	sub.pendingByKey = make(map[v3LivePatchKey]*v3LivePendingPatch)
	sub.readyKeys = nil
	sub.queuedKeys = make(map[v3LivePatchKey]struct{})
	sub.pendingBytes = 0
	sub.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub.id)
	h.removeSubscriberSessionsLocked(sub)
}

func (h *v3LiveHub) replaceSessions(sub *v3LiveSubscriber, accountScopeID string, sessionIDs []string) {
	if h == nil || sub == nil {
		return
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[sub.id] != sub {
		return
	}
	h.removeSubscriberSessionsLocked(sub)
	if accountScopeID == "" {
		return
	}
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		key := v3LiveSessionKey{AccountScopeID: accountScopeID, SessionID: sessionID}
		if h.bySession[key] == nil {
			h.bySession[key] = make(map[string]*v3LiveSubscriber)
		}
		h.bySession[key][sub.id] = sub
		sub.sessionKeys[key] = struct{}{}
	}
}

func (h *v3LiveHub) publish(accountScopeID string, patch V3RealtimeLivePatch) {
	if h == nil {
		return
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	patch.SessionID = strings.TrimSpace(patch.SessionID)
	if accountScopeID == "" || patch.SessionID == "" {
		return
	}
	key := v3LiveSessionKey{AccountScopeID: accountScopeID, SessionID: patch.SessionID}
	h.mu.RLock()
	sessionSubs := h.bySession[key]
	subs := make([]*v3LiveSubscriber, 0, len(sessionSubs))
	for _, sub := range sessionSubs {
		if sub != nil {
			subs = append(subs, sub)
		}
	}
	h.mu.RUnlock()

	for _, sub := range subs {
		if overflow := sub.enqueue(patch); overflow {
			h.markSlow(sub, "live patch subscriber pending bytes exceeded; reconnect required")
		}
	}
}

func (h *v3LiveHub) markSlow(sub *v3LiveSubscriber, reason string) {
	if h == nil || sub == nil {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "live patch subscriber is slow; reconnect required"
	}
	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		return
	}
	sub.closed = true
	sub.pendingByKey = make(map[v3LivePatchKey]*v3LivePendingPatch)
	sub.readyKeys = nil
	sub.queuedKeys = make(map[v3LivePatchKey]struct{})
	sub.pendingBytes = 0
	sub.mu.Unlock()

	h.mu.Lock()
	if h.subs[sub.id] == sub {
		delete(h.subs, sub.id)
		h.removeSubscriberSessionsLocked(sub)
	}
	h.mu.Unlock()

	select {
	case sub.slow <- v3LiveSlowConsumer{Reason: reason}:
	default:
	}
}

func (s *v3LiveSubscriber) enqueue(patch V3RealtimeLivePatch) (overflow bool) {
	if s == nil {
		return false
	}
	incomingBytes := len([]byte(patch.Text))
	key := v3LivePatchKey{SessionID: patch.SessionID, RunID: patch.RunID, StreamID: patch.StreamID}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	pending := s.pendingByKey[key]
	if pending == nil {
		if incomingBytes > v3LiveMaxPendingBytesPerStream || s.pendingBytes+incomingBytes > v3LiveMaxPendingBytesPerSubscriber {
			return true
		}
		pending = &v3LivePendingPatch{Patch: patch, Bytes: incomingBytes}
		pending.Patch.Text = ""
		_, _ = pending.Text.WriteString(patch.Text)
		s.pendingByKey[key] = pending
		s.pendingBytes += incomingBytes
		if _, queued := s.queuedKeys[key]; !queued {
			s.readyKeys = append(s.readyKeys, key)
			s.queuedKeys[key] = struct{}{}
		}
		s.signalLocked()
		return false
	}

	if pending.Patch.SessionID != patch.SessionID || pending.Patch.RunID != patch.RunID || pending.Patch.StreamID != patch.StreamID || pending.Patch.Operation != "append" || patch.Operation != "append" || patch.LiveSeqStart != pending.Patch.LiveSeqEnd+1 || patch.OffsetStart != pending.Patch.OffsetEnd {
		return true
	}
	if pending.Bytes+incomingBytes > v3LiveMaxPendingBytesPerStream || s.pendingBytes+incomingBytes > v3LiveMaxPendingBytesPerSubscriber {
		return true
	}
	_, _ = pending.Text.WriteString(patch.Text)
	pending.Bytes += incomingBytes
	pending.Patch.LiveSeqEnd = patch.LiveSeqEnd
	pending.Patch.OffsetEnd = patch.OffsetEnd
	pending.Patch.RecordedAt = patch.RecordedAt
	s.pendingBytes += incomingBytes
	s.signalLocked()
	return false
}

func (s *v3LiveSubscriber) drain(maxFrames, maxBytes int) []V3RealtimeLivePatch {
	if s == nil || maxFrames <= 0 || maxBytes <= 0 {
		return nil
	}
	type drainedPatch struct {
		patch V3RealtimeLivePatch
		text  *bytes.Buffer
	}
	drained := make([]drainedPatch, 0, maxFrames)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	remainingReady := make([]v3LivePatchKey, 0, len(s.readyKeys))
	selectedBytes := 0
	for _, key := range s.readyKeys {
		pending := s.pendingByKey[key]
		if pending == nil {
			delete(s.queuedKeys, key)
			continue
		}
		if len(drained) >= maxFrames || (len(drained) > 0 && selectedBytes+pending.Bytes > maxBytes) {
			remainingReady = append(remainingReady, key)
			continue
		}
		delete(s.pendingByKey, key)
		delete(s.queuedKeys, key)
		s.pendingBytes -= pending.Bytes
		selectedBytes += pending.Bytes
		drained = append(drained, drainedPatch{patch: pending.Patch, text: &pending.Text})
	}
	s.readyKeys = remainingReady
	shouldSignal := len(s.readyKeys) > 0
	s.mu.Unlock()

	if shouldSignal {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	out := make([]V3RealtimeLivePatch, 0, len(drained))
	for _, item := range drained {
		patch := item.patch
		patch.Text = item.text.String()
		out = append(out, patch)
	}
	return out
}

func (s *v3LiveSubscriber) signalLocked() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (h *v3LiveHub) removeSubscriberSessionsLocked(sub *v3LiveSubscriber) {
	if h == nil || sub == nil {
		return
	}
	for key := range sub.sessionKeys {
		if subs := h.bySession[key]; subs != nil {
			delete(subs, sub.id)
			if len(subs) == 0 {
				delete(h.bySession, key)
			}
		}
		delete(sub.sessionKeys, key)
	}
}
