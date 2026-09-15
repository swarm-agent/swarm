package api

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestGoogleTokenOverflowDiagnosticMatching(t *testing.T) {
	// Purpose:
	// - Requirement: Google 400 token count overflow errors must be recognized by context overflow classifiers.
	// - Threat/regression: Provider error formatting variations prevent context overflow compaction from triggering.
	// - Boundary/authority: sessionV3IsGoogleTokenOverflowDiagnostic, sessionV3IsContextOverflowDiagnostic, parseSessionV3GoogleMaxAllowedTokens in api/sessions_v3_executor.go.
	// - Narrowest test layer: Unit test verifying pattern matching across exact Google error signatures and negative cases.
	const streamingSample = `google streamGenerateContent failed status=400 body={
  "error": {
    "code": 400,
    "message": "The input token count exceeds the maximum number of tokens allowed 1048576.",
    "status": "INVALID_ARGUMENT"
  }
}`
	const unarySample = `google generateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 2097152.","status":"INVALID_ARGUMENT"}}`

	positives := []string{
		streamingSample,
		unarySample,
		"The input token count exceeds the maximum number of tokens allowed 1048576.",
		"the input token count exceeds the maximum number of tokens allowed 1048576.",
		"input token count exceeds the limit",
		"exceeds the maximum number of tokens allowed",
		"maximum number of tokens allowed 1048576",
	}
	for _, raw := range positives {
		if !sessionV3IsGoogleTokenOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsGoogleTokenOverflowDiagnostic(%q) = false, want true", raw)
		}
		if !sessionV3IsContextOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsContextOverflowDiagnostic(%q) = false, want true", raw)
		}
	}

	negatives := []string{
		"",
		"google streamGenerateContent request failed: connection reset by peer",
		"status=503 body=service unavailable",
		"status=400 body=invalid argument: model not found",
		"rate limit exceeded",
	}
	for _, raw := range negatives {
		if sessionV3IsGoogleTokenOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsGoogleTokenOverflowDiagnostic(%q) = true, want false", raw)
		}
	}

	// Test max allowed tokens parser.
	if limit := parseSessionV3GoogleMaxAllowedTokens(streamingSample); limit != 1048576 {
		t.Fatalf("parseSessionV3GoogleMaxAllowedTokens(streaming) = %d, want 1048576", limit)
	}
	if limit := parseSessionV3GoogleMaxAllowedTokens(unarySample); limit != 2097152 {
		t.Fatalf("parseSessionV3GoogleMaxAllowedTokens(unary) = %d, want 2097152", limit)
	}
	if limit := parseSessionV3GoogleMaxAllowedTokens("unrelated error"); limit != 0 {
		t.Fatalf("parseSessionV3GoogleMaxAllowedTokens(unrelated) = %d, want 0", limit)
	}
}

func TestShouldTriggerContextOverflowCompactionAt85Percent(t *testing.T) {
	// Purpose:
	// - Requirement: Google token overflow errors must verify context window utilization is >= 85% before triggering compaction.
	// - Threat/regression: Erroneous compaction on small context loops, or failure to compact when context is >= 85%.
	// - Boundary/authority: shouldTriggerContextOverflowCompaction, sessionV3ContextUtilizationPercent in api/sessions_v3_executor.go.
	// - Narrowest test layer: Executor method tests with usage summaries and message token estimations.
	const googleErrText = `google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 1048576.","status":"INVALID_ARGUMENT"}}`
	googleErr := errors.New(googleErrText)
	genericErr := errors.New("provider context overflow: context_length_exceeded")

	dir := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(dir, "google-overflow.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessionStore := pebblestore.NewSessionStore(db)
	eventLog, _ := pebblestore.NewEventLog(db)
	sessionSvc := sessionruntime.NewService(sessionStore, eventLog)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	server := &Server{
		sessions: sessionSvc,
	}
	exec := &sessionV3Executor{
		server: server,
	}

	job := sessionV3ExecutorJob{
		Principal: principal,
		SessionID: "sess-test-overflow",
		RunID:     "run-test-overflow",
	}

	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{
		ID:             job.SessionID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Overflow Test",
		Mode:           sessionruntime.ModeAuto,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Case 1: Generic context_length_exceeded should trigger compaction regardless of 85% check.
	if !exec.shouldTriggerContextOverflowCompaction(job, genericErr) {
		t.Fatal("shouldTriggerContextOverflowCompaction(genericErr) = false, want true")
	}

	// Case 2: Google error with no usage summary and no messages (< 85%) should NOT trigger compaction.
	if exec.shouldTriggerContextOverflowCompaction(job, googleErr) {
		t.Fatal("shouldTriggerContextOverflowCompaction(googleErr, empty) = true, want false (<85%)")
	}

	// Case 3: Google error with recorded usage summary at 80% (< 85%) should NOT trigger compaction.
	if err := db.PutJSON(pebblestore.KeySessionUsageSummary(job.SessionID), pebblestore.SessionUsageSummary{
		SessionID:     job.SessionID,
		ContextWindow: 1048576,
		TotalTokens:   838860, // 80.0%
		Source:        "google_api_usage",
	}); err != nil {
		t.Fatalf("set usage summary: %v", err)
	}
	if exec.shouldTriggerContextOverflowCompaction(job, googleErr) {
		t.Fatal("shouldTriggerContextOverflowCompaction(googleErr, 80%) = true, want false")
	}

	// Case 4: Google error with recorded usage summary at 86% (>= 85%) SHOULD trigger compaction.
	if err := db.PutJSON(pebblestore.KeySessionUsageSummary(job.SessionID), pebblestore.SessionUsageSummary{
		SessionID:     job.SessionID,
		ContextWindow: 1048576,
		TotalTokens:   901775, // 86.0%
		Source:        "google_api_usage",
	}); err != nil {
		t.Fatalf("set usage summary: %v", err)
	}
	if !exec.shouldTriggerContextOverflowCompaction(job, googleErr) {
		t.Fatal("shouldTriggerContextOverflowCompaction(googleErr, 86%) = false, want true (>=85%)")
	}

	// Case 5: Verify exact utilization calculation.
	util, ok := exec.sessionV3ContextUtilizationPercent(job, googleErr)
	if !ok {
		t.Fatal("sessionV3ContextUtilizationPercent returned ok=false")
	}
	if util < 85.9 || util > 86.1 {
		t.Fatalf("utilization = %v, want ~86.0", util)
	}
}

func TestShouldTriggerContextOverflowCompactionEstimatedFromMessages(t *testing.T) {
	// Purpose:
	// - Requirement: When usage summary is absent or stale, message characters estimate tokens against Google context window to evaluate >= 85%.
	// - Threat/regression: Sessions overflowing on the current turn without previous turn usage records fail instead of compacting.
	// - Boundary/authority: sessionV3ContextUtilizationPercent in api/sessions_v3_executor.go.
	// - Narrowest test layer: Executor test with message content approaching context window limit.
	const googleErrText = `google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 100000.","status":"INVALID_ARGUMENT"}}`
	googleErr := errors.New(googleErrText)

	dir := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(dir, "google-overflow-est.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessionStore := pebblestore.NewSessionStore(db)
	eventLog, _ := pebblestore.NewEventLog(db)
	sessionSvc := sessionruntime.NewService(sessionStore, eventLog)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	server := &Server{
		sessions: sessionSvc,
	}
	exec := &sessionV3Executor{
		server: server,
	}

	job := sessionV3ExecutorJob{
		Principal: principal,
		SessionID: "sess-test-est",
		RunID:     "run-test-est",
		EpochID:   "epoch-1",
	}

	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{
		ID:             job.SessionID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Overflow Est Test",
		Mode:           sessionruntime.ModeAuto,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add 360,000 characters across messages => 90,000 estimated tokens (approx 4 chars/token).
	// Context window parsed from error = 100,000 tokens => 90% utilization (>= 85%).
	largeContent := strings.Repeat("abcd", 90000) // 360,000 chars
	if _, _, _, err := sessionSvc.AppendMessage(job.SessionID, "user", largeContent, nil); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if !exec.shouldTriggerContextOverflowCompaction(job, googleErr) {
		t.Fatal("shouldTriggerContextOverflowCompaction with 90% estimated message tokens = false, want true")
	}

	util, ok := exec.sessionV3ContextUtilizationPercent(job, googleErr)
	if !ok || util < 89.9 {
		t.Fatalf("util = %v ok = %v, want >= 90.0", util, ok)
	}
}
