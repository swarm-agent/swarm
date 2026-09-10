package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	iface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type Runners interface {
	GetRunner(string) (iface.Runner, bool)
}

// RuntimeProvider uses account-authenticated runners, with no tools or continuation.
// Pricing and catalog output ceilings are not admission requirements.
type RuntimeProvider struct {
	Runners Runners
	Catalog *store.ModelCatalogStore
}

func (p *RuntimeProvider) Generate(ctx context.Context, r Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if p.Runners == nil {
		return Result{}, ErrProviderUnavailable
	}
	runner, ok := p.Runners.GetRunner(r.Model.Provider)
	if !ok {
		return Result{}, ErrProviderUnavailable
	}
	var catalog store.ModelCatalogRecord
	if p.Catalog != nil {
		var err error
		catalog, _, err = p.Catalog.GetRecord(r.Model.Provider, r.Model.Model)
		if err != nil {
			return Result{}, err
		}
	}
	response, err := runner.CreateResponse(ctx, iface.Request{Model: r.Model.Model, Thinking: r.Model.Thinking, ServiceTier: r.Model.ServiceTier, ContextMode: r.Model.ContextMode, ModelCatalog: catalog, Instructions: r.Instructions, Input: []map[string]any{{"role": "user", "content": string(r.Input)}}, ToolChoice: "none", StartNewChain: true, ForceFreshProviderContext: true})
	if err != nil {
		return Result{}, err
	}
	// Bound decoded data, not billing or a model's native reasoning allowance.
	if len(response.FunctionCalls) != 0 || len(response.Text) > 131072 || response.Usage.OutputTokens < 0 {
		return Result{}, errors.New("memory provider output rejected")
	}
	var entries []store.MemoryEntry
	dec := json.NewDecoder(bytes.NewBufferString(response.Text))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&entries); err != nil {
		return Result{}, errors.New("memory provider returned invalid JSON array")
	}
	if dec.Decode(new(any)) != io.EOF || len(entries) > 32 {
		return Result{}, errors.New("memory provider returned invalid batch")
	}
	return Result{Entries: entries, OutputTokens: int(response.Usage.OutputTokens)}, nil
}
