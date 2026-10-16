package api

import (
	"context"
	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	"testing"
)

// Purpose: the production preparation boundary must defer environment acquisition
// until stacking completes, use the preceding candidate as rebase base, and keep
// failing lines on the individual entry. Fake Git and provider isolate this
// orchestration contract without launching a sandbox or a remote build.
func TestLandingTrainEnvironment(t *testing.T) {
	for _, exit := range []int{0, 1} {
		p := &recordingLandProvider{exit: exit}
		prior := "cccccccccccccccccccccccccccccccccccccccc"
		old := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		e := &publish.Entry{DeferChecks: true, StackBase: &publish.Request{Head: prior, BaseBranch: "dev"}, Request: publish.Request{Branch: "job", BaseCommit: old}}
		rebased := false
		git := func(_ context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
			out := ""
			switch r.Command[1] {
			case "branch":
				out = "job"
			case "rev-parse":
				out = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "rebase":
				if len(r.Command) != 5 || r.Command[2] != "--onto" || r.Command[3] != prior || r.Command[4] != old {
					t.Fatalf("wrong rebase %v", r.Command)
				}
				rebased = true
			}
			return &provider.ExecResult{Stdout: out}, nil
		}
		err := prepareLandWorktreeWithExecutors(context.Background(), publish.Rule{}, e, "checkout", []string{"check"}, 1, git, p, environment.Spec{}, func(context.Context, publish.Rule, string) (publish.Base, error) {
			return publish.Base{Commit: old, Branch: "dev"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !rebased || p.acquired != 0 || e.RunChecks == nil || e.Request.BaseCommit != prior {
			t.Fatalf("not deferred %+v", e)
		}
		err = e.RunChecks()
		if (err != nil) != (exit != 0) {
			t.Fatalf("check result %v", err)
		}
		if p.acquired != 1 || p.executed != 1 || p.released != 1 || len(e.Checks) != 1 || e.Checks[0].Output != "check output" {
			t.Fatalf("provider lifecycle %+v entry %+v", p, e)
		}
	}
}

// Purpose: a train must reject an unreviewed job before rebase or environment
// acquisition. This targets the existing review gate in the deferred-check path.
func TestLandingTrainReviewGate(t *testing.T) {
	p := &recordingLandProvider{}
	e := &publish.Entry{DeferChecks: true, Request: publish.Request{Branch: "job"}}
	mutated := false
	git := func(_ context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
		out := ""
		switch r.Command[1] {
		case "branch":
			out = "job"
		case "rev-parse":
			out = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		case "rebase":
			mutated = true
		}
		return &provider.ExecResult{Stdout: out}, nil
	}
	err := prepareLandWorktreeWithExecutors(context.Background(), publish.Rule{Mode: "land", RequireReview: true}, e, "checkout", nil, 1, git, p, environment.Spec{}, func(context.Context, publish.Rule, string) (publish.Base, error) {
		return publish.Base{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branch: "dev"}, nil
	}, func(head string) error { return reviewRequired(head) })
	if err == nil || mutated || p.acquired != 0 || e.RunChecks != nil {
		t.Fatalf("review bypass: %v mutated=%v acquired=%d", err, mutated, p.acquired)
	}
}
