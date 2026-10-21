package api

import (
	"context"
	"encoding/json"
	"testing"
)

// Purpose: workerJobResultFromView/automationResult must distinguish an
// assessed empty lane from missing Git evidence. This boundary test prevents
// unavailable, inconsistent or candidate work from silently evading review;
// no provider or HTTP stack is needed to prove the result conversion.
func TestJobResultPositiveNoCommits(t *testing.T) {
	for _, tc := range []struct {
		name, state, head, base string
		commits                 []string
		want                    bool
	}{
		{name: "empty", state: "empty", head: "aaa", base: "aaa", want: true},
		{name: "unknown"},
		{name: "unavailable", state: "unavailable", head: "aaa", base: "aaa"},
		{name: "candidate missing head", state: "candidate_work", commits: []string{"bbb change"}},
		{name: "committed", state: "candidate_work", head: "bbb", base: "aaa", commits: []string{"bbb change"}},
		{name: "inconsistent empty", state: "empty", head: "bbb", base: "aaa"},
		{name: "empty missing heads", state: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := map[string]any{"status": "needs_review", "result": map[string]any{"workspace_path": t.TempDir(), "delivery_state": tc.state, "head": tc.head, "base_commit": tc.base, "commits": tc.commits}}
			core, err := workerJobResultFromView(context.Background(), nil, true, "worker", "Worker", "run", view)
			if err != nil {
				t.Fatal(err)
			}
			result := core.automationResult(context.Background(), nil, "coder")
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			got, _ := fields["no_commits"].(bool)
			if got != tc.want {
				t.Fatalf("no_commits=%v want %v: %s", got, tc.want, data)
			}
			if tc.want && result.Git != nil {
				t.Fatal("empty job has Git")
			}
			if tc.name == "committed" && (result.Git == nil || len(result.Git.Commits) != 1) {
				t.Fatal("lost commits")
			}
		})
	}
}

// Purpose: taskJobResult's empty-lane predicate must never turn missing or
// contradictory evidence into permission to skip review. Testing its conversion
// boundary is the narrowest layer for these assessment inputs.
func TestTaskResultPositiveNoCommits(t *testing.T) {
	const base = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, state, head, path, changeError string
		commits                              []any
		want                                 bool
	}{
		{name: "empty", state: "empty", head: base, path: "lane", want: true},
		{name: "unknown", head: base, path: "lane"},
		{name: "unavailable", state: "unavailable", head: base, path: "lane"},
		{name: "candidate", state: "candidate_work", head: base, path: "lane"},
		{name: "missing head", state: "empty", path: "lane"},
		{name: "missing lane", state: "empty", head: base},
		{name: "different head", state: "empty", head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", path: "lane"},
		{name: "read error", state: "empty", head: base, path: "lane", changeError: "unreadable"},
		{name: "commits", state: "empty", head: base, path: "lane", commits: []any{"bbb change"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := map[string]any{"delivery_state": tc.state, "head": tc.head, "base_commit": base, "changes_error": tc.changeError, "commits": tc.commits}
			if got := taskResultNoCommits(view, tc.path); got != tc.want {
				t.Fatalf("no commits = %v, want %v", got, tc.want)
			}
		})
	}
}
