package gitwatch

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrSubscriptionCapacity is safe to classify without returning paths or backend errors.
var ErrSubscriptionCapacity = errors.New("Git subscription capacity reached")

// Notice carries invalidation, never Git truth. Consumers inspect after ready
// (including reconnect/rebuild) and changed; loss immediately fences old facts.
type Notice struct {
	Kind string `json:"kind"`
}

// Subscriptions shares native watchers across consumers of the same worktree.
// It performs no Git commands, periodic status reads or reconciliation polling.
// A failed native backend is retried with capped backoff, not replaced by polling.
type Subscriptions struct {
	mu      sync.Mutex
	roots   map[string]*subscriptionRoot
	factory func(Config) (Backend, error)
	closed  bool
}

type subscriptionRoot struct {
	owner     *Subscriptions
	config    Config
	listeners map[chan Notice]string
	stop      chan struct{}
	done      chan struct{}
	ready     bool
}

func NewSubscriptions() *Subscriptions {
	return &Subscriptions{roots: make(map[string]*subscriptionRoot), factory: NewFSNotify}
}

// Acquire installs the watcher before emitting ready, closing the hydration gap.
// Branch is a filter, not filesystem authority. Config must already be authorized.
func (s *Subscriptions) Acquire(config Config, branch string) (<-chan Notice, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("Git subscriptions closed")
	}
	key := filepath.Clean(config.WorktreeRoot)
	r := s.roots[key]
	if r == nil {
		if len(s.roots) >= 256 {
			return nil, nil, ErrSubscriptionCapacity
		}
		r = &subscriptionRoot{owner: s, config: config, listeners: make(map[chan Notice]string), stop: make(chan struct{}), done: make(chan struct{})}
		s.roots[key] = r
		go r.run()
	} else if r.config != config {
		return nil, nil, errors.New("Git watcher identity changed")
	}
	if len(r.listeners) >= 256 {
		return nil, nil, ErrSubscriptionCapacity
	}
	ch := make(chan Notice, 1)
	r.listeners[ch] = branch
	if r.ready {
		r.notifyOne(ch, Notice{Kind: "ready"})
	}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(r.listeners, ch)
			last := len(r.listeners) == 0 && s.roots[key] == r
			if last {
				delete(s.roots, key)
				close(r.stop)
			}
			s.mu.Unlock()
			if last {
				<-r.done
			}
		})
	}, nil
}

func (s *Subscriptions) Close() {
	s.mu.Lock()
	s.closed = true
	roots := s.roots
	s.roots = make(map[string]*subscriptionRoot)
	for _, r := range roots {
		close(r.stop)
	}
	s.mu.Unlock()
	for _, r := range roots {
		<-r.done
	}
}

// Called under owner.mu. Loss cannot be overwritten by a pending change; ready
// after a rebuild is safe because both messages demand fresh inspection.
func (r *subscriptionRoot) notifyOne(ch chan Notice, notice Notice) {
	select {
	case pending := <-ch:
		if notice.Kind == "changed" && pending.Kind != "changed" {
			notice = pending
		}
	default:
	}
	ch <- notice
}

func (r *subscriptionRoot) notify(kind string, event *Event) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if kind == "ready" {
		r.ready = true
	}
	if kind == "lost" {
		r.ready = false
	}
	for ch, branch := range r.listeners {
		if event == nil || relevantChange(r.config, branch, *event) {
			r.notifyOne(ch, Notice{Kind: kind})
		}
	}
}

// Ignore sibling worktrees, object writes, reflogs and optional index locks.
// Common-dir refs and packed-refs remain watched for linked worktrees, even when
// the target ref is updated by a process outside either checkout.
func relevantChange(c Config, branch string, e Event) bool {
	path := filepath.Clean(e.Path)
	for _, root := range uniquePaths(c.GitDir, c.CommonDir) {
		if sameOrBelow(path, root) {
			if strings.HasSuffix(path, ".lock") {
				return false
			}
			rel, _ := filepath.Rel(root, path)
			return rel == "HEAD" || rel == "index" || rel == "packed-refs" ||
				rel == "config" || rel == "commondir" || rel == "." ||
				(branch != "" && filepath.ToSlash(rel) == "refs/heads/"+branch)
		}
	}
	return sameOrBelow(path, c.WorktreeRoot)
}

func (r *subscriptionRoot) run() {
	defer close(r.done)
	var backend Backend
	defer func() {
		if backend != nil {
			_ = backend.Close()
		}
	}()
	retry := time.NewTimer(0)
	defer retry.Stop()
	delay := time.Second
	var debounce *time.Timer
	var debounceC <-chan time.Time
	pending := make(map[chan Notice]bool)
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		var events <-chan Event
		if backend != nil {
			events = backend.Events()
		}
		select {
		case <-r.stop:
			return
		case <-retry.C:
			var err error
			backend, err = r.owner.factory(r.config)
			if err != nil {
				r.notify("lost", nil)
				retry.Reset(delay)
				delay = min(delay*2, 30*time.Second)
				continue
			}
			delay = time.Second
			r.notify("ready", nil)
		case <-debounceC:
			r.owner.mu.Lock()
			for ch := range pending {
				if _, exists := r.listeners[ch]; exists {
					r.notifyOne(ch, Notice{Kind: "changed"})
				}
			}
			r.owner.mu.Unlock()
			clear(pending)
			debounceC = nil
		case event, ok := <-events:
			if !ok || event.RebuildRequired {
				clear(pending)
				if debounce != nil {
					debounce.Stop()
				}
				debounceC = nil
				r.notify("lost", nil)
				_ = backend.Close()
				backend = nil
				retry.Reset(delay)
				continue
			}
			r.owner.mu.Lock()
			for ch, branch := range r.listeners {
				if relevantChange(r.config, branch, event) {
					pending[ch] = true
				}
			}
			r.owner.mu.Unlock()
			// Fixed event-triggered window bounds latency even under continuous
			// writes; there is no timer at all while the repository is idle.
			if len(pending) > 0 && debounceC == nil {
				if debounce == nil {
					debounce = time.NewTimer(180 * time.Millisecond)
				} else {
					debounce.Reset(180 * time.Millisecond)
				}
				debounceC = debounce.C
			}
		}
	}
}
