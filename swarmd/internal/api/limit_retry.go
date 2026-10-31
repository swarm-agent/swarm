package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// LimitRetryPath is the one route that makes work waiting on a provider's
// usage limit retry now: POST /v3/provider-limit-waits/retry with
// {"job_id"?: "worker/<worker>/<run>" | "task/<project>/<task>",
// "provider"?: "<provider id>"}. Without job_id every limit-waiting worker
// run, project task and workflow step of the caller's account retries.
// manage_jobs retry_now runs it (also on a linked machine).
const LimitRetryPath = "/v3/provider-limit-waits/retry"

// LimitRetryRequest names what retries now; see LimitRetryPath.
type LimitRetryRequest struct {
	JobID    string `json:"job_id,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// LimitRetryTask is one project task a retry-now continued.
type LimitRetryTask struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// LimitRetryResult says what a retry-now reached. Each entry went through its
// own rerun path; one that hits the limit again waits again.
type LimitRetryResult struct {
	WorkerRuns    []runruntime.LimitRetry    `json:"worker_runs"`
	Tasks         []LimitRetryTask           `json:"tasks"`
	WorkflowSteps []jobautomation.LimitRetry `json:"workflow_steps"`
	Retried       int                        `json:"retried"`
	Note          string                     `json:"note,omitempty"`
}

// RetryLimitWaitsNow is the one service function behind LimitRetryPath and
// manage_jobs retry_now: work waiting for a provider's limit to reset retries
// now instead of at the reset the old provider stated, for when the person
// fixed the provider (signed in to another account, added a key, picked
// another model). It moves the wait's retry time to now and hands each one to
// the path that already runs it at the reset: rerunAfterProviderLimit for a
// worker run, continueProjectTaskAfterLimit for a project task, and the
// workflow runner's due-step path for an agent step or for-each item. Budget
// admission, the bound on waits and every rerun's idempotency stay theirs.
// Worker runs go first, so a workflow step waiting on the same job finds it
// running. Nothing waiting is not an error: a second call is a no-op.
func (s *Server) RetryLimitWaitsNow(ctx context.Context, p identity.Principal, req LimitRetryRequest) (LimitRetryResult, error) {
	out := LimitRetryResult{WorkerRuns: []runruntime.LimitRetry{}, Tasks: []LimitRetryTask{}, WorkflowSteps: []jobautomation.LimitRetry{}}
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || s.sessions == nil || s.sessions.Store() == nil {
		return out, identity.ErrPrincipalRequired
	}
	account := p.AccountScopeID
	jobID, provider := strings.TrimSpace(req.JobID), strings.TrimSpace(req.Provider)
	if len(provider) > 100 {
		return out, controlMCPToolFailure("provider is at most 100 characters")
	}
	kind, a, b := "", "", ""
	if jobID != "" {
		parts := strings.Split(jobID, "/")
		if len(parts) != 3 || (parts[0] != controlMCPJobWorker && parts[0] != jobautomation.KindTask) {
			return out, controlMCPToolFailure("job_id must be a worker job (worker/<worker>/<run>) or a task (task/<project>/<task>)")
		}
		for _, part := range parts[1:] {
			if !controlMCPSafeID.MatchString(part) || part == "." || part == ".." {
				return out, controlMCPToolFailure("job_id %q is not a valid job id", jobID)
			}
		}
		kind, a, b = parts[0], parts[1], parts[2]
	}
	var failures []error
	// A named task that a worker runs waits as its worker run.
	workerID, runID := "", ""
	var task *pebblestore.ProjectTaskRecord
	switch kind {
	case controlMCPJobWorker:
		workerID, runID = a, b
	case jobautomation.KindTask:
		found := false
		var err error
		task, found, err = s.sessions.Store().GetProjectTask(account, a, b)
		if err != nil {
			return out, err
		}
		if !found {
			return out, limitRetryJobNotFound(jobID)
		}
		if task.WorkerID != "" && task.WorkerRunID != "" {
			workerID, runID = task.WorkerID, task.WorkerRunID
		}
	}
	if kind == "" || workerID != "" {
		execution, err := s.workerExecutionService()
		switch {
		case err == nil:
			retried, err := execution.RetryLimitWaitsNow(ctx, account, runruntime.LimitRetryFilter{WorkerID: workerID, RunID: runID, Provider: provider})
			out.WorkerRuns = append(out.WorkerRuns, retried...)
			if errors.Is(err, pebblestore.ErrWorkerNotFound) && kind == controlMCPJobWorker {
				return out, limitRetryJobNotFound(jobID)
			}
			if err != nil {
				failures = append(failures, err)
			}
		case kind != "":
			return out, err
		}
	}
	if kind == "" || (task != nil && workerID == "") {
		var refs []pebblestore.ProjectTaskLimitWaitRef
		if task != nil {
			refs = []pebblestore.ProjectTaskLimitWaitRef{{AccountScopeID: account, ProjectID: a, TaskID: b}}
		} else {
			all, err := s.sessions.Store().ListProjectTaskLimitWaits()
			if err != nil {
				return out, err
			}
			for _, ref := range all {
				if ref.AccountScopeID == account {
					refs = append(refs, ref)
				}
			}
		}
		for _, ref := range refs {
			status, retried, err := s.retryProjectTaskLimitNow(account, ref.ProjectID, ref.TaskID, provider)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if retried {
				out.Tasks = append(out.Tasks, LimitRetryTask{JobID: taskRunJobID(ref.ProjectID, ref.TaskID), Status: status})
			}
		}
	}
	if s.jobAutomations != nil {
		providers := map[string]string{}
		match := func(job string) bool {
			if jobID != "" && job != jobID {
				return false
			}
			if provider == "" {
				return true
			}
			if _, ok := providers[job]; !ok {
				providers[job] = s.jobProvider(account, job)
			}
			return strings.EqualFold(providers[job], provider)
		}
		steps, err := s.jobAutomations.RetryLimitWaitsNow(account, match)
		out.WorkflowSteps = append(out.WorkflowSteps, steps...)
		if err != nil {
			failures = append(failures, err)
		}
	}
	out.Retried = len(out.WorkerRuns) + len(out.Tasks) + len(out.WorkflowSteps)
	if out.Retried == 0 {
		switch {
		case jobID != "":
			out.Note = "nothing of " + jobID + " waits on a provider's limit"
		case provider != "":
			out.Note = "nothing waits on provider " + provider + "'s limit"
		default:
			out.Note = "nothing waits on a provider's limit"
		}
	}
	return out, errors.Join(failures...)
}

// retryProjectTaskLimitNow moves a limit-waiting task's retry to now and
// continues it through continueProjectTaskAfterLimit. retried is false when
// the task does not wait on a limit (or not on provider's).
func (s *Server) retryProjectTaskLimitNow(account, projectID, taskID, provider string) (string, bool, error) {
	db := s.sessions.Store()
	task, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil || !found || task.Status != pebblestore.ProjectTaskStatusLimitWaiting || !pebblestore.ProjectTaskLimitPending(*task) {
		return "", false, err
	}
	if provider != "" && !strings.EqualFold(s.sessionProvider(task.SessionID), provider) {
		return "", false, nil
	}
	now := s.taskLimitNow().UnixMilli()
	if task.LimitRetryAt > now {
		if _, err := db.UpdateProjectTask(account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Status == pebblestore.ProjectTaskStatusLimitWaiting && t.LimitRetryAt > now {
				t.LimitRetryAt = now
				t.ActionNeeded = "The provider was fixed; the task continues now."
			}
			return nil
		}); err != nil {
			return "", false, err
		}
	}
	if err := s.continueProjectTaskAfterLimit(account, projectID, taskID); err != nil {
		return "", false, err
	}
	current, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil || !found {
		return "", true, err
	}
	return current.Status, true, nil
}

// jobProvider is the provider the job's session uses ("" when unknown).
func (s *Server) jobProvider(account, jobID string) string {
	parts := strings.Split(jobID, "/")
	if len(parts) != 3 {
		return ""
	}
	switch parts[0] {
	case controlMCPJobWorker:
		run, found, err := s.sessions.Store().WorkerStore().GetWorkerRun(account, parts[1], parts[2])
		if err != nil || !found {
			return ""
		}
		return s.sessionProvider(run.SessionID)
	case jobautomation.KindTask:
		task, found, err := s.sessions.Store().GetProjectTask(account, parts[1], parts[2])
		if err != nil || !found {
			return ""
		}
		return s.sessionProvider(task.SessionID)
	}
	return ""
}

func (s *Server) sessionProvider(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return ""
	}
	return strings.TrimSpace(session.Preference.Provider)
}

// handleLimitRetry serves POST LimitRetryPath.
func (s *Server) handleLimitRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, "sessions:write") {
		return
	}
	var req LimitRetryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	out, err := s.RetryLimitWaitsNow(r.Context(), principal, req)
	if err != nil {
		// A refused request (a bad or unknown job_id) is the caller's; any
		// other failure is this machine's.
		status := http.StatusInternalServerError
		var refused controlMCPToolError
		if errors.As(err, &refused) {
			status = http.StatusBadRequest
			if refused.status == http.StatusNotFound {
				status = http.StatusNotFound
			}
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// controlMCPRetryNow is manage_jobs retry_now: the route above.
func controlMCPRetryNow(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	body := map[string]any{}
	if job := controlMCPString(args, "job_id"); job != "" {
		body["job_id"] = job
	}
	if provider := controlMCPString(args, "provider"); provider != "" {
		body["provider"] = provider
	}
	out, err := c.dispatch(http.MethodPost, LimitRetryPath, nil, body)
	if job, _ := body["job_id"].(string); err != nil && job != "" {
		return nil, controlMCPNotFound(err, "job", job, "manage_workers list with include_jobs")
	}
	return out, err
}

// limitRetryJobNotFound is a named job this account does not have (HTTP 404).
func limitRetryJobNotFound(jobID string) error {
	return controlMCPToolError{message: fmt.Sprintf("no job %s on this machine", jobID), status: http.StatusNotFound}
}
