package pebblestore

import (
	"encoding/json"
	"github.com/cockroachdb/pebble"
	"net/url"
	"strings"
)

func (ws *WorkerStore) JobReviews(account, worker, run string) ([]JobReview, error) {
	var record JobReviewRecord
	_, err := ws.store.GetJSON(jobReviewKey(account, worker, run), &record)
	if err != nil {
		return nil, err
	}
	out := make([]JobReview, 0, len(record.ByHead))
	for _, r := range record.ByHead {
		out = append(out, r)
	}
	return out, nil
}

// ReviewRecoveryPage is a bounded account-scoped scan of durable review
// evidence. It does not depend on observer delivery or RecordedJobs replay.
func (ws *WorkerStore) ReviewRecoveryPage(account, after string) ([]Signal, string, error) {
	prefix := "worker_review/" + url.PathEscape(account) + "/"
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()
	var out []Signal
	next := ""
	valid := it.First()
	if after != "" {
		valid = it.SeekGE([]byte(after))
		if valid && string(it.Key()) == after {
			valid = it.Next()
		}
	}
	for n := 0; valid && n < 100; n++ {
		key := string(it.Key())
		var record JobReviewRecord
		if err := json.Unmarshal(it.Value(), &record); err != nil {
			return nil, "", err
		}
		parts := strings.Split(strings.TrimPrefix(key, prefix), "/")
		if len(parts) == 2 {
			worker, err := url.PathUnescape(parts[0])
			if err != nil {
				return nil, "", err
			}
			run, err := url.PathUnescape(parts[1])
			if err != nil {
				return nil, "", err
			}
			for _, r := range record.ByHead {
				if r.Verdict == "ok" {
					out = append(out, Signal{Kind: "job.reviewed", Account: account, Refs: map[string]string{"worker_id": worker, "run_id": run}, Attrs: map[string]string{"verdict": "ok", "head": r.Head}})
					break
				}
			}
		}
		next = key
		valid = it.Next()
	}
	if !valid {
		next = ""
	}
	return out, next, it.Error()
}
