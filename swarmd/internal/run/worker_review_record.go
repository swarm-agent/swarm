package run

import (
	"errors"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func reviewFromResult(r store.WorkerRunRecord, report, head string, at int64) store.JobReview {
	model := ""
	if r.ModelProfile != nil {
		model = r.ModelProfile.Action.Provider + "/" + r.ModelProfile.Action.Model
	}
	review := store.ParseJobReview(report, head, "worker/"+r.WorkerID+"/"+r.ID, model, at)
	if r.Status != "succeeded" {
		review.Verdict = "block"
		review.Reason = "reviewer run " + r.Status
		if r.Error != "" {
			review.Reason += ": " + r.Error
		}
	}
	return review
}

func (s *WorkerExecutionService) recordReviewResult(r store.WorkerRunRecord) error {
	if r.Role != store.WorkerRoleReviewer {
		return nil
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	} // stopped before admission captured a target
	if snapshot.AccountScopeID != r.AccountScopeID || snapshot.UserID != r.UserID || mapString(snapshot.Metadata, "worker_execution_run_id") != r.ID {
		return store.ErrWorkerConflict
	}
	env := reviewEnvironment(snapshot.Metadata)
	parts := strings.Split(env["SWARM_REVIEW_JOB"], "/")
	if len(parts) != 3 || parts[0] != "worker" || env["SWARM_REVIEW_HEAD"] == "" {
		return errors.New("review result missing captured target")
	}
	report := ""
	var after uint64
	// Bounded transcript traversal; if the bound is exceeded fail closed rather
	// than approving an earlier message as the final verdict.
	for page := 0; page < 100; page++ {
		messages, err := s.host.runs.sessions.ListMessages(r.SessionID, after, 100)
		if err != nil {
			return err
		}
		for _, m := range messages {
			after = m.GlobalSeq
			if m.Role == "assistant" {
				report = m.Content
			}
		}
		if len(messages) < 100 {
			break
		}
		if page == 99 {
			report = ""
		}
	}
	at := r.CompletedAt
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	review := reviewFromResult(r, report, env["SWARM_REVIEW_HEAD"], at)
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	return ws.RecordJobReview(r.AccountScopeID, parts[1], parts[2], review)
}
