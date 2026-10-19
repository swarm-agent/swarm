package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Jobs in a folder whose owner set a publish command on the machine
// (`swarmctl workspace publish`) are delivered as pull requests: Swarm runs
// the owner's command with the job's branch and head, and records the pull
// request it prints, instead of merging into the folder. A pull request is
// opened only when swarm_deliver_job asks. Merging happens on the forge, under
// its own rules.

// SetPublishConfig names the owner's publish rules file (SWARM_PUBLISH_CONFIG).
func (s *Server) SetPublishConfig(path string) {
	s.publishConfig = strings.TrimSpace(path)
}

// publishRule is the publish rule of a folder, read fresh so the owner's
// change applies without a restart.
func (s *Server) publishRule(folder string) (publish.Rule, bool, error) {
	if s == nil || s.publishConfig == "" || strings.TrimSpace(folder) == "" {
		return publish.Rule{}, false, nil
	}
	load := s.loadPublishConfig
	if load == nil {
		load = publish.Load
	}
	rules, err := load(s.publishConfig)
	if err != nil {
		return publish.Rule{}, false, err
	}
	rule, ok := publish.For(rules, folder)
	return rule, ok, nil
}

// jobResult is the result part of a job's view, read as an AI receives it.
func controlMCPJobResultJSON(view map[string]any) map[string]any {
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil
	}
	var decoded map[string]any
	if json.Unmarshal(encoded, &decoded) != nil {
		return nil
	}
	return controlMCPMap(decoded["result"])
}

// publishJob opens or updates the job's pull request with the folder's
// command. It refuses a job without commits and records the pull request on
// the job's task and in the activity feed.
func (c *controlMCPCall) publishJob(kind, a, b string, refs controlMCPJobRefs, rule publish.Rule, result map[string]any) (map[string]any, error) {
	jobID := controlMCPJobID(kind, a, b)
	if result["delivery_state"] == "report_only" {
		return nil, errors.New("report-only jobs cannot be published")
	}
	if refs.run["role"] == "reviewer" {
		return nil, errors.New("reviewer jobs are report-only")
	}
	if state, _ := result["delivery_state"].(string); state == "empty" {
		return nil, controlMCPToolFailure("job %s committed nothing: there is no pull request to open (its report is the result)", jobID)
	}
	branch, _ := result["worktree_branch"].(string)
	head, _ := result["head"].(string)
	base, _ := result["base_branch"].(string)
	if head == "" || branch == "" {
		return nil, controlMCPToolFailure("job %s has no branch and head commit yet; wait until it is ready for review (swarm_get_job)", jobID)
	}
	report, _ := result["report"].(string)
	title := ""
	if task, err := c.task(refs.projectID, refs.taskID); err == nil {
		title, _ = task["title"].(string)
	}
	if strings.TrimSpace(title) == "" {
		title = "Swarm job " + jobID
	}
	if err := importJobHead(c.request.Context(), rule.Folder, controlMCPString(result, "workspace_path"), head); err != nil {
		c.signalPublish(kind, a, b, refs, "job.publish_failed", pebblestore.SignalSeverityWarning, "Opening the job's pull request failed: "+controlMCPTruncate(err.Error(), 200), map[string]string{"head": head})
		return nil, controlMCPToolFailure("%s", controlMCPTruncate(err.Error(), 400))
	}
	res, err := publish.Run(c.request.Context(), rule, publish.Request{
		Folder: rule.Folder, JobID: jobID, Branch: branch, Head: head, BaseBranch: base, Title: title, Body: report,
	})
	if err != nil {
		c.signalPublish(kind, a, b, refs, "job.publish_failed", pebblestore.SignalSeverityWarning, "Opening the job's pull request failed: "+controlMCPTruncate(err.Error(), 200), map[string]string{"head": head})
		return nil, controlMCPToolFailure("%s", controlMCPTruncate(err.Error(), 400))
	}
	if principal, ok := PrincipalFromRequest(c.request); ok && c.server != nil && c.server.sessions != nil && c.server.sessions.Store() != nil {
		_, _ = updateProjectTaskWithRetry(c.server.sessions.Store(), principal.AccountScopeID, refs.projectID, refs.taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Integration = &pebblestore.ProjectTaskIntegration{State: "pull_request", SourceBranch: branch, SourceHead: head, TargetBranch: base, TargetWorkspacePath: rule.Folder, PullRequestURL: res.URL}
			return nil
		})
	}
	if err := c.signalPublishDurable(kind, a, b, refs, "job.pull_request", pebblestore.SignalSeverityInfo, "Job delivered as a pull request", map[string]string{"pull_request_url": res.URL, "head": head}); err != nil {
		return nil, err
	}
	return map[string]any{
		"job_id":       jobID,
		"pull_request": res.URL,
		"head":         head,
		"next":         "the folder publishes jobs as pull requests: the result is on the forge, merged by its own rules; a follow-up (swarm_message_job) and another delivery update the same pull request",
	}, nil
}

// importJobHead brings a job's exact head commit into the folder, which the
// publish command pushes from: a worker job commits in its own clone of the
// folder, not in the folder. Git runs routed like all daemon Git on a folder.
func importJobHead(ctx context.Context, folder, clone, head string) error {
	git := func(args ...string) error {
		ctx := sandbox.WithGitCommandTimeout(ctx, 2*time.Minute)
		_, err := sandbox.TimedGitOutput(ctx, folder, nil, append([]string{"-c", "safe.directory=" + folder}, args...)...)
		return err
	}
	if git("cat-file", "-e", head+"^{commit}") == nil {
		return nil
	}
	if strings.TrimSpace(clone) == "" {
		return fmt.Errorf("publish: commit %s is not in %s and the job's worktree is unknown", head, folder)
	}
	if err := git("fetch", "--quiet", "--no-tags", "--", clone, head); err != nil {
		return fmt.Errorf("publish: cannot bring commit %s from the job's worktree into %s: %w", head, folder, err)
	}
	return nil
}

func (c *controlMCPCall) signalPublish(kind, a, b string, refs controlMCPJobRefs, signalKind, severity, summary string, attrs map[string]string) {
	if err := c.signalPublishDurable(kind, a, b, refs, signalKind, severity, summary, attrs); err != nil {
		log.Printf("swarmd signal %s not recorded: %v", signalKind, err)
	}
}

func (c *controlMCPCall) signalPublishDurable(kind, a, b string, refs controlMCPJobRefs, signalKind, severity, summary string, attrs map[string]string) error {
	if c.server == nil || c.server.signals == nil {
		return fmt.Errorf("publish signals unavailable")
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return fmt.Errorf("publish signal principal required")
	}
	if err := c.recordJobActivity(kind, a, b, refs, signalKind, attrs["head"], attrs["notification_id"]); err != nil {
		return err
	}
	sigRefs := map[string]string{"project_id": refs.projectID, "task_id": refs.taskID}
	if kind == controlMCPJobWorker {
		sigRefs["worker_id"], sigRefs["run_id"] = a, b
	}
	attrs["job_id"] = controlMCPJobID(kind, a, b)
	dedup := signalKind + ":" + attrs["job_id"] + ":" + attrs["head"] + ":" + attrs["send_backs"]
	if id := attrs["notification_id"]; id != "" {
		dedup = signalKind + ":" + attrs["job_id"] + ":transition:" + id
	}
	if _, err := c.server.signals.AppendOnce(pebblestore.Signal{
		Kind: signalKind, Severity: severity, Source: pebblestore.SignalSourceSwarmd, Account: principal.AccountScopeID,
		Summary: summary, DedupKey: dedup, Refs: sigRefs, Attrs: attrs,
	}); err != nil {
		return err
	}
	return nil
}

// jobFolder is the registered folder a job ran in: its task's source
// workspace. The job's result names the worktree it worked in, which is not
// the folder a publish rule is set for.
func (c *controlMCPCall) jobFolder(refs controlMCPJobRefs, result map[string]any) string {
	if refs.projectID != "" && refs.taskID != "" {
		if task, err := c.task(refs.projectID, refs.taskID); err == nil {
			if path, _ := controlMCPMap(task["source_workspace"])["path"].(string); strings.TrimSpace(path) != "" {
				return path
			}
		}
	}
	path, _ := result["workspace_path"].(string)
	return path
}
