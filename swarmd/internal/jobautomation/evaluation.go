package jobautomation

import (
	"strings"
)

// keyEvaluationPrefix holds one JobEvaluation per job: its latest job end.
const keyEvaluationPrefix = "job_automation/evaluation/"

// maxEvaluationDeliveries bounds the delivery ids one evaluation keeps.
const maxEvaluationDeliveries = 50

// JobEvaluation is what Swarm knows about the workflows for a job's latest
// end, so a task's progress tells "no workflow applies" (evaluated, no
// deliveries) from "not evaluated yet" (EvaluatedAt 0). One record per job;
// a later end replaces it. Only task job ends (task/<project>/<task>) keep
// one.
type JobEvaluation struct {
	JobID     string `json:"job_id"`
	ProjectID string `json:"project_id,omitempty"`
	Followup  string `json:"followup,omitempty"`
	// Seq is the job end's signal; EndedAt its time (Unix ms).
	Seq     uint64 `json:"seq"`
	EndedAt int64  `json:"ended_at"`
	// EvaluatedAt is when the runner evaluated the job end; 0 while it
	// has not.
	EvaluatedAt int64 `json:"evaluated_at,omitempty"`
	// Deliveries are the deliveries the job end matched (created, or the
	// existing one covering its head); empty when no workflow applies.
	Deliveries []string `json:"deliveries"`
}

// Evaluated reports whether the runner evaluated the job end.
func (e JobEvaluation) Evaluated() bool { return e.EvaluatedAt > 0 }

func evaluationKey(account, jobID string) string {
	return keyEvaluationPrefix + hexKey(account) + "/" + hexKey(jobID)
}

// IsTaskJobID reports a task job id, task/<project>/<task>.
func IsTaskJobID(jobID string) bool {
	parts := strings.Split(jobID, "/")
	return len(parts) == 3 && parts[0] == KindTask && parts[1] != "" && parts[2] != ""
}

// SetNotify installs fn, called after every committed change to a delivery
// (created, a step started or finished, finished, re-run) and to a job
// evaluation, with the job's project ("" when the job has none). It runs
// in the writer's goroutine after the write.
func (s *Store) SetNotify(fn func(account, projectID, jobID string)) {
	s.notifyMu.Lock()
	s.notify = fn
	s.notifyMu.Unlock()
}

func (s *Store) notifyChange(account, projectID, jobID string) {
	s.notifyMu.RLock()
	fn := s.notify
	s.notifyMu.RUnlock()
	if fn != nil {
		fn(account, projectID, jobID)
	}
}

// JobEvaluation reads a job's evaluation record.
func (s *Store) JobEvaluation(account, jobID string) (JobEvaluation, bool, error) {
	var e JobEvaluation
	found, err := s.db.GetJSON(evaluationKey(account, jobID), &e)
	return e, found, err
}

// MarkJobEnded records that a task job ended (its signal seq) and is not
// evaluated yet. A record of this end or a later one is kept as it is, so a
// replayed signal never turns an evaluated end back to pending.
func (s *Store) MarkJobEnded(account, projectID, jobID, followup string, seq uint64, endedAt int64) error {
	if !IsTaskJobID(jobID) {
		return nil
	}
	return s.putEvaluation(account, JobEvaluation{JobID: jobID, ProjectID: projectID, Followup: followup, Seq: seq, EndedAt: endedAt, Deliveries: []string{}}, false)
}

// RecordEvaluation records that the runner evaluated a task job end and the
// deliveries it matched (none: empty). An evaluation of an older end than
// the one recorded is not kept.
func (s *Store) RecordEvaluation(account, projectID, jobID, followup string, seq uint64, endedAt int64, deliveries []string) error {
	if !IsTaskJobID(jobID) {
		return nil
	}
	if len(deliveries) > maxEvaluationDeliveries {
		deliveries = deliveries[:maxEvaluationDeliveries]
	}
	e := JobEvaluation{JobID: jobID, ProjectID: projectID, Followup: followup, Seq: seq, EndedAt: endedAt, EvaluatedAt: s.now().UnixMilli(), Deliveries: append([]string{}, deliveries...)}
	return s.putEvaluation(account, e, true)
}

func (s *Store) putEvaluation(account string, e JobEvaluation, evaluated bool) error {
	s.mu.Lock()
	var existing JobEvaluation
	found, err := s.db.GetJSON(evaluationKey(account, e.JobID), &existing)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if found && (existing.Seq > e.Seq || (existing.Seq == e.Seq && !evaluated)) {
		s.mu.Unlock()
		return nil
	}
	if e.ProjectID == "" {
		e.ProjectID = existing.ProjectID
	}
	err = s.db.PutJSON(evaluationKey(account, e.JobID), e)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.notifyChange(account, e.ProjectID, e.JobID)
	return nil
}
