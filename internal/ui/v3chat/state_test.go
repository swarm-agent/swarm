package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

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
