package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A wait on tasks delegated to a linked machine is resolved on the home from
// what that machine reports through its link: the machine's own activity
// stream (followed with the fleet feed's reader, fleet_activity.go) says when
// something changed for the wait's project or pinned sessions, and the pinned
// tasks are then read again with the link's get_task (fleet_projects.go). No
// timer reads tasks; the only timers re-read this home's own link rules and
// bound how long an unreachable machine may keep the goal waiting.

// remoteTaskWaitGraceDefault: how long a machine may go without successful
// contact (while this home is trying) before the wait resolves as unreachable.
const remoteTaskWaitGraceDefault = 2 * fleetActivityIdle

func (s *Server) remoteTaskWaitGraceOrDefault() time.Duration {
	if s.remoteTaskWaitGrace > 0 {
		return s.remoteTaskWaitGrace
	}
	return remoteTaskWaitGraceDefault
}

func (s *Server) remoteTaskWaitRulesOrDefault() time.Duration {
	if s.remoteTaskWaitRules > 0 {
		return s.remoteTaskWaitRules
	}
	return activityHeartbeat
}

// watchRemoteTaskWait starts the single watcher for one persisted
// linked-machine wait (deduplicated by run). It is called when the executor
// yields such a run and, after a restart, for every persisted one.
func (s *Server) watchRemoteTaskWait(sessionID, runID string) {
	if s == nil || s.sessions == nil || s.fleet == nil || s.fleetTokens == nil {
		return
	}
	owner, ok, err := s.sessions.Store().GetV3SessionRunIntent(sessionID, runID)
	if err != nil || !ok || owner.Status != pebblestore.V3RunIntentWaitingTasks || owner.TaskWait == nil || owner.TaskWait.Machine == "" {
		return
	}
	key := sessionID + "\x00" + runID
	s.remoteTaskWaitMu.Lock()
	if s.remoteTaskWaitRuns == nil {
		s.remoteTaskWaitRuns = map[string]bool{}
	}
	if s.remoteTaskWaitRuns[key] {
		s.remoteTaskWaitMu.Unlock()
		return
	}
	s.remoteTaskWaitRuns[key] = true
	s.remoteTaskWaitMu.Unlock()
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	// Shutdown (CancelInFlightRuns, then WaitForInFlightRuns before the store
	// closes) cancels runCtx and waits for this watcher like other run work.
	// The durable intent stays waiting_tasks; the next start resumes it.
	s.activeRunMu.Lock()
	if s.shuttingDown.Load() {
		s.activeRunMu.Unlock()
		s.remoteTaskWaitMu.Lock()
		delete(s.remoteTaskWaitRuns, key)
		s.remoteTaskWaitMu.Unlock()
		return
	}
	s.runWG.Add(1)
	s.activeRunMu.Unlock()
	go func() {
		defer s.runWG.Done()
		defer func() {
			s.remoteTaskWaitMu.Lock()
			delete(s.remoteTaskWaitRuns, key)
			s.remoteTaskWaitMu.Unlock()
		}()
		s.followRemoteTaskWait(ctx, owner)
	}()
}

// ResumeLinkedTaskWaits starts watchers for linked-machine waits persisted
// before a restart. The durable status index is the authority. Call it only
// after the server is fully configured (the daemon does so in Run, before
// serving): watchers read the fleet store, the node key and the session
// services without synchronisation against their setters. Repeated calls
// start no second watcher for a run.
func (s *Server) ResumeLinkedTaskWaits() {
	if s == nil || s.sessions == nil {
		return
	}
	after := ""
	for {
		waits, next, err := s.sessions.Store().ListV3SessionRunIntentsByStatusPaged(pebblestore.V3RunIntentWaitingTasks, after, 64)
		if err != nil {
			log.Printf("linked task wait recovery failed: %v", err)
			return
		}
		for _, owner := range waits {
			if owner.TaskWait != nil && owner.TaskWait.Machine != "" {
				s.watchRemoteTaskWait(owner.SessionID, owner.RunID)
			}
		}
		if next == "" {
			return
		}
		after = next
	}
}

// publishProjectTaskWake publishes a committed wake and queues its
// continuation, the same for local and linked-machine waits.
func (s *Server) publishProjectTaskWake(result pebblestore.V3SessionMutationResult) {
	if result.RealtimeOutbox != nil {
		if err := s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox); err != nil {
			log.Print("task wait outbox publication failed; durable recovery retained")
		}
	}
	if e := s.v3SessionExecutor; e != nil && result.RunIntent != nil {
		intent := result.RunIntent
		e.EnqueueRun(sessionV3ExecutorJob{SessionID: intent.SessionID, RunID: intent.RunID, SourceMessageID: intent.SourceMessageID, EpochID: intent.EpochID, ResumeContext: true, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: intent.UserID, AccountScopeID: intent.AccountScopeID}})
	}
}

// followRemoteTaskWait runs until the wait is woken, superseded (new person
// message, stop, archive) or the server stops.
func (s *Server) followRemoteTaskWait(parent context.Context, owner pebblestore.V3SessionRunIntent) {
	wait := *owner.TaskWait
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: owner.UserID, AccountScopeID: owner.AccountScopeID}
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	// The fleet feed's own follower: same link client, SSE reader, frame
	// parser, contact recording, idle drop and backoff. Its frames come here.
	feed := &fleetActivityFeed{s: s, principal: principal, out: make(chan fleetActivityFrame, 64), cursors: map[string]uint64{}, startNow: map[string]bool{wait.Machine: true}}
	wg.Add(1)
	go func() {
		defer wg.Done()
		feed.followMachine(ctx, wait.Machine)
	}()
	pinned := map[string]bool{}
	for _, target := range wait.Tasks {
		pinned[target.SessionID] = true
	}
	started := time.Now().UnixMilli()
	connected, lostAt := false, int64(0)
	var last []pebblestore.V3ProjectTaskRemoteObservation

	// wake reports whether this watcher is done.
	wake := func(observed []pebblestore.V3ProjectTaskRemoteObservation, resolution *pebblestore.V3ProjectTaskRemoteResolution) bool {
		key := "task-wake:" + owner.RunID
		result, err := s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.SessionID, UserID: owner.UserID, AccountScopeID: owner.AccountScopeID, Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.run.tasks_ready", ClientRequestID: key, PayloadHash: key, TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: owner.RunID, Wake: true, Machine: wait.Machine, RemoteObservations: observed, RemoteResolution: resolution}})
		switch {
		case errors.Is(err, pebblestore.ErrProjectTaskWaitNotReady):
			return false
		case errors.Is(err, pebblestore.ErrProjectTaskWaitStale):
			return true
		case err != nil:
			log.Printf("linked task wait wake failed: %v", err)
			return false
		}
		s.publishProjectTaskWake(result)
		return true
	}
	current := func() bool {
		intent, ok, err := s.sessions.Store().GetV3SessionRunIntent(owner.SessionID, owner.RunID)
		return err != nil || (ok && intent.Status == pebblestore.V3RunIntentWaitingTasks && intent.TaskWait != nil && intent.TaskWait.Machine == wait.Machine)
	}
	// workOff: the person turned the machine's linked work rule off or
	// unlinked it. Read from this home's own records; the machine is not called.
	workOff := func() bool {
		access, err := s.sessions.Store().GetAccountPermissions(owner.AccountScopeID)
		if err != nil {
			return false
		}
		if !pebblestore.LinkedWorkOn(access.Link(wait.Machine).Work) {
			return true
		}
		if _, linked, err := s.fleetTokens.GetFleetLinkToken(owner.AccountScopeID, wait.Machine); err == nil && !linked {
			return true
		}
		_, found, err := s.fleet.GetMachine(wait.Machine)
		return err == nil && !found
	}
	observe := func() ([]pebblestore.V3ProjectTaskRemoteObservation, bool) {
		out := make([]pebblestore.V3ProjectTaskRemoteObservation, 0, len(wait.Tasks))
		for _, target := range wait.Tasks {
			raw, err := s.CallFleetProject(ctx, principal, wait.Machine, "get_task", map[string]any{"project_id": wait.ProjectID, "task_id": target.TaskID})
			var refused *fleet.Error
			if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
				out = append(out, pebblestore.V3ProjectTaskRemoteObservation{TaskID: target.TaskID, Missing: true})
				continue
			}
			if err != nil {
				return nil, false
			}
			var response struct {
				Task pebblestore.ProjectTaskRecord `json:"task"`
			}
			if json.Unmarshal(raw, &response) != nil || response.Task.ID != target.TaskID {
				return nil, false
			}
			task := response.Task
			task.EnsureTaskAttempts()
			o := pebblestore.V3ProjectTaskRemoteObservation{TaskID: target.TaskID, Archived: task.Archived || task.ProjectID != wait.ProjectID, Title: task.Title, Status: task.Status, LastError: task.LastError, ActionNeeded: task.ActionNeeded}
			if a := task.ActiveAttempt(); a != nil {
				o.AttemptID, o.SessionID, o.AttemptSummary = a.ID, a.SessionID, a.Summary
			}
			out = append(out, o)
		}
		return out, true
	}
	check := func() bool {
		if !current() {
			return true
		}
		if workOff() {
			return wake(last, &pebblestore.V3ProjectTaskRemoteResolution{Reason: pebblestore.V3ProjectTaskRemoteWorkOff})
		}
		observed, ok := observe()
		if !ok {
			return false
		}
		last = observed
		return wake(observed, nil)
	}
	lastContact := func() int64 {
		contact := lostAt
		if machine, found, err := s.fleet.GetMachine(wait.Machine); err == nil && found && machine.LastContactAt > contact {
			contact = machine.LastContactAt
		}
		return contact
	}
	grace := s.remoteTaskWaitGraceOrDefault()
	deadline := time.NewTimer(time.Hour)
	deadline.Stop()
	defer deadline.Stop()
	// arm schedules the unreachable check for when the machine will have gone
	// a full grace period without contact while this home has been trying.
	arm := func() {
		since := max(lastContact(), started)
		deadline.Reset(max(time.Until(time.UnixMilli(since).Add(grace)), 0))
	}
	rules := time.NewTicker(s.remoteTaskWaitRulesOrDefault())
	defer rules.Stop()
	if !current() {
		return
	}
	if workOff() && wake(nil, &pebblestore.V3ProjectTaskRemoteResolution{Reason: pebblestore.V3ProjectTaskRemoteWorkOff}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-feed.out:
			switch frame.event {
			case "machine":
				var state fleetActivityMachineFrame
				if json.Unmarshal(frame.data, &state) != nil {
					continue
				}
				switch state.State {
				case "online":
					connected = true
					deadline.Stop()
					if check() {
						return
					}
				case "offline":
					if connected {
						lostAt = time.Now().UnixMilli()
					}
					connected = false
					arm()
				}
			case "activity":
				var event fleetActivityEvent
				if json.Unmarshal(frame.data, &event) != nil {
					continue
				}
				if (event.ProjectID != "" && event.ProjectID == wait.ProjectID) || pinned[event.SessionID] {
					if check() {
						return
					}
				}
			case "gap":
				if check() {
					return
				}
			}
		case <-rules.C:
			if !current() {
				return
			}
			if workOff() && wake(last, &pebblestore.V3ProjectTaskRemoteResolution{Reason: pebblestore.V3ProjectTaskRemoteWorkOff}) {
				return
			}
		case <-deadline.C:
			if !current() {
				return
			}
			if connected {
				continue
			}
			if since := max(lastContact(), started); time.Since(time.UnixMilli(since)) < grace {
				arm()
				continue
			}
			if wake(last, &pebblestore.V3ProjectTaskRemoteResolution{Reason: pebblestore.V3ProjectTaskRemoteUnreachable, LastContactAt: lastContact()}) {
				return
			}
			deadline.Reset(grace) // the wake failed for another reason: try again later
		}
	}
}
