package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/signals"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: jobReviewSignal exposes verdict/head to the shared sink/watch feed,
// scopes it to the account and never emits private reason/report text. The real
// signal store is the narrowest proof of observable event privacy and routing.
func TestJobReviewSignal(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	feed := store.NewSignalStore(db)
	jobReviewSignal(signals.NewEmitter(feed))("account", "w", "r", store.JobReview{Verdict: "block", Head: "head", ReviewerJobID: "worker/reviewer/run", Reason: "private report"})
	page, err := feed.ListAfter(0, 10, store.SignalFilter{Account: "account"})
	if err != nil || len(page.Signals) != 1 {
		t.Fatal(page, err)
	}
	s := page.Signals[0]
	if s.Kind != "job.reviewed" || s.Attrs["head"] != "head" || s.Attrs["verdict"] != "block" || s.Refs["job_id"] != "worker/w/r" {
		t.Fatal(s)
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), "private report") {
		t.Fatal("private report leaked")
	}
	foreign, err := feed.ListAfter(0, 10, store.SignalFilter{Account: "foreign"})
	if err != nil || len(foreign.Signals) != 0 {
		t.Fatal(foreign, err)
	}
}
