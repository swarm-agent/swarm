// Package pricingtest loads checked-in catalog records for pricing regressions.
package pricingtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Record reads the shipped snapshot, rather than simplifying billing conditions.
func Record(t testing.TB, model string) (pricing, providerSpecific json.RawMessage) {
	t.Helper()
	// Package tests run in their source directory; locate the module root
	// without relying on runtime.Caller paths (which -trimpath rewrites).
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal/model/snapshotdata/snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var visit func(any) bool
	visit = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if x["model_id"] == model {
				pricing, err = json.Marshal(x["pricing"])
				if err != nil {
					t.Fatal(err)
				}
				providerSpecific, err = json.Marshal(x["provider_specific"])
				if err != nil {
					t.Fatal(err)
				}
				return true
			}
			for _, child := range x {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	if !visit(root) {
		t.Fatalf("model %s absent from shipped snapshot", model)
	}
	return
}
