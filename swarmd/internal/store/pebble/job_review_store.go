package pebblestore

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

type JobReviewRecord struct {
	Latest       JobReview            `json:"latest"`
	ByHead       map[string]JobReview `json:"by_head"`
	RecordedJobs map[string]bool      `json:"recorded_jobs"`
}

func jobReviewKey(account, worker, run string) string {
	return "worker_review/" + url.PathEscape(account) + "/" + url.PathEscape(worker) + "/" + url.PathEscape(run)
}

// RecordJobReview serializes review completions with worker writes. Replays
// cannot replace newer evidence; each head retains its latest completion.
func (ws *WorkerStore) RecordJobReview(account, worker, run string, review JobReview) error {
	if ws == nil || ws.store == nil {
		return errors.New("store unavailable")
	}
	parts := strings.Split(review.ReviewerJobID, "/")
	if review.Head == "" || review.RecordedAt <= 0 || len(parts) != 3 || parts[0] != "worker" || (review.Verdict != "ok" && review.Verdict != "block") {
		return errors.New("invalid job review")
	}
	ws.store.workersMu.Lock()
	changed := false
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		ws.store.publishWorkerRealtime(published)
		if changed && ws.store.jobReviewObserver != nil {
			ws.store.jobReviewObserver(account, worker, run, review)
		}
	}()
	target, found, err := ws.GetWorkerRun(account, worker, run)
	if err != nil {
		return err
	}
	if !found {
		return ErrWorkerNotFound
	}
	reviewer, found, err := ws.GetWorkerRun(account, parts[1], parts[2])
	if err != nil {
		return err
	}
	if !found || reviewer.Role != WorkerRoleReviewer || reviewer.UserID != target.UserID || reviewer.WorkspaceID != target.WorkspaceID || reviewer.Input["review_job"] != "worker/"+worker+"/"+run {
		return errors.New("reviewer does not own this review target")
	}
	key := jobReviewKey(account, worker, run)
	var record JobReviewRecord
	if _, err := ws.store.GetJSON(key, &record); err != nil {
		return err
	}
	if record.RecordedJobs[review.ReviewerJobID] {
		return nil
	}
	if record.ByHead == nil {
		record.ByHead = map[string]JobReview{}
	}
	if record.RecordedJobs == nil {
		record.RecordedJobs = map[string]bool{}
	}
	record.RecordedJobs[review.ReviewerJobID] = true
	previous := record.ByHead[review.Head]
	if review.RecordedAt >= previous.RecordedAt {
		record.ByHead[review.Head] = review
	}
	if review.RecordedAt >= record.Latest.RecordedAt {
		record.Latest = review
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	m := &workerRealtimeMutation{accountScopeID: account, userID: target.UserID, workerID: worker}
	m.putBytes(key, data)
	project, _ := target.Input["project_id"].(string)
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: worker, RunID: run, SessionID: target.SessionID, ProjectID: project, ActivityKind: "review.recorded", State: review.Verdict}); err != nil {
		return err
	}
	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return err
	}
	published = m
	changed = true
	return nil
}

func (ws *WorkerStore) GetJobReview(account, worker, run string) (*JobReview, error) {
	var record JobReviewRecord
	found, err := ws.store.GetJSON(jobReviewKey(account, worker, run), &record)
	if err != nil || !found {
		return nil, err
	}
	return &record.Latest, nil
}

// CurrentValidJobReview is the landing-policy hook: exact-head evidence only,
// including BLOCK evidence, never an approval inferred from an older head.
func (ws *WorkerStore) CurrentValidJobReview(account, worker, run, head string) (*JobReview, error) {
	var record JobReviewRecord
	found, err := ws.store.GetJSON(jobReviewKey(account, worker, run), &record)
	if err != nil || !found {
		return nil, err
	}
	review, ok := record.ByHead[head]
	if !ok {
		return nil, nil
	}
	return CurrentJobReview(&review, head), nil
}

// SetJobReviewObserver follows the worker terminal-signal pattern. Installed
// during daemon composition; notifications occur after the durable write.
func (s *Store) SetJobReviewObserver(fn func(account, worker, run string, review JobReview)) {
	s.jobReviewObserver = fn
}
