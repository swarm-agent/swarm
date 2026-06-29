package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3PlanModeEnterRequest struct {
	Reason string `json:"reason,omitempty"`
}

type sessionsV3PlanModeSubmitRequest struct {
	Title                 string                           `json:"title,omitempty"`
	Plan                  string                           `json:"plan,omitempty"`
	Document              *pebblestore.SessionPlanDocument `json:"document,omitempty"`
	ExecutionGranularity  string                           `json:"execution_granularity,omitempty"`
	ContinuationPolicy    string                           `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool                            `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanModeApproveRequest struct {
	ExecutionGranularity  string `json:"execution_granularity,omitempty"`
	ContinuationPolicy    string `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool  `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanModeStartAutomaticRequest struct {
	ExecutionGranularity string `json:"execution_granularity,omitempty"`
}

type sessionsV3PlanModeStartCheckpointedRequest struct {
	ExecutionGranularity string `json:"execution_granularity,omitempty"`
	ContinuationPolicy   string `json:"continuation_policy,omitempty"`
}

type sessionsV3PlanModeRunCurrentRequest struct {
	PlanID string `json:"plan_id,omitempty"`
}

type sessionsV3PlanModeCheckpointStartRequest struct {
	PlanID                   string `json:"plan_id,omitempty"`
	SuppressLifecycleMessage bool   `json:"suppress_lifecycle_message,omitempty"`
}

type sessionsV3PlanModeCheckpointAcceptRequest struct {
	PlanID     string `json:"plan_id,omitempty"`
	Result     string `json:"result,omitempty"`
	Notes      string `json:"notes,omitempty"`
	ReviewedAt int64  `json:"reviewed_at,omitempty"`
}

type sessionsV3PlanModeCheckpointResetRequest struct {
	PlanID string `json:"plan_id,omitempty"`
}

type sessionsV3PlanModeRunStart struct {
	RunIntent    *pebblestore.V3SessionRunIntent `json:"run_intent,omitempty"`
	CheckpointID string                          `json:"checkpoint_id,omitempty"`
	AttemptID    string                          `json:"attempt_id,omitempty"`
	Queued       bool                            `json:"queued,omitempty"`
}

func (s *Server) handleSessionV3PrimaryPlanMode(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	tail = strings.Trim(strings.TrimSpace(tail), "/")
	if tail == "enter" {
		s.handleSessionV3PrimaryPlanModeEnter(w, r, principal, sessionID)
		return
	}
	if tail == "runs/current/pause" {
		s.handleSessionV3PrimaryPlanModePauseCurrentRun(w, r, principal, sessionID)
		return
	}
	if tail == "runs/current/stop" {
		s.handleSessionV3PrimaryPlanModeStopCurrentRun(w, r, principal, sessionID)
		return
	}
	if tail == "runs/current/resume-automatic" {
		s.handleSessionV3PrimaryPlanModeResumeAutomatic(w, r, principal, sessionID)
		return
	}
	if tail == "runs/current/resume-checkpointed" {
		s.handleSessionV3PrimaryPlanModeResumeCheckpointed(w, r, principal, sessionID)
		return
	}
	if strings.HasPrefix(tail, "plans/") {
		parts := strings.Split(strings.TrimPrefix(tail, "plans/"), "/")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			switch parts[1] {
			case "submit":
				s.handleSessionV3PrimaryPlanModeSubmitPlan(w, r, principal, sessionID, parts[0])
				return
			case "approve":
				s.handleSessionV3PrimaryPlanModeApprovePlan(w, r, principal, sessionID, parts[0])
				return
			case "start-automatic":
				s.handleSessionV3PrimaryPlanModeStartPlanAutomatic(w, r, principal, sessionID, parts[0])
				return
			case "start-checkpointed":
				s.handleSessionV3PrimaryPlanModeStartPlanCheckpointed(w, r, principal, sessionID, parts[0])
				return
			}
		}
	}
	if strings.HasPrefix(tail, "checkpoints/") {
		parts := strings.Split(strings.TrimPrefix(tail, "checkpoints/"), "/")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			switch parts[1] {
			case "start":
				s.handleSessionV3PrimaryPlanModeStartCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "continue":
				s.handleSessionV3PrimaryPlanModeContinueCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "accept":
				s.handleSessionV3PrimaryPlanModeAcceptCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "restart":
				s.handleSessionV3PrimaryPlanModeRestartCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "rewind":
				s.handleSessionV3PrimaryPlanModeRewindCheckpoint(w, r, principal, sessionID, parts[0])
				return
			}
		}
	}
	writeError(w, http.StatusBadRequest, errors.New("unknown plan-mode lifecycle path"))
}

func (s *Server) handleSessionV3PrimaryPlanModeEnter(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeEnterRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.EnterPlanMode(sessionID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "enter_plan_mode", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeSubmitPlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, planID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeSubmitRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.SubmitPlanForApproval(sessionruntime.PlanLifecyclePlanInput{SessionID: sessionID, PlanID: planID, Title: req.Title, Plan: req.Plan, Document: req.Document, AgentCanSubmit: true, ExecutionGranularity: req.ExecutionGranularity, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "submit_plan", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeApprovePlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, planID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeApproveRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.ApprovePlan(sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: planID, ExecutionGranularity: req.ExecutionGranularity, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "approve_plan", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeStartPlanAutomatic(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, planID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeStartAutomaticRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		writeError(w, status, err)
		return
	}
	input := s.sessionsV3PlanModeRunInput(sessionID, planID, "")
	input.ExecutionGranularity = req.ExecutionGranularity
	result, err := s.planLifecycle.ApproveAndStartPlanAutomatic(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runStart, status, err := s.startSessionsV3PlanModeRun(principal, sessionID, "start_plan_automatic", result, false)
	if err != nil {
		writeError(w, status, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "start_plan_automatic", result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModeStartPlanCheckpointed(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, planID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeStartCheckpointedRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		writeError(w, status, err)
		return
	}
	input := s.sessionsV3PlanModeRunInput(sessionID, planID, "")
	input.ExecutionGranularity = req.ExecutionGranularity
	input.ContinuationPolicy = req.ContinuationPolicy
	result, err := s.planLifecycle.ApproveAndStartPlanCheckpointed(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runStart, status, err := s.startSessionsV3PlanModeRun(principal, sessionID, "start_plan_checkpointed", result, false)
	if err != nil {
		writeError(w, status, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "start_plan_checkpointed", result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModePauseCurrentRun(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryPlanModeRunCurrent(w, r, principal, sessionID, "pause_plan_run", s.planLifecycle.PausePlanRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeStopCurrentRun(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryPlanModeRunCurrent(w, r, principal, sessionID, "stop_plan_run", s.planLifecycle.StopPlanRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeResumeAutomatic(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryPlanModeRunCurrent(w, r, principal, sessionID, "resume_automatic", s.planLifecycle.ResumeAutomatic)
}

func (s *Server) handleSessionV3PrimaryPlanModeResumeCheckpointed(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryPlanModeRunCurrent(w, r, principal, sessionID, "resume_checkpointed", s.planLifecycle.ResumeCheckpointed)
}

func (s *Server) handleSessionV3PrimaryPlanModeRunCurrent(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, transition string, method func(sessionruntime.PlanLifecycleExecutionInput) (sessionruntime.PlanLifecycleResult, error)) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeRunCurrentRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := method(sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: req.PlanID})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, transition, result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeStartCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeStartCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "start_checkpoint", s.planLifecycle.StartCheckpoint)
}

func (s *Server) handleSessionV3PrimaryPlanModeContinueCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeStartCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "continue_checkpoint", s.planLifecycle.ContinueCheckpoint)
}

func (s *Server) handleSessionV3PrimaryPlanModeStartCheckpointWithMethod(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID, transition string, method func(sessionruntime.PlanLifecycleExecutionInput) (sessionruntime.PlanLifecycleResult, error)) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeCheckpointStartRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		writeError(w, status, err)
		return
	}
	input := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, checkpointID)
	result, err := method(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runStart, status, err := s.startSessionsV3PlanModeRun(principal, sessionID, transition, result, req.SuppressLifecycleMessage)
	if err != nil {
		writeError(w, status, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, transition, result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModeAcceptCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeCheckpointAcceptRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.AcceptCheckpoint(sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: req.PlanID, CheckpointID: checkpointID, Result: req.Result, Notes: req.Notes, ReviewedAt: req.ReviewedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "accept_checkpoint", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeRestartCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "restart_checkpoint", s.planLifecycle.RestartCheckpointRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeRewindCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "rewind_to_checkpoint", s.planLifecycle.RewindCheckpointRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID, transition string, method func(sessionruntime.PlanLifecycleExecutionInput) (sessionruntime.PlanLifecycleResult, error)) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeCheckpointResetRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		writeError(w, status, err)
		return
	}
	input := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, checkpointID)
	result, err := method(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runStart, status, err := s.startSessionsV3PlanModeRun(principal, sessionID, transition, result, false)
	if err != nil {
		writeError(w, status, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, transition, result, runStart)
}

func (s *Server) prepareSessionsV3PlanModeLifecycle(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) bool {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return false
	}
	if s.planLifecycle == nil {
		writeError(w, http.StatusInternalServerError, errors.New("plan lifecycle service not configured"))
		return false
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	} else if !found {
		writeSessionNotFound(w)
		return false
	}
	return true
}

func decodeSessionsV3PlanModeRequest(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if err := dec.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) sessionsV3PlanModeRunInput(sessionID, planID, checkpointID string) sessionruntime.PlanLifecycleExecutionInput {
	runID := sessionsV3PlanModeRunID(sessionID, planID, checkpointID)
	return sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: strings.TrimSpace(planID), CheckpointID: strings.TrimSpace(checkpointID), RunID: runID, RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: time.Now().UnixMilli()}
}

func sessionsV3PlanModeRunID(sessionID, planID, checkpointID string) string {
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%d", strings.TrimSpace(sessionID), strings.TrimSpace(planID), strings.TrimSpace(checkpointID), time.Now().UnixNano())
	sum := sha256.Sum256([]byte(seed))
	return "desktop-v3-run:" + hex.EncodeToString(sum[:16])
}

func (s *Server) startSessionsV3PlanModeRun(principal identity.Principal, sessionID, transition string, result sessionruntime.PlanLifecycleResult, suppressLifecycleMessage bool) (*sessionsV3PlanModeRunStart, int, error) {
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		return nil, status, err
	}
	checkpointID := strings.TrimSpace(result.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(result.Summary.NextCheckpointID)
	}
	if checkpointID == "" {
		return nil, http.StatusConflict, errors.New("plan lifecycle transition did not select a checkpoint to run")
	}
	runID := ""
	attemptID := strings.TrimSpace(result.AttemptID)
	if result.Plan.Document != nil {
		for _, checkpoint := range result.Plan.Document.Checkpoints {
			if strings.TrimSpace(checkpoint.ID) == checkpointID {
				runID = strings.TrimSpace(checkpoint.RunID)
				if attemptID == "" {
					attemptID = strings.TrimSpace(checkpoint.AttemptID)
				}
				break
			}
		}
		if runID == "" && result.Plan.Document.ExecutionState != nil && strings.TrimSpace(result.Plan.Document.ActiveCheckpointID) == checkpointID {
			runID = strings.TrimSpace(result.Plan.Document.ExecutionState.CurrentRunID)
			if attemptID == "" {
				attemptID = strings.TrimSpace(result.Plan.Document.ExecutionState.ActiveAttemptID)
			}
		}
	}
	if runID == "" {
		return nil, http.StatusConflict, errors.New("plan lifecycle transition did not assign a run id")
	}
	if result.PlanEvent != nil {
		if err := s.publishCommittedPlanSaved(result.Plan, result.PlanEvent); err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	if !suppressLifecycleMessage {
		if err := s.publishPlanLifecycleSystemMessage(principal, sessionID, transition, result); err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	payloadHash := sessionsV3PlanModeRunIntentPayloadHash(sessionID, runID, checkpointID, attemptID)
	clientRequestID := fmt.Sprintf("plan-mode-run:%s:%s", strings.TrimSpace(sessionID), strings.TrimSpace(runID))
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		return nil, http.StatusConflict, err
	}
	runStart := &sessionsV3PlanModeRunStart{RunIntent: mutation.RunIntent, CheckpointID: checkpointID, AttemptID: attemptID}
	if !mutation.Replayed && mutation.RunIntent != nil && mutation.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		runStart.Queued = s.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID, PlanID: result.Plan.ID, CheckpointID: checkpointID, AttemptID: attemptID, ParentSessionID: sessionID})
	}
	return runStart, http.StatusAccepted, nil
}

func (s *Server) preflightSessionsV3PlanModeFreshRun(sessionID string) (int, error) {
	if status, err := s.preflightSessionsV3PlanFreshRun(nil, identity.Principal{}, sessionID); err != nil {
		return status, err
	}
	if s.v3SessionExecutor == nil {
		return http.StatusInternalServerError, errors.New("sessions v3 executor is not configured")
	}
	if active, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return http.StatusBadRequest, err
	} else if ok {
		switch active.Status {
		case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
			return http.StatusConflict, fmt.Errorf("session already has active run %q", active.RunID)
		}
	}
	return http.StatusOK, nil
}

func sessionsV3PlanModeRunIntentPayloadHash(sessionID, runID, checkpointID, attemptID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(checkpointID) + "\x00" + strings.TrimSpace(attemptID)))
	return hex.EncodeToString(sum[:])
}

func (s *Server) finishSessionsV3PlanModeLifecycle(w http.ResponseWriter, principal identity.Principal, sessionID, transition string, result sessionruntime.PlanLifecycleResult, runStart *sessionsV3PlanModeRunStart) {
	if runStart == nil {
		if err := s.publishPlanLifecycleResult(result); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.publishPlanLifecycleSystemMessage(principal, sessionID, transition, result); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	} else if result.ModeEvent != nil {
		if err := s.publishCommittedModeUpdated(result); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	payload := map[string]any{"ok": true, "session_id": strings.TrimSpace(sessionID), "transition": transition, "execution_summary": result.Summary}
	if strings.TrimSpace(result.Plan.ID) != "" {
		payload["plan_id"] = strings.TrimSpace(result.Plan.ID)
		payload["plan"] = result.Plan
	}
	if strings.TrimSpace(result.Session.ID) != "" {
		payload["session"] = result.Session
	}
	if runStart != nil {
		if runStart.RunIntent != nil {
			payload["run_intent"] = runStart.RunIntent
		}
		if runStart.CheckpointID != "" {
			payload["checkpoint_id"] = runStart.CheckpointID
		}
		if runStart.AttemptID != "" {
			payload["attempt_id"] = runStart.AttemptID
		}
		payload["run_queued"] = runStart.Queued
	}
	_ = principal
	writeJSON(w, http.StatusOK, payload)
}
