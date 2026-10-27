package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
)

func controlMCPWorkerBudgetSchema() map[string]any {
	return map[string]any{"anyOf": []any{controlMCPObject([]string{"daily_usd"}, map[string]any{
		"daily_usd":    map[string]any{"type": "number", "minimum": 0},
		"daily_tokens": map[string]any{"type": "integer", "minimum": 0},
	}), map[string]any{"type": "null"}}, "description": "Daily UTC limits; zero is unset. Null clears limits without clearing spend or holds. Account limits still apply."}
}

type controlMCPWorkerBudget struct {
	DailyUSD    *float64 `json:"daily_usd"`
	DailyTokens int64    `json:"daily_tokens"`
}

func parseControlMCPWorkerBudget(raw any) (controlMCPWorkerBudget, error) {
	var budget controlMCPWorkerBudget
	if raw == nil {
		return budget, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return budget, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&budget); err != nil {
		return budget, err
	}
	if budget.DailyUSD == nil || math.IsNaN(*budget.DailyUSD) || math.IsInf(*budget.DailyUSD, 0) || *budget.DailyUSD < 0 || budget.DailyTokens < 0 {
		return budget, errors.New("budget requires nonnegative daily_usd and optional nonnegative integer daily_tokens")
	}
	return budget, nil
}

func (c *controlMCPCall) deployWorkerWithBudget(worker map[string]any, args map[string]any) (any, error) {
	if role := controlMCPString(args, "role"); role != "" && role != worker["role"] {
		return nil, controlMCPToolFailure("changing an existing worker's role is not supported; create a new worker")
	}
	raw, present := args["budget"]
	if present {
		if err := c.requireTokenScope("automations:write"); err != nil {
			return nil, err
		}
		budget, err := parseControlMCPWorkerBudget(raw)
		if err != nil {
			return nil, err
		}
		p, ok := PrincipalFromRequest(c.request)
		if !ok {
			return nil, errors.New("authenticated principal required")
		}
		id, _ := worker["id"].(string)
		policy, err := c.server.sessions.Store().GetWorkerBudget(p.AccountScopeID, id)
		if err != nil {
			return nil, err
		}
		cost := float64(0)
		if budget.DailyUSD != nil {
			cost = *budget.DailyUSD
		}
		// Invoke the canonical HTTP handler with the original authenticated context.
		// Do not require usage-management scope for a worker spending limit.
		if _, err := c.dispatchTo(http.HandlerFunc(c.server.handleWorkerBudget), http.MethodPut, "/v3/usage/worker-budget", url.Values{"worker_id": {id}}, map[string]any{"expected_revision": policy.Revision, "daily_cost_limit_usd": cost, "daily_tokens_limit": budget.DailyTokens}); err != nil {
			return nil, err
		}
	}
	return c.deployWorkerModel(worker, args)
}
