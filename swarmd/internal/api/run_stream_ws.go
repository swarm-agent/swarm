package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
)

// runControlRequest is the request shape for the current V3 run-control mutation.
type runControlRequest struct {
	Type string `json:"type"`
	runruntime.RunRequest
	RunID string `json:"run_id,omitempty"`
}

type runStreamState struct {
	runID     string
	sessionID string
}

type runStreamActiveRun struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	LastSeq   uint64 `json:"last_seq,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	EventSeq  uint64 `json:"event_seq,omitempty"`
}

// runControlAllocator allocates run identifiers for the V3 run-control mutation.
// Event delivery belongs to the durable V3 realtime path.
type runControlAllocator struct {
	nextRun atomic.Uint64
}

func newRunControlAllocator() *runControlAllocator { return &runControlAllocator{} }

func (m *runControlAllocator) diagnosticsSnapshot() map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{"transport": "v3.realtime"}
}

func (m *runControlAllocator) newRun(sessionID string) (*runStreamState, error) {
	if m == nil {
		return nil, errors.New("run control manager not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return &runStreamState{
		runID:     fmt.Sprintf("run_%06d", m.nextRun.Add(1)),
		sessionID: sessionID,
	}, nil
}

func (m *runControlAllocator) setStopReason(_, _ string) {}

func runStreamOwnerTransport(request runruntime.RunRequest) string {
	if request.Background {
		return "background_api"
	}
	return "v3_api"
}

func (s *Server) handleSessionV3PrimaryRunStreamControl(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run control manager not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	var inbound runControlRequest
	if err := decodeJSON(r, &inbound); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunRequest = inbound.RunRequest.Normalized()
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	if !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	switch inbound.Type {
	case "run.start", "start":
		if err := s.enforceSessionBindingWriteAccess(principal, sessionID, "run start"); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		state, err := s.runStreams.newRun(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		started := s.startRunStreamExecution(state.runID, sessionID, inbound, principal)
		if startErr := <-started; startErr != nil {
			status := http.StatusConflict
			if !errors.Is(startErr, runruntime.ErrSessionAlreadyActive) {
				status = http.StatusBadRequest
			}
			writeError(w, status, startErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "session_id": sessionID, "run_id": state.runID,
			"status": "accepted", "background": inbound.RunRequest.Background,
			"target_kind":     strings.TrimSpace(inbound.RunRequest.TargetKind),
			"target_name":     strings.TrimSpace(inbound.RunRequest.TargetName),
			"owner_transport": runStreamOwnerTransport(inbound.RunRequest),
		})
	case "run.stop", "stop":
		if inbound.RunID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
			return
		}
		if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": inbound.RunID})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported run stream control type %q", inbound.Type))
	}
}

func (s *Server) startRunStreamExecution(runID, sessionID string, inbound runControlRequest, principal identity.Principal) <-chan error {
	started := make(chan error, 1)
	if s == nil || s.runner == nil || s.runStreams == nil {
		started <- errors.New("run service is not configured")
		return started
	}
	s.beginActiveRun()
	go func() {
		defer s.endActiveRun()
		defer close(started)
		defer func() {
			if recover() != nil {
				log.Printf("run control panic contained run_id=%s session_id=%s category=unexpected_panic", runID, sessionID)
				select {
				case started <- errors.New("run panicked"):
				default:
				}
			}
		}()

		runCtx, runCancel := context.WithCancel(s.runCtx)
		defer runCancel()
		if !principal.Valid() {
			started <- identity.ErrPrincipalRequired
			return
		}
		runCtx = identity.ContextWithPrincipal(runCtx, principal)
		startSignaled := false
		result, err := s.runner.RunTurnStreaming(runCtx, sessionID, inbound.RunRequest, runruntime.RunStartMeta{
			RunID: runID, OwnerTransport: runStreamOwnerTransport(inbound.RunRequest), Principal: principal,
			ApplySessionMutation: s.applySessionV3PrimaryMutation,
		}, func(event runruntime.StreamEvent) {
			if !startSignaled && strings.EqualFold(strings.TrimSpace(event.Type), runruntime.StreamEventSessionLifecycle) && event.Lifecycle != nil && event.Lifecycle.Active {
				startSignaled = true
				started <- nil
			}
		})
		if err != nil {
			if !startSignaled {
				started <- err
			}
			return
		}
		if !startSignaled {
			started <- errors.New("run started without lifecycle acknowledgement")
		}
		for _, event := range result.Events {
			s.hub.Publish(event)
		}
	}()
	return started
}
