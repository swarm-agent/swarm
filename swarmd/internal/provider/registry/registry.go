package registry

import (
	"context"
	"sort"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type Registry struct {
	adapters map[string]provideriface.Adapter
	runners  map[string]provideriface.Runner
}

func New(adapters ...provideriface.Adapter) *Registry {
	out := &Registry{
		adapters: make(map[string]provideriface.Adapter, len(adapters)),
		runners:  make(map[string]provideriface.Runner, len(adapters)),
	}
	for _, adapter := range adapters {
		out.adapters[adapter.ID()] = adapter
	}
	return out
}

func (r *Registry) ListStatuses(ctx context.Context) ([]provideriface.Status, error) {
	ids := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	statuses := make([]provideriface.Status, 0, len(ids))
	for _, id := range ids {
		status, err := r.adapters[id].Status(ctx)
		if err != nil {
			return nil, err
		}
		runner, hasRunner := r.runners[id]
		if !hasRunner || runner == nil {
			status.Runnable = false
			if status.RunReason == "" {
				status.RunReason = "runner not registered"
			}
		} else if !status.Ready {
			status.Runnable = false
			if status.RunReason == "" {
				status.RunReason = strings.TrimSpace(status.Reason)
				if status.RunReason == "" {
					status.RunReason = "provider not ready"
				}
			}
		} else {
			status.Runnable = true
			status.RunReason = ""
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (r *Registry) Get(id string) (provideriface.Adapter, bool) {
	adapter, ok := r.adapters[id]
	return adapter, ok
}

func (r *Registry) RegisterRunner(runner provideriface.Runner) {
	if runner == nil {
		return
	}
	r.runners[runner.ID()] = runner
}

func (r *Registry) GetRunner(id string) (provideriface.Runner, bool) {
	runner, ok := r.runners[id]
	return runner, ok
}
