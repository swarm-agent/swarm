package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type recordingRunner struct {
	id        string
	calls     int
	lastReq   provideriface.Request
	requests  []provideriface.Request
	resp      provideriface.Response
	responses []provideriface.Response
	errs      []error
}

func (r *recordingRunner) ID() string {
	return r.id
}

func (r *recordingRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *recordingRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	r.lastReq = req
	r.requests = append(r.requests, req)
	idx := r.calls - 1
	if idx >= 0 && idx < len(r.errs) && r.errs[idx] != nil {
		return provideriface.Response{}, r.errs[idx]
	}
	if idx >= 0 && idx < len(r.responses) {
		out := r.responses[idx]
		if strings.TrimSpace(out.Model) == "" {
			out.Model = req.Model
		}
		return out, nil
	}
	if strings.TrimSpace(r.resp.Text) != "" || len(r.resp.FunctionCalls) > 0 || r.resp.Usage.TotalTokens > 0 || strings.TrimSpace(r.resp.ReasoningSummary) != "" {
		out := r.resp
		if strings.TrimSpace(out.Model) == "" {
			out.Model = req.Model
		}
		return out, nil
	}
	return provideriface.Response{
		Model: req.Model,
		Text:  "ok",
	}, nil
}

type runPreferenceFixture struct {
	service      *Service
	sessions     *sessionruntime.Service
	agents       *agentruntime.Service
	sessionID    string
	googleRunner *recordingRunner
	codexRunner  *recordingRunner
}

func newRunPreferenceFixture(t *testing.T) runPreferenceFixture {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "run-pref.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("google", "gemini-3-pro-preview", "high"); err != nil {
		t.Fatalf("set global model preference: %v", err)
	}

	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("test", filepath.Join(t.TempDir(), "workspace"), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	googleRunner := &recordingRunner{id: "google"}
	codexRunner := &recordingRunner{id: "codex"}
	providers.RegisterRunner(googleRunner)
	providers.RegisterRunner(codexRunner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, agentSvc, nil, 1)
	return runPreferenceFixture{
		service:      svc,
		sessions:     sessionSvc,
		agents:       agentSvc,
		sessionID:    session.ID,
		googleRunner: googleRunner,
		codexRunner:  codexRunner,
	}
}

func TestRunTurnIgnoresProviderOnlyAgentOverride(t *testing.T) {
	fixture := newRunPreferenceFixture(t)

	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Provider: "codex",
	}); err != nil {
		t.Fatalf("set provider-only override: %v", err)
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if fixture.googleRunner.calls != 1 {
		t.Fatalf("google runner calls = %d, want 1", fixture.googleRunner.calls)
	}
	if fixture.codexRunner.calls != 0 {
		t.Fatalf("codex runner calls = %d, want 0", fixture.codexRunner.calls)
	}
	if fixture.googleRunner.lastReq.Model != "gemini-3-pro-preview" {
		t.Fatalf("google runner model = %q, want gemini-3-pro-preview", fixture.googleRunner.lastReq.Model)
	}
	if result.Model != "gemini-3-pro-preview" {
		t.Fatalf("result model = %q, want gemini-3-pro-preview", result.Model)
	}
}

func TestRunTurnUsesCompleteAgentProviderModelOverride(t *testing.T) {
	fixture := newRunPreferenceFixture(t)

	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Provider: "codex",
		Model:    "gpt-5-codex",
	}); err != nil {
		t.Fatalf("set provider+model override: %v", err)
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if fixture.codexRunner.calls != 1 {
		t.Fatalf("codex runner calls = %d, want 1", fixture.codexRunner.calls)
	}
	if fixture.googleRunner.calls != 0 {
		t.Fatalf("google runner calls = %d, want 0", fixture.googleRunner.calls)
	}
	if fixture.codexRunner.lastReq.Model != "gpt-5-codex" {
		t.Fatalf("codex runner model = %q, want gpt-5-codex", fixture.codexRunner.lastReq.Model)
	}
	if result.Model != "gpt-5-codex" {
		t.Fatalf("result model = %q, want gpt-5-codex", result.Model)
	}
}

func TestRunTurnIgnoresInvalidAgentThinkingOverride(t *testing.T) {
	fixture := newRunPreferenceFixture(t)

	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Thinking: "max",
	}); err != nil {
		t.Fatalf("set invalid thinking override: %v", err)
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if fixture.googleRunner.calls != 1 {
		t.Fatalf("google runner calls = %d, want 1", fixture.googleRunner.calls)
	}
	if fixture.googleRunner.lastReq.Thinking != "high" {
		t.Fatalf("google runner thinking = %q, want high", fixture.googleRunner.lastReq.Thinking)
	}
	if result.Thinking != "high" {
		t.Fatalf("result thinking = %q, want high", result.Thinking)
	}
}

func TestRunTurnPersistsCodexTurnUsage(t *testing.T) {
	fixture := newRunPreferenceFixture(t)
	fixture.codexRunner.resp = provideriface.Response{
		Text: "ok",
		Usage: provideriface.TokenUsage{
			InputTokens:      120,
			OutputTokens:     30,
			CacheReadTokens:  60,
			CacheWriteTokens: 4,
			TotalTokens:      150,
			Source:           "codex_api_usage",
			APIUsageRaw: map[string]any{
				"input_tokens":  float64(120),
				"output_tokens": float64(30),
				"total_tokens":  float64(150),
			},
		},
	}

	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Provider: "codex",
		Model:    "gpt-5-codex",
	}); err != nil {
		t.Fatalf("set provider+model override: %v", err)
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.TurnUsage == nil {
		t.Fatalf("result turn usage is nil")
	}
	if result.UsageSummary == nil {
		t.Fatalf("result usage summary is nil")
	}
	if result.UsageSummary.TotalTokens != 150 {
		t.Fatalf("result usage summary total tokens = %d, want 150", result.UsageSummary.TotalTokens)
	}
	if result.TurnUsage.APIUsageRaw != nil {
		t.Fatalf("expected raw api usage payload to be omitted, got %#v", result.TurnUsage.APIUsageRaw)
	}
	if got := len(result.TurnUsage.APIUsageHistory); got != 0 {
		t.Fatalf("result usage history entries = %d, want 0", got)
	}

	storedSummary, ok, err := fixture.sessions.GetUsageSummary(fixture.sessionID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage summary to be stored")
	}
	if storedSummary.TotalTokens != 150 {
		t.Fatalf("stored summary total tokens = %d, want 150", storedSummary.TotalTokens)
	}
	if storedSummary.CacheReadTokens != 60 {
		t.Fatalf("stored summary cache read tokens = %d, want 60", storedSummary.CacheReadTokens)
	}
}

func TestRunTurnCompactsAndContinuesAfterContextOverflowError(t *testing.T) {
	fixture := newRunPreferenceFixture(t)
	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Provider: "codex",
		Model:    "gpt-5.4",
	}); err != nil {
		t.Fatalf("set provider+model override: %v", err)
	}
	fixture.codexRunner.errs = []error{
		errors.New(`codex websocket error event: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`),
	}
	fixture.codexRunner.responses = []provideriface.Response{
		{},
		{Text: "compact recap with current task and files"},
		{Text: "continued after compact"},
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "continue the task",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if fixture.codexRunner.calls != 3 {
		t.Fatalf("codex runner calls = %d, want 3", fixture.codexRunner.calls)
	}
	if result.AssistantMessage.Content != "continued after compact" {
		t.Fatalf("assistant content = %q, want continued after compact", result.AssistantMessage.Content)
	}
	messages, err := fixture.sessions.ListMessages(fixture.sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	foundCheckpoint := false
	for _, message := range messages {
		if strings.Contains(message.Content, "[context-compact]") && strings.Contains(message.Content, "origin=overflow") {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected overflow context checkpoint in messages: %#v", messages)
	}
	if len(fixture.codexRunner.requests) < 3 {
		t.Fatalf("recorded requests = %d, want at least 3", len(fixture.codexRunner.requests))
	}
	continuationInput := requestInputText(fixture.codexRunner.requests[2])
	if !strings.Contains(continuationInput, "Continue the same task from this recap") {
		t.Fatalf("continuation input missing compact continuation lead: %q", continuationInput)
	}
	if !strings.Contains(continuationInput, "compact recap with current task and files") {
		t.Fatalf("continuation input missing compact recap: %q", continuationInput)
	}
}

func requestInputText(req provideriface.Request) string {
	parts := make([]string, 0, len(req.Input))
	for _, item := range req.Input {
		for _, content := range asTestMapSlice(item["content"]) {
			if text, _ := content["text"].(string); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func asTestMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func TestRunTurnPersistsGoogleTurnUsage(t *testing.T) {
	fixture := newRunPreferenceFixture(t)
	fixture.googleRunner.resp = provideriface.Response{
		Text: "ok",
		Usage: provideriface.TokenUsage{
			InputTokens:      210,
			OutputTokens:     55,
			CacheReadTokens:  70,
			CacheWriteTokens: 0,
			TotalTokens:      265,
			Source:           "google_api_usage",
		},
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.TurnUsage == nil {
		t.Fatalf("result turn usage is nil")
	}
	if result.UsageSummary == nil {
		t.Fatalf("result usage summary is nil")
	}
	if result.UsageSummary.TotalTokens != 265 {
		t.Fatalf("result usage summary total tokens = %d, want 265", result.UsageSummary.TotalTokens)
	}
	if result.UsageSummary.CacheReadTokens != 70 {
		t.Fatalf("result usage summary cache read tokens = %d, want 70", result.UsageSummary.CacheReadTokens)
	}

	storedSummary, ok, err := fixture.sessions.GetUsageSummary(fixture.sessionID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage summary to be stored")
	}
	if storedSummary.Provider != "google" {
		t.Fatalf("stored provider = %q, want google", storedSummary.Provider)
	}
	if storedSummary.TotalTokens != 265 {
		t.Fatalf("stored summary total tokens = %d, want 265", storedSummary.TotalTokens)
	}
	if storedSummary.CacheReadTokens != 70 {
		t.Fatalf("stored summary cache read tokens = %d, want 70", storedSummary.CacheReadTokens)
	}
}

func TestRunTurnAgentOverrideClearsCodexRuntimeFlagsOnNonGPT54(t *testing.T) {
	fixture := newRunPreferenceFixture(t)
	if _, _, err := fixture.service.model.SetGlobalPreference("codex", "gpt-5.4", "high", "fast", "1m"); err != nil {
		t.Fatalf("set codex global preference: %v", err)
	}
	if _, _, _, err := fixture.agents.Upsert(agentruntime.UpsertInput{
		Name:     "swarm",
		Provider: "codex",
		Model:    "gpt-5-codex",
	}); err != nil {
		t.Fatalf("set provider+model override: %v", err)
	}

	result, err := fixture.service.RunTurn(context.Background(), fixture.sessionID, RunOptions{Prompt: "hello"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.Model != "gpt-5-codex" {
		t.Fatalf("result model = %q, want gpt-5-codex", result.Model)
	}
	if fixture.codexRunner.lastReq.ServiceTier != "" {
		t.Fatalf("codex service tier = %q, want empty", fixture.codexRunner.lastReq.ServiceTier)
	}
}

func TestResolvePreferenceAppliesCodexRuntimeOverridesToSupportedModels(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "resolve-pref.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)

	resolved, err := modelSvc.ResolvePreference(pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Thinking:    "high",
		ServiceTier: "fast",
		ContextMode: "1m",
	})
	if err != nil {
		t.Fatalf("ResolvePreference(gpt-5.4) error = %v", err)
	}
	if resolved.Preference.ServiceTier != "fast" || resolved.Preference.ContextMode != "1m" {
		t.Fatalf("resolved gpt-5.4 runtime = %#v, want fast/1m", resolved.Preference)
	}
	if resolved.ContextWindow != 1050000 {
		t.Fatalf("resolved gpt-5.4 context window = %d, want 1050000", resolved.ContextWindow)
	}

	resolved, err = modelSvc.ResolvePreference(pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.5",
		Thinking:    "high",
		ServiceTier: "fast",
		ContextMode: "1m",
	})
	if err != nil {
		t.Fatalf("ResolvePreference(gpt-5.5) error = %v", err)
	}
	if resolved.Preference.ServiceTier != "fast" || resolved.Preference.ContextMode != "" {
		t.Fatalf("resolved gpt-5.5 runtime = %#v, want fast only", resolved.Preference)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "gpt-5.5 fast session",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference:    &resolved.Preference,
	})
	if err != nil {
		t.Fatalf("CreateSessionWithOptions(gpt-5.5 fast) error = %v", err)
	}
	if session.Preference.ServiceTier != "fast" || session.Preference.ContextMode != "" {
		t.Fatalf("created session gpt-5.5 runtime = %#v, want fast only", session.Preference)
	}
	if resolved.ContextWindow != 272000 {
		t.Fatalf("resolved gpt-5.5 context window = %d, want 272000", resolved.ContextWindow)
	}

	resolved, err = modelSvc.ResolvePreference(pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5-codex",
		Thinking:    "high",
		ServiceTier: "fast",
		ContextMode: "1m",
	})
	if err != nil {
		t.Fatalf("ResolvePreference(gpt-5-codex) error = %v", err)
	}
	if resolved.Preference.ServiceTier != "" || resolved.Preference.ContextMode != "" {
		t.Fatalf("resolved non-gpt-5.4 runtime = %#v, want empty runtime flags", resolved.Preference)
	}
	if resolved.ContextWindow != 0 {
		t.Fatalf("resolved non-gpt-5.4 context window = %d, want 0", resolved.ContextWindow)
	}
}

func TestPrepareSandboxWorkspace_AllowsReadOnlySourceDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions semantics are required for this test")
	}

	workspace := t.TempDir()
	sourceRoot := filepath.Join(workspace, ".cache", "go", "mod", "github.com", "!data!dog", "zstd@v1.4.5")
	if err := os.MkdirAll(filepath.Join(sourceRoot, ".circleci"), 0o755); err != nil {
		t.Fatalf("create source tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, ".circleci", "config.yml"), []byte("version: 2.1\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.Chmod(sourceRoot, 0o555); err != nil {
		t.Fatalf("chmod source root read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sourceRoot, 0o755)
	})

	worktree, cleanup, err := prepareSandboxWorkspace(workspace, "run_test")
	if err != nil {
		t.Fatalf("prepare sandbox workspace: %v", err)
	}
	t.Cleanup(cleanup)

	destinationNested := filepath.Join(worktree, ".cache", "go", "mod", "github.com", "!data!dog", "zstd@v1.4.5", ".circleci")
	if _, statErr := os.Stat(destinationNested); statErr != nil {
		t.Fatalf("stat destination nested directory: %v", statErr)
	}

	destinationParent := filepath.Dir(destinationNested)
	parentInfo, err := os.Stat(destinationParent)
	if err != nil {
		t.Fatalf("stat destination parent directory: %v", err)
	}
	if parentInfo.Mode().Perm()&0o200 == 0 {
		t.Fatalf("destination parent directory is not owner-writable: mode=%#o", parentInfo.Mode().Perm())
	}
}
