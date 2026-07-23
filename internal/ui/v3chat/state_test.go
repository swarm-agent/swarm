package v3chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

func TestRunIntentLifecycleUsesAuthoritativeTimingAndTerminalState(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "s"},
		ActiveRunIntent: &client.SessionV3RunIntent{
			RunID: "run", Status: "pending_executor", CreatedAt: 1_000, CumulativeDurationMS: 60_000,
		},
	}})
	status, ok := BuildRunStatus(state, time.UnixMilli(3_500))
	if !ok || !status.Active || status.Label != "Running" || status.Timer != "0:02 (1:02)" {
		t.Fatalf("pending run status = %#v/%t", status, ok)
	}
	payload, _ := json.Marshal(map[string]any{"run_intent": client.SessionV3RunIntent{
		RunID: "run", Status: "cancelled", CreatedAt: 1_000, StartedAt: 2_000, CompletedAt: 7_000, DurationMS: 5_000, CumulativeDurationMS: 65_000,
	}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "session.run.cancelled", Payload: payload}}})
	if _, active := SelectActiveRun(state); active {
		t.Fatalf("cancelled run remained active: %#v", state)
	}
	status, ok = BuildRunStatus(state, time.UnixMilli(999_000))
	if !ok || status.Active || status.Label != "Stopped" || status.Timer != "0:05 (1:05)" {
		t.Fatalf("terminal run status = %#v/%t", status, ok)
	}
}

func TestSecondRunShowsCurrentAndAccumulatedTimersAcrossLifecycle(t *testing.T) {
	state := NewState()
	firstTerminal, _ := json.Marshal(map[string]any{"run_intent": client.SessionV3RunIntent{
		RunID: "run-a", Status: "completed", StartedAt: 1_000, CompletedAt: 5_000,
		DurationMS: 4_000, CumulativeDurationMS: 4_000,
	}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{
		Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, Payload: firstTerminal},
	}})
	status, ok := BuildRunStatus(state, time.UnixMilli(5_000))
	if !ok || status.Active || status.Timer != "0:04" {
		t.Fatalf("first terminal run status = %#v/%t, want 0:04", status, ok)
	}

	secondActive, _ := json.Marshal(map[string]any{"run_intent": client.SessionV3RunIntent{
		RunID: "run-b", Status: "running", StartedAt: 10_000, CumulativeDurationMS: 4_000,
	}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{
		Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 2, Payload: secondActive},
	}})
	status, ok = BuildRunStatus(state, time.UnixMilli(28_000))
	if !ok || !status.Active || status.Timer != "0:18 (0:22)" {
		t.Fatalf("second active run status = %#v/%t, want 0:18 (0:22)", status, ok)
	}

	secondTerminal, _ := json.Marshal(map[string]any{"run_intent": client.SessionV3RunIntent{
		RunID: "run-b", Status: "completed", StartedAt: 10_000, CompletedAt: 28_000,
		DurationMS: 18_000, CumulativeDurationMS: 22_000,
	}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{
		Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 3, Payload: secondTerminal},
	}})
	status, ok = BuildRunStatus(state, time.UnixMilli(99_000))
	if !ok || status.Active || status.Timer != "0:18 (0:22)" {
		t.Fatalf("second terminal run status = %#v/%t, want 0:18 (0:22)", status, ok)
	}
}

func TestRepeatedRunIntentUpdatesPreserveCanonicalTimerAnchor(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "s"},
		ActiveRunIntent: &client.SessionV3RunIntent{
			RunID: "run", Status: "running", CreatedAt: 1_000, StartedAt: 2_000,
			CumulativeDurationMS: 60_000, UpdatedAt: 2_000, EventSeq: 1,
		},
	}})
	assertTimer := func(want string) {
		t.Helper()
		status, ok := BuildRunStatus(state, time.UnixMilli(7_500))
		if !ok || !status.Active || status.Timer != want {
			t.Fatalf("run status = %#v/%t, want timer %q", status, ok, want)
		}
	}
	assertTimer("0:05 (1:05)")

	// Executor phase/progress payloads can omit timing while the durable run
	// intent already has the authoritative anchor. They must not reset it.
	for seq, updatedAt := range []int64{3_000, 4_000, 6_000} {
		payload, _ := json.Marshal(map[string]any{"run_intent": client.SessionV3RunIntent{
			RunID: "run", Status: "running", UpdatedAt: updatedAt,
		}})
		state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{
			Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: uint64(seq + 2), Payload: payload},
		}})
		assertTimer("0:05 (1:05)")
	}

	run, ok := SelectActiveRun(state)
	if !ok || run.StartedAt != 2_000 || run.CreatedAt != 1_000 || run.CumulativeDurationMS != 60_000 {
		t.Fatalf("repeated updates replaced canonical timing: %#v/%t", run, ok)
	}

	// A delayed duplicate with a lower canonical event sequence cannot rewind
	// timing or status either.
	state = Reduce(state, MessageResultAction{Result: client.SessionV3MessageResult{RunIntent: client.SessionV3RunIntent{
		RunID: "run", Status: "running", CreatedAt: 6_900, UpdatedAt: 6_900, EventSeq: 2,
	}}})
	assertTimer("0:05 (1:05)")
}

func TestRunTimerStartsAtOneSecondAndNeverUsesUpdatedAtFallback(t *testing.T) {
	state := NewState()
	state.CurrentRun = &RunState{ID: "run", Status: "running", StartedAt: 10_000, UpdatedAt: 10_000}
	state.LatestRun = state.CurrentRun
	status, ok := BuildRunStatus(state, time.UnixMilli(10_000))
	if !ok || !status.Active || status.Timer != "0:00" {
		t.Fatalf("new active run timer = %#v/%t, want 0:00", status, ok)
	}

	state.CurrentRun = &RunState{ID: "run", Status: "running", UpdatedAt: 9_000}
	state.LatestRun = state.CurrentRun
	status, ok = BuildRunStatus(state, time.UnixMilli(10_000))
	if !ok || !status.Active || status.Timer != "" {
		t.Fatalf("update-only run invented reset-prone timer: %#v/%t", status, ok)
	}
}

func TestModeActionShiftsModelAndProfileFromBackendPolicy(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:    client.SessionSummary{ID: "s", Mode: "auto"},
		Preference: client.ModelPreference{Provider: "codex", Model: "auto-model"},
	}})
	state = Reduce(state, ModeAction{Resolved: client.SessionV3ModeResult{
		Mode:       "plan",
		Preference: client.ModelPreference{Provider: "codex", Model: "base-plan-model"},
		AgentModelPolicy: client.SessionV3AgentModelPolicy{
			Locked: true, ProfileName: "Plan profile", ProfileSource: "saved", ProfileMode: "split",
			Preference: client.ModelPreference{Provider: "codex", Model: "resolved-plan-model", Thinking: "high"}, ContextWindow: 200000,
		},
	}})
	if state.Session.Mode != "plan" || state.Model.Preference.Model != "resolved-plan-model" || state.Model.ProfileName != "Plan profile" || state.Model.ProfileSource != "saved" || !state.Model.Locked {
		t.Fatalf("mode/model shift state = %#v", state)
	}
}

func TestSessionModeUpdatedEventAppliesPresentFieldsWithoutErasingCanonicalState(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "s", Mode: "plan", UpdatedAt: 100},
		Preference:      client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "high"},
		ContextWindow:   200000,
		MaxOutputTokens: 12000,
		AgentModelPolicy: client.SessionV3AgentModelPolicy{
			Locked: true, Reason: "plan preset", ProfileName: "Planning", ProfileSource: "saved", ProfileMode: "split",
			Preference: client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "high"}, ContextWindow: 200000, MaxOutputTokens: 12000,
		},
	}})
	payload, _ := json.Marshal(map[string]any{"mode": "auto", "updated_at": int64(200)})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{
		SessionID: "s", Seq: 2, EventType: "session.mode.updated", Payload: payload,
	}}})
	if state.Session.Mode != "auto" || state.Session.UpdatedAt != 200 {
		t.Fatalf("mode event session state = %#v", state.Session)
	}
	if state.Model.Preference.Model != "plan-model" || state.Model.ContextWindow != 200000 || state.Model.MaxOutputTokens != 12000 || state.Model.ProfileName != "Planning" || !state.Model.Locked {
		t.Fatalf("absent mode event fields erased canonical model state: %#v", state.Model)
	}

	older, _ := json.Marshal(map[string]any{"mode": "plan", "updated_at": int64(150)})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{
		SessionID: "s", Seq: 1, EventType: "session.mode.updated", Payload: older,
	}}})
	if state.Session.Mode != "auto" || state.Session.UpdatedAt != 200 {
		t.Fatalf("older mode event rewound canonical state: %#v", state.Session)
	}
}

func TestExitPlanModeRealtimeAndHydrationResolveTheSameCanonicalState(t *testing.T) {
	preference := client.ModelPreference{Provider: "codex", Model: "auto-model", Thinking: "high", ServiceTier: "fast"}
	policy := client.SessionV3AgentModelPolicy{
		Source: "agent_auto_preset", Locked: true, Reason: "auto preset", ProfileName: "Automatic", ProfileSource: "saved", ProfileMode: "split",
		Preference: preference, ContextWindow: 180000, MaxOutputTokens: 16000,
	}
	payload, _ := json.Marshal(map[string]any{
		"mode": "auto", "updated_at": int64(300), "preference": preference,
		"context_window": 180000, "max_output_tokens": 16000, "agent_model_policy": policy,
	})
	realtime := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "s", Mode: "plan", UpdatedAt: 100},
		Preference:      client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "low"},
		ContextWindow:   200000,
		MaxOutputTokens: 12000,
	}})
	realtime = Reduce(realtime, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{
		SessionID: "s", Seq: 3, EventType: "session.mode.updated", Payload: payload,
	}}})
	hydrated := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "s", Mode: "auto", UpdatedAt: 300}, Preference: preference,
		ContextWindow: 180000, MaxOutputTokens: 16000, AgentModelPolicy: policy,
	}})
	if realtime.Session.Mode != hydrated.Session.Mode || realtime.Session.UpdatedAt != hydrated.Session.UpdatedAt || !reflect.DeepEqual(realtime.Model, hydrated.Model) {
		t.Fatalf("realtime and hydrated canonical state differ:\nrealtime=%#v\nhydrated=%#v", realtime, hydrated)
	}
}

func TestHydrateAndRealtimeUsageShareCanonicalContextState(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:      client.SessionSummary{ID: "s"},
		UsageSummary: &client.SessionUsageSummary{ContextWindow: 200000, RemainingTokens: 125000, TotalTokens: 75000},
	}})
	if !state.Usage.Available || state.Usage.ContextWindow != 200000 || state.Usage.RemainingTokens != 125000 {
		t.Fatalf("hydrated usage state = %#v", state.Usage)
	}
	payload, _ := json.Marshal(map[string]any{"usage_summary": client.SessionUsageSummary{ContextWindow: 200000, RemainingTokens: 100000, TotalTokens: 100000}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "run.usage.updated", Payload: payload}}})
	if state.Usage.RemainingTokens != 100000 || state.Usage.TotalTokens != 100000 {
		t.Fatalf("realtime usage state = %#v", state.Usage)
	}
}

func TestCanonicalModeEventReplacesPostDraftHydrationModelState(t *testing.T) {
	autoDraft := client.ModelPreference{Provider: "openrouter", Model: "draft-auto", Thinking: "medium"}
	planDraft := client.ModelPreference{Provider: "codex", Model: "draft-plan", Thinking: "high"}
	state := Reduce(NewState(), PrimeNewSessionAction{
		Create: client.SessionCreateOptions{Mode: "auto", Preference: autoDraft},
		Selection: DraftModeSelection{
			Preference: autoDraft, ContextWindow: 180000,
			AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Draft Automatic", ProfileSource: "saved", Preference: autoDraft, ContextWindow: 180000},
		},
	})
	state = Reduce(state, DraftModeAction{Mode: "plan", Selection: DraftModeSelection{
		Preference: planDraft, ContextWindow: 200000,
		AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Draft Planning", ProfileSource: "saved", Preference: planDraft, ContextWindow: 200000},
	}})
	backendPlan := client.ModelPreference{Provider: "codex", Model: "backend-plan", Thinking: "low"}
	state = Reduce(state, HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "session", Mode: "plan", UpdatedAt: 100},
		Preference:      backendPlan,
		ContextWindow:   272000,
		MaxOutputTokens: 12000,
		AgentModelPolicy: client.SessionV3AgentModelPolicy{
			Locked: true, ProfileName: "Backend Planning", ProfileSource: "saved",
			Preference: backendPlan, ContextWindow: 272000, MaxOutputTokens: 12000,
		},
	}})
	if state.Session.ID != "session" || state.Model.Preference.Model != "backend-plan" || state.Model.ProfileName != "Backend Planning" || state.Model.ProfileName == "Draft Planning" {
		t.Fatalf("backend hydration retained draft assumptions: %#v", state)
	}

	backendAuto := client.ModelPreference{Provider: "codex", Model: "backend-auto", Thinking: "high", ServiceTier: "fast"}
	payload, err := json.Marshal(map[string]any{
		"mode": "auto", "updated_at": int64(200), "preference": backendAuto,
		"context_window": 180000, "max_output_tokens": 16000,
		"agent_model_policy": client.SessionV3AgentModelPolicy{
			Source: "agent_auto_preset", Locked: true, ProfileName: "Backend Automatic", ProfileSource: "saved",
			Preference: backendAuto, ContextWindow: 180000, MaxOutputTokens: 16000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{
		SessionID: "session", Seq: 1, EventType: "session.mode.updated", Payload: payload,
	}}})
	if state.Session.Mode != "auto" || state.Session.UpdatedAt != 200 || state.Model.Preference != backendAuto || state.Model.ProfileName != "Backend Automatic" || state.Model.ContextWindow != 180000 || state.Model.MaxOutputTokens != 16000 {
		t.Fatalf("canonical mode event did not reconcile post-draft state: %#v", state)
	}
	if state.Model.ProfileName == "Draft Automatic" || state.Model.Preference.Model == "draft-auto" {
		t.Fatalf("draft state leaked after canonical event: %#v", state)
	}
}

func TestHydrateAndRealtimePreferenceShareCanonicalModelState(t *testing.T) {
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:       client.SessionSummary{ID: "s"},
		Preference:    client.ModelPreference{Provider: "Codex", Model: "before", Thinking: "medium"},
		ContextWindow: 100000,
	}})
	if state.Model.Preference.Provider != "codex" || state.Model.Preference.Model != "before" || state.Model.ContextWindow != 100000 {
		t.Fatalf("hydrated model state = %#v", state.Model)
	}
	payload, _ := json.Marshal(map[string]any{"preference": client.ModelPreference{Provider: "anthropic", Model: "after", Thinking: "high"}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "session.preference.updated", Payload: payload}}})
	if state.Model.Preference.Provider != "anthropic" || state.Model.Preference.Model != "after" || state.Model.Preference.Thinking != "high" {
		t.Fatalf("realtime model state = %#v", state.Model)
	}
}

func TestReducerOrdersDeduplicatesAndReconcilesPending(t *testing.T) {
	state := NewState()
	state = Reduce(state, PendingUserAction{Pending: PendingMessage{Message: Message{ID: "m2", Role: "user", Content: "pending", OperationID: "op"}}})
	state = Reduce(state, MessageResultAction{Result: client.SessionV3MessageResult{
		Session: client.SessionSummary{ID: "s"},
		Messages: []client.SessionMessage{
			{ID: "m2", SessionID: "s", GlobalSeq: 2, Role: "user", Content: "durable", Metadata: map[string]any{"operation_id": "op"}},
			{ID: "m1", SessionID: "s", GlobalSeq: 1, Role: "assistant", Content: "first"},
			{ID: "m2", SessionID: "s", GlobalSeq: 2, Role: "user", Content: "durable"},
		},
	}})
	if len(state.Messages) != 2 || state.Messages[0].ID != "m1" || state.Messages[1].ID != "m2" {
		t.Fatalf("messages = %#v", state.Messages)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("pending = %#v", state.Pending)
	}
}

func TestFinalHandoffMetadataProjectsFromHydrationAndRealtime(t *testing.T) {
	metadata := map[string]any{
		"source": "plan_execution_final_handoff",
		"kind":   "plan_final_checkpoint_handoff",
		"final_handoff": map[string]any{
			"schema_version": 1,
			"title":          "Ready to review",
			"overview":       "The focused change is complete.",
			"impact_bullets": []any{"Compact card", "Ordinary chat continuation"},
			"suggested_prompts": []any{
				map[string]any{"label": "Review", "prompt": "Review the final handoff."},
			},
			"details": map[string]any{"report": "Full report", "changed_files": []any{"internal/ui/v3chat/page.go"}},
		},
	}
	state := Reduce(NewState(), HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:  client.SessionSummary{ID: "s"},
		Messages: []client.SessionMessage{{ID: "handoff-1", SessionID: "s", Role: "system", Content: "compact", Metadata: metadata}},
	}})
	if len(state.Messages) != 1 || !isStructuredFinalHandoffMessage(state.Messages[0]) || state.Messages[0].FinalHandoff.Title != "Ready to review" {
		t.Fatalf("hydrated final handoff = %#v", state.Messages)
	}
	if metadataString(state.Messages[0].Metadata, "kind") != "plan_final_checkpoint_handoff" || state.Messages[0].FinalHandoff.Details.Report != "Full report" {
		t.Fatalf("hydrated metadata/evidence was not preserved: %#v", state.Messages[0])
	}

	payload, _ := json.Marshal(map[string]any{"message": client.SessionMessage{
		ID: "handoff-2", SessionID: "s", GlobalSeq: 2, Role: "system", Content: "compact", Metadata: metadata,
	}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 2, EventType: "session.message.created", Payload: payload}}})
	if len(state.Messages) != 2 || !isStructuredFinalHandoffMessage(state.Messages[1]) || len(state.Messages[1].FinalHandoff.SuggestedPrompts) != 1 {
		t.Fatalf("realtime final handoff = %#v", state.Messages)
	}
}

func TestReducerProjectsTitleAndCursorOnlyAfterDurableFrame(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"title": "Immediate title"})
	state := Reduce(NewState(), RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", EndpointCursor: "cursor-2", Event: &client.SessionV3Event{ID: "e1", SessionID: "s", Seq: 2, Payload: payload}}})
	if state.Session.Title != "Immediate title" || state.EndpointCursor != "cursor-2" || state.LastEventSeq != 2 {
		t.Fatalf("state = %#v", state)
	}
	again := Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", EndpointCursor: "cursor-older", Event: &client.SessionV3Event{ID: "e1", SessionID: "s", Seq: 2, Payload: payload}}})
	if len(again.Messages) != 0 || again.Session.Title != "Immediate title" {
		t.Fatalf("duplicate changed state: %#v", again)
	}
}

func TestReasoningEventsAccumulateAndCompleteFromCanonicalRealtime(t *testing.T) {
	state := NewState()
	started, _ := json.Marshal(map[string]any{
		"run_id": "run", "step": 1, "step_id": "step-1", "reasoning_id": "reasoning-1", "reasoning_key": "analysis", "recorded_at": int64(100),
	})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: 1, EventType: "session.reasoning.started", Payload: started, TsUnixMS: 100}}})
	for seq, payload := range []map[string]any{
		{"run_id": "run", "step": 1, "step_id": "step-1", "reasoning_id": "reasoning-1", "delta": "Inspecting", "delta_mode": "replace", "recorded_at": int64(101)},
		{"run_id": "run", "step": 1, "step_id": "step-1", "reasoning_id": "reasoning-1", "delta": " files", "delta_mode": "append", "recorded_at": int64(102)},
	} {
		raw, _ := json.Marshal(payload)
		state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: uint64(seq + 2), EventType: "session.reasoning.delta", Payload: raw, TsUnixMS: int64(101 + seq)}}})
	}
	segments := SelectReasoningSegments(state)
	if len(segments) != 1 || segments[0].Text != "Inspecting files" || segments[0].Status != "running" || segments[0].GlobalSeq != 1 {
		t.Fatalf("streaming reasoning = %#v", segments)
	}
	completed, _ := json.Marshal(map[string]any{
		"run_id": "run", "step": 1, "step_id": "step-1", "reasoning_id": "reasoning-1", "summary": "Inspecting files", "recorded_at": int64(103),
	})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: 4, EventType: "session.reasoning.completed", Payload: completed, TsUnixMS: 103}}})
	segments = SelectReasoningSegments(state)
	if len(segments) != 1 || segments[0].Summary != "Inspecting files" || segments[0].Status != "done" || segments[0].CompletedAt != 103 {
		t.Fatalf("completed reasoning = %#v", segments)
	}
}

func TestLivePatchContinuityAndDurableHandoff(t *testing.T) {
	state := NewState()
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{RunID: "r", StreamID: "out", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 2, Text: "hi"}}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{RunID: "r", StreamID: "out", LiveSeqStart: 2, LiveSeqEnd: 2, OffsetStart: 2, OffsetEnd: 3, Text: "!"}}})
	if got := state.Live["r:out"].Text; got != "hi!" {
		t.Fatalf("live = %q", got)
	}
	state = Reduce(state, MessageResultAction{Result: client.SessionV3MessageResult{Message: client.SessionMessage{ID: "a", Role: "assistant", Content: "hi!", Metadata: map[string]any{"run_id": "r"}}}})
	if len(state.Live) != 0 || len(state.Messages) != 1 {
		t.Fatalf("handoff state = %#v", state)
	}
}

func TestToolEventsProjectOrderedLiveStateAndDurableMessageReplacesIt(t *testing.T) {
	startedPayload, _ := json.Marshal(map[string]any{
		"run_id": "run", "call_id": "call-read", "tool_instance_id": "step-1:call-read",
		"tool_name": "read", "arguments": `{"path":"README.md"}`, "recorded_at": int64(200),
	})
	state := Reduce(NewState(), RealtimeFrameAction{Frame: client.V3RealtimeFrame{
		Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 2, EventType: "session.tool.started", Payload: startedPayload, TsUnixMS: 200},
	}})
	live := state.Tools["call-read"]
	if live.Name != "read" || live.Arguments != `{"path":"README.md"}` || live.Status != "running" || live.GlobalSeq != 2 {
		t.Fatalf("live tool = %#v", live)
	}

	completedPayload, _ := json.Marshal(map[string]any{
		"run_id": "run", "call_id": "call-read", "tool_instance_id": "step-1:call-read",
		"tool_name": "read", "status": "completed", "raw_output": "file contents", "duration_ms": int64(25),
		"message": client.SessionMessage{
			ID: "tool-message", SessionID: "s", GlobalSeq: 3, Role: "tool",
			Content: `{"path_id":"run.tool-history.v2","tool":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","arguments":"{\"path\":\"README.md\"}","completed_output":"file contents","duration_ms":25}`,
		},
	})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{
		Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 3, EventType: "session.tool.completed", Payload: completedPayload, TsUnixMS: 225},
	}})
	if len(state.Tools) != 0 {
		t.Fatalf("durable tool did not replace live projection: %#v", state.Tools)
	}
	if len(state.Messages) != 1 || state.Messages[0].Role != "tool" || state.Messages[0].GlobalSeq != 3 {
		t.Fatalf("durable tool messages = %#v", state.Messages)
	}
	tool, ok := parseToolMessage(state.Messages[0])
	if !ok || tool.Name != "read" || tool.Output != "file contents" || tool.DurationMS != 25 {
		t.Fatalf("parsed durable tool = %#v/%t", tool, ok)
	}
}

func TestAssistantAndToolEventsRetainCanonicalTimelineSequences(t *testing.T) {
	assistantPayload, _ := json.Marshal(map[string]any{"run_id": "run", "stream_id": "assistant:run", "delta": "before"})
	toolPayload, _ := json.Marshal(map[string]any{"run_id": "run", "call_id": "call", "tool_name": "bash", "arguments": `{"command":"pwd"}`})
	state := NewState()
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: 4, EventType: "session.assistant.delta", Payload: assistantPayload}}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: 5, EventType: "session.tool.started", Payload: toolPayload}}})
	if state.Live["run:assistant:run"].GlobalSeq != 4 || state.Tools["call"].GlobalSeq != 5 {
		t.Fatalf("timeline projections = live %#v tools %#v", state.Live, state.Tools)
	}
}

func TestReducerBoundsResidentMessagesAndLiveText(t *testing.T) {
	incoming := make([]client.SessionMessage, 0, maxResidentMessages+25)
	for i := 0; i < maxResidentMessages+25; i++ {
		incoming = append(incoming, client.SessionMessage{ID: fmt.Sprintf("m-%04d", i), GlobalSeq: uint64(i + 1), Role: "assistant", Content: "bounded"})
	}
	state := Reduce(NewState(), MessageResultAction{Result: client.SessionV3MessageResult{Messages: incoming}})
	if len(state.Messages) != maxResidentMessages || state.Messages[0].GlobalSeq != 26 {
		t.Fatalf("resident message bound = %d first=%#v", len(state.Messages), state.Messages[0])
	}
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{RunID: "r", StreamID: "out", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: maxLiveSegmentBytes + 1, Text: strings.Repeat("x", maxLiveSegmentBytes+1)}}})
	if !state.NeedsRehydrate || state.StaleReason != "live patch memory limit exceeded" || len(state.Live) != 0 {
		t.Fatalf("oversized live state = %#v", state)
	}
}

func TestLivePatchGapAndCursorErrorRequireExplicitRehydrate(t *testing.T) {
	state := Reduce(NewState(), RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{RunID: "r", StreamID: "out", LiveSeqStart: 2, LiveSeqEnd: 2, OffsetStart: 1, OffsetEnd: 2, Text: "x"}}})
	if !state.NeedsRehydrate || state.Connection != ConnectionStale {
		t.Fatalf("live gap state = %#v", state)
	}
	state = Reduce(state, HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "fresh"}})
	state = Reduce(state, RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "cursor.error", Error: "expired"}})
	if !state.NeedsRehydrate || state.StaleReason != "expired" {
		t.Fatalf("cursor state = %#v", state)
	}
}
