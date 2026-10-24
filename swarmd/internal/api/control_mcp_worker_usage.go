package api

import (
	"sort"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Keep receipt coverage visible: no records is not proof of zero historical use.
func controlMCPUsageScopeView(total pebblestore.UsageScopeTotal) map[string]any {
	return map[string]any{
		"total_tokens": total.TotalTokens,
		// Media is already included in catalog cost; subscription nominal cost is not spend.
		"cost_usd":       total.CatalogCostUSD + total.ProviderCostUSD + total.ProviderEstimateCostUSD,
		"media_cost_usd": total.MediaCostUSD, "media_count": total.MediaReceipts,
		"coverage": total.Coverage, "history_complete": total.HistoryComplete,
		"unknown_receipts": total.UnknownReceipts,
	}
}

func (c *controlMCPCall) workerUsage(id, date string) (map[string]any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	p, ok := PrincipalFromRequest(c.request)
	if !ok {
		return nil, controlMCPToolFailure("authenticated principal required")
	}
	if c.server == nil || c.server.sessions == nil {
		return nil, controlMCPToolFailure("usage store unavailable")
	}
	store := c.server.sessions.Store()
	all, _, err := store.GetUsageScope(p.AccountScopeID, "worker", "", id)
	if err != nil {
		return nil, err
	}
	today, _, err := store.GetUsageScopeDay(p.AccountScopeID, "worker", "", id, date)
	if err != nil {
		return nil, err
	}
	return map[string]any{"all_time": controlMCPUsageScopeView(all), "today": controlMCPUsageScopeView(today), "today_date": date}, nil
}

func (c *controlMCPCall) workerUsageDetail() ([]map[string]any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	p, ok := PrincipalFromRequest(c.request)
	if !ok {
		return nil, controlMCPToolFailure("authenticated principal required")
	}
	if c.server == nil || c.server.sessions == nil {
		return nil, controlMCPToolFailure("usage store unavailable")
	}
	out := []map[string]any{}
	date := time.Now().UTC().Format("2006-01-02")
	// Use account-scoped store reads, not worker-management routes/scopes.
	for after := ""; ; {
		page, err := c.server.sessions.Store().WorkerStore().ListWorkers(p.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 100, After: after})
		if err != nil {
			return nil, err
		}
		for _, worker := range page.Workers {
			usage, err := c.workerUsage(worker.ID, date)
			if err != nil {
				return nil, err
			}
			usage["id"], usage["name"] = worker.ID, worker.Name
			out = append(out, usage)
		}
		if page.NextCursor == "" {
			break
		}
		after = page.NextCursor
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i]["all_time"].(map[string]any)["cost_usd"].(float64)
		b := out[j]["all_time"].(map[string]any)["cost_usd"].(float64)
		if a == b {
			return out[i]["id"].(string) < out[j]["id"].(string)
		}
		return a > b
	})
	return out, nil
}
