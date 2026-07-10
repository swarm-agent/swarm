package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	client "swarm-refactor/swarmtui/internal/client"
)

func TestPlanLifecycleEndpointAndPayloads(t *testing.T) {
	tests := []struct {
		name, wantPath string
		call           func(*client.API) error
		want           map[string]any
	}{
		{"submit", "/v3/sessions/session%201/plan-mode/plans/plan%2F1/submit", func(api *client.API) error {
			_, err := api.SubmitSessionV3Plan(context.Background(), "session 1", "plan/1", client.SessionPlanSubmitRequest{Title: "Plan", SessionPlanExecutionOptions: client.SessionPlanExecutionOptions{ExecutionGranularity: "checkpointed"}})
			return err
		}, map[string]any{"title": "Plan", "execution_granularity": "checkpointed"}},
		{"approve", "/v3/sessions/session%201/plan-mode/plans/plan%2F1/approve", func(api *client.API) error {
			v := true
			_, err := api.ApproveSessionV3Plan(context.Background(), "session 1", "plan/1", client.SessionPlanExecutionOptions{ContinuationPolicy: "automatic", ContinueAutomatically: &v})
			return err
		}, map[string]any{"continuation_policy": "automatic", "continue_automatically": true}},
		{"pause", "/v3/sessions/session%201/plan-mode/runs/current/pause", func(api *client.API) error {
			_, err := api.PauseSessionV3PlanRun(context.Background(), "session 1", client.SessionPlanCurrentRunRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"checkpoint accept", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/accept", func(api *client.API) error {
			_, err := api.AcceptSessionV3PlanCheckpoint(context.Background(), "session 1", "cp/1", client.SessionPlanCheckpointAcceptRequest{PlanID: "plan/1", Result: "ok", ReviewedAt: 7})
			return err
		}, map[string]any{"plan_id": "plan/1", "result": "ok", "reviewed_at": float64(7)}},
		{"start automatic", "/v3/sessions/session%201/plan-mode/plans/plan%2F1/start-automatic", func(api *client.API) error {
			_, err := api.StartSessionV3PlanAutomatic(context.Background(), "session 1", "plan/1", client.SessionPlanExecutionOptions{CheckpointID: "cp-1"})
			return err
		}, map[string]any{"checkpoint_id": "cp-1"}},
		{"start checkpointed", "/v3/sessions/session%201/plan-mode/plans/plan%2F1/start-checkpointed", func(api *client.API) error {
			_, err := api.StartSessionV3PlanCheckpointed(context.Background(), "session 1", "plan/1", client.SessionPlanExecutionOptions{CheckpointID: "cp-1"})
			return err
		}, map[string]any{"checkpoint_id": "cp-1"}},
		{"stop", "/v3/sessions/session%201/plan-mode/runs/current/stop", func(api *client.API) error {
			_, err := api.StopSessionV3PlanRun(context.Background(), "session 1", client.SessionPlanCurrentRunRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"resume automatic", "/v3/sessions/session%201/plan-mode/runs/current/resume-automatic", func(api *client.API) error {
			_, err := api.ResumeSessionV3PlanAutomatic(context.Background(), "session 1", client.SessionPlanCurrentRunRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"resume checkpointed", "/v3/sessions/session%201/plan-mode/runs/current/resume-checkpointed", func(api *client.API) error {
			_, err := api.ResumeSessionV3PlanCheckpointed(context.Background(), "session 1", client.SessionPlanCurrentRunRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"checkpoint start", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/start", func(api *client.API) error {
			_, err := api.StartSessionV3PlanCheckpoint(context.Background(), "session 1", "cp/1", client.SessionPlanCheckpointStartRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"checkpoint continue", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/continue", func(api *client.API) error {
			_, err := api.ContinueSessionV3PlanCheckpoint(context.Background(), "session 1", "cp/1", client.SessionPlanCheckpointStartRequest{PlanID: "plan/1"})
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"checkpoint resolve", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/resolve-block", func(api *client.API) error {
			_, err := api.ResolveSessionV3BlockedCheckpoint(context.Background(), "session 1", "cp/1", client.SessionPlanCheckpointResolveRequest{PlanID: "plan/1", StartNext: true})
			return err
		}, map[string]any{"plan_id": "plan/1", "start_next": true}},
		{"checkpoint restart", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/restart", func(api *client.API) error {
			_, err := api.RestartSessionV3PlanCheckpoint(context.Background(), "session 1", "cp/1", "plan/1")
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"checkpoint rewind", "/v3/sessions/session%201/plan-mode/checkpoints/cp%2F1/rewind", func(api *client.API) error {
			_, err := api.RewindSessionV3PlanCheckpoint(context.Background(), "session 1", "cp/1", "plan/1")
			return err
		}, map[string]any{"plan_id": "plan/1"}},
		{"revision restore", "/v3/sessions/session%201/plan-mode/lifecycle/restore-revision", func(api *client.API) error {
			_, err := api.RestoreSessionV3PlanRevision(context.Background(), "session 1", client.SessionPlanRevisionRequest{PlanID: "plan/1", Version: 2})
			return err
		}, map[string]any{"plan_id": "plan/1", "version": float64(2)}},
		{"revision restart", "/v3/sessions/session%201/plan-mode/lifecycle/restart-from-revision", func(api *client.API) error {
			_, err := api.RestartSessionV3PlanFromRevision(context.Background(), "session 1", client.SessionPlanRevisionRequest{PlanID: "plan/1", Version: 2})
			return err
		}, map[string]any{"plan_id": "plan/1", "version": float64(2)}},
		{"revision jump", "/v3/sessions/session%201/plan-mode/lifecycle/jump-to-checkpoint", func(api *client.API) error {
			_, err := api.JumpSessionV3PlanToCheckpoint(context.Background(), "session 1", client.SessionPlanRevisionRequest{PlanID: "plan/1", Version: 2, CheckpointID: "cp-2"})
			return err
		}, map[string]any{"plan_id": "plan/1", "version": float64(2), "checkpoint_id": "cp-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var got map[string]any
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.EscapedPath()
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"execution_summary":{"policy_mode":"automatic","execution_shape":"checkpointed"}}`))
			}))
			defer s.Close()
			api := client.New(s.URL)
			api.SetToken("test-token")
			if err := tt.call(api); err != nil {
				t.Fatal(err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("payload[%s] = %#v, want %#v", key, got[key], want)
				}
			}
		})
	}
}

func TestSessionPlanDocumentDecodesExecutionState(t *testing.T) {
	var plan client.SessionPlan
	if err := json.Unmarshal([]byte(`{"id":"p","document":{"execution_policy":{"mode":"automatic","shape":"checkpointed"},"execution_state":{"status":"running","active_attempt_id":"a1"},"original_checkpoints":[{"id":"cp-1"}],"checkpoints":[{"id":"cp-1","status":"needs_review","active_subtask_id":"s1","subtasks":[{"id":"s1","title":"task","status":"completed"}],"review":{"status":"pending"},"recommendation":{"decision":"ship"},"attempts":[{"id":"a1","outcome":"completed"}]}]}}`), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Document.ExecutionState.ActiveAttemptID != "a1" || plan.Document.Checkpoints[0].Subtasks[0].Status != "completed" || plan.Document.Checkpoints[0].Recommendation.Decision != "ship" {
		t.Fatalf("incomplete plan decode: %#v", plan.Document)
	}
}
