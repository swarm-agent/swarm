package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/outbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A finished job's result is built once from what swarm_get_job reports, and
// read by two consumers: the outbox, which exports it with the job's files,
// and job automations, which hand it to the user's steps. This file is the
// shared part; each consumer adds only its own fields.

// jobResultGit runs a read-only Git command in a job's checkout.
type jobResultGit func(ctx context.Context, repo string, args ...string) (string, error)

// workerJobResult is a worker job's result read from its swarm_get_job view.
type workerJobResult struct {
	jobID, runID string
	// manifest holds the fields every consumer reports as they are.
	manifest map[string]any
	result   map[string]any
	repo     string
	branch   string
	base     string
	head     string
	commits  []map[string]string
	files    []workerJobResultPath
	// hasGit is set when the job committed: a head and commits are known.
	hasGit bool
}

type workerJobResultPath struct {
	path, status string
}

// mediaPath reports whether the changed file is an image, video or audio
// file that still exists at the job's head.
func (p workerJobResultPath) mediaPath() bool {
	return outbox.MediaType(p.path) != "" && p.status != "deleted" && !strings.HasPrefix(p.status, "D") && p.status != "untracked"
}

// workerJobResultFromView reads a job's swarm_get_job view. A job whose
// commits are not readable yet returns outbox.ErrNotReady unless lastAttempt.
func workerJobResultFromView(ctx context.Context, git jobResultGit, lastAttempt bool, workerID, workerName, runID string, view map[string]any) (*workerJobResult, error) {
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
	out := &workerJobResult{jobID: jobID, runID: runID, manifest: manifest, result: result}
	out.repo, _ = result["workspace_path"].(string)
	out.branch, _ = result["worktree_branch"].(string)
	out.base, _ = result["base_commit"].(string)
	out.head, _ = result["head"].(string)
	commits := controlMCPList(result["commits"])
	if out.head == "" && out.repo != "" && controlMCPGitRevision.MatchString(out.branch) && git != nil {
		if resolved, err := git(ctx, out.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+out.branch+"^{commit}"); err == nil {
			out.head = strings.TrimSpace(resolved)
		}
	}
	// A job's run ends a moment before Swarm has assessed its commits: a
	// job with work whose commits are not readable yet is retried, and
	// reported as it is on the last attempt.
	state, _ := result["delivery_state"].(string)
	pending := state == "" || state == "unavailable" || (state == "candidate_work" && (out.head == "" || len(commits) == 0))
	if view["status"] == "needs_review" && pending && !lastAttempt {
		return nil, outbox.ErrNotReady
	}
	if text, _ := result["changes_error"].(string); text != "" {
		manifest["changes_error"] = text
	}
	if out.repo != "" && out.head != "" && len(commits) > 0 {
		out.hasGit = true
		out.commits = make([]map[string]string, 0, len(commits))
		for _, raw := range commits {
			line, _ := raw.(string)
			sha, subject, _ := strings.Cut(line, " ")
			out.commits = append(out.commits, map[string]string{"sha": sha, "subject": subject})
		}
		for _, raw := range controlMCPList(result["files"]) {
			file := controlMCPMap(raw)
			name, _ := file["path"].(string)
			status, _ := file["status"].(string)
			if name == "" {
				continue
			}
			out.files = append(out.files, workerJobResultPath{path: name, status: status})
		}
	}
	return out, nil
}

// mediaJobManifest is the part of a media job's result every consumer
// reports: what was asked for and how it ended. Its run id is the task id.
func mediaJobManifest(task *pebblestore.ProjectTaskRecord) map[string]any {
	projectID, taskID := task.ProjectID, task.ID
	manifest := map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, projectID, taskID), "run_id": taskID, "status": task.Status, "outcome": "media",
		"kind": task.Agent, "project_id": projectID, "title": task.Title, "brief": controlMCPTruncate(task.Description, 4000)}
	for key, value := range map[string]string{"model": task.Model, "aspect_ratio": task.AspectRatio, "resolution": task.Resolution, "error": task.LastError, "workspace_path": task.WorkspacePath} {
		if value != "" {
			manifest[key] = value
		}
	}
	if task.DurationSeconds > 0 {
		manifest["duration_seconds"] = task.DurationSeconds
	}
	return manifest
}

// mediaJobFile is one file of a media job with the name it is reported under.
type mediaJobFile struct {
	name string
	controlMCPMediaFile
}

// mediaJobFiles lists a media job's files in order, named by the job's title.
func mediaJobFiles(task *pebblestore.ProjectTaskRecord) []mediaJobFile {
	files := []mediaJobFile{}
	for i, file := range mediaTaskFiles(task) {
		files = append(files, mediaJobFile{name: file.filename(task.Title, i+1), controlMCPMediaFile: file})
	}
	return files
}

// ---- results for job automations -------------------------------------------

// automationResult turns a worker job's result into the record its
// automations receive. media files are hashed from the job's head.
func (r *workerJobResult) automationResult(ctx context.Context, git *outbox.Builder, role string) *jobautomation.Result {
	out := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: r.jobID, Kind: jobautomation.KindWorker, Role: role, RunID: r.runID}
	if worker := controlMCPMap(r.manifest["worker"]); worker != nil {
		out.Worker = &jobautomation.ResultWorker{}
		out.Worker.ID, _ = worker["id"].(string)
		out.Worker.Name, _ = worker["name"].(string)
	}
	status, _ := r.manifest["status"].(string)
	out.JobStatus = status
	out.Status = jobautomation.NormalizeStatus(status)
	out.ProjectID, _ = r.manifest["project_id"].(string)
	out.Report, _ = r.manifest["report"].(string)
	out.Error, _ = r.manifest["error"].(string)
	if cost := controlMCPMap(r.manifest["cost"]); cost != nil {
		out.Cost = jobResultCost(cost["total_tokens"], cost["estimated_cost_usd"], cost["model"])
	}
	if !r.hasGit {
		return out
	}
	gitResult := &jobautomation.ResultGit{Branch: r.branch, BaseCommit: r.base, Head: r.head, Worktree: r.repo, Files: []string{}}
	gitResult.BaseBranch, _ = r.result["base_branch"].(string)
	for _, commit := range r.commits {
		gitResult.Commits = append(gitResult.Commits, jobautomation.ResultCommit{SHA: commit["sha"], Subject: commit["subject"]})
	}
	for _, file := range r.files {
		gitResult.Files = append(gitResult.Files, file.path)
		if !file.mediaPath() || git == nil {
			continue
		}
		blob, err := git.File(ctx, r.repo, r.head, file.path)
		if err != nil {
			continue
		}
		_ = removeJobResultBlob(blob)
		out.Files = append(out.Files, jobautomation.ResultFile{Path: file.path, SHA256: blob.SHA256, Type: outbox.MediaType(file.path), Size: blob.Size})
	}
	out.Git = gitResult
	return out
}

// mediaJobAutomationResult is a media job's result for its automations.
func mediaJobAutomationResult(task *pebblestore.ProjectTaskRecord) *jobautomation.Result {
	manifest := mediaJobManifest(task)
	out := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: manifest["job_id"].(string), Kind: jobautomation.KindMedia, Role: task.Agent,
		RunID: task.ID, ProjectID: task.ProjectID, Folder: task.WorkspacePath, JobStatus: task.Status, Status: jobautomation.NormalizeStatus(task.Status),
		Brief: task.Description, Error: task.LastError, FinishedAt: task.UpdatedAt}
	if task.Status == "failed" {
		out.Status = jobautomation.StatusFailed
	}
	if task.SourceWorkspace.Path != "" {
		out.Folder = task.SourceWorkspace.Path
	}
	for _, file := range mediaJobFiles(task) {
		if len(file.data) == 0 {
			continue
		}
		sum := sha256.Sum256(file.data)
		out.Files = append(out.Files, jobautomation.ResultFile{Path: file.name, SHA256: hex.EncodeToString(sum[:]), Type: file.mediaType, Size: int64(len(file.data))})
	}
	if task.Model != "" {
		out.Cost = &jobautomation.ResultCost{Model: task.Model}
	}
	return out
}

func jobResultCost(tokens, usd, model any) *jobautomation.ResultCost {
	cost := &jobautomation.ResultCost{}
	switch n := tokens.(type) {
	case float64:
		cost.Tokens = int64(n)
	case int:
		cost.Tokens = int64(n)
	case int64:
		cost.Tokens = n
	}
	switch v := usd.(type) {
	case float64:
		cost.EstimatedUSD = &v
	}
	cost.Model, _ = model.(string)
	return cost
}

func removeJobResultBlob(blob outbox.Blob) error {
	if blob.Path == "" {
		return nil
	}
	return os.Remove(blob.Path)
}
