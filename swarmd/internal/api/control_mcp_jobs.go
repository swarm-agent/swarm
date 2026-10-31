package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Workers and jobs are explicit persistent application operations, separate
// from default Swarm goal delegation. After deploying a worker, a client gives
// it jobs in the person's folders,
// waits, reads the result and merges it. A job is a worker run, which Swarm
// executes as a project task on its own branch, in the folder's sandbox, with
// only that folder's secrets. These tools compose the same allowlisted routes
// the other Swarm Control tools use; the route handlers keep every check.

// Media a job can ask for; each runs as a project task on the media models.
var controlMCPMediaKinds = map[string]string{"image": "image", "video": "video", "sound": "sound"}

// A job id names its kind and the two ids Swarm needs to find it again:
// worker/<worker id>/<run id> for a worker's job, media/<project id>/<task id>
// for a media request, design/<project id>/<task id> for a design job. The
// parts are validated like every other id.
const (
	controlMCPJobWorker = "worker"
	controlMCPJobMedia  = "media"
	controlMCPJobDesign = "design"
)

func controlMCPJobID(kind, a, b string) string { return kind + "/" + a + "/" + b }

func controlMCPParseJobID(args map[string]any) (string, string, string, error) {
	parts := strings.Split(controlMCPString(args, "job_id"), "/")
	if len(parts) != 3 || (parts[0] != controlMCPJobWorker && parts[0] != controlMCPJobMedia && parts[0] != controlMCPJobDesign) {
		return "", "", "", controlMCPToolFailure("job_id must be a job id from give_job, request_media, swarm_design or list_workers (worker/<worker>/<run>, media/<project>/<task> or design/<project>/<task>)")
	}
	for _, part := range parts[1:] {
		if !controlMCPSafeID.MatchString(part) || part == "." || part == ".." || controlMCPReservedIDs[part] {
			return "", "", "", controlMCPToolFailure("job_id %q is not a valid job id", controlMCPString(args, "job_id"))
		}
	}
	return parts[0], parts[1], parts[2], nil
}

// ---- projects and their folders --------------------------------------------

// controlMCPJobPlace is where a job runs: a project and one of its folders.
type controlMCPJobPlace struct {
	projectID, projectName, path, workspaceID string
}

// place resolves a job's project (by id or exact name) and the folder in it:
// the named folder, or the project's only folder when none is named. Every
// job belongs to a project the AI created first with swarm_create_project.
func (c *controlMCPCall) place(projectRef, path string) (controlMCPJobPlace, error) {
	projectRef = strings.TrimSpace(projectRef)
	response, err := c.dispatch(http.MethodGet, "/v3/projects", url.Values{"limit": {"200"}}, nil)
	if err != nil {
		return controlMCPJobPlace{}, err
	}
	projects := []map[string]any{}
	for _, raw := range controlMCPList(response["projects"]) {
		if project := controlMCPMap(raw); project != nil {
			projects = append(projects, project)
		}
	}
	if len(projects) == 0 {
		return controlMCPJobPlace{}, controlMCPToolFailure("there is no project yet: ask the person which folders their work is in and create one with swarm_create_project (a name and those folders)")
	}
	names := []string{}
	var match map[string]any
	for _, project := range projects {
		id, _ := project["id"].(string)
		name, _ := project["name"].(string)
		names = append(names, name+" ("+id+")")
		if projectRef != "" && (id == projectRef || strings.EqualFold(name, projectRef)) {
			if match != nil && id != projectRef {
				return controlMCPJobPlace{}, controlMCPToolFailure("several projects are named %q: pass the project id from swarm_list_projects", projectRef)
			}
			match = project
		}
	}
	if projectRef == "" {
		if len(projects) != 1 {
			return controlMCPJobPlace{}, controlMCPToolFailure("name the project: one of %s", strings.Join(names, ", "))
		}
		match = projects[0]
	}
	if match == nil {
		return controlMCPJobPlace{}, controlMCPToolFailure("no project %q: one of %s, or create it with swarm_create_project", projectRef, strings.Join(names, ", "))
	}
	place := controlMCPJobPlace{}
	place.projectID, _ = match["id"].(string)
	place.projectName, _ = match["name"].(string)
	if !controlMCPSafeID.MatchString(place.projectID) {
		return controlMCPJobPlace{}, controlMCPToolFailure("project %q has no usable id", projectRef)
	}
	folders := controlMCPProjectWorkspaces(match)
	paths := []string{}
	for _, folder := range folders {
		if p, _ := folder["path"].(string); p != "" {
			paths = append(paths, p)
		}
	}
	pick := func(folder map[string]any) controlMCPJobPlace {
		place.path, _ = folder["path"].(string)
		place.workspaceID, _ = folder["workspace_id"].(string)
		return place
	}
	if strings.TrimSpace(path) == "" {
		switch len(folders) {
		case 0:
			return controlMCPJobPlace{}, controlMCPToolFailure("project %s has no folder: add one with swarm_set_project_workspaces", place.projectName)
		case 1:
			return pick(folders[0]), nil
		}
		return controlMCPJobPlace{}, controlMCPToolFailure("project %s has several folders: name one as workspace (%s)", place.projectName, strings.Join(paths, ", "))
	}
	if !filepath.IsAbs(path) {
		return controlMCPJobPlace{}, controlMCPToolFailure("workspace must be an absolute folder path of the project (%s)", strings.Join(paths, ", "))
	}
	path = filepath.Clean(path)
	for _, folder := range folders {
		if folder["path"] == path {
			return pick(folder), nil
		}
	}
	return controlMCPJobPlace{}, controlMCPToolFailure("%s is not a folder of project %s (%s): add it with swarm_set_project_workspaces", path, place.projectName, strings.Join(paths, ", "))
}

// ---- workers --------------------------------------------------------------

func controlMCPWorkerPreset(worker map[string]any) string {
	preset, _ := controlMCPMap(worker["metadata"])["preset"].(string)
	return preset
}

// listWorkers reads every worker once (the box has few).
func (c *controlMCPCall) listWorkers() ([]map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/workers", url.Values{"limit": {"100"}}, nil)
	if err != nil {
		return nil, err
	}
	workers := []map[string]any{}
	for _, raw := range controlMCPList(response["workers"]) {
		if worker := controlMCPMap(raw); worker != nil {
			workers = append(workers, worker)
		}
	}
	return workers, nil
}

// deployPreset explicitly provisions only the requested preset.
func (c *controlMCPCall) deployPreset(preset string) ([]map[string]any, error) {
	response, err := c.dispatch(http.MethodPost, "/v3/workers:ensure-presets", nil, map[string]any{"preset": preset})
	if err != nil {
		return nil, err
	}
	workers := []map[string]any{}
	for _, raw := range controlMCPList(response["workers"]) {
		if worker := controlMCPMap(raw); worker != nil {
			workers = append(workers, worker)
		}
	}
	return workers, nil
}

// resolveWorker finds a worker by preset name (coder, designer, finder, reviewer), by
// id, or by its exact name.
func (c *controlMCPCall) resolveWorker(ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, controlMCPToolFailure("worker is required: a deployed worker's id or name from swarm_list_workers (deploy one with swarm_deploy_worker first)")
	}
	match := func(workers []map[string]any) map[string]any {
		for _, worker := range workers {
			if worker["id"] == ref || controlMCPWorkerPreset(worker) == strings.ToLower(ref) {
				return worker
			}
		}
		for _, worker := range workers {
			if name, _ := worker["name"].(string); strings.EqualFold(name, ref) {
				return worker
			}
		}
		return nil
	}
	workers, err := c.listWorkers()
	if err != nil {
		return nil, err
	}
	if worker := match(workers); worker != nil {
		return worker, nil
	}

	hint := "use an existing id from manage_workers list"
	if c.advertisedToolAllowed("manage_workers create") {
		hint += "; explicitly deploy a persistent worker with manage_workers create"
	}
	if c.advertisedToolAllowed("manage_projects propose_task") {
		hint += "; delegate a goal with manage_projects propose_task"
	}
	return nil, controlMCPToolFailure("no worker %q: %s", ref, hint)
}

func controlMCPWorkerView(worker map[string]any) map[string]any {
	view := controlMCPWorkerSummary(worker)
	if role, _ := worker["role"].(string); role != "" {
		view["role"] = role
	}
	if preset := controlMCPWorkerPreset(worker); preset != "" {
		view["preset"] = preset
	}
	if bindings := controlMCPMap(worker["local_bindings"]); len(bindings) > 0 {
		view["home_workspace_id"] = bindings["primary"]
	}
	return view
}

func controlMCPListWorkerJobs(c *controlMCPCall, args map[string]any) (any, error) {
	includeUsage := c.requireTokenScope("sessions:read") == nil
	workers, err := c.listWorkers()
	if err != nil {
		return nil, err
	}
	jobCounts := map[string]int{}
	if includeUsage {
		p, ok := PrincipalFromRequest(c.request)
		if !ok {
			return nil, controlMCPToolFailure("authenticated principal required")
		}
		if c.server == nil || c.server.sessions == nil {
			return nil, controlMCPToolFailure("usage store unavailable")
		}
		counts, err := c.server.sessions.Store().WorkerStore().CountWorkerRuns(p.AccountScopeID, 0, 0)
		if err != nil {
			return nil, err
		}
		for id, count := range counts.ByWorker {
			jobCounts[id] = count.Runs
		}
	}
	date := time.Now().UTC().Format("2006-01-02")
	out := []map[string]any{}
	for _, worker := range workers {
		view := controlMCPWorkerView(worker)
		p, _ := PrincipalFromRequest(c.request)
		id, _ := worker["id"].(string)
		budget, err := c.server.sessions.Store().GetWorkerBudgetStatus(p.AccountScopeID, id)
		if err != nil {
			return nil, err
		}
		budgetView := map[string]any{"daily_usd": budget.DailyCostLimitUSD, "daily_tokens": budget.DailyTokensLimit, "date": budget.Date, "blocked": budget.Blocked, "blocked_reason": budget.BlockedReason}
		view["budget"] = budgetView
		if includeUsage {
			budgetView["usage"] = budget.Usage
			id, _ := worker["id"].(string)
			usage, err := c.workerUsage(id, date)
			if err != nil {
				return nil, err
			}
			usage["jobs"] = jobCounts[id]
			view["usage"] = usage
		}
		if model := c.workerModelSummary(worker); model != nil {
			view["model"] = model
		}
		if include, _ := args["include_jobs"].(bool); include {
			id, _ := worker["id"].(string)
			if runs, err := c.dispatch(http.MethodGet, "/v3/workers/"+id+"/runs", url.Values{"limit": {"5"}}, nil); err == nil {
				jobs := []map[string]any{}
				for _, raw := range controlMCPList(runs["runs"]) {
					if run := controlMCPMap(raw); run != nil {
						runID, _ := run["id"].(string)
						job := controlMCPPick(run, "status", "request_source")
						job["model"] = controlMCPRunModel(run)
						job["job_id"] = controlMCPJobID(controlMCPJobWorker, id, runID)
						jobs = append(jobs, job)
					}
				}
				view["recent_jobs"] = jobs
			}
		}
		out = append(out, view)
	}
	return map[string]any{"workers": out}, nil
}

func controlMCPDeployWorker(c *controlMCPCall, args map[string]any) (any, error) {
	if raw, present := args["budget"]; present {
		if _, err := parseControlMCPWorkerBudget(raw); err != nil {
			return nil, err
		}
	}
	if _, hasAgent := args["agent"]; hasAgent && controlMCPString(args, "preset") != "" {
		return nil, controlMCPToolFailure("agent is for custom worker creation; use swarm_update_worker to change an existing worker's agent")
	}
	if raw, exists := args["model"]; exists && raw != nil {
		if _, err := c.workerModel(raw); err != nil {
			return nil, err
		}
	}
	if ref := controlMCPString(args, "worker"); ref != "" {
		worker, err := c.resolveWorker(ref)
		if err != nil {
			return nil, err
		}
		return c.deployWorkerWithBudget(worker, args)
	}
	if name := controlMCPString(args, "name"); name != "" {
		workers, err := c.listWorkers()
		if err != nil {
			return nil, err
		}
		var match map[string]any
		for _, worker := range workers {
			if worker["name"] == name {
				if match != nil {
					return nil, controlMCPToolFailure("several workers have that name; pass worker id")
				}
				match = worker
			}
		}
		if match != nil {
			return c.deployWorkerWithBudget(match, args)
		}
	}
	if preset := strings.ToLower(controlMCPString(args, "preset")); preset != "" {
		if controlMCPString(args, "name") != "" || controlMCPString(args, "instructions") != "" {
			return nil, controlMCPToolFailure("give either preset, or name, role and instructions for a custom worker")
		}
		workers, err := c.deployPreset(preset)
		if err != nil {
			return nil, err
		}
		for _, worker := range workers {
			if controlMCPWorkerPreset(worker) == preset {
				return c.deployWorkerWithBudget(worker, args)
			}
		}
		return nil, controlMCPToolFailure("preset %q was not deployed", preset)
	}
	name, instructions, role := controlMCPString(args, "name"), controlMCPString(args, "instructions"), controlMCPString(args, "role")
	if name == "" || instructions == "" || role == "" {
		return nil, controlMCPToolFailure("a custom worker needs name, role (coder, designer, finder, reviewer or swarm) and instructions; or pass preset")
	}
	body := map[string]any{
		"name":            name,
		"role":            role,
		"agent":           controlMCPString(args, "agent"),
		"instructions":    instructions,
		"idempotency_key": controlMCPRequestID(),
	}
	if description := controlMCPString(args, "description"); description != "" {
		body["description"] = description
	}
	created, err := c.dispatch(http.MethodPost, "/v3/workers", nil, body)
	if err != nil {
		return nil, err
	}
	worker := controlMCPMap(created["worker"])
	workerID, _ := worker["id"].(string)
	if !controlMCPSafeID.MatchString(workerID) {
		return nil, controlMCPToolFailure("worker created without a usable id")
	}
	if raw, present := args["budget"]; present {
		if _, err := c.deployWorkerWithBudget(worker, map[string]any{"budget": raw}); err != nil {
			return nil, controlMCPToolFailure("worker %s created but budget not applied; not activated: %s", workerID, err)
		}
	}
	activated, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/activate", nil, map[string]any{
		"expected_revision": controlMCPRevision(worker),
		"local_bindings":    map[string]string{},
	})
	if err != nil {
		return nil, controlMCPToolFailure("worker %s created but not activated: %s", workerID, err.Error())
	}
	modelArgs := map[string]any{}
	if raw := args["model"]; raw != nil {
		modelArgs["model"] = raw
	}
	result, err := c.deployWorkerModel(controlMCPMap(activated["worker"]), modelArgs)
	if err != nil {
		return nil, controlMCPToolFailure("worker %s created and activated without the requested model pin: %s", workerID, err)
	}
	return result, nil
}

// ---- jobs -----------------------------------------------------------------

func controlMCPGiveJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	var override *pebblestore.SessionModelProfileSnapshot
	if raw, exists := args["model"]; exists {
		var err error
		override, err = c.workerModel(raw)
		if err != nil {
			return nil, err
		}
	}
	brief := strings.TrimSpace(controlMCPString(args, "brief"))
	if brief == "" {
		return nil, controlMCPToolFailure("brief is required: what to do and how to know it is done")
	}
	worker, err := c.resolveWorker(controlMCPString(args, "worker"))
	if err != nil {
		return nil, err
	}
	workerID, _ := worker["id"].(string)
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	if err := c.environmentBlocksJob(place); err != nil {
		return nil, err
	}
	then, hasThen := args["then"]
	if hasThen {
		if err := controlMCPValidateThen(c, then); err != nil {
			return nil, err
		}
	}
	brief += tool.TaskFilesScope(controlMCPList(args["files"]))
	key := controlMCPString(args, "idempotency_key")
	if key == "" {
		key = controlMCPRequestID()
	}
	// The project id travels in the run's input: Swarm runs the job as that
	// project's task, and refuses it when the folder is not the project's.
	input := map[string]any{"project_id": place.projectID}
	if commits, ok := args["review_commits"].([]string); ok {
		input["review_commits"] = commits
	}
	if checkout := controlMCPString(args, "review_checkout"); checkout != "" {
		input["review_checkout"] = checkout
	}
	response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/direct", nil, map[string]any{
		"prompt":          brief,
		"review_job":      controlMCPString(args, "review_job"),
		"input":           input,
		"idempotency_key": key,
		"workspace_path":  place.path,
		"model_profile":   override,
	})
	if err != nil {
		return nil, err
	}
	run := controlMCPMap(response["run"])
	runID, _ := run["id"].(string)
	if !controlMCPSafeID.MatchString(runID) {
		return nil, controlMCPToolFailure("Swarm accepted the job without a usable run id")
	}
	jobID := controlMCPJobID(controlMCPJobWorker, workerID, runID)
	var thenView map[string]any
	if hasThen && then != nil {
		// The job's own automation, scoped to it; set before the job can end.
		thenView, err = c.controlMCPThen(then, "then: "+jobID, place.path, jobautomation.Scope{Job: jobID})
		if err != nil {
			return nil, controlMCPToolFailure("the job %s started, but its then steps were not set: %s", jobID, err.Error())
		}
	}
	out, err := c.jobView(controlMCPJobWorker, workerID, runID, controlMCPInt(args, "wait_seconds", 0), false, 0)
	if err != nil {
		return nil, err
	}
	if thenView != nil {
		out["then"] = thenView
	}
	out["job_id"] = jobID
	out["idempotency_key"] = key
	out["project"] = map[string]any{"id": place.projectID, "name": place.projectName}
	out["workspace"] = place.path
	out["worker"] = controlMCPWorkerView(worker)
	return out, nil
}

// jobRefs are the ids behind one job: its session (once it has one) and the
// project task that carries its branch, review and integration.
type controlMCPJobRefs struct {
	sessionID, projectID, taskID string
	run                          map[string]any
}

func (c *controlMCPCall) jobRefs(kind, a, b string) (controlMCPJobRefs, error) {
	if kind == controlMCPJobMedia || kind == controlMCPJobDesign {
		task, err := c.task(a, b)
		if err != nil {
			return controlMCPJobRefs{}, err
		}
		sessionID, _ := task["session_id"].(string)
		return controlMCPJobRefs{sessionID: sessionID, projectID: a, taskID: b}, nil
	}
	response, err := c.dispatch(http.MethodGet, "/v3/workers/"+a+"/runs/"+b, nil, nil)
	if err != nil {
		return controlMCPJobRefs{}, controlMCPNotFound(err, "job", controlMCPJobID(kind, a, b), "swarm_list_workers with include_jobs")
	}
	run := controlMCPMap(response["run"])
	refs := controlMCPJobRefs{run: run}
	refs.sessionID, _ = run["session_id"].(string)
	if review := controlMCPMap(run["review"]); review != nil {
		refs.projectID, _ = review["project_id"].(string)
		refs.taskID, _ = review["task_id"].(string)
	}
	if refs.projectID == "" && refs.sessionID != "" {
		// The run's session records its project task from the moment it starts.
		if detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(refs.sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil); err == nil {
			metadata := controlMCPMap(controlMCPMap(detail["session"])["metadata"])
			refs.projectID, _ = metadata["project_id"].(string)
			refs.taskID, _ = metadata["task_id"].(string)
		}
	}
	if !controlMCPSafeID.MatchString(refs.projectID) || !controlMCPSafeID.MatchString(refs.taskID) {
		refs.projectID, refs.taskID = "", ""
	}
	return refs, nil
}

// controlMCPJobSettled lists the job states a wait returns on.
var controlMCPJobSettled = map[string]bool{
	"needs_review": true, "completed": true, "failed": true, "blocked": true, "rejected": true,
	"needs_input": true, "waiting_on_approval": true, "cancelled": true, "superseded": true,
	"pending_approval": true, "stopped": true,
}

// jobState reads a job once: its status, the question or approval it waits
// on, and the task when it has one.
func (c *controlMCPCall) jobState(kind, a, b string) (map[string]any, controlMCPJobRefs, error) {
	refs, err := c.jobRefs(kind, a, b)
	if err != nil {
		return nil, refs, err
	}
	state := map[string]any{}
	status := ""
	if refs.run != nil {
		state["model"] = controlMCPRunModel(refs.run)
		if cleanup, ok := refs.run["cleanup"]; ok {
			state["cleanup"] = cleanup
		}
		status, _ = refs.run["status"].(string)
		// The receipt keeps its first run's error; once a follow-up started,
		// the job's error is that follow-up's own (none while it runs or after
		// it completed).
		errKey := "error"
		if followup, _ := refs.run["followup_status"].(string); followup != "" {
			errKey = "followup_error"
		}
		if errText, _ := refs.run[errKey].(string); strings.TrimSpace(errText) != "" {
			state["error"] = controlMCPTruncate(errText, 400)
		}
	}
	if refs.projectID != "" {
		if task, err := c.task(refs.projectID, refs.taskID); err == nil {
			if taskStatus, _ := task["status"].(string); taskStatus != "" {
				status = taskStatus
			}
			for key, limit := range map[string]int{"action_needed": 400, "last_error": 400} {
				if text, ok := task[key].(string); ok && strings.TrimSpace(text) != "" {
					state[key] = controlMCPTruncate(text, limit)
				}
			}
			for key, value := range controlMCPPick(task, "worktree_branch", "base_branch") {
				state[key] = value
			}
		}
	}
	// A task's review state must not mask a still-running worker run.
	if runStatus, _ := refs.run["status"].(string); runStatus == "admitted" || runStatus == "running" {
		status = "in_progress"
	}
	state["run_active"] = false
	if c.server != nil {
		if execution, err := c.server.workerExecutionService(); err == nil {
			state["run_active"] = execution.RunActive(refs.sessionID)
		}
	}
	if refs.sessionID != "" {
		if detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(refs.sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil); err == nil {
			switch intent, _ := controlMCPMap(detail["active_run_intent"])["status"].(string); intent {
			case "pending_executor", "waiting_tasks":
				state["run_active"] = true
			}
			// A follow-up the receipt does not track (admitted without the
			// sandbox's follow-up capacity) still replaces the job's error.
			if latest, ok := c.untrackedFollowupRun(refs.sessionID, refs.run); ok {
				delete(state, "error")
				if !latest.Active && latest.Status != sessionruntime.RunIntentCompleted && strings.TrimSpace(latest.BlockedReason) != "" {
					state["error"] = controlMCPTruncate(latest.BlockedReason, 400)
				}
			}
			questions, approvals := []map[string]any{}, 0
			var waiting []string
			for _, raw := range controlMCPList(detail["pending_permissions"]) {
				summary := controlMCPPermissionSummary(controlMCPMap(raw))
				waiting = append(waiting, controlMCPJobPermissionWait(controlMCPMap(raw)))
				if summary["kind"] == "question" {
					questions = append(questions, controlMCPPick(summary, "id", "tool_arguments"))
				} else {
					approvals++
				}
			}
			switch {
			case len(questions) > 0:
				status = "needs_input"
				state["questions"] = questions
				state["waiting_on"] = strings.Join(waiting, "; ")
			case approvals > 0:
				status = "waiting_on_approval"
				state["waiting_on"] = strings.Join(waiting, "; ")
			default:
				// A follow-up from swarm_message_job runs in the job's session
				// after its task settled: the job is working again.
				switch intent, _ := controlMCPMap(detail["active_run_intent"])["status"].(string); intent {
				case "pending_executor", "running", "waiting_tasks":
					status = "in_progress"
				}
			}
		} else {
			// Readiness cannot be proven when the follow-up session is unreadable.
			state["run_active"] = true
		}
	}
	switch status {
	case "succeeded":
		status = "needs_review"
	case "admitted", "running":
		status = "in_progress"
	}
	for _, key := range []string{"followup_status", "followup_error", "sandbox_cleanup_error"} {
		if text, _ := refs.run[key].(string); text != "" {
			state[key] = controlMCPTruncate(text, 400)
		}
	}
	switch followup, _ := refs.run["followup_status"].(string); followup {
	case "failed", "interrupted", "cancelled":
		if status != "needs_input" && status != "waiting_on_approval" {
			status = "failed"
		}
	case "running", "queued":
		if status != "needs_input" && status != "waiting_on_approval" {
			status = "in_progress"
		}
	case "":
		// A job whose own run was stopped (swarm_stop_run) stays open until a
		// message continues it.
		if status == "failed" && controlMCPRunStopped(refs.run) {
			status = "stopped"
		}
	}
	// swarm_stop_job on a stopped job marks it cancelled; its settled receipt
	// keeps its status (WorkerExecutionService.cancelStoppedJob).
	if controlMCPRunCancelled(refs.run) && status != "in_progress" {
		status = "cancelled"
	}
	if cleanup, _ := refs.run["sandbox_cleanup_error"].(string); cleanup != "" && status != "in_progress" && status != "failed" && status != "needs_input" && status != "waiting_on_approval" {
		// Only the job's disposable container is left over; its commits are
		// untouched, so the finished work stays readable and deliverable.
		if status == "needs_review" || status == "completed" {
			state["work_status"] = status
		}
		status = "blocked"
	}
	state["status"] = status
	return state, refs, nil
}

// untrackedFollowupRun is the job session's latest run when it is a follow-up
// the receipt does not track: the receipt's own run settled, no
// followup_status was recorded, and the latest run was created after the
// receipt completed. Read only after the session detail was dispatched (and so
// ownership-checked) for this caller.
func (c *controlMCPCall) untrackedFollowupRun(sessionID string, run map[string]any) (sessionruntime.SessionRunState, bool) {
	if c.server == nil || c.server.sessions == nil || sessionID == "" {
		return sessionruntime.SessionRunState{}, false
	}
	if followup, _ := run["followup_status"].(string); followup != "" {
		return sessionruntime.SessionRunState{}, false
	}
	if status, _ := run["status"].(string); status != "succeeded" && status != "failed" {
		return sessionruntime.SessionRunState{}, false
	}
	completedAt := int64(controlMCPNumber(run["completed_at"]))
	if completedAt <= 0 {
		return sessionruntime.SessionRunState{}, false
	}
	latest, ok, err := c.server.sessions.GetSessionRunState(sessionID)
	if err != nil || !ok || latest.RunID == "" || latest.CreatedAt <= completedAt {
		return sessionruntime.SessionRunState{}, false
	}
	return latest, true
}

// controlMCPRunStopped reports a worker job whose own run was stopped and that
// a message continues (pebblestore.WorkerRunStopped).
func controlMCPRunStopped(run map[string]any) bool {
	status, _ := run["status"].(string)
	errText, _ := run["error"].(string)
	return status == "failed" && !controlMCPRunCancelled(run) && strings.HasPrefix(errText, pebblestore.WorkerRunStoppedPrefix)
}

// controlMCPRunCancelled reports a settled worker job that was cancelled
// after its run ended (swarm_stop_job on a stopped job).
func controlMCPRunCancelled(run map[string]any) bool {
	cancelled, _ := run["cancel_requested"].(bool)
	status, _ := run["status"].(string)
	return cancelled && status == "failed"
}

// controlMCPJobNext says what to do with a job in this status. committed is
// false for a job that settled with no commits (a finder's report, for
// example): there is nothing to merge.
func controlMCPJobNext(status string, committed bool) string {
	switch status {
	case "needs_review":
		if !committed {
			return "the work is done and committed no changes, so there is nothing to merge: the result is the report (read it here or with swarm_get_job)"
		}
		return "the work is done: read it here (include_diff for the patch), then swarm_deliver_job merges it into the folder"
	case "completed":
		return "the job is finished and merged"
	case "needs_input":
		return "the worker asked something: reply with swarm_answer_job"
	case "waiting_on_approval":
		return "see waiting_on for the exact tool and recovery action; deny stale approvals and retry within the job's authorized scope"
	case "stopped":
		return "the job's run was stopped and the job is kept open on its branch: swarm_message_job continues it there; swarm_stop_job cancels it"
	case "failed", "blocked", "cancelled", "superseded", "rejected":
		return "the job did not finish: see error and the report, then give a corrected job with swarm_give_job"
	case "pending_approval":
		return "the job waits for the machine owner's approval (large media batches)"
	case "in_progress", "queued", "planning":
		return "the job is running: call swarm_get_job with wait_seconds to wait for it"
	}
	return ""
}

func controlMCPJobWaitSettled(state map[string]any) bool {
	return controlMCPJobSettled[controlMCPString(state, "status")]
}

// waitForJob returns the job's state once it settles or at the deadline. It
// wakes on committed realtime records for the job's session or project task
// and re-reads; it never polls on a timer.
func (c *controlMCPCall) waitForJob(kind, a, b string, seconds int) (map[string]any, controlMCPJobRefs, map[string]any, error) {
	started := time.Now()
	elapsed := func() int { return int(time.Since(started).Seconds()) }
	settled := controlMCPJobWaitSettled
	state, refs, err := c.jobState(kind, a, b)
	if err != nil || seconds <= 0 || settled(state) {
		return state, refs, map[string]any{"settled": err == nil && settled(state), "waited_seconds": elapsed()}, err
	}
	if seconds > controlMCPMaxWaitSeconds {
		seconds = controlMCPMaxWaitSeconds
	}
	var hub *v3RealtimeOutboxHub
	if c.server != nil {
		hub = c.server.v3RealtimeOutbox
	}
	sub := hub.subscribe()
	if sub == nil {
		return state, refs, map[string]any{"settled": false, "waited_seconds": elapsed()}, nil
	}
	defer hub.unsubscribe(sub)
	if state, refs, err = c.jobState(kind, a, b); err != nil || settled(state) {
		return state, refs, map[string]any{"settled": err == nil, "waited_seconds": elapsed()}, err
	}
	mine := func(record sessionruntime.RealtimeOutboxRecord) bool {
		var payload struct {
			ProjectID string `json:"project_id"`
			TaskID    string `json:"task_id"`
		}
		if refs.sessionID == "" || record.SessionID == refs.sessionID {
			return true
		}
		return refs.projectID != "" && json.Unmarshal(record.Event.Payload, &payload) == nil &&
			payload.ProjectID == refs.projectID && (payload.TaskID == "" || payload.TaskID == refs.taskID)
	}
	check := func() bool {
		state, refs, err = c.jobState(kind, a, b)
		return err != nil || settled(state)
	}
	outcome := controlMCPWaitCoalesced(sub.send, sub.slow, c.request.Context().Done(), time.Duration(seconds)*time.Second, controlMCPJobRecheckGap, mine, check)
	switch outcome {
	case controlMCPWaitDone:
		return state, refs, map[string]any{"settled": err == nil, "waited_seconds": elapsed()}, err
	case controlMCPWaitCancelled:
		return state, refs, map[string]any{"settled": false, "waited_seconds": elapsed()}, nil
	}
	// Timed out, or the event feed fell behind: one last look.
	state, refs, err = c.jobState(kind, a, b)
	waited := map[string]any{"settled": err == nil && settled(state), "waited_seconds": elapsed()}
	if outcome == controlMCPWaitTimedOut {
		waited["timed_out"] = true
	}
	return state, refs, waited, err
}

// controlMCPJobRecheckGap bounds how often a wait re-reads a job's state. A
// running worker emits events constantly and each re-read is costly (it looks
// at the job's worktree), so re-reading on every event once held a 110 s wait
// open for 15 minutes.
const controlMCPJobRecheckGap = 2 * time.Second

type controlMCPWaitOutcome int

const (
	controlMCPWaitDone controlMCPWaitOutcome = iota
	controlMCPWaitTimedOut
	controlMCPWaitBehind
	controlMCPWaitCancelled
)

// controlMCPWaitCoalesced waits until check reports done, the deadline passes,
// the event feed falls behind or done closes. Relevant events only mark the
// job as changed; check runs at most once per gap (and once more when the
// last change is older than gap), and never after the deadline, so the wait
// ends on time however many events arrive.
func controlMCPWaitCoalesced[R any, S any](events <-chan R, slow <-chan S, cancelled <-chan struct{}, wait, gap time.Duration, relevant func(R) bool, check func() bool) controlMCPWaitOutcome {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(gap)
	defer tick.Stop()
	end := time.Now().Add(wait)
	var last time.Time
	dirty := false
	run := func() bool {
		dirty, last = false, time.Now()
		return check()
	}
	for {
		select {
		case <-deadline.C:
			return controlMCPWaitTimedOut
		case <-cancelled:
			return controlMCPWaitCancelled
		case <-slow:
			return controlMCPWaitBehind
		case record := <-events:
			if !relevant(record) {
				continue
			}
			dirty = true
			if time.Since(last) < gap {
				continue
			}
		case <-tick.C:
			if !dirty {
				continue
			}
		}
		if !time.Now().Before(end) {
			return controlMCPWaitTimedOut
		}
		if run() {
			return controlMCPWaitDone
		}
	}
}

// jobView is get_job: the job's state, and once its work is ready for review
// or finished, its result (branch, commits, files, report, cost, diff).
func (c *controlMCPCall) jobView(kind, a, b string, wait int, includeDiff bool, diffChars int) (map[string]any, error) {
	state, refs, waited, err := c.waitForJob(kind, a, b, wait)
	if err != nil {
		return nil, err
	}
	out := state
	if wait > 0 {
		out["waited"] = waited
	}
	status, _ := state["status"].(string)
	committed := true
	if refs.projectID != "" && (status == "needs_review" || status == "completed" || status == "failed" || state["work_status"] != nil) {
		resultArgs := map[string]any{"project_id": refs.projectID, "task_id": refs.taskID, "include_diff": includeDiff}
		if diffChars > 0 {
			resultArgs["diff_chars"] = float64(diffChars)
		}
		if result, err := controlMCPTaskResult(c, resultArgs); err == nil {
			res := controlMCPMap(result)
			for _, key := range []string{"task_id", "next", "status", "title"} {
				delete(res, key)
			}
			out["result"] = res
			committed = controlMCPResultCommitted(res)
		} else {
			out["result_error"] = controlMCPTruncate(err.Error(), 300)
		}
	}
	if kind == controlMCPJobWorker && refs.run != nil {
		c.fillReviewFixResult(out, a, b, refs.run)
	}
	if refs.run != nil {
		if review := controlMCPMap(refs.run["review"]); review["verdict"] != nil {
			out["review"] = review
		}
		if deliverables := controlMCPWorkerRunSummary(refs.run)["deliverables"]; deliverables != nil {
			out["deliverables"] = deliverables
		}
	}
	if refs.projectID != "" {
		out["project_id"] = refs.projectID
	}
	if refs.sessionID != "" {
		out["session_id"] = refs.sessionID
	}
	nextStatus := status
	if work, ok := state["work_status"].(string); ok {
		nextStatus = work
	}
	if next := controlMCPJobNext(nextStatus, committed); next != "" {
		out["next"] = next
	}
	result := controlMCPMap(out["result"])
	if _, uncommitted := result["uncommitted_changes"]; uncommitted && status == "needs_review" {
		out["next"] = controlMCPUncommittedNext
	}
	integration := controlMCPMap(result["integration"])
	if delivered := taskDeliveredState(controlMCPString(integration, "state"), controlMCPString(integration, "pull_request_url")); delivered != "" {
		delete(out, "action_needed")
		if delivered == "pull_request" {
			out["next"] = "the job was delivered as a pull request: " + controlMCPString(integration, "pull_request_url")
		} else {
			out["next"] = "the job is finished and merged"
		}
	}
	return out, nil
}

// fillReviewFixResult shows, on the job of a reviewer an automation step
// allowed to commit security fixes, its fix commits and head on its own
// agent/review-<run> branch, read with Git from its checkout by the same
// reader the automation step uses (Server.reviewFixCheckout). A reviewer's
// delivery assessment is report-only and never reports a head, so this is
// the result's head. A checkout that cannot be read is reported as
// result.fix_error with no head, never as a head without fixes.
func (c *controlMCPCall) fillReviewFixResult(out map[string]any, workerID, runID string, run map[string]any) {
	if c.server == nil {
		return
	}
	if fixes, _ := controlMCPMap(run["input"])["review_fixes"].(string); fixes != jobautomation.FixesSecurity {
		return
	}
	account, _ := run["account_scope_id"].(string)
	git, fixing, err := c.server.reviewFixCheckout(c.request.Context(), account, workerID, runID)
	if !fixing {
		return
	}
	result := controlMCPMap(out["result"])
	if result == nil {
		result = map[string]any{}
	}
	if err != nil {
		delete(result, "head")
		result["fix_error"] = controlMCPTruncate(err.Error(), 300)
		out["result"] = result
		return
	}
	commits := []string{}
	for _, commit := range git.Commits {
		commits = append(commits, controlMCPTruncate(commit.SHA+" "+commit.Subject, 200))
	}
	result["head"], result["branch"], result["base_commit"], result["commits"] = git.Head, git.Branch, git.BaseCommit, commits
	delete(result, "fix_error")
	out["result"] = result
}

// controlMCPUncommittedNext is a reviewable job's next step while its worktree
// holds changes it did not commit: only commits are merged, so the job is not
// "nothing to merge" and not ready to deliver either.
const controlMCPUncommittedNext = "the worker left uncommitted changes in its worktree (result.uncommitted_changes), and only commits are merged: ask it with swarm_message_job to commit them (finishing any unfinished merge or cherry-pick) or to discard what it does not want, then swarm_deliver_job merges the branch"

// controlMCPResultCommitted reports whether a job's result has commits to
// merge. The delivery assessment is the authority ("empty": no commits or no
// net change since the job's base); without one, an empty commit list read
// from the job's branch says the same. Anything unknown counts as committed,
// so a merge is never hidden.
func controlMCPResultCommitted(result map[string]any) bool {
	if state, _ := result["delivery_state"].(string); state != "" {
		return state != "empty"
	}
	if commits, ok := result["commits"].([]string); ok {
		return len(commits) > 0
	}
	return true
}

// controlMCPMaxTranscriptMessages bounds get_job's transcript.
const controlMCPMaxTranscriptMessages = 50

func controlMCPGetJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	kind, a, b, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if kind == controlMCPJobDesign {
		out, err := c.designJobView(a, b, controlMCPInt(args, "wait_seconds", 0))
		if err == nil {
			if deliveries, ok := c.jobDeliveries(controlMCPJobID(kind, a, b)); ok {
				out["automations"] = deliveries
			}
		}
		return out, err
	}
	include, _ := args["include_diff"].(bool)
	out, err := c.jobView(kind, a, b, controlMCPInt(args, "wait_seconds", 0), include, controlMCPInt(args, "diff_chars", 0))
	if err != nil {
		return nil, err
	}
	out["job_id"] = controlMCPJobID(kind, a, b)
	if deliveries, ok := c.jobDeliveries(controlMCPJobID(kind, a, b)); ok {
		out["automations"] = deliveries
	}
	if kind == controlMCPJobMedia {
		return c.controlMCPMediaJobFiles(out, a, b, args)
	}
	if transcript, _ := args["include_transcript"].(bool); transcript {
		sessionID, _ := out["session_id"].(string)
		if sessionID == "" {
			out["transcript_error"] = "the job has not started a session yet"
			return out, nil
		}
		limit := min(max(controlMCPInt(args, "transcript_messages", 10), 1), controlMCPMaxTranscriptMessages)
		state, err := c.sessionState(sessionID, limit, controlMCPInt(args, "detail_chars", 0))
		if err != nil {
			out["transcript_error"] = controlMCPTruncate(err.Error(), 300)
			return out, nil
		}
		out["transcript"] = state["messages"]
	}
	return out, nil
}

// controlMCPMessageJob sends the worker a follow-up in the job's own session:
// more to do, a correction, or a redo, on the same branch, so swarm_get_job
// and swarm_deliver_job keep working on the one job. While the job's run is
// active (its first run or a follow-up) the message is queued for that run:
// the running worker receives it with its next tool result, or, if the run
// ends first, it starts the job's next run. The answer says which
// (sent.delivery). A stopped job's message continues it.
func controlMCPMessageJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	kind, a, b, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if kind == controlMCPJobDesign {
		return nil, controlMCPDesignRefused("swarm_message_job")
	}
	message, _ := args["message"].(string)
	if utf8.RuneCountInString(message) > permission.MaxReplyCharacters {
		return nil, controlMCPToolFailure("message exceeds %d characters; no truncation is performed", permission.MaxReplyCharacters)
	}
	if strings.TrimSpace(message) == "" {
		return nil, controlMCPToolFailure("message is required")
	}
	state, refs, err := c.jobState(kind, a, b)
	if err != nil {
		return nil, err
	}
	if state["status"] == "needs_input" {
		return nil, controlMCPToolFailure("the worker is waiting on a question: reply with swarm_answer_job")
	}
	if refs.sessionID == "" {
		return nil, controlMCPToolFailure("the job has not started yet: wait for it with swarm_get_job, then send the message")
	}
	requestID := controlMCPString(args, "client_request_id")
	if requestID == "" {
		requestID = controlMCPRequestID()
	}
	run, err := c.sendMessage(refs.sessionID, message, requestID)
	if err != nil {
		return nil, controlMCPFollowupRefused(err)
	}
	if queued, _ := run["queued"].(bool); queued {
		run["delivery"] = controlMCPQueuedDelivery(run)
	} else {
		run["delivery"] = "started: the message starts the job's next run on the same branch"
	}
	out := map[string]any{"job_id": controlMCPJobID(kind, a, b), "client_request_id": requestID, "sent": run, "next": "the worker continues in this job: swarm_get_job with wait_seconds waits for it, then swarm_deliver_job merges the branch"}
	if wait := controlMCPInt(args, "wait_seconds", 0); wait > 0 {
		view, err := c.jobView(kind, a, b, wait, false, 0)
		if err != nil {
			return nil, err
		}
		view["job_id"] = out["job_id"]
		view["sent"] = run
		return view, nil
	}
	return out, nil
}

// controlMCPFollowupBusy is how a send refused because another follow-up is
// already running in the job says so: the run-intent store's one-active-run
// refusal and the worker run gate's own conflict both carry it. A reopen
// step answers it busy and waits (jobAutomationReopener.Reopen).
const controlMCPFollowupBusy = "a follow-up is already running in this job"

// controlMCPQueuedDelivery says how a message queued for a job's active run
// reaches the worker.
func controlMCPQueuedDelivery(run map[string]any) string {
	forRun, _ := run["queued_for_run_id"].(string)
	if forRun == "" {
		forRun = "its active run"
	} else {
		forRun = "its active run " + forRun
	}
	return "queued: the job is running (" + forRun + "); the worker receives this message with its next tool result, or, if that run ends first, the message starts the job's next run on the same branch"
}

// controlMCPActiveRunRefusal matches the run-intent store's one-active-run
// refusal, `session "<id>" already has active v3 run "<run>"`.
var controlMCPActiveRunRefusal = regexp.MustCompile(`already has active v3 run "([^"]+)"`)

// controlMCPFollowupRefused says plainly that a follow-up was not sent when
// Swarm refuses to start it: the job's state does not allow it (409), or
// another run in the job's session won the race to start. It names that run.
func controlMCPFollowupRefused(err error) error {
	var refused controlMCPToolError
	if !errors.As(err, &refused) {
		return err
	}
	switch {
	case strings.Contains(refused.message, "already has active v3 run"):
		named := ""
		if match := controlMCPActiveRunRefusal.FindStringSubmatch(refused.message); len(match) == 2 && controlMCPSafeID.MatchString(match[1]) {
			named = " (follow-up run " + match[1] + ")"
		}
		return controlMCPToolFailure("the follow-up was not sent: %s%s; wait for it with swarm_get_job (wait_seconds), then send the next message", controlMCPFollowupBusy, named)
	case refused.status == http.StatusConflict:
		return controlMCPToolFailure("the follow-up was not sent: %s", refused.message)
	}
	return err
}

// controlMCPAnswerJob replies to a worker's question (ask_user). It answers
// questions only: approving a tool call stays with the machine owner.
func controlMCPAnswerJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	kind, a, b, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if kind == controlMCPJobDesign {
		return nil, controlMCPDesignRefused("swarm_answer_job")
	}
	answer, _ := args["answer"].(string)
	if utf8.RuneCountInString(answer) > permission.MaxReplyCharacters {
		return nil, controlMCPToolFailure("answer exceeds %d characters; no truncation is performed", permission.MaxReplyCharacters)
	}
	if strings.TrimSpace(answer) == "" {
		return nil, controlMCPToolFailure("answer is required")
	}
	state, refs, err := c.jobState(kind, a, b)
	if err != nil {
		return nil, err
	}
	questions, _ := state["questions"].([]map[string]any)
	if len(questions) == 0 {
		return nil, controlMCPToolFailure("this job has no open question (status %v)", state["status"])
	}
	questionID, _ := questions[0]["id"].(string)
	if requested := controlMCPString(args, "question_id"); requested != "" {
		questionID = ""
		for _, q := range questions {
			if q["id"] == requested {
				questionID = requested
			}
		}
		if questionID == "" {
			return nil, controlMCPToolFailure("question %q is not open on this job (see swarm_get_job)", requested)
		}
	}
	if _, err := controlMCPResolvePermission(c, map[string]any{"session_id": refs.sessionID, "permission_id": questionID, "action": "allow_once", "answer": answer}); err != nil {
		return nil, err
	}
	return map[string]any{"job_id": controlMCPJobID(kind, a, b), "answered": questionID, "next": "the worker continues: swarm_get_job with wait_seconds waits for it"}, nil
}

func controlMCPDeliverJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	// A task of Swarm's own agents (task/<project>/<task>, a workflow's job
	// id): its workflow steps re-run here; its Git delivery is
	// swarm_integrate_task.
	if projectID, taskID, ok := parseTaskJobID(controlMCPString(args, "job_id")); ok {
		if rerun, _ := args["automation"].(bool); !rerun {
			return nil, controlMCPToolFailure("a task of Swarm's own agents is integrated with swarm_integrate_task; swarm_deliver_job with automation=true re-runs its workflow's failed or stopped steps")
		}
		return c.rerunJobAutomations(jobautomation.KindTask, projectID, taskID)
	}
	kind, a, b, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if rerun, _ := args["automation"].(bool); rerun {
		// Re-run the failed or stopped steps of the job's newest delivery
		// at its current head (per automation); nothing is merged.
		return c.rerunJobAutomations(kind, a, b)
	}
	if kind == controlMCPJobDesign {
		return nil, controlMCPDesignRefused("swarm_deliver_job")
	}
	refs, err := c.jobRefs(kind, a, b)
	if err != nil {
		return nil, err
	}
	if refs.projectID == "" {
		return nil, controlMCPToolFailure("this job has no result to merge yet (see swarm_get_job)")
	}
	if refs.run["role"] == pebblestore.WorkerRoleReviewer {
		return nil, controlMCPToolFailure("reviewer jobs are report-only; there is no branch or file delivery to publish or merge (a reviewer's security fixes reach a job only through the automation step that allowed them: see the delivery's head)")
	}
	// A folder whose owner set a publish command gets a pull request, never
	// a local merge.
	if c.server != nil && c.server.publishConfig != "" {
		view, err := c.jobView(kind, a, b, 0, false, 0)
		if err != nil {
			return nil, err
		}
		result := controlMCPJobResultJSON(view)
		rule, ok, err := c.server.publishRule(c.jobFolder(refs, result))
		if err != nil {
			return nil, controlMCPToolFailure("this folder's publish setting cannot be read: %s", controlMCPTruncate(err.Error(), 200))
		}
		if ok {
			return c.publishJob(kind, a, b, refs, rule, result)
		}
	}
	out, err := controlMCPIntegrate(c, map[string]any{"project_id": refs.projectID, "task_id": refs.taskID}, "swarm_deliver_job", "swarm_get_job")
	if err != nil {
		return nil, err
	}
	result := controlMCPMap(out)
	delete(result, "task")
	result["job_id"] = controlMCPJobID(kind, a, b)
	head, _ := controlMCPMap(result["integration"])["resulting_target_head"].(string)
	if err := c.recordJobActivity(kind, a, b, refs, "job.delivered", head); err != nil {
		return nil, err
	}
	c.signalDelivered(kind, a, b, refs, controlMCPMap(result["integration"]))
	return result, nil
}

// signalDelivered records job.delivered in the machine's feed, so monitors
// and the outbox see that a result was merged, where and at which head.
func (c *controlMCPCall) signalDelivered(kind, a, b string, refs controlMCPJobRefs, integration map[string]any) {
	if c.server == nil || c.server.signals == nil {
		return
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return
	}
	sigRefs := map[string]string{"project_id": refs.projectID, "task_id": refs.taskID}
	if kind == controlMCPJobWorker {
		sigRefs["worker_id"], sigRefs["run_id"] = a, b
	}
	attrs := map[string]string{"job_id": controlMCPJobID(kind, a, b)}
	for _, key := range []string{"target_branch", "resulting_target_head", "state"} {
		if value, _ := integration[key].(string); value != "" {
			attrs[key] = value
		}
	}
	if _, err := c.server.signals.Append(pebblestore.Signal{
		Kind: "job.delivered", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd,
		Account: principal.AccountScopeID, Summary: "Job merged into its folder", DedupKey: "job.delivered:" + b,
		Refs: sigRefs, Attrs: attrs,
	}); err != nil {
		log.Printf("swarmd signal job.delivered not recorded: %v", err)
	}
}

func controlMCPStopJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	kind, a, b, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if kind == controlMCPJobDesign {
		return c.stopDesignJob(a, b)
	}
	if kind == controlMCPJobMedia {
		refs, err := c.jobRefs(kind, a, b)
		if err != nil {
			return nil, err
		}
		if refs.sessionID == "" {
			return nil, controlMCPToolFailure("this media job has not started; nothing to stop")
		}
		out, err := controlMCPStopRun(c, map[string]any{"session_id": refs.sessionID, "reason": controlMCPString(args, "reason")})
		if err == nil && c.server != nil {
			if principal, ok := PrincipalFromRequest(c.request); ok {
				c.server.signalMediaCancelled(principal, a, b, controlMCPString(args, "reason"))
			}
		}
		return out, err
	}
	response, err := c.dispatch(http.MethodPost, "/v3/workers/"+a+"/runs/"+b+"/cancel", nil, map[string]any{})
	if err != nil {
		// A job that already ended has nothing to cancel: say so instead of
		// the run's bare revision or state conflict.
		if state, _, stateErr := c.jobState(kind, a, b); stateErr == nil {
			switch status, _ := state["status"].(string); status {
			case "needs_review", "completed", "failed", "blocked", "cancelled", "rejected", "superseded":
				return map[string]any{"job_id": controlMCPJobID(kind, a, b), "status": status, "stopped": false, "note": "the job already ended; nothing to stop"}, nil
			}
		}
		return nil, controlMCPNotFound(err, "job", controlMCPJobID(kind, a, b), "swarm_list_workers with include_jobs")
	}
	run := controlMCPMap(response["run"])
	status, _ := run["status"].(string)
	if controlMCPRunCancelled(run) {
		// A stopped job's receipt keeps its settled status; the cancel mark
		// closes it.
		status = "cancelled"
	}
	return map[string]any{"job_id": controlMCPJobID(kind, a, b), "status": status}, nil
}

// controlMCPScheduleJob deploys a worker that runs one job on a schedule in
// one folder. The schedule is part of the new worker, so it is approved with
// it; changing another worker's schedules stays with the machine owner.
func controlMCPScheduleJob(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	brief := controlMCPString(args, "brief")
	if brief == "" {
		return nil, controlMCPToolFailure("brief is required: what each scheduled job does and how to know it is done")
	}
	template, err := c.resolveWorker(controlMCPString(args, "worker"))
	if err != nil {
		return nil, err
	}
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	then, hasThen := args["then"]
	if hasThen {
		if err := controlMCPValidateThen(c, then); err != nil {
			return nil, err
		}
	}
	projectID := place.projectID
	name := controlMCPString(args, "name")
	templateName, _ := template["name"].(string)
	if name == "" {
		name = templateName + ": " + controlMCPTruncate(strings.SplitN(brief, "\n", 2)[0], 60)
	}
	instructions, _ := template["instructions"].(string)
	// The worker listing shortens instructions for display; the scheduled
	// worker needs them whole.
	if id, _ := template["id"].(string); controlMCPSafeID.MatchString(id) {
		if full, err := c.worker(id); err == nil {
			if text, _ := full["instructions"].(string); strings.TrimSpace(text) != "" {
				instructions = text
			}
		}
	}
	role, _ := template["role"].(string)
	plan := map[string]any{"title": controlMCPTruncate(name, 120), "goal": brief, "checkpoints": []any{map[string]any{"title": "Scheduled job", "objective": brief, "acceptance_criteria": []any{"Complete the job as briefed"}}}}
	createArgs := map[string]any{"name": name, "instructions": firstNonEmptyString(instructions, brief), "project_id": projectID, "schedule": args["schedule"], "scheduled_plan": plan}
	body, automation, err := controlMCPWorkerCreateBody(createArgs)
	if err != nil {
		return nil, err
	}
	if role != "" {
		body["role"] = role
	}
	path, workspaceID := place.path, place.workspaceID
	created, err := c.dispatch(http.MethodPost, "/v3/workers", nil, body)
	if err != nil {
		return nil, err
	}
	worker := controlMCPMap(created["worker"])
	workerID, _ := worker["id"].(string)
	if !controlMCPSafeID.MatchString(workerID) {
		return nil, controlMCPToolFailure("worker created without a usable id")
	}
	activated, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/activate", nil, map[string]any{
		"expected_revision": controlMCPRevision(worker),
		"local_bindings":    map[string]string{"primary": workspaceID},
	})
	if err != nil {
		return nil, controlMCPToolFailure("scheduled worker %s created but not activated: %s", workerID, err.Error())
	}
	worker = controlMCPMap(activated["worker"])
	scheduleID := ""
	for _, raw := range controlMCPList(worker["automations"]) {
		if a := controlMCPMap(raw); a != nil && a["name"] == automation["name"] {
			scheduleID, _ = a["id"].(string)
		}
	}
	if !controlMCPSafeID.MatchString(scheduleID) {
		return nil, controlMCPToolFailure("scheduled worker %s activated but its schedule id is missing", workerID)
	}
	enabled, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/automations/"+scheduleID+"/enable", nil, map[string]any{"expected_worker_revision": controlMCPRevision(worker)})
	if err != nil {
		return nil, controlMCPToolFailure("scheduled worker %s activated but its schedule was not enabled: %s", workerID, err.Error())
	}
	out := map[string]any{
		"worker":      controlMCPWorkerView(controlMCPMap(enabled["worker"])),
		"schedule_id": scheduleID,
		"project":     map[string]any{"id": place.projectID, "name": place.projectName},
		"workspace":   path,
		"next":        "each run is a job: swarm_list_workers with include_jobs lists them, swarm_activity reports them, swarm_manage_worker pauses or switches the schedule",
	}
	if hasThen && then != nil {
		thenView, err := c.controlMCPThen(then, "then: schedule "+scheduleID, place.path, jobautomation.Scope{Worker: workerID, Schedule: scheduleID})
		if err != nil {
			return nil, controlMCPToolFailure("the schedule %s is set, but its then steps were not: %s", scheduleID, err.Error())
		}
		out["then"] = thenView
	}
	return out, nil
}

func controlMCPRequestMedia(c *controlMCPCall, args map[string]any) (any, error) {
	if retry := controlMCPString(args, "retry_of"); retry != "" {
		if err := c.requireTokenScope("sessions:write"); err != nil {
			return nil, err
		}
		p, ok := PrincipalFromRequest(c.request)
		if !ok {
			return nil, errors.New("media requires authentication")
		}
		place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
		if err != nil {
			return nil, err
		}
		origin, err := c.server.projectTaskOrigin(c.request.Context(), p, place.projectID)
		if err != nil {
			return nil, err
		}
		access, err := c.server.sessions.Store().GetAccountPermissions(p.AccountScopeID)
		if err != nil {
			return nil, err
		}
		input := tool.ProjectTaskCreateInput{OriginSessionID: origin, WorkspacePath: place.path, AutoApprove: access.Tasks == "auto"}
		task, err := c.server.retryMedia(c.request.Context(), p, place.projectID, input, retry, args)
		if err != nil {
			return nil, controlMCPToolFailure("%s", err)
		}
		return mediaJobFileView(map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, place.projectID, task.ID), "status": task.Status, "kind": task.Agent}, task, nil, controlMCPMediaJobGuidance)
	}
	kind := controlMCPMediaKinds[controlMCPString(args, "kind")]
	if kind == "" {
		return nil, controlMCPToolFailure("kind must be image, video or sound")
	}
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	projectID, path := place.projectID, place.path
	brief := controlMCPString(args, "brief")
	if brief == "" && controlMCPString(args, "operation") == "speech" {
		brief = "Generate speech narration"
	}
	taskArgs := map[string]any{"project_id": projectID, "role": kind, "brief": brief, "workspace": path}
	if title := controlMCPString(args, "title"); title != "" {
		taskArgs["title"] = title
	}
	// The model and its options go to the media task as given; Swarm checks
	// them against the model before anything is spent.
	body := map[string]any{}
	for _, arg := range []string{"model", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(controlMCPString(args, arg)); value != "" {
			body[arg] = value
		}
	}
	speech, err := mediaSpeechSettings(args, kind)
	if err != nil {
		return nil, controlMCPToolFailure("%s", err)
	}
	if speech != nil {
		body["operation"], body["speech"] = "speech", speech
	}
	if kind == "video" {
		// One clip, as asked. Without this the task router plans a two-scene
		// story: two paid generations joined into one file of twice the length.
		body["video_type"], body["scenes_count"] = "single", 1
		if operation := strings.TrimSpace(controlMCPString(args, "operation")); operation != "" {
			body["operation"] = operation
		}
	}
	// An image edit is an image request with an input image: the task's
	// operation field is the video one.
	for arg, field := range map[string]string{"duration_seconds": "duration_seconds", "count": "variant_count"} {
		if value := controlMCPInt(args, arg, 0); value > 0 {
			body[field] = value
		}
	}
	inputs, err := c.controlMCPMediaInputs(args["inputs"])
	if err != nil {
		return nil, err
	}
	if len(inputs) > 0 {
		body["attached_media"] = inputs
	}
	taskArgs["_body"] = body
	created, err := controlMCPRunTask(c, taskArgs)
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && strings.Contains(toolErr.message, "HTTP 400") {
			return nil, controlMCPToolFailure("%s (swarm_media_models lists each model's options)", toolErr.message)
		}
		return nil, err
	}
	taskID, _ := controlMCPMap(created)["task_id"].(string)
	if !controlMCPSafeID.MatchString(taskID) {
		return nil, controlMCPToolFailure("Swarm created no media task")
	}
	out, err := c.jobView(controlMCPJobMedia, projectID, taskID, controlMCPInt(args, "wait_seconds", 0), false, 0)
	if err != nil {
		return nil, err
	}
	out["job_id"] = controlMCPJobID(controlMCPJobMedia, projectID, taskID)
	out["kind"] = kind
	return c.controlMCPMediaJobFiles(out, projectID, taskID, nil)
}

// ---- monitoring ------------------------------------------------------------

func controlMCPStatus(c *controlMCPCall, _ map[string]any) (any, error) {
	if err := c.requireTokenScope("signals:read"); err != nil {
		return nil, controlMCPToolFailure("this key cannot read the machine's status (it lacks signals:read; keys made before monitoring existed need replacing)")
	}
	summary, err := c.dispatch(http.MethodGet, "/v3/box/summary", nil, nil)
	if err != nil {
		return nil, err
	}
	out := controlMCPPick(summary, "status", "reasons", "machine", "sandbox", "secrets_gateway", "permissions", "attention", "workers", "runners", "usage", "keys", "running_now")
	if installer := os.Getenv("SWARM_INSTALLER"); installer != "" {
		machine := controlMCPMap(out["machine"])
		if machine == nil {
			machine = map[string]any{}
			out["machine"] = machine
		}
		update := controlMCPMap(machine["update"])
		if update == nil {
			update = map[string]any{}
			machine["update"] = update
		}
		// Quote custom paths as shell arguments; the standard installer path
		// stays directly readable and no command is executed by the daemon.
		if strings.ContainsAny(installer, " \t\r\n'\"\\$`;|&<>()*?[]{}!#~") {
			installer = "'" + strings.ReplaceAll(installer, "'", "'\"'\"'") + "'"
		}
		update["command"] = "sudo bash " + installer + " update"
		update["how"] = "The person (or their AI running as the person, with sudo) runs it on the machine; it waits until no job runs and Swarm restarts by itself."
	}
	boxStatusFeedFields(out, controlMCPMap(summary["outbox"]), controlMCPMap(summary["signals"]))
	return out, nil
}

// boxStatusFeedFields adds what a status derives from a box summary beyond
// its picked keys: the outbox when it is on or failing, and activity_cursor
// (the signal feed's latest sequence) for activity. The home's swarm_status
// and a linked machine's status (ReadFleetMachineStatus) both use it, so
// they carry the same fields.
func boxStatusFeedFields(out, export, feed map[string]any) {
	if export != nil && (export["enabled"] == true || export["failing"] == true) {
		out["outbox"] = export
	}
	if feed != nil {
		if latest, ok := feed["latest_seq"].(float64); ok && latest >= 0 {
			out["activity_cursor"] = strconv.FormatUint(uint64(latest), 10)
		}
	}
}

// controlMCPActivity reads the machine's signal feed after a cursor and adds
// the job id to each worker signal, so the AI can open it with get_job.
func controlMCPActivity(c *controlMCPCall, args map[string]any) (any, error) {
	if cursor, present := args["since_cursor"]; present {
		value, ok := cursor.(string)
		if !ok {
			return nil, controlMCPToolFailure("since_cursor must be a decimal cursor string")
		}
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return nil, controlMCPToolFailure("invalid since_cursor")
		}
		if err := c.requireTokenScope("sessions:read"); err != nil {
			return nil, err
		}
		return c.dispatch(http.MethodGet, "/v3/activity", url.Values{"cursor": {value}}, nil)
	}
	if err := c.requireTokenScope("signals:read"); err != nil {
		return nil, controlMCPToolFailure("this key cannot read the activity feed (it lacks signals:read; keys made before monitoring existed need replacing)")
	}
	query := url.Values{"limit": {strconv.Itoa(controlMCPInt(args, "limit", 30))}}
	if after := controlMCPString(args, "after"); after != "" {
		if _, err := strconv.ParseUint(after, 10, 64); err != nil {
			return nil, controlMCPToolFailure("after must be the activity_cursor from swarm_status or a previous swarm_activity")
		}
		query.Set("after", after)
	}
	if severity := controlMCPString(args, "min_severity"); severity != "" {
		query.Set("min_severity", severity)
	}
	if wait := controlMCPInt(args, "wait_seconds", 0); wait > 0 {
		query.Set("wait", strconv.Itoa(min(wait, maxSignalWaitSeconds)))
	}
	page, err := c.dispatch(http.MethodGet, "/v3/signals", query, nil)
	if err != nil {
		return nil, err
	}
	return controlMCPSignalPage(page), nil
}

// controlMCPSignalPage is a /v3/signals page as swarm_activity shows it (and
// a home's linked activity read): each signal's metadata with its job id,
// the next cursor and whether more wait.
func controlMCPSignalPage(page map[string]any) map[string]any {
	events := []map[string]any{}
	for _, raw := range controlMCPList(page["signals"]) {
		signal := controlMCPMap(raw)
		if signal == nil {
			continue
		}
		event := controlMCPPick(signal, "seq", "at", "kind", "severity", "summary")
		refs := controlMCPMap(signal["refs"])
		workerID, _ := refs["worker_id"].(string)
		runID, _ := refs["run_id"].(string)
		if controlMCPSafeID.MatchString(workerID) && controlMCPSafeID.MatchString(runID) {
			event["job_id"] = controlMCPJobID(controlMCPJobWorker, workerID, runID)
		} else if jobID, _ := controlMCPMap(signal["attrs"])["job_id"].(string); strings.HasPrefix(jobID, controlMCPJobMedia+"/") {
			event["job_id"] = jobID
		}
		if len(refs) > 0 {
			event["refs"] = refs
		}
		events = append(events, event)
	}
	out := map[string]any{"events": events}
	if next, ok := page["next_after"].(float64); ok {
		out["cursor"] = strconv.FormatUint(uint64(next), 10)
	}
	if latest, ok := page["latest_seq"].(float64); ok {
		if next, _ := page["next_after"].(float64); latest > next {
			out["more"] = true
		}
	}
	return out
}

func controlMCPListSecrets(c *controlMCPCall, _ map[string]any) (any, error) {
	if err := c.requireTokenScope("secrets:read"); err != nil {
		return nil, controlMCPToolFailure("this key cannot read secret status (it lacks secrets:read; keys made before this existed need replacing)")
	}
	response, err := c.dispatch(http.MethodGet, "/v3/secrets/status", nil, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"gateway_enabled": response["gateway_enabled"],
		"secrets":         response["secrets"],
		"note":            "Values are never shown. The person runs sudo swarmctl secret set --name NAME --hosts HOST and types the value at its hidden prompt, never the AI. Never ask for a secret or key in chat. After asking the person how long, have them run sudo swarmctl secret grant --name NAME --workspace FOLDER --days N (or --hours N); a job uses the secrets granted to its folder.",
	}, nil
}

// ---- worker models ---------------------------------------------------------

// controlMCPWorkerRoleSlots maps a worker role to its role model setting.
var controlMCPWorkerRoleSlots = map[string]string{
	"coder": "system-coder", "designer": "system-designer", "finder": "system-finder", "reviewer": "system-reviewer", "swarm": "swarm",
}

// roleModels resolves inherited worker models through the execution authority,
// including catalog recommendations and account-default fallbacks.
func (c *controlMCPCall) roleModels(role string) (map[string]map[string]any, error) {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || !principal.Valid() || c.server == nil {
		return nil, errors.New("worker model resolution requires an authenticated account")
	}
	execution, err := c.server.workerExecutionService()
	if err != nil {
		return nil, err
	}
	profile, err := execution.ResolveRoleModelProfile(principal.AccountScopeID, role, nil)
	if err != nil {
		return nil, err
	}
	a := profile.Action
	return map[string]map[string]any{controlMCPWorkerRoleSlots[role]: {"provider": a.Provider, "model": a.Model, "thinking": a.Thinking, "service_tier": a.ServiceTier}}, nil
}

// workerModelSummary resolves only the default this worker inherits. Summary
// failures are data, not failures of an otherwise successful list or mutation.
func (c *controlMCPCall) workerModelSummary(worker map[string]any) map[string]any {
	if pinned := controlMCPWorkerModel(worker, nil); pinned != nil {
		return pinned
	}
	role := controlMCPString(worker, "role")
	if role == "" {
		role = "swarm"
	}
	models, err := c.roleModels(role)
	if err != nil {
		return map[string]any{"source": "role default", "status": "unresolved", "reason": err.Error()}
	}
	return controlMCPWorkerModel(worker, models)
}

// controlMCPWorkerModel is the model a worker's next job uses: its own pinned
// model, or (the default) its role's model setting, which follows changes made
// with swarm_set_agent_model.
func controlMCPWorkerModel(worker map[string]any, roleModels map[string]map[string]any) map[string]any {
	profile := controlMCPMap(worker["model_profile"])
	if profile != nil {
		if follows, _ := profile["use_account_default"].(bool); !follows && profile["action_use_account_default"] != true {
			if pinned := controlMCPPick(controlMCPMap(profile["action"]), "provider", "model", "thinking"); len(pinned) > 0 {
				pinned = controlMCPRoleModel(controlMCPMap(profile["action"]))
				pinned["source"] = "pinned"
				return pinned
			}
		}
	}
	role, _ := worker["role"].(string)
	if role == "" {
		role = "swarm"
	}
	model := roleModels[controlMCPWorkerRoleSlots[role]]
	if model == nil {
		return nil
	}
	out := controlMCPRoleModel(model)
	out["source"] = "role default"
	return out
}

// controlMCPListRuns lists runs across all workers through GET /v3/runs.
func controlMCPListRuns(c *controlMCPCall, args map[string]any) (any, error) {
	query := url.Values{"limit": {strconv.Itoa(controlMCPInt(args, "limit", 50))}}
	for _, name := range []string{"since", "until"} {
		raw, _ := args[name].(string)
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, controlMCPToolFailure("%s must be an RFC 3339 time such as 2026-10-13T00:00:00Z", name)
		}
		query.Set(name, strconv.FormatInt(at.UnixMilli(), 10))
	}
	statuses := []string{}
	for _, raw := range controlMCPList(args["status"]) {
		if status, _ := raw.(string); status != "" {
			statuses = append(statuses, status)
		}
	}
	if len(statuses) > 0 {
		query.Set("status", strings.Join(statuses, ","))
	}
	if worker, _ := args["worker"].(string); strings.TrimSpace(worker) != "" {
		query.Set("worker_id", strings.TrimSpace(worker))
	}
	if cursor, _ := args["cursor"].(string); strings.TrimSpace(cursor) != "" {
		query.Set("cursor", strings.TrimSpace(cursor))
	}
	return c.dispatch(http.MethodGet, "/v3/runs", query, nil)
}
