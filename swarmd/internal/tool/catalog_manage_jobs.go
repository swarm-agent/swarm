package tool

// manage_jobs in the catalog: the six actions the Orchestrator had, plus
// answer_job, deliver_job, stop_job and schedule_job, which run the same
// functions as the swarm_* job tools (swarm_answer_job, swarm_deliver_job,
// swarm_stop_job, swarm_schedule_job). The Orchestrator's definition
// (manageJobsDefinition) and Swarm Control's manage_jobs tool are generated
// from this declaration; one handler (Runtime.executeManageJobs) serves both.

const manageJobsToolName = "manage_jobs"

// ManageJobsToolName is manage_jobs' name for the services behind it.
const ManageJobsToolName = manageJobsToolName

// JobsWaitingStatuses are the job statuses that wait on someone: a question
// for answer_job, or a tool approval the machine owner gives. A job answer
// with one of them is in the envelope state waiting.
var JobsWaitingStatuses = []string{"needs_input", "waiting_on_approval", "pending_approval"}

const manageJobsDescription = "Worker jobs: read runs, jobs and workflow deliveries; give a deployed worker a job, message it, answer its question, deliver (merge) or stop it, and schedule a worker that runs a job on a schedule. Mutations require an explicit owner permission grant for this agent (manage_jobs.<agent>.<action>); self-targeting is forbidden. A job waiting on a question or approval answers with state waiting. Read results over the inline limit return JSON text pages: next names the same read with page_offset and page_digest; concatenate json_page. No host files are created." + FleetWorkDescription

func manageJobsFields() []CatalogField {
	str := func(description string) map[string]any {
		if description == "" {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "string", "description": description}
	}
	return []CatalogField{
		{"machine", map[string]any{"type": "string", "description": FleetWorkDescription}},
		{"job_id", str("")},
		{"id", str("Workflow ID for deliveries.")},
		{"worker", str("")},
		{"brief", str("")},
		{"message", str("")},
		{"answer", str("answer_job: the reply to the worker's question. It answers questions only, never approves a tool call.")},
		{"question_id", str("answer_job: which question, when there are several (from get_job).")},
		{"reason", str("stop_job: why it stops.")},
		{"schedule", map[string]any{"type": "object", "description": "schedule_job: kind interval (interval_seconds, at least 60) or cron (5-field cron and an IANA timezone).", "required": []string{"kind"}, "properties": map[string]any{
			"kind":             map[string]any{"type": "string", "enum": []string{"interval", "cron"}},
			"interval_seconds": map[string]any{"type": "integer", "minimum": 60},
			"cron":             map[string]any{"type": "string", "maxLength": 100},
			"timezone":         map[string]any{"type": "string", "maxLength": 100},
		}}},
		{"name", str("schedule_job: name for the scheduled worker.")},
		{"project", str("")},
		{"folder", str("")},
		{"since", str("")},
		{"until", str("")},
		{"status", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		{"limit", map[string]any{"type": "integer", "minimum": 1, "maximum": 50}},
		{"cursor", str("")},
		{"client_request_id", str("")},
		{"idempotency_key", str("")},
		{"workspace", str("give_job, schedule_job: the same as folder (which of the project's folders, an absolute path).")},
		{"files", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "give_job: path prefixes the job may change (relative to the folder); written into the brief as its scope."}},
		{"review_job", str("give_job, reviewer workers only: task/<project>/<task> or worker/<worker>/<run> of a job in the same project, with commits; its exact head is reviewed, report-only.")},
		{"review_checkout", map[string]any{"type": "string", "enum": []string{"same", "new"}, "description": "give_job, reviewer workers only: same (default: the reviewed job's own worktree, read-only) or new (a disposable checkout at that head)."}},
		{"automation", map[string]any{"type": "boolean", "description": "deliver_job: instead of merging, re-run only the failed or stopped steps of the job's workflow deliveries (as rerun_failed_steps)."}},
		{"include_diff", map[string]any{"type": "boolean", "description": "get_job: also return the patch against the base (cut at diff_chars)."}},
		{"diff_chars", map[string]any{"type": "integer", "minimum": 0, "maximum": 200000, "description": "get_job: diff size limit when include_diff is set."}},
		{"include_transcript", map[string]any{"type": "boolean", "description": "get_job: also return the worker's latest messages and tool calls in this job (truncated; tool results summarized)."}},
		{"transcript_messages", map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "get_job: how many latest messages include_transcript returns. Default 10."}},
		{"detail_chars", map[string]any{"type": "integer", "minimum": 0, "maximum": 20000, "description": "get_job: raise the per-message character limit of the transcript."}},
		{"wait_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": ControlTaskWaitMaxSeconds, "description": "Swarm Control: wait up to this many seconds (at most 110) until the job settles (needs_review, completed, failed, needs_input or cancelled). Default 0."}},
		{"model", map[string]any{"description": "give_job, Swarm Control: the model for this job (provider, model, thinking, optional fast), instead of the worker's.", "type": "object", "required": []string{"provider", "model", "thinking"}, "properties": map[string]any{"provider": map[string]any{"type": "string"}, "model": map[string]any{"type": "string"}, "thinking": map[string]any{"type": "string"}, "fast": map[string]any{"type": "boolean"}}}},
		{"then", map[string]any{"type": "object", "description": "give_job, schedule_job, Swarm Control: steps to run when the job ends (a workflow scoped to this job: steps, when, retries, as manage_workflows propose). Without it nothing runs.", "required": []string{"steps"}, "properties": map[string]any{
			"steps":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"when":    map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "always"}},
			"retries": map[string]any{"type": "integer", "minimum": 0},
		}}},
		{"page_digest", str("Digest from the first page; required for continuation.")},
		{"page_offset", map[string]any{"type": "integer", "minimum": 0, "description": "UTF-8 byte offset returned by a previous oversized read; repeat identical filters."}},
	}
}

func manageJobsCatalog() CatalogTool {
	const (
		read  = CatalogLevelRead
		write = CatalogLevelWrite
		anyM  = CatalogReachAny
	)
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	page := []string{"page_digest", "page_offset"}
	with := func(base []string, more ...string) []string {
		return append(append([]string(nil), base...), more...)
	}
	// Levels are remote.ToolScope of the swarm_* tool with the same effect:
	// swarm_list_runs, swarm_get_job, swarm_list_workflows (deliveries),
	// swarm_deliver_job (rerun_failed_steps is its automation re-run and
	// deliver_job its merge), swarm_give_job, swarm_message_job,
	// swarm_answer_job, swarm_stop_job and swarm_schedule_job.
	actions := []CatalogAction{
		{Action: "list_runs", Inputs: with([]string{"machine", "worker", "status", "since", "until", "limit", "cursor"}, page...), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "This machine's worker runs across every worker, newest first, with job_id, worker, status, source and times; next_cursor pages back in time."},
		{Action: "get_job", Inputs: with([]string{"machine", "job_id", "include_diff", "diff_chars", "include_transcript", "transcript_messages", "detail_chars"}, page...), ControlInputs: []string{"wait_seconds"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "A job's status, open questions, result (branch, commits, files, report, cost) and its workflow deliveries."},
		{Action: "deliveries", Inputs: with([]string{"machine", "id", "job_id", "limit"}, page...), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "A workflow's (id) or job's (job_id) deliveries and their steps."},
		{Action: "rerun_failed_steps", Inputs: []string{"machine", "job_id"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Re-run only the failed or stopped steps of the job's workflow deliveries."},
		{Action: "give_job", Inputs: []string{"machine", "worker", "brief", "project", "folder", "workspace", "files", "review_job", "review_checkout", "idempotency_key"}, ControlInputs: []string{"model", "then", "wait_seconds"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Start a job: a deployed worker (id or name) works on the brief in one of the project's folders."},
		{Action: "message_job", Inputs: []string{"machine", "job_id", "message", "client_request_id"}, ControlInputs: []string{"wait_seconds"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Send the worker a follow-up in the same job (more to do, a correction, a redo) on the same branch."},
		{Action: "answer_job", Inputs: []string{"machine", "job_id", "answer", "question_id"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Reply to the question a waiting job asked (status needs_input; get_job shows it). It answers questions only, never approves a tool call."},
		{Action: "deliver_job", Inputs: []string{"machine", "job_id", "automation"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Deliver a needs_review job's commits: a local git merge into its folder's branch, or a pull request where the folder's owner set up publishing. Refused when it committed nothing or on conflicts."},
		{Action: "stop_job", Inputs: []string{"machine", "job_id", "reason"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Cancel a running or stopped job for good; its branch and commits stay for reading."},
		{Action: "schedule_job", Inputs: []string{"machine", "worker", "brief", "schedule", "project", "folder", "workspace", "name"}, ControlInputs: []string{"then"}, Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Deploy a worker that runs one job on a schedule in one folder of a project, based on a deployed worker's role and instructions."},
	}
	// The API scopes of the route behind each action: /v3/runs reads
	// signals; a job is read with sessions:read; deliveries are workflow
	// records (automations); every change runs on the job's session or
	// worker routes and needs sessions:write first (the handler's own check).
	scopes := map[string][]string{
		"list_runs":  {"signals:read"},
		"get_job":    {"sessions:read"},
		"deliveries": {"automations:read", "automations:write"},
	}
	nextActions := map[string]string{
		"list_runs":    "get_job with a run's job_id reads it.",
		"give_job":     "get_job with the job_id reads it as it works and when it ends.",
		"message_job":  "get_job with the job_id reads it as it works.",
		"answer_job":   "get_job with the job_id reads it as it continues.",
		"deliver_job":  "get_job with the job_id reads the merge state.",
		"schedule_job": "list_runs with the worker lists its runs as the schedule runs them.",
	}
	for i := range actions {
		actions[i].Tool = manageJobsToolName
		actions[i].NextAction = nextActions[actions[i].Action]
		actions[i].WaitingStatuses = JobsWaitingStatuses
		if declared, ok := scopes[actions[i].Action]; ok {
			actions[i].APIScopes = declared
		} else {
			actions[i].APIScopes = []string{"sessions:write"}
		}
	}
	return CatalogTool{Name: manageJobsToolName, Description: manageJobsDescription, Fields: manageJobsFields(), Actions: actions}
}

func manageJobsDefinition() Definition {
	return manageJobsCatalog().OrchestratorDefinition()
}
