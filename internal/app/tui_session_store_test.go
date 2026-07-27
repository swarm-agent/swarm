package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestTUISessionStoreResetReplacesAndMergePreserves(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "cursor-a",
		SessionsByID: map[string]client.SessionSummary{
			"a": {ID: "a", Title: "A", SessionAPI: "v3"},
			"b": {ID: "b", Title: "B", SessionAPI: "v3"},
		},
		ProjectionsBySession: map[string]client.SessionV3Projection{
			"a": {SessionID: "a", LastEventSeq: 1},
			"b": {SessionID: "b", LastEventSeq: 2},
		},
		SessionOrder: []string{"a", "b"},
	})
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "cursor-c",
		SessionsByID: map[string]client.SessionSummary{
			"c": {ID: "c", Title: "C", SessionAPI: "v3"},
		},
		ProjectionsBySession: map[string]client.SessionV3Projection{"c": {SessionID: "c", LastEventSeq: 3}},
		SessionOrder:         []string{"c"},
	})
	if sessions := store.HomeSessions(); len(sessions) != 1 || sessions[0].ID != "c" {
		t.Fatalf("reset sessions = %#v, want only c", sessions)
	}
	if got := store.EndpointCursor(); got != "cursor-c" {
		t.Fatalf("endpoint cursor = %q", got)
	}

	store.MergeWorkset(client.SessionV3Workset{
		SessionsByID: map[string]client.SessionSummary{
			"d": {ID: "d", Title: "D", SessionAPI: "v3"},
		},
		ProjectionsBySession: map[string]client.SessionV3Projection{"d": {SessionID: "d", LastEventSeq: 4}},
		SessionOrder:         []string{"d"},
	})
	ids := sessionIDs(store.HomeSessions())
	if !reflect.DeepEqual(ids, []string{"c", "d"}) {
		t.Fatalf("merged ids = %#v", ids)
	}
}

func TestTUISessionStoreCopiedReadsDoNotMutateStore(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SessionsByID:      map[string]client.SessionSummary{"a": {ID: "a", Title: "A", SessionAPI: "v3", Metadata: map[string]any{"k": "v"}}},
		MessagesBySession: map[string][]client.SessionMessage{"a": {{ID: "m1", SessionID: "a", Content: "hello", Metadata: map[string]any{"m": "v"}}}},
		SessionOrder:      []string{"a"},
	})
	snapshot, ok := store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing")
	}
	snapshot.Session.Title = "mutated"
	snapshot.Session.Metadata["k"] = "changed"
	snapshot.Messages[0].Content = "changed"
	snapshot.Messages[0].Metadata["m"] = "changed"
	second, ok := store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing after mutation")
	}
	if second.Session.Title != "A" || second.Session.Metadata["k"] != "v" || second.Messages[0].Content != "hello" || second.Messages[0].Metadata["m"] != "v" {
		t.Fatalf("store leaked mutable read: %#v %#v", second.Session, second.Messages)
	}
}

func TestTUISessionStoreDesiredSubscriptionsUseEndpointCursorAndLastSeq(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "endpoint-1",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 12}},
		SessionOrder:           []string{"a"},
	})
	subs := store.DesiredSubscriptions("tui:client")
	if len(subs) != 1 || subs[0].SessionID != "a" || subs[0].EndpointCursor != "endpoint-1" || subs[0].LastSeq != 12 || subs[0].SubscriptionID != "tui:client:session:a" {
		t.Fatalf("subscriptions = %#v", subs)
	}
}

func TestTUISessionStoreEndpointWatermarkAdvancesCursorWithoutMutatingLastSeq(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "cursor-1",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 12, ProjectionHighWatermarkSeq: 12}},
		SessionOrder:           []string{"a"},
	})

	result := store.ApplyRealtimeFrame(client.V3RealtimeFrame{
		Kind:             "endpoint.watermark",
		EndpointCursor:   "cursor-2",
		HighWatermarkSeq: 99,
		Rev:              99,
		PrevRev:          98,
	})
	if result.Changed || result.NeedsRehydrate {
		t.Fatalf("endpoint watermark mutated application state: %#v", result)
	}
	if got := store.EndpointCursor(); got != "cursor-2" {
		t.Fatalf("endpoint cursor = %q, want cursor-2", got)
	}
	subs := store.DesiredSubscriptions("tui:client")
	if len(subs) != 1 || subs[0].EndpointCursor != "cursor-2" || subs[0].LastSeq != 12 || subs[0].SubscriptionID != "tui:client:session:a" {
		t.Fatalf("subscriptions after watermark = %#v", subs)
	}
	projection := store.workset.ProjectionsBySession["a"]
	if projection.LastEventSeq != 12 || projection.ProjectionHighWatermarkSeq != 12 {
		t.Fatalf("projection mutated from endpoint watermark: %#v", projection)
	}
}

func TestTUISessionStoreMessageResultDoesNotReplaceTUIEndpointCursor(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "v3c1.tui-signed",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 12}},
		SessionOrder:           []string{"a"},
	})

	result := store.MergeMessageResult(client.SessionV3MessageResult{
		Session:    client.SessionSummary{ID: "a", SessionAPI: "v3"},
		Projection: client.SessionV3Projection{SessionID: "a", LastEventSeq: 13},
		Message:    client.SessionMessage{ID: "m1", SessionID: "a", Content: "hello"},
		RealtimeOutbox: &client.SessionV3RealtimeOutboxRow{
			EndpointCursor: "cursor-123",
			SessionID:      "a",
		},
	})
	if !result.Changed {
		t.Fatalf("message result did not change store: %#v", result)
	}
	if got := store.EndpointCursor(); got != "v3c1.tui-signed" {
		t.Fatalf("endpoint cursor = %q, want original TUI workset cursor", got)
	}
	subs := store.DesiredSubscriptions("tui:client")
	if len(subs) != 1 || subs[0].SessionID != "a" || subs[0].EndpointCursor != "v3c1.tui-signed" || subs[0].LastSeq != 13 {
		t.Fatalf("subscriptions after message result = %#v", subs)
	}
}

func TestTUISessionStoreOrdersHydratedEventsAndMessages(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{
		Session:    client.SessionSummary{ID: "a", SessionAPI: "v3"},
		Projection: client.SessionV3Projection{SessionID: "a", LastEventSeq: 3},
		Messages: []client.SessionMessage{
			{ID: "m3", SessionID: "a", GlobalSeq: 30, CreatedAt: 300},
			{ID: "m1", SessionID: "a", GlobalSeq: 10, CreatedAt: 100},
			{ID: "m2", SessionID: "a", GlobalSeq: 20, CreatedAt: 200},
		},
		Events: []client.SessionV3Event{
			{ID: "e3", SessionID: "a", Seq: 3, TsUnixMS: 300},
			{ID: "e1", SessionID: "a", Seq: 1, TsUnixMS: 100},
			{ID: "e2", SessionID: "a", Seq: 2, TsUnixMS: 200},
		},
	})

	snapshot, ok := store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing")
	}
	if got := sessionMessageIDs(snapshot.Messages); !reflect.DeepEqual(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("hydrated messages order = %#v", got)
	}
	if got := sessionEventIDs(snapshot.Events); !reflect.DeepEqual(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("hydrated events order = %#v", got)
	}
}

func TestTUISessionStoreMergeResultOrdersAndReplacesEventsAndMessages(t *testing.T) {
	store := newTUISessionStore()
	result := store.MergeMessageResult(client.SessionV3MessageResult{
		Session: client.SessionSummary{ID: "a", SessionAPI: "v3"},
		Messages: []client.SessionMessage{
			{ID: "m2", SessionID: "a", GlobalSeq: 2, CreatedAt: 200, Content: "old"},
			{ID: "m1", SessionID: "a", GlobalSeq: 1, CreatedAt: 100, Content: "one"},
		},
		Events: []client.SessionV3Event{
			{ID: "e2", SessionID: "a", Seq: 2, TsUnixMS: 200, EventType: "two"},
			{ID: "e1", SessionID: "a", Seq: 1, TsUnixMS: 100, EventType: "one"},
		},
	})
	if !result.Changed {
		t.Fatalf("initial merge did not change store: %#v", result)
	}
	result = store.MergeMessageResult(client.SessionV3MessageResult{
		Session: client.SessionSummary{ID: "a", SessionAPI: "v3"},
		Message: client.SessionMessage{ID: "m2", SessionID: "a", GlobalSeq: 2, CreatedAt: 200, Content: "updated"},
		Events: []client.SessionV3Event{
			{ID: "e2", SessionID: "a", Seq: 2, TsUnixMS: 200, EventType: "updated"},
			{ID: "e3", SessionID: "a", Seq: 3, TsUnixMS: 300, EventType: "three"},
		},
	})
	if !result.Changed {
		t.Fatalf("replacement merge did not change store: %#v", result)
	}

	snapshot, ok := store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing")
	}
	if got := sessionMessageIDs(snapshot.Messages); !reflect.DeepEqual(got, []string{"m1", "m2"}) {
		t.Fatalf("merged messages order = %#v", got)
	}
	if snapshot.Messages[1].Content != "updated" {
		t.Fatalf("replacement message not retained: %#v", snapshot.Messages)
	}
	if got := sessionEventIDs(snapshot.Events); !reflect.DeepEqual(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("merged events order = %#v", got)
	}
	if snapshot.Events[1].EventType != "updated" {
		t.Fatalf("replacement event not retained: %#v", snapshot.Events)
	}
}

func TestTUISessionStoreRealtimeMessagesRetainSeqOrderAndReplacement(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SessionsByID:      map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		SessionOrder:      []string{"a"},
		MessagesBySession: map[string][]client.SessionMessage{},
		EventsBySession:   map[string][]client.SessionV3Event{},
	})

	frames := []client.V3RealtimeFrame{
		realtimeMessageFrame("a", "e1", 1, client.SessionMessage{ID: "m3", SessionID: "a", GlobalSeq: 30, CreatedAt: 300, Content: "three"}),
		realtimeMessageFrame("a", "e2", 2, client.SessionMessage{ID: "m1", SessionID: "a", GlobalSeq: 10, CreatedAt: 100, Content: "one"}),
		realtimeMessageFrame("a", "e3", 3, client.SessionMessage{ID: "m2", SessionID: "a", GlobalSeq: 20, CreatedAt: 200, Content: "two"}),
	}
	for _, frame := range frames {
		result := store.ApplyRealtimeFrame(frame)
		if !result.Changed {
			t.Fatalf("realtime message frame did not change store: frame=%#v result=%#v", frame, result)
		}
	}

	snapshot, ok := store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing")
	}
	if got := sessionMessageIDs(snapshot.Messages); !reflect.DeepEqual(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("realtime messages order = %#v", got)
	}
	if got := sessionEventIDs(snapshot.Events); !reflect.DeepEqual(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("realtime events order = %#v", got)
	}

	result := store.MergeMessageResult(client.SessionV3MessageResult{
		Session: client.SessionSummary{ID: "a", SessionAPI: "v3"},
		Message: client.SessionMessage{
			ID:        "m2",
			SessionID: "a",
			GlobalSeq: 20,
			CreatedAt: 200,
			Content:   "updated",
		},
		Events: []client.SessionV3Event{{ID: "e2", SessionID: "a", Seq: 2, TsUnixMS: 200, EventType: "message.updated"}},
	})
	if !result.Changed {
		t.Fatalf("duplicate replacement did not change store: %#v", result)
	}
	snapshot, ok = store.ChatSnapshot("a")
	if !ok {
		t.Fatalf("ChatSnapshot missing after replacement")
	}
	if got := sessionMessageIDs(snapshot.Messages); !reflect.DeepEqual(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("messages order after replacement = %#v", got)
	}
	if snapshot.Messages[1].Content != "updated" {
		t.Fatalf("replacement message not retained in order: %#v", snapshot.Messages)
	}
	if got := sessionEventIDs(snapshot.Events); !reflect.DeepEqual(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("events order after replacement = %#v", got)
	}
	if snapshot.Events[1].EventType != "message.updated" {
		t.Fatalf("replacement event not retained in order: %#v", snapshot.Events)
	}
}

func TestTUISessionStoreHydrationDoesNotReplaceTUIEndpointCursor(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "v3c1.tui-signed",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 12}},
		SessionOrder:           []string{"a"},
	})

	store.MergeHydrated(client.SessionV3Hydrated{
		Session:                client.SessionSummary{ID: "a", Title: "Hydrated", SessionAPI: "v3"},
		Projection:             client.SessionV3Projection{SessionID: "a", LastEventSeq: 13},
		SnapshotEndpointCursor: "desktop-like-cursor",
		Messages:               []client.SessionMessage{{ID: "m1", SessionID: "a", Content: "hi"}},
	})
	if got := store.EndpointCursor(); got != "v3c1.tui-signed" {
		t.Fatalf("endpoint cursor = %q, want original TUI workset cursor", got)
	}
	snapshot, ok := store.ChatSnapshot("a")
	if !ok || !snapshot.Hydrated || snapshot.EndpointCursor != "v3c1.tui-signed" || len(snapshot.Messages) != 1 {
		t.Fatalf("hydrated snapshot = %#v ok=%v", snapshot, ok)
	}
	subs := store.DesiredSubscriptions("tui:client")
	if len(subs) != 1 || subs[0].SessionID != "a" || subs[0].EndpointCursor != "v3c1.tui-signed" || subs[0].LastSeq != 13 {
		t.Fatalf("subscriptions after hydration = %#v", subs)
	}
}

func TestTUISessionStoreTerminalRealtimeFrameDoesNotPoisonEndpointCursor(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "v3c1.tui-signed",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 12}},
		SessionOrder:           []string{"a"},
	})

	result := store.ApplyRealtimeFrame(client.V3RealtimeFrame{Kind: tuiRealtimeKindCursorError, SessionID: "a", EndpointCursor: "cursor-error", Error: "forced cursor gap"})
	if !result.NeedsRehydrate || result.Reason != "forced cursor gap" {
		t.Fatalf("terminal frame result = %#v", result)
	}
	if got := store.EndpointCursor(); got != "v3c1.tui-signed" {
		t.Fatalf("endpoint cursor = %q, want original TUI workset cursor", got)
	}
	subs := store.DesiredSubscriptions("tui:client")
	if len(subs) != 1 || subs[0].EndpointCursor != "v3c1.tui-signed" || subs[0].LastSeq != 12 {
		t.Fatalf("subscriptions after terminal frame = %#v", subs)
	}
}

func TestTUISessionStoreRealtimeOrderingAcceptsSparseSequencesAndTerminalFrames(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SessionsByID:         map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession: map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 1}},
		SessionOrder:         []string{"a"},
	})
	dup := store.ApplyRealtimeFrame(realtimeEventFrame("a", 1, "session.title.updated", map[string]any{"title": "ignored"}))
	if dup.Changed || store.HomeSessions()[0].Title == "ignored" {
		t.Fatalf("duplicate changed store: result=%#v sessions=%#v", dup, store.HomeSessions())
	}
	ordered := store.ApplyRealtimeFrame(realtimeEventFrame("a", 2, "session.title.updated", map[string]any{"title": "updated"}))
	if !ordered.Changed || store.HomeSessions()[0].Title != "updated" {
		t.Fatalf("ordered frame not applied: result=%#v sessions=%#v", ordered, store.HomeSessions())
	}
	sparse := store.ApplyRealtimeFrame(realtimeEventFrame("a", 4, "session.title.updated", map[string]any{"title": "sparse"}))
	if !sparse.Changed || sparse.NeedsRehydrate || store.StaleState().Stale || store.HomeSessions()[0].Title != "sparse" {
		t.Fatalf("sparse result=%#v stale=%#v sessions=%#v", sparse, store.StaleState(), store.HomeSessions())
	}
	terminal := store.ApplyRealtimeFrame(client.V3RealtimeFrame{Kind: tuiRealtimeKindSlowConsumer, SessionID: "a"})
	if !terminal.NeedsRehydrate || store.StaleState().Reason != tuiRealtimeKindSlowConsumer {
		t.Fatalf("terminal result=%#v stale=%#v", terminal, store.StaleState())
	}
}

func TestTUISessionStorePermissionAndHydrationState(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "v3c1.tui-signed",
		SessionsByID:           map[string]client.SessionSummary{"a": {ID: "a", SessionAPI: "v3"}},
		ProjectionsBySession:   map[string]client.SessionV3Projection{"a": {SessionID: "a", LastEventSeq: 1}},
		SessionOrder:           []string{"a"},
	})
	if !store.NeedsHydration("a") {
		t.Fatalf("new workset session should need hydration")
	}
	store.MergeHydrated(client.SessionV3Hydrated{
		Session:                client.SessionSummary{ID: "a", Title: "Hydrated", SessionAPI: "v3"},
		Projection:             client.SessionV3Projection{SessionID: "a", LastEventSeq: 2},
		SnapshotEndpointCursor: "endpoint-hydrated",
		Messages:               []client.SessionMessage{{ID: "m1", SessionID: "a", Content: "hi"}},
		PendingPermissions:     []client.PermissionRecord{{ID: "p1", SessionID: "a", Status: "pending"}},
	})
	if store.NeedsHydration("a") {
		t.Fatalf("hydrated session still needs hydration")
	}
	snapshot, ok := store.ChatSnapshot("a")
	if !ok || !snapshot.Hydrated || len(snapshot.Messages) != 1 || len(snapshot.PendingPerms) != 1 || snapshot.EndpointCursor != "v3c1.tui-signed" {
		t.Fatalf("hydrated snapshot = %#v ok=%v", snapshot, ok)
	}
	result := store.ApplyRealtimeFrame(realtimeEventFrame("a", 3, "permission.updated", map[string]any{"permission": map[string]any{"id": "p1", "session_id": "a", "status": "approved"}}))
	if !result.Changed {
		t.Fatalf("permission frame did not change store")
	}
	if got := store.HomeSessions()[0].PendingPermissionCount; got != 0 {
		t.Fatalf("pending permission count = %d, want 0", got)
	}
}

func realtimeEventFrame(sessionID string, seq uint64, eventType string, payload map[string]any) client.V3RealtimeFrame {
	raw, _ := json.Marshal(payload)
	event := client.SessionV3Event{ID: eventType, SessionID: sessionID, Seq: seq, EventType: eventType, Payload: raw}
	return client.V3RealtimeFrame{Kind: tuiRealtimeKindEvent, SessionID: sessionID, EventType: eventType, LastSeq: seq, Event: &event}
}

func realtimeMessageFrame(sessionID, eventID string, seq uint64, message client.SessionMessage) client.V3RealtimeFrame {
	raw, _ := json.Marshal(map[string]any{"message": message})
	event := client.SessionV3Event{ID: eventID, SessionID: sessionID, Seq: seq, EventType: "message.updated", Payload: raw, TsUnixMS: message.CreatedAt}
	return client.V3RealtimeFrame{Kind: tuiRealtimeKindEvent, SessionID: sessionID, EventType: event.EventType, LastSeq: seq, Event: &event}
}

func sessionIDs(sessions []model.SessionSummary) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func sessionMessageIDs(messages []client.SessionMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func sessionEventIDs(events []client.SessionV3Event) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}
