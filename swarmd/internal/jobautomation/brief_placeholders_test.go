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

// Purpose: Store.Put and RenderInputs must reject unresolved template namespaces
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
