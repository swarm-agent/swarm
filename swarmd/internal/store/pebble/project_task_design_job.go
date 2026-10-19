package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/design"
)

// Design job states. A design job is a project task whose execution is the
// daemon's design pipeline (no agent session, no worktree, nothing to merge).
const (
	DesignJobRunning   = "running"
	DesignJobCompleted = "completed"
	DesignJobFailed    = "failed"
	DesignJobCancelled = "cancelled"
)

// Bounds of the durable per-item summary. The full per-design detail is the
// results.json the job writes next to the designs; the task record keeps a
// bounded copy so get_job works without reading the workspace.
const (
	designJobMaxPathBytes    = 1024
	designJobMaxBatchName    = 200
	designJobMaxErrorRunes   = 1000
	designJobMaxItemDiags    = 3
	designJobMaxDiagRunes    = 200
	designJobMaxItemErrRunes = 400
)

// designJobActivePrefix indexes running design jobs so daemon start can mark
// the ones a restart interrupted without scanning every task.
const designJobActivePrefix = "project_task_design_active/"

// ProjectTaskDesignDiagnostic is one bounded check finding.
type ProjectTaskDesignDiagnostic struct {
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
}

// ProjectTaskDesignItem is one design's durable outcome.
type ProjectTaskDesignItem struct {
	N             int                           `json:"n"`
	Path          string                        `json:"path,omitempty"`
	SHA256        string                        `json:"sha256,omitempty"`
	Status        string                        `json:"status"`
	Error         string                        `json:"error,omitempty"`
	Variation     string                        `json:"variation,omitempty"`
	CheckBefore   string                        `json:"check,omitempty"`
	Before        []ProjectTaskDesignDiagnostic `json:"diagnostics,omitempty"`
	RepairEdits   int                           `json:"repair_edits,omitempty"`
	RepairRefused string                        `json:"repair_refused,omitempty"`
	CheckAfter    string                        `json:"check_after_repair,omitempty"`
	After         []ProjectTaskDesignDiagnostic `json:"diagnostics_after_repair,omitempty"`
	Calls         int                           `json:"calls"`
	Tokens        int64                         `json:"tokens"`
	CostUSD       *float64                      `json:"cost_usd,omitempty"`
	UsageComplete bool                          `json:"usage_complete"` // every call reported usage and a price
	DurationMS    int64                         `json:"duration_ms"`
}

// ProjectTaskDesignJob is the request and durable outcome of a design job.
type ProjectTaskDesignJob struct {
	Mode            string                  `json:"mode"` // create | edit
	Brief           string                  `json:"brief"`
	Count           int                     `json:"count"`
	Style           string                  `json:"style,omitempty"`
	Variations      []string                `json:"variations,omitempty"`
	BatchName       string                  `json:"batch_name,omitempty"`
	Folder          string                  `json:"folder,omitempty"` // the job's own output folder (create)
	ResultsPath     string                  `json:"results_path,omitempty"`
	EditPath        string                  `json:"edit_path,omitempty"`
	State           string                  `json:"state"`
	Error           string                  `json:"error,omitempty"`
	Provider        string                  `json:"provider,omitempty"`
	Model           string                  `json:"model,omitempty"`
	Thinking        string                  `json:"thinking,omitempty"`
	Items           []ProjectTaskDesignItem `json:"items,omitempty"`
	Counts          map[string]int          `json:"counts,omitempty"`
	Calls           int                     `json:"calls"`
	Tokens          int64                   `json:"tokens"`
	CostUSD         float64                 `json:"cost_usd"`
	CostComplete    bool                    `json:"cost_complete"`
	EditBaseSHA256  string                  `json:"edit_base_sha256,omitempty"`
	EditFinalSHA256 string                  `json:"edit_final_sha256,omitempty"`
	ResultsError    string                  `json:"results_error,omitempty"`
	StartedAt       int64                   `json:"started_at"`
	FinishedAt      int64                   `json:"finished_at,omitempty"`
}

// Request is the pipeline request this job runs.
func (j *ProjectTaskDesignJob) Request() design.Request {
	return design.Request{Brief: j.Brief, Count: j.Count, Style: j.Style, Variations: append([]string(nil), j.Variations...), Folder: j.Folder, EditPath: j.EditPath}
}

// Finished counts the designs with an outcome.
func (j *ProjectTaskDesignJob) Finished() int {
	n := 0
	for _, item := range j.Items {
		if item.Status != "" {
			n++
		}
	}
	return n
}

// ValidDesignRelativePath reports whether p is a clean workspace-relative
// slash path with no parent, absolute, hidden-Git or empty component.
func ValidDesignRelativePath(p string) bool {
	if p == "" || len(p) > designJobMaxPathBytes || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "." {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

func boundedDesignText(s string, max int) bool { return utf8.RuneCountInString(s) <= max }

// Validate checks the request bounds, relative paths and outcome bounds.
func (j *ProjectTaskDesignJob) Validate() error {
	if j == nil {
		return nil
	}
	if err := design.Validate(j.Request()); err != nil {
		return fmt.Errorf("design job: %w", err)
	}
	switch j.Mode {
	case string(design.ModeCreate):
		if j.EditPath != "" || !ValidDesignRelativePath(j.Folder) || j.ResultsPath != path.Join(j.Folder, design.ResultsFile) {
			return errors.New("design job: create needs a workspace-relative folder and its results path")
		}
	case string(design.ModeEdit):
		if j.Folder != "" || j.ResultsPath != "" || !ValidDesignRelativePath(j.EditPath) || j.Count != 1 {
			return errors.New("design job: edit needs one workspace-relative edit_path")
		}
	default:
		return fmt.Errorf("design job: unknown mode %q", j.Mode)
	}
	switch j.State {
	case DesignJobRunning, DesignJobCompleted, DesignJobFailed, DesignJobCancelled:
	default:
		return fmt.Errorf("design job: unknown state %q", j.State)
	}
	if !boundedDesignText(j.BatchName, designJobMaxBatchName) || !boundedDesignText(j.Error, designJobMaxErrorRunes) || !boundedDesignText(j.ResultsError, designJobMaxErrorRunes) {
		return errors.New("design job: text field exceeds its bound")
	}
	for _, field := range []string{j.Provider, j.Model, j.Thinking} {
		if len(field) > 256 {
			return errors.New("design job: model field exceeds 256 bytes")
		}
	}
	if len(j.Items) > j.Count {
		return errors.New("design job: more items than designs requested")
	}
	for _, item := range j.Items {
		if item.N < 1 || item.N > j.Count || (item.Path != "" && !ValidDesignRelativePath(item.Path)) || len(item.SHA256) > 64 ||
			!boundedDesignText(item.Error, designJobMaxItemErrRunes) || !boundedDesignText(item.RepairRefused, designJobMaxItemErrRunes) ||
			!boundedDesignText(item.Variation, design.MaxVariationRunes) || len(item.Before) > designJobMaxItemDiags || len(item.After) > designJobMaxItemDiags {
			return errors.New("design job: item outside its bounds")
		}
		for _, d := range append(append([]ProjectTaskDesignDiagnostic(nil), item.Before...), item.After...) {
			if len(d.Kind) > 64 || !boundedDesignText(d.Message, designJobMaxDiagRunes) {
				return errors.New("design job: diagnostic outside its bounds")
			}
		}
	}
	if j.StartedAt <= 0 || (j.FinishedAt != 0 && j.FinishedAt < j.StartedAt) {
		return errors.New("design job: invalid timestamps")
	}
	if (j.State == DesignJobRunning) != (j.FinishedAt == 0) {
		return errors.New("design job: only a running job has no finish time")
	}
	return nil
}

type designJobActiveRef struct {
	AccountScopeID string `json:"account_id"`
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
}

func designJobActiveKey(account, project, task string) string {
	return designJobActivePrefix + keyPart(account) + "/" + keyPart(project) + "/" + keyPart(task)
}

func designJobRunning(t ProjectTaskRecord) bool {
	return t.Design != nil && t.Design.State == DesignJobRunning
}

// setDesignJobActiveIndex keeps the running-job index in the same batch as
// the task write that changes the job's state.
func setDesignJobActiveIndex(batch *pebble.Batch, account string, prior, next ProjectTaskRecord) error {
	if designJobRunning(prior) && !designJobRunning(next) {
		if err := batch.Delete([]byte(designJobActiveKey(account, prior.ProjectID, prior.ID)), nil); err != nil {
			return err
		}
	}
	if designJobRunning(next) {
		raw, err := json.Marshal(designJobActiveRef{account, next.ProjectID, next.ID})
		if err != nil {
			return err
		}
		return batch.Set([]byte(designJobActiveKey(account, next.ProjectID, next.ID)), raw, nil)
	}
	return nil
}

// InterruptedDesignJobMessage is the error a restart leaves on a running job.
func InterruptedDesignJobMessage(finished, total int) string {
	return fmt.Sprintf("interrupted by daemon restart; %d/%d finished", finished, total)
}

// ReconcileInterruptedDesignJobs marks every design job still running at
// daemon start as failed. Nothing is resumed: the files already written stay,
// the job record says how many designs finished. It returns how many jobs it
// marked.
func (s *SessionStore) ReconcileInterruptedDesignJobs(nowUnixMs int64) (int, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return 0, errors.New("database not available")
	}
	var refs []designJobActiveRef
	err := s.store.IteratePrefix(designJobActivePrefix, 100000, func(_ string, value []byte) error {
		var ref designJobActiveRef
		if err := json.Unmarshal(value, &ref); err != nil {
			return fmt.Errorf("design job index: %w", err)
		}
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, ref := range refs {
		staleKey := []byte(designJobActiveKey(ref.AccountScopeID, ref.ProjectID, ref.TaskID))
		current, found, err := s.GetProjectTask(ref.AccountScopeID, ref.ProjectID, ref.TaskID)
		if err != nil {
			return marked, fmt.Errorf("reconcile design job %s/%s: %w", ref.ProjectID, ref.TaskID, err)
		}
		if !found || !designJobRunning(*current) {
			if err := s.store.db.Delete(staleKey, pebble.Sync); err != nil {
				return marked, err
			}
			continue
		}
		_, err = s.UpdateProjectTask(ref.AccountScopeID, ref.ProjectID, ref.TaskID, func(t *ProjectTaskRecord) error {
			if !designJobRunning(*t) {
				return errors.New("design job settled during reconciliation")
			}
			message := InterruptedDesignJobMessage(t.Design.Finished(), t.Design.Count)
			t.Design.State, t.Design.Error, t.Design.FinishedAt = DesignJobFailed, message, max(nowUnixMs, t.Design.StartedAt)
			t.Status, t.LastError = "failed", message
			t.ActionNeeded = "The design job was interrupted by a daemon restart; start a new one for the designs that did not finish."
			return nil
		})
		if err != nil {
			return marked, fmt.Errorf("reconcile design job %s/%s: %w", ref.ProjectID, ref.TaskID, err)
		}
		marked++
	}
	return marked, nil
}
