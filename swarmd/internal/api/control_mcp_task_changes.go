package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// FollowProjectTaskChanges follows one project's tasks for a wait that has
// no conversation to wake (Swarm Control's wait_tasks, tool
// catalog_control_wait.go). It wakes on the sources a task watch wakes on:
// here, committed realtime outbox records for the project or a pinned task
// session (as swarm_wait_task does); on a linked machine, the machine's
// activity feed through the fleet feed's own follower (as a linked watch
// does). A signal only says "read again": the waiter re-reads the tasks
// through get_task with the caller's identity, so nothing is learned from
// the signal itself. It runs until ctx ends.
func (s *Server) FollowProjectTaskChanges(ctx context.Context, p identity.Principal, machine, projectID string) (tool.ProjectTaskChanges, error) {
	if s == nil || !p.Valid() || strings.TrimSpace(projectID) == "" {
		return nil, errors.New("task changes need an authenticated user and a project")
	}
	f := &projectTaskChanges{c: make(chan struct{}, 1), pinned: map[string]bool{}}
	if hook := projectTaskChangesFollowed; hook != nil {
		hook(f)
	}
	machine = strings.TrimSpace(machine)
	if machine == "" {
		hub := s.v3RealtimeOutbox
		sub := hub.subscribe()
		if sub == nil {
			return nil, errors.New("task changes cannot be followed: realtime delivery is not running")
		}
		go func() {
			defer func() { hub.unsubscribe(sub) }()
			for {
				select {
				case <-ctx.Done():
					return
				case record := <-sub.send:
					var payload struct {
						ProjectID string `json:"project_id"`
					}
					if f.has(record.SessionID) || (json.Unmarshal(record.Event.Payload, &payload) == nil && payload.ProjectID == projectID) {
						f.signal()
					}
				case <-sub.slow:
					// Dropped as a slow consumer: follow again and have
					// the tasks read, since a change may have been missed.
					hub.unsubscribe(sub)
					if sub = hub.subscribe(); sub == nil {
						return
					}
					f.signal()
				}
			}
		}()
		return f, nil
	}
	feed := &fleetActivityFeed{s: s, principal: p, out: make(chan fleetActivityFrame, 64), cursors: map[string]uint64{}, startNow: map[string]bool{machine: true}}
	go feed.followMachine(ctx, machine)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-feed.out:
				switch frame.event {
				case "machine":
					var state fleetActivityMachineFrame
					if json.Unmarshal(frame.data, &state) == nil && state.State == "online" {
						f.signal()
					}
				case "activity":
					var event fleetActivityEvent
					if json.Unmarshal(frame.data, &event) == nil && ((event.ProjectID != "" && event.ProjectID == projectID) || f.has(event.SessionID)) {
						f.signal()
					}
				case "gap":
					f.signal()
				}
			}
		}
	}()
	return f, nil
}

// projectTaskChangesFollowed is a test hook (nil in the daemon): it sees each
// follow, so a test can act once the waiter pinned a task's session, i.e.
// after it read the tasks' baseline.
var projectTaskChangesFollowed func(*projectTaskChanges)

// projectTaskChanges is one follow: a one-slot signal and the pinned task
// sessions whose events concern it.
type projectTaskChanges struct {
	c      chan struct{}
	mu     sync.Mutex
	pinned map[string]bool
}

func (f *projectTaskChanges) C() <-chan struct{} { return f.c }

// Pin adds task sessions (bounded, as a linked watch pins at most a few per
// task).
func (f *projectTaskChanges) Pin(sessionIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range sessionIDs {
		if id = strings.TrimSpace(id); id != "" && len(f.pinned) < 64 {
			f.pinned[id] = true
		}
	}
}

func (f *projectTaskChanges) has(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pinned[sessionID]
}

func (f *projectTaskChanges) signal() {
	select {
	case f.c <- struct{}{}:
	default:
	}
}
