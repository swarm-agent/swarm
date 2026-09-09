package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	iface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

type Runners interface {
	GetRunner(string) (iface.Runner, bool)
}

// RuntimeProvider uses the existing account-authenticated provider registry, but
// builds a fresh single response with no tools, invoker, workspace or continuation.
type RuntimeProvider struct {
	Runners Runners
	Catalog *store.ModelCatalogStore
}

func (p *RuntimeProvider) Quote(ctx context.Context, m store.AgentModelAssignment, input, output int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch m.Provider {
	case "codex", "anthropic", "google", "openrouter":
	default:
		return 0, ErrProviderLimits
	}
	if p.Catalog == nil || p.Runners == nil {
		return 0, ErrProviderLimits
	}
	if _, ok := p.Runners.GetRunner(m.Provider); !ok {
		return 0, ErrProviderLimits
	}
	c, ok, err := p.Catalog.GetRecord(m.Provider, m.Model)
	if err != nil {
		return 0, err
	}
	if !ok || c.ExpiresAt <= time.Now().UnixMilli() || output < 1 || output > c.MaxOutputTokens || input+output > c.ContextWindow {
		return 0, ErrProviderLimits
	}
	// Tier/context multipliers must never be guessed. Only catalog default pricing
	// is supported until the snapshot supplies an explicit bounded tier quote.
	if m.ServiceTier != "" && m.ServiceTier != "default" && m.ServiceTier != "standard" {
		return 0, ErrProviderLimits
	}
	var price struct {
		Currency string  `json:"currency"`
		Input    float64 `json:"input_price_per_million_tokens"`
		Output   float64 `json:"output_price_per_million_tokens"`
	}
	if json.Unmarshal(c.Pricing, &price) != nil || price.Currency != "USD" || price.Input <= 0 || price.Output <= 0 {
		return 0, ErrProviderLimits
	}
	// Reserve full input at 2x (covers cache-write premium) and full output.
	amount := math.Ceil(float64(input)*price.Input*2 + float64(output)*price.Output)
	if math.IsInf(amount, 0) || math.IsNaN(amount) || amount < 1 || amount > 100000000 {
		return 0, ErrProviderLimits
	}
	return int64(amount), nil
}
func (p *RuntimeProvider) Generate(ctx context.Context, r Request) (Result, error) {
	quote, err := p.Quote(ctx, r.Model, len(r.Input)+len(r.Instructions), r.OutputTokens)
	if err != nil {
		return Result{}, err
	}
	if quote > r.SpendLimit {
		return Result{}, ErrProviderLimits
	}
	runner, ok := p.Runners.GetRunner(r.Model.Provider)
	if !ok {
		return Result{}, ErrProviderLimits
	}
	c, ok, err := p.Catalog.GetRecord(r.Model.Provider, r.Model.Model)
	if err != nil || !ok {
		return Result{}, ErrProviderLimits
	}
	response, err := runner.CreateResponse(ctx, iface.Request{Model: r.Model.Model, Thinking: r.Model.Thinking, ServiceTier: r.Model.ServiceTier, ContextMode: r.Model.ContextMode, ModelCatalog: c, MaxOutputTokens: r.OutputTokens, Instructions: r.Instructions, Input: []map[string]any{{"role": "user", "content": string(r.Input)}}, ToolChoice: "none", StartNewChain: true, ForceFreshProviderContext: true})
	if err != nil {
		return Result{}, err
	}
	if len(response.FunctionCalls) != 0 || len(response.Text) > r.OutputTokens || response.Usage.OutputTokens > int64(r.OutputTokens) || response.Usage.OutputTokens < 0 {
		return Result{}, errors.New("memory provider output rejected")
	}
	var entry *store.MemoryEntry
	dec := json.NewDecoder(bytes.NewBufferString(response.Text))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&entry); err != nil {
		return Result{}, errors.New("memory provider returned invalid JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return Result{}, errors.New("memory provider returned trailing output")
	}
	// Conservatively charge the entire reserved quote, not an unverified estimate.
	return Result{Entry: entry, OutputTokens: int(response.Usage.OutputTokens), SpendMicrounits: quote}, nil
}
