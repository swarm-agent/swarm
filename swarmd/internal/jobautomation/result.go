// Package jobautomation runs the steps a person (or their AI) configured for
// when a job ends. Swarm records one result per job end and runs only the
// user's own steps: with no automation nothing happens and the person
// integrates the job by hand. See docs/automations.md.
package jobautomation

import (
	"errors"
	"strings"
)

// ResultVersion is the version of the result JSON a step receives.
const ResultVersion = 1

// Job kinds.
const (
	KindWorker = "worker"
	KindMedia  = "media"
	KindDesign = "design"
	// KindTask is a task one of Swarm's own agents (swarm, coder, finder,
	// designer) finished: swarm_run_task's work, or a follow-up in its session.
	KindTask = "task"
)

// Normalized result statuses.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Result is one job end: what the job was given, what it reported, its
// commits or files, and its cost. A worker job ends once per follow-up; each
// end is its own result (Followup).
type Result struct {
	V             int           `json:"v"`
	JobID         string        `json:"job_id"`
	Kind          string        `json:"kind"`
	Role          string        `json:"role,omitempty"`
	Worker        *ResultWorker `json:"worker,omitempty"`
	RunID         string        `json:"run_id"`
	Followup      string        `json:"followup,omitempty"`
	ProjectID     string        `json:"project_id,omitempty"`
	Folder        string        `json:"folder,omitempty"`
	Status        string        `json:"status"`
	JobStatus     string        `json:"job_status,omitempty"`
	FinishedAt    int64         `json:"finished_at"`
	Brief         string        `json:"brief,omitempty"`
	Report        string        `json:"report,omitempty"`
	Error         string        `json:"error,omitempty"`
	RequestSource string        `json:"request_source,omitempty"`
	ScheduleID    string        `json:"schedule_id,omitempty"`
	Git           *ResultGit    `json:"git,omitempty"`
	Files         []ResultFile  `json:"files,omitempty"`
	Cost          *ResultCost   `json:"cost,omitempty"`
	// Review is a reviewer job's recorded verdict and findings.
	Review map[string]any `json:"review,omitempty"`
}

type ResultWorker struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ResultGit is a job's commits: present only when the job committed.
type ResultGit struct {
	Branch     string         `json:"branch"`
	BaseBranch string         `json:"base_branch,omitempty"`
	BaseCommit string         `json:"base_commit"`
	Head       string         `json:"head"`
	Commits    []ResultCommit `json:"commits"`
	Files      []string       `json:"files"`
	// Worktree is the job's own checkout, where its head can be fetched from.
	Worktree string `json:"worktree,omitempty"`
}

type ResultCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// ResultFile is a file a media or design job produced (or a media file a
// worker's commits added).
type ResultFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Type   string `json:"type"`
	Size   int64  `json:"size,omitempty"`
}

type ResultCost struct {
	Tokens       int64    `json:"tokens"`
	EstimatedUSD *float64 `json:"estimated_usd,omitempty"`
	Model        string   `json:"model,omitempty"`
}

// ErrNotReady is a job end whose result has not settled yet (its commits are
// still being assessed): the consumer retries it with backoff.
var ErrNotReady = errors.New("the job's result is not settled yet")

// ResultKey keys a result by job and follow-up.
func ResultKey(jobID, followup string) string {
	if strings.TrimSpace(followup) == "" {
		followup = "0"
	}
	return jobID + "#" + followup
}

// NormalizeStatus maps a job's own end state to succeeded, failed or
// cancelled.
func NormalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "completed", "needs_review", "done":
		return StatusSucceeded
	case "cancelled", "canceled", "stopped":
		return StatusCancelled
	default:
		return StatusFailed
	}
}
