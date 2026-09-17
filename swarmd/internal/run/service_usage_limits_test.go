package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceRunTurn_DailyUsageLimitExceeded(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionStore := pebblestore.NewSessionStore(store)
	accountID := "test-acct-limits"
	userID := "user-1"
	sessionID := "sess-limit-test"

	_, err = sessionStore.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         userID,
		AccountScopeID: accountID,
		IdempotencyKey: "create-sess-limit",
		PayloadHash:    "create-sess-limit",
		Kind:           pebblestore.V3SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         userID,
			AccountScopeID: accountID,
			WorkspacePath:  t.TempDir(),
			WorkspaceName:  "ws",
			Title:          "Limit Test",
			Mode:           sessionruntime.ModeAuto,
			Preference:     pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "high"},
		},
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

	// Set daily limit to $0.05
	_, err = sessions.SetUsageLimit(accountID, 0.05, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	// Record prior turn of $0.10
	now := time.Now().UTC().UnixMilli()
	_, _, _, err = sessions.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:        sessionID,
		AccountScopeID:   accountID,
		UserID:           userID,
		RunID:            "run-prior",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		EstimatedCostUSD: 0.10,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("record turn usage: %v", err)
	}

	// Attempt runTurn - should be rejected by pre-run limit check
	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: accountID,
		UserID:         userID,
	}
	_, err = svc.RunTurnWithOptions(context.Background(), sessionID, RunOptions{
		Prompt:    "hello",
		RunID:     "run-blocked",
		Principal: principal,
	})
	if err == nil {
		t.Fatal("expected error for run exceeding daily limit, got nil")
	}
	if !strings.Contains(err.Error(), "daily usage limit exceeded") {
		t.Fatalf("expected error to mention 'daily usage limit exceeded', got %q", err.Error())
	}
}
