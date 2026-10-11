// Package signals raises Swarm's own events into the machine signal feed
// (pebblestore.SignalStore, served at /v3/signals).
package signals

import (
	"log"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Emitter appends signals for the daemon's own events. Raising a signal never
// fails the operation that raised it: a write error is logged and dropped,
// because the feed reports on work and must not be able to stop it.
type Emitter struct {
	store *pebblestore.SignalStore
	now   func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// maxThrottleKeys bounds the throttle memory; when exceeded it starts over,
// which at worst lets one extra signal per key through.
const maxThrottleKeys = 10_000

func NewEmitter(store *pebblestore.SignalStore) *Emitter {
	return &Emitter{store: store, now: time.Now, last: map[string]time.Time{}}
}

// Emit appends sig with source swarmd. A nil Emitter does nothing.
func (e *Emitter) Emit(sig pebblestore.Signal) {
	if e == nil || e.store == nil {
		return
	}
	sig.Source = pebblestore.SignalSourceSwarmd
	if _, err := e.store.Append(sig); err != nil {
		log.Printf("swarmd signal %s not recorded: %v", strings.TrimSpace(sig.Kind), err)
	}
}

// EmitThrottled emits sig unless one with the same key was emitted within
// window, for events that can repeat quickly (a refused request retried in a
// loop). It reports whether sig was emitted.
func (e *Emitter) EmitThrottled(key string, window time.Duration, sig pebblestore.Signal) bool {
	if e == nil || e.store == nil {
		return false
	}
	now := e.now()
	e.mu.Lock()
	if at, ok := e.last[key]; ok && now.Sub(at) < window {
		e.mu.Unlock()
		return false
	}
	if len(e.last) >= maxThrottleKeys {
		e.last = map[string]time.Time{}
	}
	e.last[key] = now
	e.mu.Unlock()
	e.Emit(sig)
	return true
}
