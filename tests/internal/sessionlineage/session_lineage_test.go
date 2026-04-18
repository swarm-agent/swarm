package sessionlineage_test

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestSessionLineageDisplaySuppressesBogusBackgroundAgentLabel(t *testing.T) {
	summary := model.SessionSummary{
		Metadata: map[string]any{
			"background":  true,
			"launch_mode": "background",
			"target_kind": "agent",
			"target_name": "swarm",
		},
	}

	if got := ui.SessionLineageDisplay(ui.SessionLineageFromSummary(summary)); got != "" {
		t.Fatalf("SessionLineageDisplay() = %q, want empty label for non-background target kind", got)
	}
}

func TestSessionLineageDisplayShowsBackgroundCommitLabel(t *testing.T) {
	summary := model.SessionSummary{
		Metadata: map[string]any{
			"background":  true,
			"launch_mode": "background",
			"target_kind": "background",
			"target_name": "commit",
		},
	}

	if got := ui.SessionLineageDisplay(ui.SessionLineageFromSummary(summary)); got != "bg:commit" {
		t.Fatalf("SessionLineageDisplay() = %q, want %q", got, "bg:commit")
	}
}
