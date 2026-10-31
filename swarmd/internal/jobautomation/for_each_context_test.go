package jobautomation

import (
	"strings"
	"testing"
)

// Purpose: NormalizeSteps must refuse step-result context and delta-review
// options on for_each definitions: queueItems renders only item/input context,
// bypassing runAgent. Admission is the narrowest boundary preventing saved
// workflows from silently sending unresolved evidence or doing a full review.
func TestForEachRejectsUnsupportedStepContext(t *testing.T) {
	cases := []struct{ name, brief, since, want string }{
		{"output", "{{steps.review.output}} {{item.id}}", "", "step-result placeholders"},
		{"verdict", "{{steps.review.verdict}}", "", "step-result placeholders"},
		{"head", "{{steps.review.head}}", "", "step-result placeholders"},
		{"handoff", "{{steps.review.handoff}}", "", "step-result placeholders"},
		{"since", "Review {{item.id}}", "review", "does not accept since"},
		{"item-only", "Review {{item.id}}", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := []Step{
				{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review"},
				{ID: "items", Type: StepAgent, Worker: "worker-reviewer", Brief: tc.brief, Since: tc.since, ForEach: &ForEach{File: "items.json"}},
			}
			err := NormalizeSteps(steps)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("supported item brief rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsupported context must fail admission with %q, got %v", tc.want, err)
			}
		})
	}
}
