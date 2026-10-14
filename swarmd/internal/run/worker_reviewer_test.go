package run

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: validateReviewRequest rejects reviewer targets on other worker roles
// and ambiguous/missing targets before admission; unit validation is the
// narrowest layer for these input rules, not checkout allocation evidence.
func TestReviewerRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		role  string
		input map[string]any
		valid bool
	}{
		{store.WorkerRoleReviewer, map[string]any{"review_job": "worker/w/r"}, true},
		{store.WorkerRoleReviewer, map[string]any{"review_ref": "dev"}, true},
		{store.WorkerRoleReviewer, nil, false},
		{store.WorkerRoleCoder, map[string]any{"review_job": "worker/w/r"}, false},
		{store.WorkerRoleReviewer, map[string]any{"review_job": "bad"}, false},
		{store.WorkerRoleReviewer, map[string]any{"review_job": "worker/w/r", "review_ref": "dev"}, false},
	} {
		if err := validateReviewRequest(tc.role, tc.input); (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	found := false
	for _, p := range WorkerPresets {
		if p.Role == store.WorkerRoleReviewer {
			found = true
		}
	}
	if !found {
		t.Fatal("reviewer preset missing")
	}
}
