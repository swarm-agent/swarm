package tool

import (
	"context"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: V1 adapter methods must fail closed even when called directly. The
// actual V2 provider/schema/store workflow is proved in run's integration test;
// this narrow boundary test prevents restoring save-first conversion by accident.
func TestAutomationV2RetiresLegacyPlanAdapters(t *testing.T) {
	r := NewRuntime(1)
	for name, call := range map[string]func() (string, error){
		"edit": func() (string, error) {
			return r.EditParentAutomation(context.Background(), WorkspaceScope{}, store.SessionPlanAutomationIntent{}, "edit", 1)
		},
		"review": func() (string, error) {
			return r.ReviewPlanAutomation(context.Background(), WorkspaceScope{}, store.SessionPlanAutomationIntent{})
		},
		"instructions": func() (string, error) {
			return r.ProposeParentAutomationInstructions(context.Background(), WorkspaceScope{}, store.SessionPlanAutomationIntent{}, "edit", 1, &store.SessionPlanDocument{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := call()
			if err == nil || out != "" || !strings.Contains(err.Error(), "retired") {
				t.Fatalf("legacy adapter returned authority: %s %v", out, err)
			}
		})
	}
	for _, d := range r.Definitions() {
		if d.Name == "manage_automation" || d.Name == "manage_workers" {
			p := d.Parameters["properties"].(map[string]any)
			if p["definition"] != nil {
				t.Fatal("legacy definition advertised")
			}
			for _, a := range p["action"].(map[string]any)["enum"].([]string) {
				if a != "review" && a != "context" && a != "list" && a != "progress" && a != "help" {
					t.Fatal("mutation advertised", a)
				}
			}
		}
	}
}
