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
	runruntime "swarm/packages/swarmd/internal/run"
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
	ContinuationPolicy    string                           `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool                            `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanModeApproveRequest struct {
	ContinuationPolicy    string `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool  `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanModeStartAutomaticRequest struct {
	CheckpointID          string `json:"checkpoint_id,omitempty"`
	ContinuationPolicy    string `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool  `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanModeStartCheckpointedRequest struct {
	CheckpointID          string `json:"checkpoint_id,omitempty"`
	ContinuationPolicy    string `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool  `json:"continue_automatically,omitempty"`
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

type sessionsV3PlanModeCheckpointResolveBlockRequest struct {
	PlanID       string `json:"plan_id,omitempty"`
	Result       string `json:"result,omitempty"`
	Notes        string `json:"notes,omitempty"`
	ReviewedAt   int64  `json:"reviewed_at,omitempty"`
	StartNext    bool   `json:"start_next,omitempty"`
	ContinueNext bool   `json:"continue_next,omitempty"`
}

type sessionsV3PlanLifecycleFollowupRequest struct {
	PlanID                   string   `json:"plan_id,omitempty"`
	CheckpointID             string   `json:"checkpoint_id,omitempty"`
	ChangeRequest            string   `json:"change_request,omitempty"`
	CheckpointTitle          string   `json:"checkpoint_title,omitempty"`
	Title                    string   `json:"title,omitempty"`
	Tasks                    []string `json:"tasks,omitempty"`
	AcceptanceCriteria       []string `json:"acceptance_criteria,omitempty"`
	Notes                    string   `json:"notes,omitempty"`
	SourceMessageID          string   `json:"source_message_id,omitempty"`
	FollowupCheckpointPolicy string   `json:"followup_checkpoint_policy,omitempty"`
	SuppressLifecycleMessage bool     `json:"suppress_lifecycle_message,omitempty"`
}

type sessionsV3PlanLifecycleProposalRequest struct {
	PlanID                string                           `json:"plan_id,omitempty"`
	Title                 string                           `json:"title,omitempty"`
	Plan                  string                           `json:"plan,omitempty"`
	Document              *pebblestore.SessionPlanDocument `json:"document,omitempty"`
	Reason                string                           `json:"reason,omitempty"`
	ContinuationPolicy    string                           `json:"continuation_policy,omitempty"`
	ContinueAutomatically *bool                            `json:"continue_automatically,omitempty"`
}

type sessionsV3PlanLifecycleAmendRequest struct {
	PlanID                  string                           `json:"plan_id,omitempty"`
	Title                   string                           `json:"title,omitempty"`
	Plan                    string                           `json:"plan,omitempty"`
	Document                *pebblestore.SessionPlanDocument `json:"document,omitempty"`
	BaseRevision            int                              `json:"base_revision,omitempty"`
	UpdateSummary           string                           `json:"update_summary,omitempty"`
	Summary                 string                           `json:"summary,omitempty"`
	ReplaceFromCheckpointID string                           `json:"replace_from_checkpoint_id,omitempty"`
	CheckpointID            string                           `json:"checkpoint_id,omitempty"`
	AmendFutureCheckpoints  bool                             `json:"amend_future_checkpoints,omitempty"`
	OverrideStale           bool                             `json:"override_stale,omitempty"`
}

type sessionsV3PlanLifecycleFollowupPolicyRequest struct {
	PlanID                   string `json:"plan_id,omitempty"`
	FollowupCheckpointPolicy string `json:"followup_checkpoint_policy,omitempty"`
	Policy                   string `json:"policy,omitempty"`
	Reason                   string `json:"reason,omitempty"`
}

type sessionsV3PlanLifecycleRestoreRevisionRequest struct {
	PlanID                   string `json:"plan_id,omitempty"`
	Version                  int    `json:"version,omitempty"`
	RevisionVersion          int    `json:"revision_version,omitempty"`
	CheckpointID             string `json:"checkpoint_id,omitempty"`
	ContinuationPolicy       string `json:"continuation_policy,omitempty"`
	ContinueAutomatically    *bool  `json:"continue_automatically,omitempty"`
	Restart                  bool   `json:"restart,omitempty"`
	Start                    bool   `json:"start,omitempty"`
	SkipPrior                bool   `json:"skip_prior,omitempty"`
	SuppressLifecycleMessage bool   `json:"suppress_lifecycle_message,omitempty"`
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
	if tail == "lifecycle/start-session-checkpoint" {
		s.handleSessionV3PrimaryPlanModeStartSessionCheckpoint(w, r, principal, sessionID)
		return
	}
	if tail == "lifecycle/request-followup-checkpoint" {
		s.handleSessionV3PrimaryPlanModeRequestFollowupCheckpoint(w, r, principal, sessionID)
		return
	}
	if tail == "lifecycle/amend-plan" {
		s.handleSessionV3PrimaryPlanModeAmendPlan(w, r, principal, sessionID)
		return
	}
	if tail == "lifecycle/request-new-plan" {
		s.handleSessionV3PrimaryPlanModeRequestNewPlan(w, r, principal, sessionID)
		return
	}
	if tail == "lifecycle/followup-policy" {
		s.handleSessionV3PrimaryPlanModeSetFollowupPolicy(w, r, principal, sessionID)
		return
	}
	if tail == "lifecycle/restore-revision" || tail == "lifecycle/restart-from-revision" || tail == "lifecycle/jump-to-checkpoint" {
		s.handleSessionV3PrimaryPlanModeRestoreRevision(w, r, principal, sessionID, tail == "lifecycle/restart-from-revision", tail == "lifecycle/jump-to-checkpoint")
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
			case "resume":
				s.handleSessionV3PrimaryPlanModeResumeCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "restart":
				s.handleSessionV3PrimaryPlanModeRestartCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "rewind":
				s.handleSessionV3PrimaryPlanModeRewindCheckpoint(w, r, principal, sessionID, parts[0])
				return
			case "resolve-block":
				s.handleSessionV3PrimaryPlanModeResolveBlockedCheckpoint(w, r, principal, sessionID, parts[0])
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
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	transition, err := s.resolveSessionsV3ModeTransition(session, sessionruntime.ModePlan)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("resolve plan model policy: %w", err))
		return
	}
	next := session
	next.Mode = sessionruntime.ModePlan
	next.Preference = transition.Preference
	next.Metadata = cloneSessionsV3Metadata(next.Metadata)
	next.Metadata["agent_profile"] = cloneSessionsV3AgentProfile(transition.ActiveProfile)
	next.Metadata["agent_name"] = strings.TrimSpace(transition.ActiveProfile.Name)
	next.Metadata["resolved_agent_name"] = strings.TrimSpace(transition.ActiveProfile.Name)
	next.UpdatedAt = time.Now().UnixMilli()
	payload := map[string]any{
		"session_id": sessionID, "mode": next.Mode, "updated_at": next.UpdatedAt,
		"preference": transition.Preference, "context_window": transition.ContextWindow, "max_output_tokens": transition.MaxOutputTokens,
		"agent_model_policy": transition.AgentModelPolicy,
	}
	eventPayload, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMode, payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := fmt.Sprintf("plan-mode-enter:%s:%d", sessionID, next.UpdatedAt)
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationUpdateMode, EventType: "session.mode.updated", EventPayload: eventPayload, Session: &next, NowUnixMs: next.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	result := sessionruntime.PlanLifecycleResult{Session: next, Action: "enter_plan_mode", Message: "entered plan mode", ModeChanged: true, V3Mutation: &mutation}
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
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	transition, err := s.resolveSessionsV3ModeTransition(session, sessionruntime.ModeAuto)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("resolve auto model policy: %w", err))
		return
	}
	activeProfile := cloneSessionsV3AgentProfile(transition.ActiveProfile)
	result, err := s.planLifecycle.SubmitPlanForApproval(sessionruntime.PlanLifecyclePlanInput{
		SessionID:             sessionID,
		PlanID:                planID,
		Title:                 req.Title,
		Plan:                  req.Plan,
		Document:              req.Document,
		AgentCanSubmit:         true,
		ContinuationPolicy:    req.ContinuationPolicy,
		ContinueAutomatically: req.ContinueAutomatically,
		ApplySessionMutation:  s.applySessionV3PrimaryMutation,
		ModeEventFields: map[string]any{
			"preference":         transition.Preference,
			"context_window":     transition.ContextWindow,
			"max_output_tokens":  transition.MaxOutputTokens,
			"agent_model_policy": transition.AgentModelPolicy,
		},
		ModePreference:        transition.Preference,
		ModeAgentProfile:      &activeProfile,
		BuildLifecycleMessage: func(plan pebblestore.SessionPlanSnapshot, summary sessionruntime.PlanExecutionSummary) *pebblestore.MessageSnapshot {
			message, ok := runruntime.BuildPlanExecutionLifecycleSystemMessage(runruntime.PlanExecutionLifecycleMessageInput{Action: "approve_and_start", Plan: plan, Payload: map[string]any{"action": "approve_and_start", "checkpoint_id": summary.NextCheckpointID, "next_checkpoint_id": summary.NextCheckpointID, "next_action": "run_checkpoint_with_fresh_context"}})
			if !ok {
				return nil
			}
			return &pebblestore.MessageSnapshot{Role: "system", Content: message.Content, Metadata: message.Metadata}
		},
	})
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
	result, err := s.planLifecycle.ApprovePlan(sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: planID, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically})
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
	input, err := s.sessionsV3PlanModeRunInput(sessionID, planID, strings.TrimSpace(req.CheckpointID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	input.ContinuationPolicy = req.ContinuationPolicy
	input.ContinueAutomatically = req.ContinueAutomatically
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
	input, err := s.sessionsV3PlanModeRunInput(sessionID, planID, strings.TrimSpace(req.CheckpointID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	input.ContinuationPolicy = req.ContinuationPolicy
	input.ContinueAutomatically = req.ContinueAutomatically
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

func (s *Server) handleSessionV3PrimaryPlanModeStartSessionCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleFollowupRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	checkpointID := strings.TrimSpace(firstNonEmptyString(req.CheckpointID, "cp-1"))
	if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
		writeError(w, status, err)
		return
	}
	input, err := s.sessionsV3PlanModeRunInput(sessionID, "", checkpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result, err := s.planLifecycle.StartSessionCheckpoint(sessionruntime.PlanLifecycleSessionCheckpointInput{SessionID: sessionID, ChangeRequest: req.ChangeRequest, Title: firstNonEmptyString(req.CheckpointTitle, req.Title), CheckpointID: checkpointID, Tasks: req.Tasks, AcceptanceCriteria: req.AcceptanceCriteria, Notes: req.Notes, SourceMessageID: req.SourceMessageID, RunID: input.RunID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, StartedAt: input.StartedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var runStart *sessionsV3PlanModeRunStart
	if result.Summary.NextCheckpointID != "" && result.Summary.NextCheckpointStatus == sessionruntime.PlanCheckpointStatusInProgress && strings.TrimSpace(result.AttemptID) != "" {
		var status int
		runStart, status, err = s.startSessionsV3PlanModeRun(principal, sessionID, "start_session_checkpoint", result, req.SuppressLifecycleMessage)
		if err != nil {
			writeError(w, status, err)
			return
		}
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "start_session_checkpoint", result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModeRequestFollowupCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleFollowupRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input, err := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result, err := s.planLifecycle.RequestFollowupCheckpoint(sessionruntime.PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: req.PlanID, ChangeRequest: req.ChangeRequest, Title: firstNonEmptyString(req.CheckpointTitle, req.Title), Tasks: req.Tasks, AcceptanceCriteria: req.AcceptanceCriteria, Notes: req.Notes, SourceMessageID: req.SourceMessageID, GlobalDefaultPolicy: req.FollowupCheckpointPolicy, ApprovalConfirmed: true, RunID: input.RunID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, StartedAt: input.StartedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var runStart *sessionsV3PlanModeRunStart
	if result.Summary.NextCheckpointID != "" && result.Summary.NextCheckpointStatus == sessionruntime.PlanCheckpointStatusInProgress && strings.TrimSpace(result.AttemptID) != "" {
		var status int
		runStart, status, err = s.startSessionsV3PlanModeRun(principal, sessionID, "request_followup_checkpoint", result, req.SuppressLifecycleMessage)
		if err != nil {
			writeError(w, status, err)
			return
		}
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "request_followup_checkpoint", result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModeRequestNewPlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryPlanModeProposal(w, r, principal, sessionID, "request_new_plan", s.planLifecycle.RequestNewPlan)
}

func (s *Server) handleSessionV3PrimaryPlanModeAmendPlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleAmendRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.AmendPlan(sessionruntime.PlanLifecycleAmendmentInput{SessionID: sessionID, PlanID: req.PlanID, Title: req.Title, Plan: req.Plan, Document: req.Document, BaseRevision: req.BaseRevision, UpdateSummary: firstNonEmptyString(req.UpdateSummary, req.Summary), ReplaceFromCheckpointID: firstNonEmptyString(req.ReplaceFromCheckpointID, req.CheckpointID), AmendFutureCheckpoints: req.AmendFutureCheckpoints, OverrideStale: req.OverrideStale})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "amend_plan", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeRestoreRevision(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string, restartPath bool, jumpPath bool) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleRestoreRevisionRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	version := req.Version
	if version <= 0 {
		version = req.RevisionVersion
	}
	start := req.Start || restartPath || jumpPath
	skipPrior := req.SkipPrior || jumpPath
	input := sessionruntime.PlanLifecycleRevisionRestoreInput{SessionID: sessionID, PlanID: req.PlanID, Version: version, CheckpointID: req.CheckpointID, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically, Restart: req.Restart || restartPath || jumpPath, Start: start, SkipPrior: skipPrior}
	if start {
		if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
			writeError(w, status, err)
			return
		}
		runInput, runInputErr := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, req.CheckpointID)
		if runInputErr != nil {
			writeError(w, http.StatusInternalServerError, runInputErr)
			return
		}
		input.RunID = runInput.RunID
		input.RunSessionID = runInput.RunSessionID
		input.ParentSessionID = runInput.ParentSessionID
		input.StartedAt = runInput.StartedAt
	}
	transition := "restore_revision"
	if jumpPath || skipPrior {
		transition = "jump_to_checkpoint"
	} else if restartPath || start {
		transition = "restart_from_revision"
	}
	result, err := s.planLifecycle.RestorePlanRevision(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var runStart *sessionsV3PlanModeRunStart
	if start {
		var status int
		runStart, status, err = s.startSessionsV3PlanModeRun(principal, sessionID, transition, result, req.SuppressLifecycleMessage)
		if err != nil {
			writeError(w, status, err)
			return
		}
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, transition, result, runStart)
}

func (s *Server) handleSessionV3PrimaryPlanModeSetFollowupPolicy(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleFollowupPolicyRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.planLifecycle.SetFollowupCheckpointPolicy(sessionruntime.PlanLifecycleFollowupPolicyInput{SessionID: sessionID, PlanID: req.PlanID, FollowupCheckpointPolicy: firstNonEmptyString(req.FollowupCheckpointPolicy, req.Policy), Reason: req.Reason})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "set_followup_checkpoint_policy", result, nil)
}

func (s *Server) handleSessionV3PrimaryPlanModeProposal(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, transition string, method func(sessionruntime.PlanLifecycleProposalInput) (sessionruntime.PlanLifecycleResult, error)) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanLifecycleProposalRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := method(sessionruntime.PlanLifecycleProposalInput{SessionID: sessionID, PlanID: req.PlanID, Title: req.Title, Plan: req.Plan, Document: req.Document, Reason: req.Reason, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, transition, result, nil)
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
	input, err := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, checkpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
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

func (s *Server) handleSessionV3PrimaryPlanModeResumeCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "resume_checkpoint", s.planLifecycle.ResumeCheckpointRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeRestartCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "restart_checkpoint", s.planLifecycle.RestartCheckpointRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeRewindCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	s.handleSessionV3PrimaryPlanModeResetCheckpointWithMethod(w, r, principal, sessionID, checkpointID, "rewind_to_checkpoint", s.planLifecycle.RewindCheckpointRun)
}

func (s *Server) handleSessionV3PrimaryPlanModeResolveBlockedCheckpoint(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, checkpointID string) {
	if !s.prepareSessionsV3PlanModeLifecycle(w, r, principal, sessionID) {
		return
	}
	var req sessionsV3PlanModeCheckpointResolveBlockRequest
	if err := decodeSessionsV3PlanModeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	startNext := req.StartNext || req.ContinueNext
	if startNext {
		if status, err := s.preflightSessionsV3PlanModeFreshRun(sessionID); err != nil {
			writeError(w, status, err)
			return
		}
	}
	input, err := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, checkpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	input.Result = req.Result
	input.Notes = req.Notes
	input.ReviewedAt = req.ReviewedAt
	input.StartNext = startNext
	result, err := s.planLifecycle.ResolveBlockedCheckpoint(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var runStart *sessionsV3PlanModeRunStart
	if startNext && result.Summary.NextCheckpointID != "" && result.Summary.NextCheckpointStatus == sessionruntime.PlanCheckpointStatusInProgress && strings.TrimSpace(result.AttemptID) != "" {
		var status int
		runStart, status, err = s.startSessionsV3PlanModeRun(principal, sessionID, "resolve_blocked_checkpoint", result, false)
		if err != nil {
			writeError(w, status, err)
			return
		}
	}
	s.finishSessionsV3PlanModeLifecycle(w, principal, sessionID, "resolve_blocked_checkpoint", result, runStart)
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
	input, err := s.sessionsV3PlanModeRunInput(sessionID, req.PlanID, checkpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
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

func (s *Server) sessionsV3PlanModeRunInput(sessionID, planID, checkpointID string) (sessionruntime.PlanLifecycleExecutionInput, error) {
	attemptOrdinal := 1
	resolvedPlanID := strings.TrimSpace(planID)
	resolvedCheckpointID := strings.TrimSpace(checkpointID)
	if s != nil && s.sessions != nil {
		if plan, ok, err := s.sessions.GetActivePlan(strings.TrimSpace(sessionID)); err == nil && ok {
			if resolvedPlanID == "" {
				resolvedPlanID = strings.TrimSpace(plan.ID)
			}
			if plan.Document != nil {
				if resolvedCheckpointID == "" {
					resolvedCheckpointID = strings.TrimSpace(plan.Document.ActiveCheckpointID)
				}
				for _, checkpoint := range plan.Document.Checkpoints {
					if strings.TrimSpace(checkpoint.ID) == resolvedCheckpointID {
						attemptOrdinal = len(checkpoint.Attempts) + 1
						break
					}
				}
			}
		}
	}
	for {
		attemptID := fmt.Sprintf("%s:attempt-%d", resolvedCheckpointID, attemptOrdinal)
		runID := sessionsV3PlanModeRunID(sessionID, resolvedPlanID, resolvedCheckpointID, attemptID)
		if s == nil || s.sessions == nil {
			return sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: resolvedPlanID, CheckpointID: resolvedCheckpointID, AttemptID: attemptID, RunID: runID, RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: time.Now().UnixMilli()}, nil
		}
		_, exists, err := s.sessions.GetV3SessionRunIntent(strings.TrimSpace(sessionID), runID)
		if err != nil {
			return sessionruntime.PlanLifecycleExecutionInput{}, fmt.Errorf("check checkpoint run id uniqueness: %w", err)
		}
		if !exists {
			return sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: resolvedPlanID, CheckpointID: resolvedCheckpointID, AttemptID: attemptID, RunID: runID, RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: time.Now().UnixMilli()}, nil
		}
		attemptOrdinal++
	}
}

func sessionsV3PlanModeRunID(sessionID, planID, checkpointID, attemptID string) string {
	seed := strings.Join([]string{strings.TrimSpace(sessionID), strings.TrimSpace(planID), strings.TrimSpace(checkpointID), strings.TrimSpace(attemptID)}, "\x00")
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
			return nil, http.StatusBadRequest, s.reconcileSessionsV3PlanModeStartFailure(result, sessionID, checkpointID, attemptID, runID, err)
		}
	}
	if !suppressLifecycleMessage {
		if err := s.publishPlanLifecycleSystemMessage(principal, sessionID, transition, result); err != nil {
			return nil, http.StatusBadRequest, s.reconcileSessionsV3PlanModeStartFailure(result, sessionID, checkpointID, attemptID, runID, err)
		}
	}
	payloadHash := sessionsV3PlanModeRunIntentPayloadHash(sessionID, runID, checkpointID, attemptID)
	clientRequestID := fmt.Sprintf("plan-mode-run:%s:%s", strings.TrimSpace(sessionID), strings.TrimSpace(runID))
	resumeContext := strings.EqualFold(strings.TrimSpace(transition), "resume_checkpoint")
	epochResult, err := s.sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, PayloadHash: payloadHash, Reason: strings.TrimSpace(transition), PlanID: strings.TrimSpace(result.Plan.ID), CheckpointID: checkpointID, AttemptID: attemptID, RunID: runID, RunSessionID: sessionID, ParentSessionID: sessionID, ResumeContext: resumeContext, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		// A failure after Pebble committed the epoch (for example while publishing
		// its outbox head) still leaves an exact durable executor intent. Recover
		// that intent rather than pausing a run which can safely execute.
		if committed, ok, readErr := s.sessions.GetV3SessionRunIntent(sessionID, runID); readErr == nil && ok {
			epochResult.Epoch.EpochID = committed.EpochID
			epochResult.RunIntent = &committed
			err = nil
		} else {
			return nil, http.StatusConflict, s.reconcileSessionsV3PlanModeStartFailure(result, sessionID, checkpointID, attemptID, runID, err)
		}
	}
	if epochResult.RunIntent == nil {
		return nil, http.StatusConflict, s.reconcileSessionsV3PlanModeStartFailure(result, sessionID, checkpointID, attemptID, runID, errors.New("execution epoch did not return its committed pending run intent"))
	}
	intent := *epochResult.RunIntent
	_ = s.publishCommittedV3RealtimeOutbox(epochResult.Outbox)
	runStart := &sessionsV3PlanModeRunStart{RunIntent: &intent, CheckpointID: checkpointID, AttemptID: attemptID}
	if !epochResult.Replayed && intent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		runStart.Queued = s.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID, EpochID: epochResult.Epoch.EpochID, PlanID: result.Plan.ID, CheckpointID: checkpointID, AttemptID: attemptID, ParentSessionID: sessionID, ResumeContext: resumeContext})
	}
	return runStart, http.StatusAccepted, nil
}

func (s *Server) reconcileSessionsV3PlanModeStartFailure(result sessionruntime.PlanLifecycleResult, sessionID, checkpointID, attemptID, runID string, startErr error) error {
	if s == nil || s.planLifecycle == nil {
		return startErr
	}
	reconciled, changed, err := s.planLifecycle.ReconcileCancelledRun(sessionruntime.PlanLifecycleExecutionInput{
		SessionID:       strings.TrimSpace(sessionID),
		PlanID:          strings.TrimSpace(result.Plan.ID),
		CheckpointID:    strings.TrimSpace(checkpointID),
		AttemptID:       strings.TrimSpace(attemptID),
		RunID:           strings.TrimSpace(runID),
		RunSessionID:    strings.TrimSpace(sessionID),
		ParentSessionID: strings.TrimSpace(sessionID),
		Notes:           "checkpoint start failed before a durable executor intent was available: " + startErr.Error(),
		ReviewedAt:      time.Now().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("%w; reconcile failed checkpoint start: %v", startErr, err)
	}
	if !changed {
		return fmt.Errorf("%w; checkpoint start ownership could not be reconciled", startErr)
	}
	if err := s.publishPlanLifecycleResult(reconciled); err != nil {
		return fmt.Errorf("%w; reconciled checkpoint start but could not publish reconciliation: %v", startErr, err)
	}
	return startErr
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

func (s *Server) resolveSessionsV3FollowupCheckpointPolicy(result sessionruntime.PlanLifecycleResult) string {
	globalDefault := ""
	if s != nil && s.uiSettings != nil {
		accountScopeID := strings.TrimSpace(result.Plan.AccountScopeID)
		if accountScopeID == "" {
			accountScopeID = strings.TrimSpace(result.Session.AccountScopeID)
		}
		if settings, err := s.uiSettings.GetForAccount(accountScopeID); err == nil {
			globalDefault = strings.TrimSpace(settings.Chat.FollowupCheckpointPolicyDefault)
		}
	}
	return sessionruntime.ResolvePlanFollowupCheckpointPolicy(result.Plan.Document, globalDefault)
}

func (s *Server) finishSessionsV3PlanModeLifecycle(w http.ResponseWriter, principal identity.Principal, sessionID, transition string, result sessionruntime.PlanLifecycleResult, runStart *sessionsV3PlanModeRunStart) {
	if result.V3Mutation != nil {
		// Native V3 lifecycle commits already contain ordered durable events and
		// outbox rows; replaying legacy envelopes would duplicate them.
	} else if runStart == nil {
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
	changeType := strings.TrimPrefix(strings.TrimSpace(transition), "request_")
	if changeType == "" || changeType == transition {
		changeType = strings.TrimSpace(result.Action)
	}
	policyEffective := ""
	if transition == "request_followup_checkpoint" && result.Plan.Document != nil {
		policyEffective = s.resolveSessionsV3FollowupCheckpointPolicy(result)
	}
	approvalRequired := transition == "amend_plan" || transition == "request_new_plan" || (transition == "request_followup_checkpoint" && policyEffective == sessionruntime.PlanFollowupCheckpointPolicyRequireApproval)
	runQueued := false
	if runStart != nil {
		runQueued = runStart.Queued
	}
	payload := map[string]any{"ok": true, "session_id": strings.TrimSpace(sessionID), "transition": transition, "change_type": changeType, "policy_effective": policyEffective, "approval_required": approvalRequired, "run_queued": runQueued, "execution_summary": result.Summary}
	if strings.TrimSpace(result.Plan.ID) != "" {
		payload["plan_id"] = strings.TrimSpace(result.Plan.ID)
		payload["plan"] = result.Plan
	}
	if strings.TrimSpace(result.CheckpointID) != "" {
		payload["checkpoint_id"] = strings.TrimSpace(result.CheckpointID)
	}
	if strings.TrimSpace(result.AttemptID) != "" {
		payload["attempt_id"] = strings.TrimSpace(result.AttemptID)
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
