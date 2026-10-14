package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/outbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The outbox (docs/checkpoints/outbox.md) exports the machine's signal feed
// and its jobs' results to storage the owner chose. This file is its daemon
// side: what a finished job's result is (the same view swarm_get_job gives an
// AI), GET /v3/outbox, and the owner account it exports.

// SetOutboxStatus installs the outbox's progress for GET /v3/outbox and the
// box summary; nil means the machine has no outbox.
func (s *Server) SetOutboxStatus(status func() outbox.Status) {
	s.outboxStatus = status
}

func (s *Server) outboxState() (outbox.Status, bool) {
	if s.outboxStatus == nil {
		return outbox.Status{}, false
	}
	return s.outboxStatus(), true
}

func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if principal, ok := PrincipalFromRequest(r); !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, signalsReadScope) {
		return
	}
	status, _ := s.outboxState()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "outbox": status})
}

// OutboxAccount is the account whose feed and jobs the outbox exports: the
// machine owner's. Empty until the owner exists (only machine-wide signals
// are exported then).
func (s *Server) OutboxAccount() string {
	if s.identitySessions == nil {
		return ""
	}
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil || !isCompleteProductActor(actor) {
		return ""
	}
	return strings.TrimSpace(actor.AccountScopeID)
}

// outboxCall is a read-only Swarm Control call as the machine owner, so the
// outbox reads a job exactly as swarm_get_job does, through the same routes
// and checks.
func (s *Server) outboxCall(ctx context.Context) (*controlMCPCall, error) {
	if s.identitySessions == nil {
		return nil, errors.New("identity unavailable")
	}
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil || !isCompleteProductActor(actor) {
		return nil, errors.New("the machine owner's account is not set up")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/v3/outbox", nil)
	if err != nil {
		return nil, err
	}
	req.Host, req.RemoteAddr = "127.0.0.1", "127.0.0.1:0"
	req = requestWithActorContext(req, actor)
	req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{
		ID: "outbox", Name: "Swarm outbox (internal, read only)",
		Scopes:         []string{"swarm:read", "sessions:read", "automations:read", "agents:read", "signals:read"},
		AccountScopeID: actor.AccountScopeID, UserID: actor.UserID,
	})
	return &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}, nil
}

// outboxJobSignals are the signals that end a worker job.
var outboxJobSignals = map[string]bool{"worker.run.succeeded": true, "worker.run.failed": true, "worker.run.cancelled": true}

// outboxMediaSignals are the signals that end a media job.
var outboxMediaSignals = map[string]bool{"media.job.succeeded": true, "media.job.failed": true}

// OutboxResultBuilder builds a finished worker job's result: the job as
// swarm_get_job reports it, a Git bundle of its commits, and the media files
// its commits added or changed (read from its head commit).
func (s *Server) OutboxResultBuilder() outbox.ResultBuilder {
	return func(ctx context.Context, sig pebblestore.Signal, b *outbox.Builder) (*outbox.Export, error) {
		if outboxMediaSignals[sig.Kind] {
			return s.outboxMediaResult(sig, b)
		}
		if !outboxJobSignals[sig.Kind] {
			return nil, nil
		}
		workerID, runID := sig.Refs["worker_id"], sig.Refs["run_id"]
		if !controlMCPSafeID.MatchString(workerID) || !controlMCPSafeID.MatchString(runID) {
			return nil, nil
		}
		c, err := s.outboxCall(ctx)
		if err != nil {
			return nil, err
		}
		view, err := c.jobView(controlMCPJobWorker, workerID, runID, 0, false, 0)
		if err != nil {
			var toolErr controlMCPToolError
			if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
				return nil, nil // the job was deleted: the record still says it ended
			}
			return nil, err
		}
		workerName := ""
		if response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID, nil, nil); err == nil {
			workerName, _ = controlMCPMap(response["worker"])["name"].(string)
		}
		return outboxResultFromView(ctx, b, workerID, workerName, runID, view)
	}
}

// outboxResultFromView turns a job's swarm_get_job view into its export.
func outboxResultFromView(ctx context.Context, b *outbox.Builder, workerID, workerName, runID string, view map[string]any) (*outbox.Export, error) {
	// Read the view as an AI receives it: the tool's values are Go types
	// ([]string, []map[string]any) until they are encoded.
	if encoded, err := json.Marshal(view); err != nil {
		return nil, err
	} else if err := json.Unmarshal(encoded, &view); err != nil {
		return nil, err
	}
	jobID := controlMCPJobID(controlMCPJobWorker, workerID, runID)
	manifest := map[string]any{"job_id": jobID, "run_id": runID, "status": view["status"]}
	worker := map[string]any{"id": workerID}
	if workerName != "" {
		worker["name"] = workerName
	}
	manifest["worker"] = worker
	for _, key := range []string{"project_id", "session_id", "error", "deliverables"} {
		if value, ok := view[key]; ok {
			manifest[key] = value
		}
	}
	result := controlMCPMap(view["result"])
	for _, key := range []string{"workspace_path", "report", "what_did_do", "what_not_done", "cost", "delivery_state", "integration"} {
		if value, ok := result[key]; ok {
			manifest[key] = value
		}
	}
	export := &outbox.Export{JobID: jobID, RunID: runID, Manifest: manifest}
	outcome := "report"
	repo, _ := result["workspace_path"].(string)
	branch, _ := result["worktree_branch"].(string)
	base, _ := result["base_commit"].(string)
	head, _ := result["head"].(string)
	commits := controlMCPList(result["commits"])
	if head == "" && repo != "" && controlMCPGitRevision.MatchString(branch) {
		if resolved, err := b.GitOutput(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}"); err == nil {
			head = strings.TrimSpace(resolved)
		}
	}
	// A job's run ends a moment before Swarm has assessed its commits: a
	// job with work whose commits are not readable yet is retried, and
	// exported as it is on the last attempt.
	state, _ := result["delivery_state"].(string)
	pending := state == "" || state == "unavailable" || (state == "candidate_work" && (head == "" || len(commits) == 0))
	if view["status"] == "needs_review" && pending && !b.LastAttempt {
		return nil, outbox.ErrNotReady
	}
	if text, _ := result["changes_error"].(string); text != "" {
		manifest["changes_error"] = text
	}
	if repo != "" && head != "" && len(commits) > 0 {
		outcome = "code"
		git := map[string]any{"branch": branch, "base_branch": result["base_branch"], "base_commit": base, "head": head}
		list := make([]map[string]string, 0, len(commits))
		for _, raw := range commits {
			line, _ := raw.(string)
			sha, subject, _ := strings.Cut(line, " ")
			list = append(list, map[string]string{"sha": sha, "subject": subject})
		}
		git["commits"] = list
		paths := []string{}
		skipped := []map[string]string{}
		for _, raw := range controlMCPList(result["files"]) {
			file := controlMCPMap(raw)
			name, _ := file["path"].(string)
			status, _ := file["status"].(string)
			if name == "" {
				continue
			}
			paths = append(paths, name)
			if outbox.MediaType(name) == "" || status == "deleted" || strings.HasPrefix(status, "D") || status == "untracked" {
				continue
			}
			blob, err := b.File(ctx, repo, head, name)
			if err != nil {
				skipped = append(skipped, map[string]string{"path": name, "reason": outboxReason(err)})
				continue
			}
			export.Blobs = append(export.Blobs, blob)
			outcome = "media"
			exportFiles, _ := manifest["files"].([]map[string]any)
			manifest["files"] = append(exportFiles, map[string]any{"path": name, "sha256": blob.SHA256, "size": blob.Size,
				"type": outbox.MediaType(name), "role": "primary", "key": b.BlobKey(blob.SHA256)})
		}
		git["files"] = paths
		if branch != "" && base != "" {
			if blob, err := b.Bundle(ctx, repo, branch, base); err == nil {
				export.Blobs = append(export.Blobs, blob)
				git["bundle"] = map[string]any{"sha256": blob.SHA256, "size": blob.Size, "key": b.BlobKey(blob.SHA256)}
			} else {
				git["bundle_error"] = outboxReason(err)
			}
		}
		if len(skipped) > 0 {
			manifest["skipped"] = skipped
		}
		manifest["git"] = git
	}
	manifest["outcome"] = outcome
	return export, nil
}

func outboxReason(err error) string {
	if errors.Is(err, outbox.ErrTooLarge) {
		return "larger than the outbox file limit"
	}
	return controlMCPTruncate(err.Error(), 200)
}

// outboxMediaResult builds a media job's result: its files, as stored on the
// job, and what was asked for. Its run id is the job's task id.
func (s *Server) outboxMediaResult(sig pebblestore.Signal, b *outbox.Builder) (*outbox.Export, error) {
	projectID, taskID := sig.Refs["project_id"], sig.Refs["task_id"]
	if !controlMCPSafeID.MatchString(projectID) || !controlMCPSafeID.MatchString(taskID) || s.sessions == nil || s.sessions.Store() == nil {
		return nil, nil
	}
	task, found, err := s.sessions.Store().GetProjectTask(sig.Account, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil {
		return nil, nil // the job was deleted: the record still says it ended
	}
	return outboxMediaExport(task, b), nil
}

// outboxMediaExport is a media job's export: its manifest and its files.
func outboxMediaExport(task *pebblestore.ProjectTaskRecord, b *outbox.Builder) *outbox.Export {
	projectID, taskID := task.ProjectID, task.ID
	jobID := controlMCPJobID(controlMCPJobMedia, projectID, taskID)
	manifest := map[string]any{"job_id": jobID, "run_id": taskID, "status": task.Status, "outcome": "media",
		"kind": task.Agent, "project_id": projectID, "title": task.Title, "brief": controlMCPTruncate(task.Description, 4000)}
	for key, value := range map[string]string{"model": task.Model, "aspect_ratio": task.AspectRatio, "resolution": task.Resolution, "error": task.LastError, "workspace_path": task.WorkspacePath} {
		if value != "" {
			manifest[key] = value
		}
	}
	if task.DurationSeconds > 0 {
		manifest["duration_seconds"] = task.DurationSeconds
	}
	export := &outbox.Export{JobID: jobID, RunID: taskID, Manifest: manifest}
	files, skipped := []map[string]any{}, []map[string]string{}
	for i, file := range mediaTaskFiles(task) {
		name := file.filename(task.Title, i+1)
		if len(file.data) == 0 {
			if file.record.Status == "failed" {
				skipped = append(skipped, map[string]string{"path": name, "reason": controlMCPTruncate(file.record.Description, 200)})
			}
			continue
		}
		blob, err := b.Bytes(file.data, file.mediaType)
		if err != nil {
			skipped = append(skipped, map[string]string{"path": name, "reason": outboxReason(err)})
			continue
		}
		export.Blobs = append(export.Blobs, blob)
		entry := map[string]any{"path": name, "sha256": blob.SHA256, "size": blob.Size, "type": file.mediaType, "role": "primary", "key": b.BlobKey(blob.SHA256)}
		for key, value := range map[string]string{"model": file.record.Model, "aspect_ratio": file.record.AspectRatio, "resolution": file.record.Resolution} {
			if value != "" {
				entry[key] = value
			}
		}
		if file.record.DurationSeconds > 0 {
			entry["duration_seconds"] = file.record.DurationSeconds
		}
		files = append(files, entry)
	}
	if len(files) > 0 {
		manifest["files"] = files
	}
	if len(skipped) > 0 {
		manifest["skipped"] = skipped
	}
	return export
}

// OutboxStatusBuilder builds the outbox's status snapshot: the box summary
// (GET /v3/box/summary, today in UTC) read as the machine owner, exactly as
// swarm_status gives it to an AI. It holds ids, names, counts and times only.
func (s *Server) OutboxStatusBuilder() outbox.StatusBuilder {
	return func(ctx context.Context) (map[string]any, error) {
		c, err := s.outboxCall(ctx)
		if err != nil {
			return nil, err
		}
		return c.dispatch(http.MethodGet, "/v3/box/summary", nil, nil)
	}
}
