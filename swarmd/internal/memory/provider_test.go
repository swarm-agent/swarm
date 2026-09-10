package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	iface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: verify RuntimeProvider's actual request has no execution authority,
// rejects tool output but accepts missing pricing/output ceilings. Fake transport
// is the narrowest layer that observes the complete provider-neutral request.
type testRunner struct {
	request  iface.Request
	response iface.Response
	calls    int
}

func (r *testRunner) ID() string { return "codex" }
func (r *testRunner) CreateResponse(_ context.Context, q iface.Request) (iface.Response, error) {
	r.calls++
	r.request = q
	return r.response, nil
}
func (r *testRunner) CreateResponseStreaming(ctx context.Context, q iface.Request, _ func(iface.StreamEvent)) (iface.Response, error) {
	return r.CreateResponse(ctx, q)
}

type testRunners struct{ r *testRunner }

func (r testRunners) GetRunner(string) (iface.Runner, bool) { return r.r, true }
func TestMemoryProviderAuthorityAndPricing(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog := store.NewModelCatalogStore(db)
	c := store.ModelCatalogRecord{Provider: "codex", Model: "model", ContextWindow: 32000, MaxOutputTokens: 2000, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Pricing: json.RawMessage(`{"currency":"USD","input_price_per_million_tokens":1,"output_price_per_million_tokens":2}`)}
	if err = catalog.SetRecord(c); err != nil {
		t.Fatal(err)
	}
	r := &testRunner{response: iface.Response{Text: "null", Usage: iface.TokenUsage{OutputTokens: 1}}}
	p := RuntimeProvider{Catalog: catalog, Runners: testRunners{r}}
	m := store.AgentModelAssignment{Provider: "codex", Model: "model", Thinking: "medium"}
	_, err = p.Generate(context.Background(), Request{Model: m, Input: []byte("[]"), Instructions: "bounded", OutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := r.request
	if len(got.Tools) != 0 || got.ToolInvoker != nil || got.WorkspacePath != "" || got.AllowContinuation || !got.StartNewChain || got.MaxOutputTokens != 0 || got.Model != "model" {
		t.Fatal("provider authority leaked")
	}
	r.response.FunctionCalls = []iface.FunctionCall{{Name: "bash"}}
	if _, err = p.Generate(context.Background(), Request{Model: m, Input: []byte("[]"), OutputTokens: 100}); err == nil {
		t.Fatal("tool response accepted")
	}
	r.response.FunctionCalls = nil
	m.ServiceTier = "priority"
	c.ExpiresAt = 1
	if err = catalog.SetRecord(c); err != nil {
		t.Fatal(err)
	}
	c.Pricing = nil
	c.MaxOutputTokens = 0
	if err = catalog.SetRecord(c); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Generate(context.Background(), Request{Model: m, Input: []byte("[]")}); err != nil {
		t.Fatal("pricing or output ceiling blocked generation", err)
	}
	p.Catalog = nil
	if _, err = p.Generate(context.Background(), Request{Model: m, Input: []byte("[]")}); err != nil {
		t.Fatal("missing catalog blocked generation", err)
	}
}
