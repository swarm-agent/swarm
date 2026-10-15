package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"testing"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: prepareLandWorktree and Queue.Process must never invoke Land for
// absent, blocking, stale or patch-changing evidence. This command-boundary
// test proves refusal postconditions and conflict abort without privileged Git.
func TestLandRequireReview(t *testing.T) {
	old, rebased, base := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, tc := range []struct {
		name, verdict                           string
		stale, move, changed, conflict, ungated bool
		want                                    bool
	}{
		{name: "missing"}, {name: "block", verdict: "block"}, {name: "stale", verdict: "ok", stale: true},
		{name: "exact", verdict: "ok", want: true}, {name: "same patch", verdict: "ok", move: true, want: true},
		{name: "changed patch", verdict: "ok", move: true, changed: true}, {name: "conflict", verdict: "ok", conflict: true},
		{name: "legacy", ungated: true, move: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := publish.NewQueue(&landMemoryStore{})
			entry := publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: "/project", Branch: "agent/job", Head: old, BaseBranch: "dev"}}
			if _, err := q.Enqueue(entry); err != nil {
				t.Fatal(err)
			}
			head := old
			land, rebase, abort := 0, 0, 0
			err := q.Process(context.Background(), "account", "/project", publish.QueueHooks{
				Prepare: func(ctx context.Context, e *publish.Entry) error {
					return prepareLandWorktree(ctx, publish.Rule{Mode: "land", RequireReview: !tc.ungated}, e, "/project/job", nil, 1,
						func(_ context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
							out := &provider.ExecResult{}
							switch strings.Join(r.Command, " ") {
							case "git branch --show-current":
								out.Stdout = "agent/job"
							case "git rev-parse HEAD":
								out.Stdout = head
							case "git rebase " + base:
								rebase++
								if tc.conflict {
									out.ExitCode = 1
								} else if tc.move {
									head = rebased
								}
							case "git rebase --abort":
								abort++
							case "git diff --name-only --diff-filter=U":
								out.Stdout = "file.go"
							default:
								if r.Command[0] == "bash" {
									patch := strings.Repeat("d", 40)
									if head == rebased && tc.changed {
										patch = strings.Repeat("e", 40)
									}
									out.Stdout = patch + " " + strings.Repeat("0", 40)
								}
							}
							return out, nil
						}, func(context.Context, publish.Rule, string) (publish.Base, error) {
							return publish.Base{Commit: base, Branch: "dev"}, nil
						},
						func(h string) error {
							review := &store.JobReview{Head: old, Verdict: tc.verdict}
							if tc.stale {
								review.Head = base
							}
							valid := store.CurrentJobReview(review, h)
							if valid == nil || valid.Verdict != "ok" {
								return reviewRequired(h)
							}
							return nil
						})
				},
				Land: func(_ context.Context, e publish.Entry) (publish.LandResult, error) {
					land++
					return publish.LandResult{Commit: e.Request.Head}, nil
				},
				SendBack: func(context.Context, publish.Entry) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := q.Get("account", "job")
			if err != nil {
				t.Fatal(err)
			}
			if (land == 1) != tc.want {
				t.Fatalf("land calls %d state %+v", land, got)
			}
			if tc.want && (got.State != "landed" || got.Commit != head) {
				t.Fatalf("bad receipt %+v", got)
			}
			if !tc.want && !tc.conflict && !strings.Contains(got.Reason, "give the reviewer `review_job`") {
				t.Fatalf("missing fix: %+v", got)
			}
			if tc.conflict && (abort != 1 || got.State != "sent_back") {
				t.Fatalf("conflict not aborted: %+v", got)
			}
			if (tc.verdict != "ok" || tc.stale) && !tc.ungated && rebase != 0 {
				t.Fatal("unreviewed head mutated")
			}
		})
	}
}

// Purpose: AutoPublisher.once consumes OK review signals, but never BLOCK or
// unrelated signals; the event predicate is the narrowest routing-layer proof.
func TestAutoPublishReviewSignal(t *testing.T) {
	for _, tc := range []struct {
		kind, verdict string
		want          bool
	}{
		{"job.reviewed", "ok", true}, {"job.reviewed", "block", false}, {"job.reviewed", "", false},
		{"worker.run.succeeded", "", true}, {"job.landed", "ok", false},
	} {
		if autoPublishSignal(tc.kind, tc.verdict) != tc.want {
			t.Fatal(tc)
		}
	}
	if reviewRequired("head").Error() != "needs an OK review of head: give the reviewer `review_job`" {
		t.Fatal("missing remediation")
	}
}

// Purpose: publishJob must consume CurrentValidJobReview from the authenticated
// account, refusing without queue mutation for missing/BLOCK/stale evidence.
// The real Pebble fixture proves durable admission rather than trusting JSON.
func TestPublishJobRequiresRecordedReview(t *testing.T) {
	s, _, handler := setupWorkerAPITestServer(t)
	ws := s.sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{WorkerID: coder.ID, UserID: "user-test", SessionID: "publish-review-session", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "worker/" + coder.ID + "/" + target.ID
	head := strings.Repeat("a", 40)
	request := httptest.NewRequest("POST", "/mcp", nil)
	request = request.WithContext(context.WithValue(request.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", AccountScopeID: "acct-test", UserID: "user-test"}))
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, "user-test", "acct-test"); err != nil {
		t.Fatal(err)
	}
	c := &controlMCPCall{server: s, request: request, next: handler}
	q, err := s.landingQueue()
	if err != nil {
		t.Fatal(err)
	}
	for i, verdict := range []string{"missing", "block", "stale", "ok"} {
		if verdict != "missing" {
			r, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: "user-test", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": jobID}})
			if err != nil {
				t.Fatal(err)
			}
			reviewed := head
			v := verdict
			if verdict == "stale" {
				reviewed = strings.Repeat("b", 40)
				v = "ok"
			}
			if err := ws.RecordJobReview("acct-test", coder.ID, target.ID, store.JobReview{Head: reviewed, Verdict: v, ReviewerJobID: "worker/" + reviewer.ID + "/" + r.ID, RecordedAt: int64(i + 1)}); err != nil {
				t.Fatal(err)
			}
		}
		out, err := c.publishJob(controlMCPJobWorker, coder.ID, target.ID, controlMCPJobRefs{}, publish.Rule{Folder: "/project", Mode: "land", RequireReview: true}, map[string]any{"head": head, "worktree_branch": "agent/job", "base_branch": "dev"}, true)
		if err != nil {
			t.Fatal(err)
		}
		entry, _, err := q.Get("acct-test", jobID)
		if err != nil {
			t.Fatal(err)
		}
		if verdict == "ok" {
			if out["delivery_state"] != "queued" || entry.Request.Head != head {
				t.Fatalf("OK not queued: %+v", out)
			}
		} else {
			if out["delivery_state"] != "refused" || out["delivery_reason"] != reviewRequired(head).Error() || entry.State != "" {
				t.Fatalf("unauthorized queue mutation: %+v %+v", out, entry)
			}
		}
	}
	// Purpose: admission must reuse queue-owned approval after transient failure,
	// without accepting an exact-head BLOCK. Real store plus publishJob exercises
	// the shared explicit/automatic admission boundary and re-enqueue persistence.
	rebased := strings.Repeat("c", 40)
	approved, err := ws.CurrentValidJobReview("acct-test", coder.ID, target.ID, head)
	if err != nil {
		t.Fatal(err)
	}
	failHooks := publish.QueueHooks{Prepare: func(_ context.Context, e *publish.Entry) error {
		e.Review = &publish.ReviewEquivalence{ReviewedHead: head, RebasedHead: rebased, PatchID: "patch", ReviewID: approved.ReviewerJobID}
		e.Request.Head = rebased
		return nil
	}, Land: func(context.Context, publish.Entry) (publish.LandResult, error) {
		return publish.LandResult{}, errors.New("transient")
	}}
	if err := q.Process(context.Background(), "acct-test", "/project", failHooks); err != nil {
		t.Fatal(err)
	}
	rule := publish.Rule{Folder: "/project", Mode: "land", RequireReview: true}
	result := map[string]any{"head": rebased, "worktree_branch": "agent/job", "base_branch": "dev"}
	for _, explicit := range []bool{false, true} {
		out, err := c.publishJob(controlMCPJobWorker, coder.ID, target.ID, controlMCPJobRefs{}, rule, result, explicit)
		entry, _, getErr := q.Get("acct-test", jobID)
		if err != nil || getErr != nil || out["delivery_state"] != "queued" || entry.Review == nil {
			t.Fatalf("retry stranded: %+v %+v %v %v", out, entry, err, getErr)
		}
		if err := q.Process(context.Background(), "acct-test", "/project", failHooks); err != nil {
			t.Fatal(err)
		}
	}
	r, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: "user-test", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": jobID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.RecordJobReview("acct-test", coder.ID, target.ID, store.JobReview{Head: rebased, Verdict: "block", ReviewerJobID: "worker/" + reviewer.ID + "/" + r.ID, RecordedAt: 10}); err != nil {
		t.Fatal(err)
	}
	out, err := c.publishJob(controlMCPJobWorker, coder.ID, target.ID, controlMCPJobRefs{}, rule, result, true)
	entry, _, getErr := q.Get("acct-test", jobID)
	if err != nil || getErr != nil || out["delivery_state"] != "refused" || entry.State != "land_failed" {
		t.Fatalf("BLOCK bypassed: %+v %+v %v", out, entry, err)
	}
}

// Purpose: rejectBlockingPatch is the preparation policy boundary: exact-head
// and equivalent-head BLOCK both override an older OK, while distinct patches
// do not. Lookup failure must fail closed, never authorize landing.
func TestLandReviewBlockingPatch(t *testing.T) {
	for _, tc := range []struct {
		name, head, patch string
		fail, want        bool
	}{
		{"rebased", "current", "different", false, true},
		{"equivalent", "other", "same", false, true},
		{"different", "other", "different", false, false},
		{"lookup error", "other", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectBlockingPatch("current", "same", []store.JobReview{{Head: "original", Verdict: "ok"}, {Head: tc.head, Verdict: "block"}}, func(string) (string, error) {
				if tc.fail {
					return "", errors.New("unavailable")
				}
				return tc.patch, nil
			})
			if (err != nil) != tc.want {
				t.Fatalf("BLOCK decision: %v", err)
			}
		})
	}
}
