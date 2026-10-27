package jobautomation

import (
	"strings"
	"testing"
)

// Purpose: RenderItem must serialize the entire item deterministically alongside
// field references, without recursively expanding data. This unit boundary is
// the narrowest proof of compact JSON and the existing missing-field behavior.
func TestRenderWholeItem(t *testing.T) {
	item := map[string]any{"z": []any{"{{item.id}}", true}, "id": "a", "nested": map[string]any{"b": 2, "a": nil}}
	encoded := `{"id":"a","nested":{"a":null,"b":2},"z":["{{item.id}}",true]}`
	for brief, want := range map[string]string{
		"{{item}}":             encoded,
		"{{item.id}} {{item}}": "a " + encoded,
		"{{item.missing}}":     "",
	} {
		if got := RenderItem(brief, item); got != want {
			t.Fatalf("%s: got %q, want %q", brief, got, want)
		}
	}
}

// Purpose: Store.Put and Automation.Normalize must reject unresolved template namespaces
// before saving, not dispatch literal placeholders. A real temporary store is
// the narrowest boundary proving rejected updates leave the revision unchanged.
func TestSaveBriefPlaceholders(t *testing.T) {
	_, store := openTestStore(t)
	a := testAutomation(t.TempDir())
	a.Steps = []Step{{ID: "make", Type: StepAgent, Worker: "maker", Brief: "Make {{item.id}} {{item}} {{inputs.brand}}", ForEach: &ForEach{File: "plan.json"}}}
	a.Inputs = map[string]Input{"brand": {}}
	a.With = map[string]string{"brand": "literal {{unknown.data}}"}
	saved, _, err := store.Put(testAccount, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		brief string
		each  bool
	}{
		{"{{item.x}}", false}, {"{{item}}", false}, {"{{unknown.x}}", true},
		{"{{event.x}}", false}, {"{{inputs}}", true}, {"{{item.}}", true},
		{"{{inputs.missing}}", true},
	} {
		bad := a
		bad.Steps = []Step{{ID: "make", Type: StepAgent, Worker: "maker", Brief: tc.brief}}
		if tc.each {
			bad.Steps[0].ForEach = &ForEach{File: "plan.json"}
		}
		if _, _, err := store.Put(testAccount, bad); !IsValidationError(err) || !strings.Contains(err.Error(), tc.brief) {
			t.Fatalf("%s: %v", tc.brief, err)
		}
		got, err := store.Get(testAccount, saved.ID)
		if err != nil || got.Revision != saved.Revision || got.Steps[0].Brief != saved.Steps[0].Brief {
			t.Fatalf("rejected update changed definition: %+v %v", got, err)
		}
	}
}

// Purpose: admission-only placeholder validation must not silently disable old
// durable definitions. A legacy record loaded from the real store must still
// match and create a delivery; resaving it must fail without changing it.
// Store.Get, Automation.Matches and Store.CreateDelivery are the narrow runtime
// boundaries that exposed the regression, without requiring a worker process.
func TestLegacyBriefPlaceholdersStillCreateDelivery(t *testing.T) {
	for _, brief := range []string{"{{item.x}}", "{{unknown.x}}"} {
		t.Run(brief, func(t *testing.T) {
			db, store := openTestStore(t)
			a := testAutomation(t.TempDir())
			a.ID, a.Account, a.Revision = "legacy", testAccount, 1
			a.Steps = []Step{{ID: "make", Type: StepAgent, Worker: "maker", Brief: brief}}
			if err := db.PutJSON(definitionKey(a.Account, a.ID), a); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Get(a.Account, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			result := Result{JobID: "worker/maker/run", Folder: a.Scope.Folder, Status: StatusSucceeded}
			if !loaded.Matches(&result) {
				t.Fatal("legacy definition silently stopped matching")
			}
			delivery, created, err := store.CreateDelivery(loaded, result)
			if err != nil || !created {
				t.Fatalf("delivery: %+v %v %v", delivery, created, err)
			}
			if _, err := store.GetDelivery(a.Account, delivery.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Put(a.Account, loaded); !IsValidationError(err) {
				t.Fatalf("resave: %v", err)
			}
			unchanged, err := store.Get(a.Account, a.ID)
			if err != nil || unchanged.Revision != a.Revision || unchanged.Steps[0].Brief != brief {
				t.Fatalf("legacy record changed: %+v %v", unchanged, err)
			}
		})
	}
}
