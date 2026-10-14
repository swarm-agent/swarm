package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: POST .../tasks/{id}/integrate refuses deliveries that are not
// candidate_work/integrated. Dogfooding (s10) got "delivery is not actionable
// ... review recovery" for a worker task whose agent left greetings.log
// uncommitted, with no hint of the state or fix. The refusal must name the
// exact delivery state and the action/endpoint that resolves it. The message
// builder is the narrowest layer: the handler's gating condition is unchanged,
// so this proves wording only, not integration authority.
func TestTaskDeliveryNotActionableMessageNamesStateAndRemedy(t *testing.T) {
	cases := []struct {
		name       string
		assessment *pebblestore.TaskDeliveryAssessment
		want       []string
	}{
		{"uncommitted worker output", &pebblestore.TaskDeliveryAssessment{State: "empty", ReasonCode: "no_commits", Reason: "No commits since captured base", Freshness: "observed", SourceDirty: true, SourceBranch: "agent/worker-x"}, []string{`"empty"`, "no_commits", "uncommitted changes", "commit the changes", `"agent/worker-x"`, "POST /v3/projects/p1/tasks/t1/integrate"}},
		{"no work", &pebblestore.TaskDeliveryAssessment{State: "empty", ReasonCode: "no_commits", Freshness: "observed"}, []string{`"empty"`, "nothing to integrate"}},
		{"rewritten", &pebblestore.TaskDeliveryAssessment{State: "history_rewritten", ReasonCode: "base_not_on_target", Freshness: "observed"}, []string{`"history_rewritten"`, "POST /v3/projects/p1/tasks/t1/recover-integrate"}},
		{"stale", &pebblestore.TaskDeliveryAssessment{State: "unavailable", ReasonCode: "heads_changed", Freshness: "stale"}, []string{`"unavailable"`, `freshness "stale"`, "nothing was integrated", "retry POST /v3/projects/p1/tasks/t1/integrate", "GET /v3/projects/p1/tasks/t1"}},
		{"inspection timeout", &pebblestore.TaskDeliveryAssessment{State: "unavailable", ReasonCode: "inspection_timeout", Reason: "Git inspection exceeded its time budget", Freshness: "unknown"}, []string{`"unavailable"`, "inspection_timeout", "exceeded its time budget", `freshness "unknown"`, "retry POST /v3/projects/p1/tasks/t1/integrate"}},
		{"ambiguous", &pebblestore.TaskDeliveryAssessment{State: "ambiguous", ReasonCode: "base_not_ancestor", Freshness: "observed"}, []string{`"ambiguous"`, "base_not_ancestor", "inspect the task worktree"}},
		{"missing", nil, []string{"not assessed", "GET /v3/projects/p1/tasks/t1"}},
	}
	for _, tc := range cases {
		got := taskDeliveryNotActionableMessage("p1", "t1", tc.assessment)
		if !strings.HasPrefix(got, "delivery is not actionable for direct integration") {
			t.Fatalf("%s: message lost its stable prefix: %q", tc.name, got)
		}
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: %q missing %q", tc.name, got, want)
			}
		}
	}
}
