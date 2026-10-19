package api

import (
	"context"
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

// outboxResultFromView turns a job's swarm_get_job view into its export: the
// shared job result (job_result.go) plus the job's media files and a Git
// bundle of its commits.
func outboxResultFromView(ctx context.Context, b *outbox.Builder, workerID, workerName, runID string, view map[string]any) (*outbox.Export, error) {
	core, err := workerJobResultFromView(ctx, b.GitOutput, b.LastAttempt, workerID, workerName, runID, view)
	if err != nil {
		return nil, err
	}
	manifest := core.manifest
	export := &outbox.Export{JobID: core.jobID, RunID: runID, Manifest: manifest}
	outcome := "report"
	if core.hasGit {
		outcome = "code"
		repo, branch, base, head := core.repo, core.branch, core.base, core.head
		git := map[string]any{"branch": branch, "base_branch": core.result["base_branch"], "base_commit": base, "head": head}
		git["commits"] = core.commits
		paths := []string{}
		skipped := []map[string]string{}
		for _, file := range core.files {
			name := file.path
			paths = append(paths, name)
			if !file.mediaPath() {
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

// outboxMediaExport is a media job's export: the shared media result
// (job_result.go) and its files.
func outboxMediaExport(task *pebblestore.ProjectTaskRecord, b *outbox.Builder) *outbox.Export {
	manifest := mediaJobManifest(task)
	export := &outbox.Export{JobID: manifest["job_id"].(string), RunID: task.ID, Manifest: manifest}
	files, skipped := []map[string]any{}, []map[string]string{}
	for _, file := range mediaJobFiles(task) {
		name := file.name
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
