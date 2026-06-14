package pebblestore

import (
	"strings"
	"testing"
)

func TestReplayV3SessionEventsProjectsOrderedPrimaryState(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-replay")

	first, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-replay",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-replay-1",
		PayloadHash:    "hash-message-replay-1",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "first replay message",
		},
		RunIntent: &V3SessionRunIntent{Status: V3RunIntentPendingExecutor},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("append first message: %v", err)
	}
	second, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-replay",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-replay-2",
		PayloadHash:    "hash-message-replay-2",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "second replay message",
		},
		RunIntent: &V3SessionRunIntent{Status: V3RunIntentDispatchBlocked, BlockedReason: "cp5 blocked"},
		NowUnixMs: 3000,
	})
	if err != nil {
		t.Fatalf("append second message: %v", err)
	}

	replay, err := sessions.ReplayV3SessionEvents("session-replay", 0, 10)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if replay.Session == nil || replay.Session.ID != "session-replay" || replay.Session.MessageCount != 2 {
		t.Fatalf("replay session = %+v", replay.Session)
	}
	if replay.Projection.LastEventSeq != 3 || replay.Projection.ProjectionHighWatermarkSeq != 3 || replay.HighWatermarkSeq != 3 || replay.NextSeq != 3 {
		t.Fatalf("replay projection/watermark = %+v high=%d next=%d", replay.Projection, replay.HighWatermarkSeq, replay.NextSeq)
	}
	if len(replay.Events) != 3 || replay.Events[0].Seq != 1 || replay.Events[1].Seq != 2 || replay.Events[2].Seq != 3 {
		t.Fatalf("replay events = %+v", replay.Events)
	}
	if len(replay.Messages) != 2 || replay.Messages[0].ID != first.Message.ID || replay.Messages[1].ID != second.Message.ID {
		t.Fatalf("replay messages = %+v", replay.Messages)
	}
	if len(replay.RunIntents) != 2 || replay.RunIntents[0].RunID != first.RunIntent.RunID || replay.RunIntents[1].Status != V3RunIntentDispatchBlocked {
		t.Fatalf("replay run intents = %+v", replay.RunIntents)
	}
}

func TestReplayV3SessionEventsSupportsCursorLimitAndDetectsGaps(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-cursor")
	for i := 0; i < 3; i++ {
		suffix := string(rune('a' + i))
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID:      "session-cursor",
			UserID:         "user-1",
			AccountScopeID: "account-1",
			IdempotencyKey: "message-cursor-" + suffix,
			PayloadHash:    "hash-message-cursor-" + suffix,
			Kind:           V3SessionMutationAppendMessage,
			Message: &MessageSnapshot{
				Role:    "user",
				Content: "cursor message " + suffix,
			},
		})
		if err != nil {
			t.Fatalf("append cursor message %d: %v", i, err)
		}
	}

	page, err := sessions.ReplayV3SessionEvents("session-cursor", 1, 2)
	if err != nil {
		t.Fatalf("replay page: %v", err)
	}
	if len(page.Events) != 2 || page.Events[0].Seq != 2 || page.Events[1].Seq != 3 || page.HighWatermarkSeq != 3 || page.NextSeq != 3 {
		t.Fatalf("page replay = %+v", page)
	}

	if err := store.Delete(KeyV3SessionEvent("session-cursor", 3)); err != nil {
		t.Fatalf("delete event to create gap: %v", err)
	}
	_, err = sessions.ReplayV3SessionEvents("session-cursor", 1, 10)
	if err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("gap replay err = %v, want sequence gap", err)
	}
}

func TestV3RunIntentPreservesCreatedAtAcrossStatusTransitions(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-run-timestamps")

	pending, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-run-timestamps",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-run-timestamps",
		PayloadHash:    "hash-message-run-timestamps",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "run timestamp preservation"},
		RunIntent:      &V3SessionRunIntent{RunID: "run-timestamps", Status: V3RunIntentPendingExecutor},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("record pending run: %v", err)
	}
	if pending.RunIntent == nil || pending.RunIntent.CreatedAt != 2000 || pending.RunIntent.UpdatedAt != 2000 || pending.RunIntent.EventSeq != pending.PrimarySeq {
		t.Fatalf("pending run intent = %+v primarySeq=%d", pending.RunIntent, pending.PrimarySeq)
	}

	running, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-run-timestamps",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "claim-run-timestamps",
		PayloadHash:    "hash-claim-run-timestamps",
		Kind:           V3SessionMutationRecordRunIntent,
		RunIntent:      &V3SessionRunIntent{RunID: "run-timestamps", Status: V3RunIntentRunning},
		NowUnixMs:      3000,
	})
	if err != nil {
		t.Fatalf("record running run: %v", err)
	}
	if running.RunIntent == nil || running.RunIntent.CreatedAt != 2000 || running.RunIntent.UpdatedAt != 3000 || running.RunIntent.EventSeq != running.PrimarySeq {
		t.Fatalf("running run intent = %+v primarySeq=%d", running.RunIntent, running.PrimarySeq)
	}

	failed, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-run-timestamps",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "fail-run-timestamps",
		PayloadHash:    "hash-fail-run-timestamps",
		Kind:           V3SessionMutationRecordRunIntent,
		RunIntent:      &V3SessionRunIntent{RunID: "run-timestamps", Status: V3RunIntentFailed, BlockedReason: "tool loop protection"},
		NowUnixMs:      4000,
	})
	if err != nil {
		t.Fatalf("record failed run: %v", err)
	}
	if failed.RunIntent == nil || failed.RunIntent.CreatedAt != 2000 || failed.RunIntent.UpdatedAt != 4000 || failed.RunIntent.EventSeq != failed.PrimarySeq || failed.RunIntent.BlockedReason != "tool loop protection" {
		t.Fatalf("failed run intent = %+v primarySeq=%d", failed.RunIntent, failed.PrimarySeq)
	}

	stored, ok, err := sessions.GetV3SessionRunIntent("session-run-timestamps", "run-timestamps")
	if err != nil || !ok {
		t.Fatalf("load final run intent ok=%v err=%v", ok, err)
	}
	if stored.Status != V3RunIntentFailed || stored.CreatedAt != 2000 || stored.UpdatedAt != 4000 || stored.EventSeq != failed.PrimarySeq {
		t.Fatalf("stored final run intent = %+v", stored)
	}
	if active, ok, err := sessions.GetV3SessionActiveRunIntent("session-run-timestamps"); err != nil || ok {
		t.Fatalf("active run after terminal ok=%v err=%v intent=%+v", ok, err, active)
	}

	runningIndexed, err := sessions.ListV3SessionRunIntentsByStatus(V3RunIntentRunning, 10)
	if err != nil {
		t.Fatalf("list running run intents: %v", err)
	}
	for _, intent := range runningIndexed {
		if intent.RunID == "run-timestamps" {
			t.Fatalf("terminal run still indexed as running: %+v", runningIndexed)
		}
	}
	failedIndexed, err := sessions.ListV3SessionRunIntentsByStatus(V3RunIntentFailed, 10)
	if err != nil {
		t.Fatalf("list failed run intents: %v", err)
	}
	if len(failedIndexed) != 1 || failedIndexed[0].RunID != "run-timestamps" || failedIndexed[0].CreatedAt != 2000 || failedIndexed[0].UpdatedAt != 4000 {
		t.Fatalf("failed indexed intents = %+v", failedIndexed)
	}
}

func TestRepairV3SessionRunStateBackfillsLegacyRunIntentsOnly(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-legacy-repair")
	createV3SessionForTest(t, sessions, "session-lifecycle-only")

	legacyPending := V3SessionRunIntent{
		SessionID:      "session-legacy-repair",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		RunID:          "run-pending-old",
		Status:         V3RunIntentPendingExecutor,
		CreatedAt:      2000,
		UpdatedAt:      3000,
		EventSeq:       2,
	}
	legacyRunning := V3SessionRunIntent{
		SessionID:      "session-legacy-repair",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		RunID:          "run-running-new",
		Status:         V3RunIntentRunning,
		CreatedAt:      2500,
		UpdatedAt:      3500,
		EventSeq:       3,
	}
	if err := store.PutJSON(KeyV3SessionRunIntent(legacyPending.SessionID, legacyPending.RunID), legacyPending); err != nil {
		t.Fatalf("seed legacy pending intent: %v", err)
	}
	if err := store.PutJSON(KeyV3SessionRunIntentStatus(legacyPending.Status, legacyPending.UpdatedAt, legacyPending.AccountScopeID, legacyPending.SessionID, legacyPending.RunID), legacyPending); err != nil {
		t.Fatalf("seed pending status index: %v", err)
	}
	if err := store.PutJSON(KeyV3SessionRunIntent(legacyRunning.SessionID, legacyRunning.RunID), legacyRunning); err != nil {
		t.Fatalf("seed legacy running intent: %v", err)
	}
	if err := store.PutJSON(KeyV3SessionRunIntentStatus(legacyRunning.Status, legacyRunning.UpdatedAt, legacyRunning.AccountScopeID, legacyRunning.SessionID, legacyRunning.RunID), legacyRunning); err != nil {
		t.Fatalf("seed running status index: %v", err)
	}
	if err := store.PutJSON(KeySessionLifecycle("session-lifecycle-only"), SessionLifecycleSnapshot{
		SessionID:      "session-lifecycle-only",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		RunID:          "run-lifecycle-only",
		Active:         true,
		Phase:          "running",
		StartedAt:      2600,
		UpdatedAt:      3600,
	}); err != nil {
		t.Fatalf("seed lifecycle-only active: %v", err)
	}

	repair, err := sessions.RepairV3SessionRunStatesFromLegacyRunIntents("account-1", 0)
	if err != nil {
		t.Fatalf("repair legacy run states: %v", err)
	}
	if repair.CandidateSessions != 1 || repair.RepairedSessions != 1 || repair.SkippedCanonical != 0 {
		t.Fatalf("repair summary = %+v", repair)
	}
	state, ok, err := sessions.GetV3SessionRunState("session-legacy-repair")
	if err != nil || !ok {
		t.Fatalf("load repaired run state ok=%v err=%v", ok, err)
	}
	if !state.Active || state.RunID != "run-running-new" || state.Status != V3RunIntentRunning || state.EventSeq != 3 || state.StartedAt != 2500 {
		t.Fatalf("repaired run state = %+v", state)
	}
	active, ok, err := sessions.GetV3SessionActiveRunIntent("session-legacy-repair")
	if err != nil || !ok || active.RunID != "run-running-new" {
		t.Fatalf("active run after repair ok=%v err=%v intent=%+v", ok, err, active)
	}
	if lifecycleState, ok, err := sessions.GetV3SessionRunState("session-lifecycle-only"); err != nil || ok {
		t.Fatalf("lifecycle-only record must not repair canonical run state ok=%v err=%v state=%+v", ok, err, lifecycleState)
	}
}

func TestRepairV3SessionRunStateDoesNotOverwriteCanonicalInactive(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-canonical-inactive")

	legacyRunning := V3SessionRunIntent{
		SessionID:      "session-canonical-inactive",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		RunID:          "run-stale-running",
		Status:         V3RunIntentRunning,
		CreatedAt:      2000,
		UpdatedAt:      3000,
		EventSeq:       2,
	}
	canonicalTerminal := V3SessionRunIntent{
		SessionID:      "session-canonical-inactive",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		RunID:          "run-terminal",
		Status:         V3RunIntentCompleted,
		CreatedAt:      4000,
		UpdatedAt:      5000,
		EventSeq:       4,
	}
	if err := store.PutJSON(KeyV3SessionRunIntent(legacyRunning.SessionID, legacyRunning.RunID), legacyRunning); err != nil {
		t.Fatalf("seed stale running intent: %v", err)
	}
	if err := store.PutJSON(KeyV3SessionRunIntentStatus(legacyRunning.Status, legacyRunning.UpdatedAt, legacyRunning.AccountScopeID, legacyRunning.SessionID, legacyRunning.RunID), legacyRunning); err != nil {
		t.Fatalf("seed stale running status index: %v", err)
	}
	if err := store.PutJSON(KeyV3SessionRunIntentActive("session-canonical-inactive"), v3SessionRunStateFromIntent(canonicalTerminal)); err != nil {
		t.Fatalf("seed canonical inactive run state: %v", err)
	}

	repair, err := sessions.RepairV3SessionRunStatesFromLegacyRunIntents("account-1", 0)
	if err != nil {
		t.Fatalf("repair legacy run states: %v", err)
	}
	if repair.CandidateSessions != 1 || repair.RepairedSessions != 0 || repair.SkippedCanonical != 1 {
		t.Fatalf("repair summary = %+v", repair)
	}
	state, ok, err := sessions.GetV3SessionRunState("session-canonical-inactive")
	if err != nil || !ok {
		t.Fatalf("load canonical inactive ok=%v err=%v", ok, err)
	}
	if state.Active || state.RunID != "run-terminal" || state.Status != V3RunIntentCompleted {
		t.Fatalf("canonical inactive was overwritten by repair: %+v", state)
	}
	if active, ok, err := sessions.GetV3SessionActiveRunIntent("session-canonical-inactive"); err != nil || ok {
		t.Fatalf("canonical inactive must win over stale legacy scan ok=%v err=%v intent=%+v", ok, err, active)
	}
}
