package run

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRecordProviderUsageSnapshotCommitsAtomicMutationAndOutbox(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionStore := pebblestore.NewSessionStore(store)
	created, err := sessionStore.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: "usage-child", UserID: "user-1", AccountScopeID: "account-1",
		IdempotencyKey: "create-usage-child", PayloadHash: "create-usage-child",
		Kind:      pebblestore.V3SessionMutationCreateSession,
		Session:   &pebblestore.SessionSnapshot{ID: "usage-child", WorkspacePath: "/workspace", WorkspaceName: "workspace", Title: "child"},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	svc := NewService(sessions, nil, nil, nil, nil, nil, nil, events)

	var mutationKinds []string
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		mutationKinds = append(mutationKinds, input.Kind)
		return sessions.ApplySessionMutation(input)
	}
	turn, summary, legacyEvent, err := svc.recordProviderUsageSnapshot(
		"usage-child", "run-1", "codex", "gpt-test", 1000, 1,
		provideriface.TokenUsage{Source: "codex_api_usage", Transport: "websocket", InputTokens: 600, OutputTokens: 50, TotalTokens: 650},
		identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, apply,
	)
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if legacyEvent != nil {
		t.Fatalf("atomic usage mutation returned legacy event: %+v", legacyEvent)
	}
	if len(mutationKinds) != 1 || mutationKinds[0] != pebblestore.V3SessionMutationRecordUsage {
		t.Fatalf("mutation kinds = %v, want one record_usage", mutationKinds)
	}
	if turn.TotalTokens != 650 || summary.TotalTokens != 650 || summary.RemainingTokens != 350 {
		t.Fatalf("committed usage turn=%+v summary=%+v", turn, summary)
	}
	committedEvents, err := sessionStore.ListV3SessionEvents("usage-child", created.Event.Seq, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(committedEvents) != 1 || committedEvents[0].EventType != "run.usage.updated" || committedEvents[0].Seq != created.Event.Seq+1 {
		t.Fatalf("usage events = %+v", committedEvents)
	}
	outbox, err := sessionStore.ListV3RealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].Event.EventType != "run.usage.updated" || outbox[0].Event.Seq != committedEvents[0].Seq {
		t.Fatalf("usage outbox = %+v", outbox)
	}
}

func TestMergeTokenUsagePreservesCurrentRequestOccupancyAndCumulativeFireworksCost(t *testing.T) {
	snapshot := provideriface.TokenUsage{Source: "codex_api_usage", InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	snapshot = mergeTokenUsage(snapshot, provideriface.TokenUsage{Source: "codex_api_usage", InputTokens: 180, OutputTokens: 20, TotalTokens: 200})
	if snapshot.InputTokens != 180 || snapshot.OutputTokens != 20 || snapshot.TotalTokens != 200 {
		t.Fatalf("provider snapshot should replace counters: %+v", snapshot)
	}

	fireworks := provideriface.TokenUsage{Source: "fireworks_api_usage", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, EstimatedCostUSD: 0.01}
	fireworks = mergeTokenUsage(fireworks, provideriface.TokenUsage{Source: "fireworks_api_usage", InputTokens: 80, OutputTokens: 20, TotalTokens: 100, EstimatedCostUSD: 0.02})
	if fireworks.InputTokens != 80 || fireworks.OutputTokens != 20 || fireworks.TotalTokens != 100 {
		t.Fatalf("fireworks current-request counters should replace the prior snapshot: %+v", fireworks)
	}
	if fireworks.EstimatedCostUSD != 0.03 {
		t.Fatalf("fireworks cost should accumulate across requests: %+v", fireworks)
	}
}
