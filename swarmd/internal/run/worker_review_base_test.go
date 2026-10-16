package run

import (
	"strings"
	"testing"
)

// Purpose: resolveReviewTarget must capture immutable fork/rebase evidence,
// rejecting moving or missing refs before admission. The comparison parser is
// the narrowest boundary for both in-memory and durable JSON inputs.
func TestReviewComparisonExactCommits(t *testing.T) {
	old, newBase, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	base, commits, err := reviewComparisonCommits(old, nil)
	if err != nil || base != old || len(commits) != 1 {
		t.Fatalf("fork: %s %v %v", base, commits, err)
	}
	base, commits, err = reviewComparisonCommits(old, map[string]any{"review_commits": []any{newBase, old, head}})
	if err != nil || base != newBase || len(commits) != 3 || commits[1] != old || commits[2] != head {
		t.Fatalf("rebase: %s %v %v", base, commits, err)
	}
	for _, raw := range []any{[]any{}, []any{"refs/swarm/publish/dev"}, []any{newBase, 42}, "invalid"} {
		if _, commits, err := reviewComparisonCommits(old, map[string]any{"review_commits": raw}); err == nil || commits != nil {
			t.Fatalf("accepted invalid evidence: %v", raw)
		}
	}
	if _, _, err := reviewComparisonCommits("", nil); err == nil {
		t.Fatal("missing base accepted")
	}
}
