package run

import (
	"bytes"
	"encoding/json"
	"errors"

	store "swarm/packages/swarmd/internal/store/pebble"
)

func workerToolBudget(args map[string]any) (*store.WorkerBudgetRequest, error) {
	raw, present := args["budget"]
	if !present {
		return nil, nil
	}
	budget := &store.WorkerBudgetRequest{}
	if raw == nil {
		return budget, nil
	}
	var input struct {
		USD    *float64 `json:"daily_usd"`
		Tokens int64    `json:"daily_tokens"`
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	if input.USD == nil {
		return nil, errors.New("budget requires daily_usd")
	}
	budget.DailyUSD, budget.DailyTokens = *input.USD, input.Tokens
	return budget, budget.Validate()
}
