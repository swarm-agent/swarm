package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestRunIsGoogleTokenOverflowDiagnostic(t *testing.T) {
	// Purpose:
	// - Requirement: Google 400 input token overflow errors must be classified as context overflow in run.Service.
	// - Threat/regression: Failure to recognize Google's exact error format skips compaction recovery in RunTurn.
	// - Boundary/authority: isGoogleTokenOverflowDiagnostic, isContextOverflowDiagnostic, parseGoogleMaxAllowedTokens in run/service.go.
	// - Narrowest test layer: Unit test verifying pattern matching and limit parsing.
	const sample = `google streamGenerateContent failed status=400 body={
  "error": {
    "code": 400,
    "message": "The input token count exceeds the maximum number of tokens allowed 1048576.",
    "status": "INVALID_ARGUMENT"
  }
}`

	if !isGoogleTokenOverflowDiagnostic(sample) {
		t.Fatal("isGoogleTokenOverflowDiagnostic(sample) = false, want true")
	}
	if !isContextOverflowDiagnostic(sample) {
		t.Fatal("isContextOverflowDiagnostic(sample) = false, want true")
	}
	if limit := parseGoogleMaxAllowedTokens(sample); limit != 1048576 {
		t.Fatalf("parseGoogleMaxAllowedTokens(sample) = %d, want 1048576", limit)
	}

	negative := "google streamGenerateContent failed status=503 body=overloaded"
	if isGoogleTokenOverflowDiagnostic(negative) {
		t.Fatal("isGoogleTokenOverflowDiagnostic(negative) = true, want false")
	}
}

func TestRunContextUtilizationPercent(t *testing.T) {
	// Purpose:
	// - Requirement: runContextUtilizationPercent must calculate utilization against context window from usage summary or estimated input tokens.
	// - Threat/regression: Erroneous calculations cause false compaction triggers below 85% or dropped compactions above 85%.
	// - Boundary/authority: runContextUtilizationPercent in run/service.go.
	// - Narrowest test layer: Service helper method test verifying >= 85% and < 85% utilization across sources.
	svc := &Service{}
	googleErr := errors.New(`google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 1048576.","status":"INVALID_ARGUMENT"}}`)

	// Case 1: Usage summary at 86% (>= 85%).
	usage86 := &pebblestore.SessionUsageSummary{
		ContextWindow: 1048576,
		TotalTokens:   901775, // 86%
	}
	util, ok := svc.runContextUtilizationPercent("session-1", 1048576, usage86, nil, googleErr)
	if !ok || util < 85.9 || util > 86.1 {
		t.Fatalf("usage86: util=%v, ok=%v, want ~86.0 and ok=true", util, ok)
	}

	// Case 2: Usage summary at 80% (< 85%).
	usage80 := &pebblestore.SessionUsageSummary{
		ContextWindow: 1048576,
		TotalTokens:   838860, // 80%
	}
	util, ok = svc.runContextUtilizationPercent("session-1", 1048576, usage80, nil, googleErr)
	if !ok || util < 79.9 || util > 80.1 {
		t.Fatalf("usage80: util=%v, ok=%v, want ~80.0 and ok=true", util, ok)
	}

	// Case 3: No usage summary, but input messages estimate 90% utilization (360,000 chars / 4 = 90,000 tokens out of 100,000).
	err100k := errors.New(`google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 100000.","status":"INVALID_ARGUMENT"}}`)
	largeInput := []map[string]any{
		{"role": "user", "content": strings.Repeat("abcd", 90000)},
	}
	util, ok = svc.runContextUtilizationPercent("session-1", 100000, nil, largeInput, err100k)
	if !ok || util < 89.9 {
		t.Fatalf("largeInput: util=%v, ok=%v, want >= 90.0 and ok=true", util, ok)
	}

	// Case 4: No usage summary, small input (1,000 chars / 4 = 250 tokens out of 100,000 = 0.25%).
	smallInput := []map[string]any{
		{"role": "user", "content": "short prompt"},
	}
	util, ok = svc.runContextUtilizationPercent("session-1", 100000, nil, smallInput, err100k)
	if !ok || util >= 85.0 {
		t.Fatalf("smallInput: util=%v, ok=%v, want < 85.0", util, ok)
	}
}

type googleOverflowTestRunner struct {
	t         *testing.T
	mu        sync.Mutex
	calls     int
	requests  []provideriface.Request
	responses []provideriface.Response
	errs      []error
}

func (r *googleOverflowTestRunner) ID() string {
	return "google-fake"
}

func (r *googleOverflowTestRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{ContextMode: provideriface.ExecutionEpochContextStatelessFullInput}
}

func (r *googleOverflowTestRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	return provideriface.MediaAdapterDeclaration{}, errors.New("not supported")
}

func (r *googleOverflowTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *googleOverflowTestRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.requests = append(r.requests, req)
	idx := r.calls - 1
	var err error
	if idx < len(r.errs) && r.errs[idx] != nil {
		err = r.errs[idx]
	}
	var resp provideriface.Response
	if idx < len(r.responses) {
		resp = r.responses[idx]
		if resp.Model == "" {
			resp.Model = req.Model
		}
	} else {
		resp = provideriface.Response{Model: req.Model, Text: "default ok"}
	}
	if r.t != nil {
		r.t.Logf("call #%d model=%s inputLen=%d err=%v respText=%q", r.calls, req.Model, len(req.Input), err, resp.Text)
	}
	if err != nil {
		return provideriface.Response{}, err
	}
	return resp, nil
}

func newGoogleOverflowTestFixture(t *testing.T) (*Service, *sessionruntime.Service, *pebblestore.Store, *googleOverflowTestRunner, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "overflow.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         "user-1",
		AccountScopeID: "account-1",
		Title:          "google overflow",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "google-fake", Model: "gemini-flash", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &googleOverflowTestRunner{t: t}
	providers := registry.New()
	providers.RegisterRunner(runner)

	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	catalogStore := pebblestore.NewModelCatalogStore(store)
	catalog := model.NewCatalogService(catalogStore)
	models := model.NewService(pebblestore.NewModelStore(store), events, catalog)
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatalf("ensure model defaults: %v", err)
	}
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{
		Provider: "google-fake", Model: "gemini-flash", ContextWindow: 1048576, MaxOutputTokens: 32000,
	}); err != nil {
		t.Fatalf("configure catalog: %v", err)
	}

	svc := NewService(sessions, models, providers, tool.NewRuntime(1), nil, agents, nil, events)
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	configured := pebblestore.AgentModelAssignment{Provider: "google-fake", Model: "gemini-flash", Thinking: "off"}
	if _, err := settingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account-1",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured,
		},
	}); err != nil {
		t.Fatalf("configure system-agent models: %v", err)
	}
	svc.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore))

	return svc, sessions, store, runner, session.ID
}

func TestRunTurnGoogleOverflowCompactionSuccessAt85Percent(t *testing.T) {
	// Purpose:
	// - Requirement: When Google returns a 400 token overflow error and context utilization is >= 85%,
	//   RunTurn must trigger context compaction with the Compact agent and continue the run with Gemini.
	// - Threat/regression: Assistant run fails with [run-failed] instead of compacting and continuing.
	// - Boundary/authority: Service.RunTurn, tryContextOverflowCompaction in run/service.go.
	// - Narrowest test layer: RunTurn integration with mock Google runner returning 400 overflow followed by compact & continuation.
	svc, sessions, store, runner, sessionID := newGoogleOverflowTestFixture(t)

	// Set usage summary to 901,775 / 1,048,576 = 86% (>= 85%).
	if err := store.PutJSON(pebblestore.KeySessionUsageSummary(sessionID), pebblestore.SessionUsageSummary{
		SessionID:     sessionID,
		ContextWindow: 1048576,
		TotalTokens:   901775,
		Source:        "google_api_usage",
	}); err != nil {
		t.Fatalf("set usage summary: %v", err)
	}

	googleErr := errors.New(`google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 1048576.","status":"INVALID_ARGUMENT"}}`)

	// Call 1: Google token overflow error on main prompt.
	// Call 2: Compact agent summarizes context.
	// Call 3: Gemini continues after compaction.
	runner.errs = []error{googleErr, nil, nil}
	runner.responses = []provideriface.Response{
		{},
		{Text: "compact recap: user requested analysis, tools ran cleanly."},
		{Text: "here is the continuation answer from Gemini"},
	}

	result, err := svc.RunTurnWithOptions(context.Background(), sessionID, RunOptions{
		Prompt:               "analyze the repository",
		Principal:            identity.Principal{UserID: "user-1", AccountScopeID: "account-1"},
		ApplySessionMutation: sessions.ApplySessionMutation,
	})
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()

	if calls != 3 {
		t.Fatalf("runner calls = %d, want 3 (overflow + compact + continuation)", calls)
	}
	if result.AssistantMessage.Content != "here is the continuation answer from Gemini" {
		t.Fatalf("assistant content = %q, want continuation answer", result.AssistantMessage.Content)
	}

	// Verify checkpoint message exists in session messages.
	messages, err := sessions.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	foundCompactTool := false
	for _, msg := range messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "compact") && strings.Contains(msg.Content, "overflow") {
			foundCompactTool = true
			break
		}
	}
	if !foundCompactTool {
		t.Fatal("expected compact tool message with origin=overflow in session messages")
	}
}

func TestRunTurnGoogleOverflowRefusesCompactionUnder85Percent(t *testing.T) {
	// Purpose:
	// - Requirement: When Google returns a 400 token overflow error but context utilization is < 85%,
	//   RunTurn must NOT trigger compaction and must return the error immediately.
	// - Threat/regression: Spurious compactions loop endlessly on small prompts that fail for other reasons.
	// - Boundary/authority: Service.RunTurn, tryContextOverflowCompaction, runContextUtilizationPercent in run/service.go.
	// - Narrowest test layer: RunTurn test with 50% usage asserting error return and no compaction attempt.
	svc, sessions, store, runner, sessionID := newGoogleOverflowTestFixture(t)

	// Set usage summary to 500,000 / 1,048,576 = 47.6% (< 85%).
	if err := store.PutJSON(pebblestore.KeySessionUsageSummary(sessionID), pebblestore.SessionUsageSummary{
		SessionID:     sessionID,
		ContextWindow: 1048576,
		TotalTokens:   500000,
		Source:        "google_api_usage",
	}); err != nil {
		t.Fatalf("set usage summary: %v", err)
	}

	googleErr := errors.New(`google streamGenerateContent failed status=400 body={"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 1048576.","status":"INVALID_ARGUMENT"}}`)
	runner.errs = []error{googleErr}

	_, err := svc.RunTurnWithOptions(context.Background(), sessionID, RunOptions{
		Prompt:               "short prompt",
		Principal:            identity.Principal{UserID: "user-1", AccountScopeID: "account-1"},
		ApplySessionMutation: sessions.ApplySessionMutation,
	})
	if err == nil {
		runner.mu.Lock()
		reqs := make([]string, len(runner.requests))
		for i, r := range runner.requests {
			reqs[i] = r.Model
		}
		runner.mu.Unlock()
		t.Fatalf("RunTurn succeeded (req models=%v), want error when utilization < 85%%", reqs)
	}
	if !strings.Contains(err.Error(), "The input token count exceeds the maximum number of tokens allowed") {
		t.Fatalf("err = %v, want token count exceeds error", err)
	}

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()

	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1 (no compaction attempted)", calls)
	}
}
