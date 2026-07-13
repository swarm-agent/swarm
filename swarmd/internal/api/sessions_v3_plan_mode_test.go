package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PlanModeDedicatedLifecycleEndpointsSuccess(t *testing.T) {
	t.Run("enter plan mode", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-enter-create", "plan mode enter")
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModeAuto); err != nil {
			t.Fatalf("set auto mode: %v", err)
		}

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/enter", `{}`)
		if payload["transition"] != "enter_plan_mode" {
			t.Fatalf("transition = %v", payload["transition"])
		}
		updated, ok, err := sessionSvc.GetSession(created.ID)
		if err != nil || !ok {
			t.Fatalf("get session: ok=%v err=%v", ok, err)
		}
		if sessionruntime.NormalizeMode(updated.Mode) != sessionruntime.ModePlan {
			t.Fatalf("mode = %q, want plan", updated.Mode)
		}
	})

	t.Run("submit plan for approval", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-submit-create", "plan mode submit")
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModePlan); err != nil {
			t.Fatalf("set plan mode: %v", err)
		}
		doc := sessionsV3PlanModeTestDocument("plan-submit", sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusPending}})
		rawDoc, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal doc: %v", err)
		}
		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/plans/plan-submit/submit", fmt.Sprintf(`{"title":"Submit Plan","document":%s}`, rawDoc))
		if payload["transition"] != "submit_plan" || payload["plan_id"] != "plan-submit" {
			t.Fatalf("payload = %#v", payload)
		}
		updated, ok, err := sessionSvc.GetSession(created.ID)
		if err != nil || !ok {
			t.Fatalf("get session: ok=%v err=%v", ok, err)
		}
		if sessionruntime.NormalizeMode(updated.Mode) != sessionruntime.ModeAuto {
			t.Fatalf("mode = %q, want auto", updated.Mode)
		}
	})

	t.Run("approve plan", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-approve-create", "plan mode approve")
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-approve", sessionsV3PlanModeTestDocument("plan-approve", "", "", "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}}), "draft")
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModeAuto); err != nil {
			t.Fatalf("set auto mode: %v", err)
		}

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/plans/plan-approve/approve", `{"continuation_policy":"review_each_checkpoint"}`)
		if payload["transition"] != "approve_plan" || payload["plan_id"] != "plan-approve" {
			t.Fatalf("payload = %#v", payload)
		}
		plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-approve")
		if plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint || plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed {
			t.Fatalf("policy = %#v", plan.Document.ExecutionPolicy)
		}
	})

	t.Run("start plan automatic queues backend run", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-start-auto-create", "plan mode start auto")
		sessionsV3PlanModeInstallDelayedExecutor(server)
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-start-auto", sessionsV3PlanModeTestDocument("plan-start-auto", "", "", "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}}), "draft")
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModeAuto); err != nil {
			t.Fatalf("set auto mode: %v", err)
		}

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/plans/plan-start-auto/start-automatic", `{"continuation_policy":"automatic","continue_automatically":true}`)
		assertSessionsV3PlanModeQueuedRun(t, payload, sessionSvc, created.ID, "start_plan_automatic", "cp-1")
		plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-start-auto")
		if plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic {
			t.Fatalf("policy = %#v", plan.Document.ExecutionPolicy)
		}
	})

	t.Run("start plan checkpointed queues backend run", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-start-checkpointed-create", "plan mode start checkpointed")
		sessionsV3PlanModeInstallDelayedExecutor(server)
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-start-checkpointed", sessionsV3PlanModeTestDocument("plan-start-checkpointed", "", "", "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}, {ID: "cp-2"}}), "draft")
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModeAuto); err != nil {
			t.Fatalf("set auto mode: %v", err)
		}

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/plans/plan-start-checkpointed/start-checkpointed", `{"continuation_policy":"automatic","continue_automatically":true}`)
		assertSessionsV3PlanModeQueuedRun(t, payload, sessionSvc, created.ID, "start_plan_checkpointed", "cp-1")
		plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-start-checkpointed")
		if plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic {
			t.Fatalf("policy = %#v", plan.Document.ExecutionPolicy)
		}
	})

	t.Run("pause and stop current run", func(t *testing.T) {
		for _, action := range []struct {
			name       string
			path       string
			transition string
		}{
			{name: "pause", path: "/plan-mode/runs/current/pause", transition: "pause_plan_run"},
			{name: "stop", path: "/plan-mode/runs/current/stop", transition: "stop_plan_run"},
		} {
			t.Run(action.name, func(t *testing.T) {
				server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-"+action.name+"-create", "plan mode "+action.name)
				doc := sessionsV3PlanModeTestDocument("plan-"+action.name, sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-1", SessionID: created.ID}})
				doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-1", CurrentSessionID: created.ID}
				sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-"+action.name, doc, "approved")

				payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, action.path, `{}`)
				if payload["transition"] != action.transition {
					t.Fatalf("payload = %#v", payload)
				}
				plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-"+action.name)
				if plan.Document.ExecutionState == nil || plan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateIdle {
					t.Fatalf("execution state = %#v, want idle", plan.Document.ExecutionState)
				}
			})
		}
	})

	t.Run("resume policy switching", func(t *testing.T) {
		for _, action := range []struct {
			name       string
			path       string
			transition string
			wantMode   string
			startMode  string
		}{
			{name: "resume automatic", path: "/plan-mode/runs/current/resume-automatic", transition: "resume_automatic", wantMode: sessionruntime.PlanExecutionPolicyModeAutomatic, startMode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint},
			{name: "resume checkpointed", path: "/plan-mode/runs/current/resume-checkpointed", transition: "resume_checkpointed", wantMode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, startMode: sessionruntime.PlanExecutionPolicyModeAutomatic},
		} {
			t.Run(action.name, func(t *testing.T) {
				server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				created := createSessionsV3PrimaryTestSession(t, server, strings.ReplaceAll(action.name, " ", "-")+"-create", action.name)
				doc := sessionsV3PlanModeTestDocument("plan-resume", action.startMode, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}})
				sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-resume", doc, "approved")

				payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, action.path, `{}`)
				if payload["transition"] != action.transition {
					t.Fatalf("payload = %#v", payload)
				}
				plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-resume")
				if plan.Document.ExecutionPolicy.Mode != action.wantMode {
					t.Fatalf("mode = %q, want %q", plan.Document.ExecutionPolicy.Mode, action.wantMode)
				}
			})
		}
	})

	t.Run("checkpoint start continue restart and rewind queue backend runs", func(t *testing.T) {
		for _, action := range []struct {
			name       string
			path       string
			transition string
			doc        *pebblestore.SessionPlanDocument
		}{
			{name: "start", path: "/plan-mode/checkpoints/cp-1/start", transition: "start_checkpoint", doc: sessionsV3PlanModeTestDocument("plan-start-cp", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}})},
			{name: "continue", path: "/plan-mode/checkpoints/cp-1/continue", transition: "continue_checkpoint", doc: sessionsV3PlanModeTestDocument("plan-continue-cp", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}})},
			{name: "restart", path: "/plan-mode/checkpoints/cp-1/restart", transition: "restart_checkpoint", doc: sessionsV3PlanModeTestDocument("plan-restart-cp", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "old-attempt", RunID: "old-run"}})},
			{name: "rewind", path: "/plan-mode/checkpoints/cp-1/rewind", transition: "rewind_to_checkpoint", doc: sessionsV3PlanModeTestDocument("plan-rewind-cp", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-2", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusApproved}}, {ID: "cp-2", Status: sessionruntime.PlanCheckpointStatusFailed}})},
		} {
			t.Run(action.name, func(t *testing.T) {
				server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-cp-"+action.name+"-create", "plan mode cp "+action.name)
				sessionsV3PlanModeInstallDelayedExecutor(server)
				sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, action.doc.ID, action.doc, "approved")

				payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, action.path, `{}`)
				assertSessionsV3PlanModeQueuedRun(t, payload, sessionSvc, created.ID, action.transition, "cp-1")
			})
		}
	})

	t.Run("resolve blocked checkpoint emits lifecycle system message", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-resolve-block-create", "plan mode resolve block")
		sessionsV3PlanModeInstallDelayedExecutor(server)
		doc := sessionsV3PlanModeTestDocument("plan-resolve-block", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusBlocked, AttemptID: "attempt-blocked", RunID: "run-blocked"}, {ID: "cp-2", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending}})
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-1", LastAttemptID: "attempt-blocked", LastOutcome: sessionruntime.PlanCheckpointStatusBlocked}
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-resolve-block", doc, "approved")

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/checkpoints/cp-1/resolve-block", `{"start_next":true,"reviewed_at":1234}`)
		if payload["transition"] != "resolve_blocked_checkpoint" || payload["checkpoint_id"] != "cp-2" || payload["run_queued"] != true {
			t.Fatalf("payload = %#v", payload)
		}
		messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) != 1 {
			t.Fatalf("message count = %d, want 1: %#v", len(messages), messages)
		}
		message := messages[0]
		if message.Role != "system" || message.Metadata["source"] != runruntime.PlanExecutionLifecycleMessageSource || message.Metadata["kind"] != "plan_execution_break" || message.Metadata["action"] != "resolve_blocked_checkpoint" || message.Metadata["next_action"] != "run_checkpoint_with_fresh_context" {
			t.Fatalf("message role/metadata = role %q metadata %#v", message.Role, message.Metadata)
		}
		if !strings.Contains(message.Content, "Blocked checkpoint resolved; starting next checkpoint — Automatic mode") || !strings.Contains(message.Content, "Resolved: Checkpoint 1 — Blocked") || !strings.Contains(message.Content, "Checkpoint: Checkpoint 2 — Next") || !strings.Contains(message.Content, "Context: Starting the next checkpoint with fresh context.") {
			t.Fatalf("message content missing resolve/start details: %q", message.Content)
		}
	})

	t.Run("accept checkpoint review", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-accept-create", "plan mode accept")
		doc := sessionsV3PlanModeTestDocument("plan-accept", sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusPending}}, {ID: "cp-2", Status: sessionruntime.PlanCheckpointStatusPending}})
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1", LastOutcome: sessionruntime.PlanCheckpointStatusCompleted}
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-accept", doc, "approved")

		payload := postSessionsV3PlanModeTestJSON(t, server, created.ID, "/plan-mode/checkpoints/cp-1/accept", `{"result":"approved"}`)
		if payload["transition"] != "accept_checkpoint" {
			t.Fatalf("payload = %#v", payload)
		}
		plan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, "plan-accept")
		if plan.Document.Checkpoints[0].Review == nil || plan.Document.Checkpoints[0].Review.Status != sessionruntime.PlanCheckpointReviewStatusApproved || plan.Document.ActiveCheckpointID != "cp-2" {
			t.Fatalf("document after accept = %#v", plan.Document)
		}
		messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) != 1 {
			t.Fatalf("message count = %d, want 1: %#v", len(messages), messages)
		}
		message := messages[0]
		if message.Role != "system" || message.Metadata["source"] != runruntime.PlanExecutionLifecycleMessageSource || message.Metadata["kind"] != "plan_execution_break" || message.Metadata["action"] != "accept_checkpoint" {
			t.Fatalf("message role/metadata = role %q metadata %#v", message.Role, message.Metadata)
		}
		if !strings.Contains(message.Content, "Checkpoint review accepted — Manual review mode") || !strings.Contains(message.Content, "Next: Checkpoint 2") {
			t.Fatalf("message content missing manual review mode/next checkpoint: %q", message.Content)
		}
	})
}

func TestSessionsV3PlanModeDedicatedLifecycleEndpointValidation(t *testing.T) {
	t.Run("rejects invalid source modes and states", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-invalid-create", "plan mode invalid")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/plans/missing/submit", `{}`, http.StatusBadRequest)
		if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModePlan); err != nil {
			t.Fatalf("set plan mode: %v", err)
		}
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/enter", `{}`, http.StatusConflict)
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-invalid", sessionsV3PlanModeTestDocument("plan-invalid", "", "", "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}}), "draft")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/plans/plan-invalid/approve", `{}`, http.StatusBadRequest)
	})

	t.Run("rejects missing active plans missing checkpoints and invalid checkpoints", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-missing-create", "plan mode missing")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/plans/active/approve", `{}`, http.StatusBadRequest)

		sessionsV3PlanModeInstallDelayedExecutor(server)
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-empty", sessionsV3PlanModeTestDocument("plan-empty", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "", nil), "approved")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/checkpoints/cp-missing/start", `{}`, http.StatusBadRequest)
	})

	t.Run("rejects pause when no run is in progress", func(t *testing.T) {
		server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-pause-idle-create", "plan mode pause idle")
		doc := sessionsV3PlanModeTestDocument("plan-pause-idle", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}})
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateIdle}
		sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, "plan-pause-idle", doc, "approved")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/runs/current/pause", `{}`, http.StatusBadRequest)
	})

	t.Run("rejects old overloaded and alias lifecycle paths without fallback", func(t *testing.T) {
		server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		created := createSessionsV3PrimaryTestSession(t, server, "plan-mode-no-alias-create", "plan mode no alias")
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plans/execution", `{"action":"start_plan"}`, http.StatusBadRequest)
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/plans/plan-1/start", `{}`, http.StatusBadRequest)
		postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/checkpoints/cp-1/accept-and-start-next", `{}`, http.StatusBadRequest)
	})
}

func sessionsV3PlanModeTestDocument(planID, mode, shape, activeCheckpointID string, checkpoints []pebblestore.SessionPlanCheckpoint) *pebblestore.SessionPlanDocument {
	return &pebblestore.SessionPlanDocument{
		ID:                 planID,
		Title:              "Plan " + planID,
		Status:             "approved",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: mode, Shape: shape},
		ActiveCheckpointID: activeCheckpointID,
		Checkpoints:        checkpoints,
	}
}

func sessionsV3PlanModeSeedPlan(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, planID string, doc *pebblestore.SessionPlanDocument, status string) pebblestore.SessionPlanSnapshot {
	t.Helper()
	if status == "" {
		status = "approved"
	}
	if doc != nil {
		doc.ID = planID
		if strings.TrimSpace(doc.Title) == "" {
			doc.Title = "Plan " + planID
		}
	}
	plan, _, err := sessionSvc.SavePlanWithMetadata(sessionID, planID, "Plan "+planID, "# Plan "+planID, status, status, true, sessionruntime.PlanSaveMetadata{Document: doc, UpdateSummary: "seed plan", UpdateScope: "test", UpdateKind: "test"})
	if err != nil {
		t.Fatalf("seed plan %s: %v", planID, err)
	}
	return plan
}

func sessionsV3PlanModeGetPlan(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, planID string) pebblestore.SessionPlanSnapshot {
	t.Helper()
	plan, ok, err := sessionSvc.GetPlan(sessionID, planID)
	if err != nil || !ok {
		t.Fatalf("get plan %s: ok=%v err=%v", planID, ok, err)
	}
	if plan.Document == nil {
		t.Fatalf("plan %s missing document", planID)
	}
	return plan
}

func sessionsV3PlanModeInstallDelayedExecutor(server *Server) {
	exec := newSessionV3Executor(server)
	exec.startDelay = time.Hour
	server.v3SessionExecutor = exec
}

func postSessionsV3PlanModeTestJSON(t *testing.T, server *Server, sessionID, path, body string) map[string]any {
	t.Helper()
	rec := postSessionsV3PlanModeTestStatus(t, server, sessionID, path, body, http.StatusOK)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func postSessionsV3PlanModeTestStatus(t *testing.T, server *Server, sessionID, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != wantStatus {
		t.Fatalf("POST %s status = %d, want %d, body=%s", path, rec.Code, wantStatus, rec.Body.String())
	}
	return rec
}

func assertSessionsV3PlanModeQueuedRun(t *testing.T, payload map[string]any, sessionSvc *sessionruntime.Service, sessionID, transition, checkpointID string) {
	t.Helper()
	if payload["transition"] != transition || payload["checkpoint_id"] != checkpointID || payload["run_queued"] != true {
		t.Fatalf("payload = %#v, want transition %q checkpoint %q queued", payload, transition, checkpointID)
	}
	intent, ok, err := sessionSvc.GetSessionActiveRunIntent(sessionID)
	if err != nil || !ok {
		t.Fatalf("active run intent: ok=%v err=%v", ok, err)
	}
	if intent.Status != sessionruntime.RunIntentPendingExecutor || strings.TrimSpace(intent.RunID) == "" || strings.TrimSpace(intent.EpochID) == "" {
		t.Fatalf("intent = %#v, want pending executor with run and epoch ids", intent)
	}
	epoch, ok, err := sessionSvc.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok {
		t.Fatalf("active execution epoch: ok=%v err=%v", ok, err)
	}
	if epoch.EpochID != intent.EpochID || epoch.Boundary.CheckpointID != checkpointID || epoch.Boundary.AttemptID != intent.AttemptID || epoch.Boundary.RunID != intent.RunID {
		t.Fatalf("epoch and intent disagree: epoch=%#v intent=%#v", epoch, intent)
	}
}
