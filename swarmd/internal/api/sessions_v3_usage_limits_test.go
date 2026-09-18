package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3UsageLimits_GetAndSet(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	// 1. Initial GET /v3/usage/limits
	req := httptest.NewRequest(http.MethodGet, "/v3/usage/limits", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v3/usage/limits status=%d, body=%s", rec.Code, rec.Body.String())
	}

	var getResp SessionUsageLimitsResponse
	if err := json.NewDecoder(rec.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if !getResp.OK {
		t.Fatal("expected OK=true")
	}
	if getResp.Limits.Enabled {
		t.Fatal("expected enabled=false initially")
	}

	// 2. POST /v3/usage/limits with valid limit
	postBody, _ := json.Marshal(map[string]any{
		"daily_cost_limit_usd": 25.50,
		"enabled":              true,
	})
	req = httptest.NewRequest(http.MethodPost, "/v3/usage/limits", bytes.NewReader(postBody))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v3/usage/limits status=%d, body=%s", rec.Code, rec.Body.String())
	}

	var postResp SessionUsageLimitsResponse
	if err := json.NewDecoder(rec.Body).Decode(&postResp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if !postResp.OK || !postResp.Limits.Enabled || postResp.Limits.DailyCostLimitUSD != 25.50 {
		t.Fatalf("unexpected POST response: %+v", postResp.Limits)
	}

	// 3. Verify GET returns the updated values
	req = httptest.NewRequest(http.MethodGet, "/v3/usage/limits", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	var getResp2 SessionUsageLimitsResponse
	_ = json.NewDecoder(rec.Body).Decode(&getResp2)
	if !getResp2.Limits.Enabled || getResp2.Limits.DailyCostLimitUSD != 25.50 {
		t.Fatalf("GET after POST did not reflect changes: %+v", getResp2.Limits)
	}

	// 4. Negative validation: negative limit should return 400
	badBody, _ := json.Marshal(map[string]any{
		"daily_cost_limit_usd": -5.0,
	})
	req = httptest.NewRequest(http.MethodPost, "/v3/usage/limits", bytes.NewReader(badBody))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative limit, got %d", rec.Code)
	}
}

func TestSessionsV3UsageLimits_PreRunCheck(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()

	// 1. Create a session
	sessionID := "sess_limit_check_1"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		Title:          "Limit Check Session",
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		WorkspacePath:  t.TempDir(),
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 2. Set daily limit to $0.05
	_, err = sessionSvc.SetUsageLimit(principal.AccountScopeID, 0.05, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	// 3. Record turn usage of $0.10 (exceeding limit)
	now := time.Now().UTC().UnixMilli()
	_, _, _, err = sessionSvc.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:        sessionID,
		AccountScopeID:   principal.AccountScopeID,
		UserID:           principal.UserID,
		RunID:            "run_prior_1",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		EstimatedCostUSD: 0.10,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("record turn usage: %v", err)
	}

	// 4. Verify CheckDailyLimit returns exceeded
	exceeded, currentCost, limitCost, err := sessionSvc.CheckDailyLimit(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("CheckDailyLimit: %v", err)
	}
	if !exceeded || currentCost < 0.09 || limitCost != 0.05 {
		t.Fatalf("expected exceeded=true, got exceeded=%v cost=%v limit=%v", exceeded, currentCost, limitCost)
	}

	// 5. Test pre-run check in sessionV3Executor
	exec := newSessionV3Executor(server)
	runID := "run_new_blocked_1"
	job := sessionV3ExecutorJob{
		Principal: principal,
		SessionID: sessionID,
		RunID:     runID,
	}

	// Put intent as PendingExecutor
	pending := pebblestore.V3SessionRunIntent{
		SessionID:      sessionID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		RunID:          runID,
		Status:         sessionruntime.RunIntentPendingExecutor,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, runID, pending.Status, "", "session.assistant.queued", "")
	if err != nil {
		t.Fatalf("hash pending intent: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "queue-run-test",
		IdempotencyKey:  "queue-run-test",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.queued",
		RunIntent:       &pending,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("record pending intent: %v", err)
	}

	// Run job
	exec.run(context.Background(), job)

	// Verify intent was marked as failed with limit exceeded reason
	savedIntent, ok, err := server.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok {
		t.Fatalf("get run intent: ok=%v err=%v", ok, err)
	}
	if savedIntent.Status != sessionruntime.RunIntentFailed {
		t.Fatalf("expected intent status failed, got %q", savedIntent.Status)
	}
	if !strings.Contains(savedIntent.BlockedReason, "daily usage limit exceeded") {
		t.Fatalf("expected blocked reason to mention daily usage limit exceeded, got %q", savedIntent.BlockedReason)
	}
}

func TestSessionsV3UsageLimits_WatcherAndMultiSessionKill(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()

	// 1. Create two sessions
	sess1 := "sess_multi_kill_1"
	sess2 := "sess_multi_kill_2"
	for _, sid := range []string{sess1, sess2} {
		_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      sid,
			Title:          "Multi Session Test",
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			WorkspacePath:  t.TempDir(),
			Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "high"},
		})
		if err != nil {
			t.Fatalf("create session %s: %v", sid, err)
		}
	}

	// 2. Set daily limit to $0.05
	_, err := sessionSvc.SetUsageLimit(principal.AccountScopeID, 0.05, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	// 3. Create executor and mock two active in-flight running sessions
	exec := newSessionV3Executor(server)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	job1 := sessionV3ExecutorJob{Principal: principal, SessionID: sess1, RunID: "run-m1"}
	job2 := sessionV3ExecutorJob{Principal: principal, SessionID: sess2, RunID: "run-m2"}

	now := time.Now().UTC().UnixMilli()
	for _, j := range []sessionV3ExecutorJob{job1, job2} {
		pending := pebblestore.V3SessionRunIntent{
			SessionID:      j.SessionID,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			RunID:          j.RunID,
			Status:         sessionruntime.RunIntentPendingExecutor,
			UpdatedAt:      now,
		}
		payloadHash, err := sessionV3ExecutorPayloadHash(j.SessionID, j.RunID, pending.Status, "", "session.assistant.queued", "")
		if err != nil {
			t.Fatalf("hash intent %s: %v", j.RunID, err)
		}
		if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       j.SessionID,
			UserID:          principal.UserID,
			AccountScopeID:  principal.AccountScopeID,
			ClientRequestID: "queue-" + j.RunID,
			IdempotencyKey:  "queue-" + j.RunID,
			PayloadHash:     payloadHash,
			RequestHash:     payloadHash,
			Kind:            sessionruntime.SessionMutationRecordRunIntent,
			EventType:       "session.assistant.queued",
			RunIntent:       &pending,
			NowUnixMs:       now,
		}); err != nil {
			t.Fatalf("record intent %s: %v", j.RunID, err)
		}
	}

	exec.attachCancel(job1, cancel1)
	exec.attachCancel(job2, cancel2)

	// Verify both runs are not canceled yet
	if exec.isRunCanceled(job1) || exec.isRunCanceled(job2) {
		t.Fatal("expected runs not canceled initially")
	}

	// 4. Record usage that breaches the daily limit ($0.10 > $0.05)
	_, _, _, err = sessionSvc.RecordTurnUsage(sess1, pebblestore.SessionTurnUsageSnapshot{
		SessionID:        sess1,
		AccountScopeID:   principal.AccountScopeID,
		UserID:           principal.UserID,
		RunID:            "run_prior_exceed",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		EstimatedCostUSD: 0.10,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}

	// 5. Trigger watcher enforcement check
	exec.checkAndEnforceUsageLimits()

	// 6. Verify BOTH running sessions were killed!
	if !exec.isRunCanceled(job1) {
		t.Errorf("expected job1 to be canceled by watcher")
	}
	if !exec.isRunCanceled(job2) {
		t.Errorf("expected job2 to be canceled by watcher")
	}

	if ctx1.Err() == nil {
		t.Errorf("expected ctx1 to be canceled")
	}
	if ctx2.Err() == nil {
		t.Errorf("expected ctx2 to be canceled")
	}

	// Verify intents in store were recorded as cancelled
	for _, j := range []sessionV3ExecutorJob{job1, job2} {
		intent, ok, err := server.sessions.GetSessionRunIntent(j.SessionID, j.RunID)
		if err != nil || !ok {
			t.Errorf("get intent %s: ok=%v err=%v", j.RunID, ok, err)
			continue
		}
		if intent.Status != sessionruntime.RunIntentCancelled {
			t.Errorf("expected intent %s to be cancelled, got %q", j.RunID, intent.Status)
		}
		if !strings.Contains(intent.BlockedReason, "daily usage limit exceeded") {
			t.Errorf("expected reason to mention daily usage limit exceeded, got %q", intent.BlockedReason)
		}
	}
}

func TestSessionsV3UsageLimits_MediaGenerationFactorsIntoDailyLimit(t *testing.T) {
	_, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()

	sessionID := "sess_media_limit_test"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		Title:          "Media Limit Test",
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		WorkspacePath:  t.TempDir(),
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 1. Set daily limit to $3.00
	_, err = sessionSvc.SetUsageLimit(principal.AccountScopeID, 3.00, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	// Initially not exceeded
	exceeded, cost, _, err := sessionSvc.CheckDailyLimit(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("check daily limit: %v", err)
	}
	if exceeded || cost != 0 {
		t.Fatalf("expected not exceeded initially, got exceeded=%v cost=%f", exceeded, cost)
	}

	// 2. Simulate generating a 4K Veo video artifact ($4.80) and incrementing daily usage
	today := time.Now().UTC().Format("2006-01-02")
	acc, err := sessionSvc.IncrementDailyUsage(principal.AccountScopeID, today, 4.80, 0)
	if err != nil {
		t.Fatalf("increment daily usage: %v", err)
	}
	if acc.TotalCostUSD < 4.79 || acc.TotalCostUSD > 4.81 {
		t.Fatalf("expected accumulator total $4.80, got %f", acc.TotalCostUSD)
	}

	// 3. Verify CheckDailyLimit is now EXCEEDED because of the media cost!
	exceeded, currentCost, limitCost, err := sessionSvc.CheckDailyLimit(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("CheckDailyLimit after media: %v", err)
	}
	if !exceeded {
		t.Fatalf("expected daily limit to be exceeded by media cost, but was false")
	}
	if currentCost < 4.79 || currentCost > 4.81 {
		t.Fatalf("expected currentCost $4.80, got %f", currentCost)
	}
	if limitCost != 3.00 {
		t.Fatalf("expected limitCost $3.00, got %f", limitCost)
	}
}
