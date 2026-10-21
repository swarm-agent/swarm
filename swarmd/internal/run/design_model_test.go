package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type designFakeRunner struct {
	mu       sync.Mutex // swarm_design runs designs concurrently
	requests []provideriface.Request
	text     string
	usage    provideriface.TokenUsage
	// behave, when set, replaces the fixed reply; attempt counts from 1.
	behave func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error)
}

func (r *designFakeRunner) ID() string { return "fixture-provider" }

func (r *designFakeRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, func(provideriface.StreamEvent) {})
}

func (r *designFakeRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	text, usage, behave, attempt := r.text, r.usage, r.behave, len(r.requests)
	r.mu.Unlock()
	if behave != nil {
		return behave(ctx, attempt, onEvent)
	}
	onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: text})
	return provideriface.Response{Text: text, Usage: usage}, nil
}

type designModelFixture struct {
	service   *Service
	store     *pebblestore.SessionStore
	runner    *designFakeRunner
	principal identity.Principal
	sessionID string
}

// newDesignModelFixture builds the real catalog, account settings, session
// and usage stores with one fake provider runner. The account's Designer
// model differs from its Swarm model so resolution is observable.
func newDesignModelFixture(t *testing.T) designModelFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "design-model.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	pricing := json.RawMessage(`{"input_price_per_million_tokens":1,"output_price_per_million_tokens":2}`)
	catalog := pebblestore.NewModelCatalogStore(db)
	var records []pebblestore.ModelCatalogRecord
	for _, name := range []string{"designer-model", "swarm-model"} {
		records = append(records, pebblestore.ModelCatalogRecord{Provider: "fixture-provider", Model: name, Reasoning: true, ThinkingOptions: []string{"low", "high"}, DefaultThinking: "low", ContextWindow: 100000, Pricing: pricing})
	}
	if err := catalog.ReplaceSnapshot(records, pebblestore.ModelCatalogMeta{Source: "fixture"}); err != nil {
		t.Fatal(err)
	}
	settings := pebblestore.NewAgentModelSettingsStore(db)
	designer := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "designer-model", Thinking: "high"}
	other := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "swarm-model", Thinking: "low"}
	if _, err := settings.PutForAccount(pebblestore.AgentModelSettingsRecord{AccountScopeID: "account-a", Swarm: pebblestore.SwarmAgentModelAssignments{Action: other, Plan: other}, SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: other, Finder: other, Coder: other, Designer: designer, Router: other}}); err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	runner := &designFakeRunner{text: "<!doctype html><html><body>ok</body></html>"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"}
	pref := pebblestore.ModelPreference{Provider: "fixture-provider", Model: "designer-model", Thinking: "high"}
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-design-receipt", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: t.TempDir(), Preference: &pref})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{
		sessions:           sessions,
		providers:          providers,
		agents:             agentruntime.NewService(pebblestore.NewAgentStore(db), events),
		model:              model.NewService(pebblestore.NewModelStore(db), events, model.NewCatalogService(catalog)),
		agentModelSettings: agentmodelsettings.NewService(settings),
	}
	return designModelFixture{service: s, store: sessions.Store(), runner: runner, principal: principal, sessionID: session.ID}
}

// Requirement: a design call uses the account's Designer model as one
// tool-free, fresh-context request, and records the provider's reported usage
// on the job's receipt session with the committed cost.
// Threat: a design call inherits tools or provider continuation, runs on
// another role's model, or reports cost the receipt store did not commit.
// Owners: Service.DesignModel (agentmodel.ResolveSystemAgent,
// resolveCompactModelRuntime) and designModel.Complete/record
// (recordProviderUsageSnapshot). Real catalog, settings, session and usage
// stores with a fake runner are the narrowest layer that sees both the
// provider request and the committed receipt without a live provider.
func TestDesignModelUsesDesignerModelAndRecordsReceipt(t *testing.T) {
	f := newDesignModelFixture(t)
	f.runner.usage = provideriface.TokenUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, Source: "fixture"}
	m, err := f.service.DesignModel(f.principal, f.sessionID, "task_design")
	if err != nil {
		t.Fatal(err)
	}
	if info := m.Info(); info.Provider != "fixture-provider" || info.Model != "designer-model" || info.Thinking != "high" {
		t.Fatalf("not the account Designer model: %#v", info)
	}
	reply, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 3, Instructions: "system", Input: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runner.requests) != 1 {
		t.Fatalf("runner calls = %d", len(f.runner.requests))
	}
	req := f.runner.requests[0]
	if req.ToolChoice != "none" || req.Tools != nil || req.ParallelToolCalls || !req.ForceFreshProviderContext || req.NativeContinuationAllowed || req.Model != "designer-model" || req.Thinking != "high" || req.Instructions != "system" || len(req.Input) != 1 {
		t.Fatalf("unexpected provider request: %#v", req)
	}
	if reply.Text != f.runner.text {
		t.Fatalf("reply text %q", reply.Text)
	}
	u := reply.Usage
	if !u.Reported || u.Input != 1000 || u.Output != 500 || u.Total != 1500 || u.CostUSD == nil || u.PriceStatus != "known" {
		t.Fatalf("usage not from the committed receipt: %#v", u)
	}
	receipt, found, err := f.store.GetTurnUsage(f.sessionID, "design:task_design:3:generate")
	if err != nil || !found {
		t.Fatalf("receipt missing: found=%v err=%v", found, err)
	}
	if receipt.InputTokens != 1000 || receipt.Provider != "fixture-provider" || receipt.Model != "designer-model" || receipt.EstimatedCostUSD != *u.CostUSD {
		t.Fatalf("receipt and reply disagree: %#v vs %#v", receipt, u)
	}
}

// Requirement: a provider that reports no usage leaves the call unreported
// with no cost and no receipt. Threat: synthesized token or cost numbers.
// Owners: designModel.record. Same store-backed layer as above.
func TestDesignModelUnreportedUsageIsNotSynthesized(t *testing.T) {
	f := newDesignModelFixture(t)
	m, err := f.service.DesignModel(f.principal, f.sessionID, "task_design")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 1, Instructions: "system", Input: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Usage.Reported || reply.Usage.CostUSD != nil || reply.Usage.Total != 0 {
		t.Fatalf("synthesized usage: %#v", reply.Usage)
	}
	if _, found, err := f.store.GetTurnUsage(f.sessionID, "design:task_design:1:generate"); err != nil || found {
		t.Fatalf("receipt recorded without reported usage: found=%v err=%v", found, err)
	}
}

// Requirement: the account's daily spending limit refuses a design call
// before the provider is called, as design.ErrBudgetExceeded so the pipeline
// stops starting designs. Threat: spending past the limit, or a limit refusal
// that looks like an ordinary model failure. Owners: designModel.Complete and
// session.Service.CheckDailyLimit with the real usage accumulator.
func TestDesignModelDailyLimitRefusesBeforeSpending(t *testing.T) {
	f := newDesignModelFixture(t)
	f.runner.usage = provideriface.TokenUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, Source: "fixture"}
	m, err := f.service.DesignModel(f.principal, f.sessionID, "task_design")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 1, Instructions: "s", Input: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: "account-a", DailyCostLimitUSD: 0.001, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	reply, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 2, Instructions: "s", Input: "b"})
	if !errors.Is(err, design.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if len(f.runner.requests) != 1 || reply.Usage.Reported || reply.Text != "" {
		t.Fatalf("the refused call reached the provider: calls=%d reply=%#v", len(f.runner.requests), reply)
	}
	if _, found, _ := f.store.GetTurnUsage(f.sessionID, "design:task_design:2:generate"); found {
		t.Fatal("a refused call recorded a receipt")
	}
}

// Requirement: an account without a runnable Designer model gets a clear
// error and no model. Threat: silently designing on another account's or a
// hardcoded model. Owner: Service.DesignModel.
func TestDesignModelRequiresAccountDesignerSettings(t *testing.T) {
	f := newDesignModelFixture(t)
	other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-without-settings"}
	if m, err := f.service.DesignModel(other, f.sessionID, "task_design"); err == nil || m != nil {
		t.Fatalf("resolved a Designer model without account settings: %v", m)
	}
	if len(f.runner.requests) != 0 {
		t.Fatal("resolution called the provider")
	}
}

// Requirement: a design call whose attempt ended before any output arrived
// (its own deadline, or a stream that closed empty without a finish reason)
// is sent once more; an attempt that had already streamed text is never
// replayed, and a second empty attempt gives up. Both attempts' reported usage
// is recorded under separate run ids and summed on the call.
// Threat: 10% of live designs failed on the per-call limit before Vertex sent
// anything; retrying blindly would double-spend partial replies or loop.
// Owners: designModel.Complete/attempt and addDesignUsage. The store-backed
// fixture with a scripted runner is the narrowest layer that sees deadlines,
// provider requests and committed receipts together.
func TestDesignModelRetriesOnlyAnAttemptWithoutOutput(t *testing.T) {
	waitForDeadline := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	html := "<!doctype html><html><body>ok</body></html>"
	cases := []struct {
		name     string
		behave   func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error)
		requests int
		wantErr  bool
		attempts int
	}{
		{"deadline before output, then success", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			if attempt == 1 {
				return provideriface.Response{Usage: provideriface.TokenUsage{InputTokens: 100, TotalTokens: 100, Source: "fixture"}}, waitForDeadline(ctx)
			}
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: html})
			return provideriface.Response{Text: html, Usage: provideriface.TokenUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, Source: "fixture"}}, nil
		}, 2, false, 2},
		{"empty incomplete stream, then success", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			if attempt == 1 {
				return provideriface.Response{}, provideriface.ErrStreamIncomplete
			}
			return provideriface.Response{Text: html}, nil
		}, 2, false, 2},
		{"deadline after output is not replayed", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "<!doctype html><html><bo"})
			return provideriface.Response{}, waitForDeadline(ctx)
		}, 1, true, 1},
		{"incomplete stream after output is not replayed", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "<!doctype html>"})
			return provideriface.Response{}, provideriface.ErrStreamIncomplete
		}, 1, true, 1},
		{"two empty attempts give up", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			return provideriface.Response{}, waitForDeadline(ctx)
		}, 2, true, 2},
		{"other provider errors are not replayed", func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			return provideriface.Response{}, errors.New("400 bad request")
		}, 1, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDesignModelFixture(t)
			f.runner.behave = tc.behave
			m, err := f.service.DesignModel(f.principal, f.sessionID, "task_design")
			if err != nil {
				t.Fatal(err)
			}
			m.(*designModel).timeout = 50 * time.Millisecond
			reply, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 1, Instructions: "system", Input: "brief"})
			if (err != nil) != tc.wantErr || len(f.runner.requests) != tc.requests || reply.Usage.Attempts != tc.attempts {
				t.Fatalf("err=%v requests=%d attempts=%d", err, len(f.runner.requests), reply.Usage.Attempts)
			}
			if !tc.wantErr && reply.Text != html {
				t.Fatalf("reply text %q", reply.Text)
			}
		})
	}

	// Usage of both attempts is recorded separately and summed on the call.
	f := newDesignModelFixture(t)
	f.runner.behave = cases[0].behave
	m, err := f.service.DesignModel(f.principal, f.sessionID, "task_design")
	if err != nil {
		t.Fatal(err)
	}
	m.(*designModel).timeout = 50 * time.Millisecond
	reply, err := m.Complete(context.Background(), design.ModelCall{Phase: "generate", Item: 1, Instructions: "system", Input: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	first, found1, _ := f.store.GetTurnUsage(f.sessionID, "design:task_design:1:generate")
	retry, found2, _ := f.store.GetTurnUsage(f.sessionID, "design:task_design:1:generate:retry1")
	if !found1 || !found2 || first.TotalTokens != 100 || retry.TotalTokens != 1500 {
		t.Fatalf("attempt receipts: %v %v %#v %#v", found1, found2, first, retry)
	}
	if u := reply.Usage; !u.Reported || u.Total != 1600 || u.CostUSD == nil || *u.CostUSD != first.EstimatedCostUSD+retry.EstimatedCostUSD {
		t.Fatalf("summed usage: %#v", u)
	}
}
