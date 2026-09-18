package tool

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestToolDefinitionsTokenBudget(t *testing.T) {
	rt := NewRuntime(1)
	defs := rt.Definitions()
	raw, err := json.Marshal(defs)
	if err != nil {
		t.Fatalf("marshal definitions: %v", err)
	}

	type item struct {
		name  string
		bytes int
	}
	var items []item
	for _, d := range defs {
		dRaw, _ := json.Marshal(d)
		items = append(items, item{d.Name, len(dRaw)})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].bytes > items[j].bytes
	})

	for _, it := range items {
		t.Logf("Tool %-22s: %6d bytes (~%4d tokens)", it.name, it.bytes, it.bytes/4)
	}

	totalBytes := len(raw)
	estimatedTokens := (totalBytes + 3) / 4
	t.Logf("TOTAL: %d bytes (~%d estimated tokens)", totalBytes, estimatedTokens)

	const maxTotalEstimatedTokens = 20000
	if estimatedTokens > maxTotalEstimatedTokens {
		t.Fatalf("total estimated tool definition tokens %d exceeds budget %d", estimatedTokens, maxTotalEstimatedTokens)
	}
}
