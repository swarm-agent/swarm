package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/providerlimit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a native reviewer task (no worker) whose provider turn ended
// on the provider's usage, rate or session limit, or that gave up waiting
// for it, records no review: it gave no verdict, so it is never a BLOCK
// ("missing or malformed review verdict"). A reviewer task that failed for
// any other reason, Swarm's own artifact quota among them, still records
// BLOCK.
// Threat (socials daily check): the review task of a workflow failed on the
// provider's session limit and was recorded as block / review unreadable.
// Owners: Server.repairTaskReview over providerlimit.Is / IsExhausted and
// the project task store on a real Pebble store and session; this is the
// narrowest layer that reads the stored task and its transcript.
func TestNativeReviewerProviderLimitRecordsNoVerdict(t *testing.T) {
	limit := "claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	for _, tc := range []struct {
		name, lastError string
		verdict         string // "" when no review is recorded
	}{
		{"session limit", limit, ""},
		{"gave up waiting", providerlimit.Exhausted(12, limit), ""},
		{"swarm quota", "artifact v3 git: quota exceeded", "block"},
		{"ordinary failure", "the checkout could not be read", "block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenFixture(t)
			p := testPrincipal()
			task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "limited-review", ProjectID: "limited-project", Agent: "reviewer", StartCommit: strings.Repeat("a", 40), Status: "running"})
			settled, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
				current.Status, current.LastError = "failed", tc.lastError
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			review, err := f.server.repairTaskReview(settled)
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.verdict == "" {
				if review != nil || stored.Review != nil {
					t.Fatalf("a provider limit was recorded as a review: returned %+v stored %+v", review, stored.Review)
				}
				return
			}
			if review == nil || review.Verdict != tc.verdict || stored.Review == nil || stored.Review.Verdict != tc.verdict {
				t.Fatalf("a failed reviewer did not record %s: returned %+v stored %+v", tc.verdict, review, stored.Review)
			}
		})
	}
}
