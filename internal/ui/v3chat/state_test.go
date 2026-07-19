package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

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
