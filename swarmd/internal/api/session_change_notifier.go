package api

import "sync"

// sessionChangeNotifier wakes long-poll readers (GET /v3/sessions/{id}/wait,
// GET /v3/sessions:agent-events) when a V3 session change has committed. It is
// an accelerator only: every reader re-reads durable session events after a
// wake and also returns when its wait ends, so a missed wake delays an answer
// by at most the wait, it never loses one.
//
// Keys are "session:<id>" and "account:<account scope>". A watcher takes its
// channel before reading durable state, so a change committed in between still
// closes it.
type sessionChangeNotifier struct {
	mu       sync.Mutex
	watchers map[string]*sessionChangeWatch
}

type sessionChangeWatch struct {
	ch   chan struct{}
	refs int
}

func newSessionChangeNotifier() *sessionChangeNotifier {
	return &sessionChangeNotifier{watchers: map[string]*sessionChangeWatch{}}
}

func sessionChangeKey(sessionID string) string { return "session:" + sessionID }
func accountChangeKey(account string) string   { return "account:" + account }

// watch returns a channel closed by the next notify of key, and a release
// function the caller must call when it stops waiting.
func (n *sessionChangeNotifier) watch(key string) (<-chan struct{}, func()) {
	if n == nil {
		return nil, func() {}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	entry := n.watchers[key]
	if entry == nil {
		entry = &sessionChangeWatch{ch: make(chan struct{})}
		n.watchers[key] = entry
	}
	entry.refs++
	released := false
	return entry.ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if released {
			return
		}
		released = true
		entry.refs--
		if entry.refs <= 0 && n.watchers[key] == entry {
			delete(n.watchers, key)
		}
	}
}

// notify wakes every current watcher of each key.
func (n *sessionChangeNotifier) notify(keys ...string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, key := range keys {
		if entry := n.watchers[key]; entry != nil {
			close(entry.ch)
			delete(n.watchers, key)
		}
	}
}

// watchedKeys reports how many keys are watched (tests).
func (n *sessionChangeNotifier) watchedKeys() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.watchers)
}
