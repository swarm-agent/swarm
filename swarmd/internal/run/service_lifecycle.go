package run

import (
	"context"
	"errors"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	lifecyclePhaseStarting    = "starting"
	lifecyclePhaseRunning     = "running"
	lifecyclePhaseBlocked     = "blocked"
	lifecyclePhaseCompleted   = "completed"
	lifecyclePhaseCancelled   = "cancelled"
	lifecyclePhaseErrored     = "errored"
	lifecyclePhaseInterrupted = "interrupted"
)

var (
	ErrSessionAlreadyActive = errors.New("session already has an active run")
	ErrSessionRunNotActive  = errors.New("session has no active run")
)

type activeSessionRun struct {
	runID      string
	generation uint64
	cancel     context.CancelFunc
	stopReason string
	userStop   bool
}

func (s *Service) GetSessionLifecycle(sessionID string) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session service is not configured")
	}
	return s.sessions.GetLifecycle(sessionID)
}

func (s *Service) ReconcileActiveLifecycles(reason string) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run interrupted"
	}
	active, err := s.sessions.ListActiveLifecycles(100000)
	if err != nil {
		return err
	}
	for _, snapshot := range active {
		now := time.Now().UnixMilli()
		updated := snapshot
		updated.Active = false
		updated.Phase = lifecyclePhaseInterrupted
		updated.EndedAt = now
		updated.UpdatedAt = now
		updated.StopReason = reason
		updated.Error = ""
		if err := s.sessions.UpsertLifecycle(updated); err != nil {
			return err
		}
		s.publishLifecycleSnapshot(updated)
	}
	return nil
}

func (s *Service) StopSessionRun(sessionID, runID, reason string) error {
	if s == nil || s.sessions == nil {
		return errors.New("run service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	reason = strings.TrimSpace(reason)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if reason == "" {
		reason = "run stopped by user"
	}

	s.lifecycleMu.Lock()
	current, ok, err := s.sessions.GetLifecycle(sessionID)
	if err != nil {
		s.lifecycleMu.Unlock()
		return err
	}
	if !ok || !current.Active {
		s.lifecycleMu.Unlock()
		return ErrSessionRunNotActive
	}
	if runID != "" && !strings.EqualFold(runID, strings.TrimSpace(current.RunID)) {
		s.lifecycleMu.Unlock()
		return ErrSessionRunNotActive
	}
	active := s.activeRuns[sessionID]
	if active == nil || !strings.EqualFold(strings.TrimSpace(active.runID), strings.TrimSpace(current.RunID)) {
		s.lifecycleMu.Unlock()
		return ErrSessionRunNotActive
	}
	active.userStop = true
	active.stopReason = reason
	cancel := active.cancel
	s.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Service) beginSessionLifecycle(sessionID, runID, ownerTransport string) (pebblestore.SessionLifecycleSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionLifecycleSnapshot{}, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	ownerTransport = strings.TrimSpace(ownerTransport)
	if sessionID == "" {
		return pebblestore.SessionLifecycleSnapshot{}, errors.New("session id is required")
	}
	if runID == "" {
		return pebblestore.SessionLifecycleSnapshot{}, errors.New("run id is required")
	}

	now := time.Now().UnixMilli()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	current, ok, err := s.sessions.GetLifecycle(sessionID)
	if err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, err
	}
	if ok && current.Active {
		return pebblestore.SessionLifecycleSnapshot{}, ErrSessionAlreadyActive
	}

	next := pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		RunID:          runID,
		Active:         true,
		Phase:          lifecyclePhaseStarting,
		StartedAt:      now,
		EndedAt:        0,
		UpdatedAt:      now,
		Generation:     current.Generation + 1,
		StopReason:     "",
		Error:          "",
		OwnerTransport: ownerTransport,
	}
	if err := s.sessions.UpsertLifecycle(next); err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, err
	}
	s.activeRuns[sessionID] = &activeSessionRun{
		runID:      next.RunID,
		generation: next.Generation,
	}
	return next, nil
}

func (s *Service) attachLifecycleCancel(sessionID, runID string, cancel context.CancelFunc) {
	if s == nil || cancel == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return
	}

	var cancelImmediately bool
	s.lifecycleMu.Lock()
	if active := s.activeRuns[sessionID]; active != nil && strings.EqualFold(strings.TrimSpace(active.runID), runID) {
		active.cancel = cancel
		cancelImmediately = active.userStop
	}
	s.lifecycleMu.Unlock()

	if cancelImmediately {
		cancel()
	}
}

func (s *Service) transitionSessionLifecycle(sessionID, runID, phase string) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	phase = strings.TrimSpace(phase)
	if sessionID == "" || runID == "" || phase == "" {
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}

	now := time.Now().UnixMilli()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	current, ok, err := s.sessions.GetLifecycle(sessionID)
	if err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, err
	}
	if !ok || !current.Active || !strings.EqualFold(strings.TrimSpace(current.RunID), runID) {
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}
	if strings.EqualFold(strings.TrimSpace(current.Phase), phase) {
		return current, false, nil
	}
	current.Phase = phase
	current.UpdatedAt = now
	if phase == lifecyclePhaseRunning {
		current.StopReason = ""
		current.Error = ""
	}
	if err := s.sessions.UpsertLifecycle(current); err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, err
	}
	return current, true, nil
}

func (s *Service) transitionSessionLifecycleForEvent(event StreamEvent) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	switch strings.TrimSpace(event.Type) {
	case StreamEventPermissionReq:
		return s.transitionSessionLifecycle(event.SessionID, event.RunID, lifecyclePhaseBlocked)
	case StreamEventPermissionUpdate:
		if event.Permission != nil && strings.EqualFold(strings.TrimSpace(event.Permission.Status), pebblestore.PermissionStatusPending) {
			return s.transitionSessionLifecycle(event.SessionID, event.RunID, lifecyclePhaseBlocked)
		}
		return s.transitionSessionLifecycle(event.SessionID, event.RunID, lifecyclePhaseRunning)
	default:
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}
}

func (s *Service) finishSessionLifecycle(sessionID, runID string, runErr error) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}

	now := time.Now().UnixMilli()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	current, ok, err := s.sessions.GetLifecycle(sessionID)
	if err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, err
	}
	if !ok || !current.Active || !strings.EqualFold(strings.TrimSpace(current.RunID), runID) {
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}
	active := s.activeRuns[sessionID]
	if active != nil && !strings.EqualFold(strings.TrimSpace(active.runID), runID) {
		active = nil
	}

	phase, stopReason, errorText := classifyLifecycleFinish(runErr, active)
	current.Active = false
	current.Phase = phase
	current.EndedAt = now
	current.UpdatedAt = now
	current.StopReason = stopReason
	current.Error = errorText
	if err := s.sessions.UpsertLifecycle(current); err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, err
	}
	delete(s.activeRuns, sessionID)
	return current, true, nil
}

func classifyLifecycleFinish(runErr error, active *activeSessionRun) (phase, stopReason, errorText string) {
	switch {
	case runErr == nil:
		return lifecyclePhaseCompleted, "", ""
	case errors.Is(runErr, context.Canceled):
		if active != nil && active.userStop {
			reason := strings.TrimSpace(active.stopReason)
			if reason == "" {
				reason = "run stopped by user"
			}
			return lifecyclePhaseCancelled, reason, ""
		}
		reason := "run interrupted"
		if active != nil && strings.TrimSpace(active.stopReason) != "" {
			reason = strings.TrimSpace(active.stopReason)
		}
		return lifecyclePhaseInterrupted, reason, ""
	default:
		return lifecyclePhaseErrored, "", strings.TrimSpace(runErr.Error())
	}
}

func (s *Service) publishLifecycleSnapshot(snapshot pebblestore.SessionLifecycleSnapshot) {
	snapshotCopy := snapshot
	s.publishStreamEventEnvelope(StreamEvent{
		Type:      StreamEventSessionLifecycle,
		SessionID: snapshotCopy.SessionID,
		RunID:     snapshotCopy.RunID,
		Lifecycle: &snapshotCopy,
	})
}

func emitLifecycleSnapshot(emit StreamHandler, snapshot pebblestore.SessionLifecycleSnapshot) {
	if emit == nil {
		return
	}
	snapshotCopy := snapshot
	emit(StreamEvent{
		Type:      StreamEventSessionLifecycle,
		SessionID: snapshotCopy.SessionID,
		RunID:     snapshotCopy.RunID,
		Lifecycle: &snapshotCopy,
	})
}

func normalizeLifecycleTransport(onEvent StreamHandler) string {
	if onEvent != nil {
		return "ws"
	}
	return "http"
}

func isLifecycleActivePhase(phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case lifecyclePhaseStarting, lifecyclePhaseRunning, lifecyclePhaseBlocked:
		return true
	default:
		return false
	}
}
