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
	TriggerID     string        `json:"trigger_id,omitempty"`
	ScheduleID    string        `json:"schedule_id,omitempty"`
	Git           *ResultGit    `json:"git,omitempty"`
	Files         []ResultFile  `json:"files,omitempty"`
	Cost          *ResultCost   `json:"cost,omitempty"`
	// NoCommits means the lane was positively assessed as having no commits
	// since its captured base. Missing Git alone means unknown, not empty.
	NoCommits bool `json:"no_commits,omitempty"`
	// Review is a reviewer job's recorded verdict and findings.
	Review map[string]any `json:"review,omitempty"`
	// Deliverables is everything the job produced that a workflow can hand
	// over: its branch, the files it committed or wrote, its media and its
	// report (see Deliverable).
	Deliverables []Deliverable `json:"deliverables,omitempty"`
}

// Deliverable kinds.
const (
	DeliverableBranch = "branch"
	DeliverableFile   = "file"
	DeliverableMedia  = "media"
	DeliverableReport = "report"
)

// Where a deliverable's content is read from.
const (
	// SourceGit: the file at Head in the folder's repository (a job's
	// commits are in it from the moment they are made).
	SourceGit = "git"
	// SourceFolder: the file at Path in the folder itself, as long as it
	// still has SHA256.
	SourceFolder = "folder"
	// SourceMedia: a media job's stored file, named by Path.
	SourceMedia = "media"
	// SourceReport: the job's report (Result.Report).
	SourceReport = "report"
)

// Deliverable is one thing a job (or a workflow step) produced that a
// deliver step can hand over: a branch (Branch at Head), a file or media file
// (Path, with its SHA256, Size and Type, read from Source), or the report.
type Deliverable struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Type   string `json:"type,omitempty"`
	Source string `json:"source,omitempty"`
	// From is "job" for the job's own output, or the id of the workflow step
	// that produced it.
	From string `json:"from,omitempty"`
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

// EndedBlocked reports a job end whose own run stopped on a declared blocker:
// the run's status says it did not succeed and its error is the blocker.
// Error text alone is not enough: a successful run must never be read as
// blocked because an earlier run's blocker is still on the job.
func (r Result) EndedBlocked() bool {
	return r.Status == StatusFailed && strings.HasPrefix(r.Error, "blocked: ")
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
