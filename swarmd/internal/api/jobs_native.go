package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// nativeJobMutations are the manage_jobs actions that change a job or start
// agent work: each needs the owner's grant for a session caller.
var nativeJobMutations = map[string]bool{
	"give_job": true, "message_job": true, "rerun_failed_steps": true,
	"answer_job": true, "deliver_job": true, "stop_job": true, "schedule_job": true,
}

// nativeJobReadScopes and nativeJobWriteScopes are the API scopes a job
// action's internal request carries: reads, and changes as well. A Swarm
// Control caller holds only those its key holds too.
var (
	nativeJobReadScopes  = []string{"sessions:read", "automations:read", "swarm:read", "signals:read", "projects:read"}
	nativeJobWriteScopes = []string{"sessions:write", "automations:write"}
)

// nativeJobInputs are the only arguments that reach the job handlers:
// callers cannot smuggle then, model overrides, integration flags or a
// different authority through manage_jobs.
var nativeJobInputs = []string{"job_id", "worker", "brief", "message", "answer", "question_id", "reason", "schedule", "name", "project", "since", "until", "status", "cursor", "client_request_id", "idempotency_key"}

// ManageJobs is the one job service behind manage_jobs: the Orchestrator and
// every agent granted manage_jobs (a session caller), Swarm Control (a caller
// whose key scopes ride in ctx, tool.ControlScopesFromContext) and a linked
// home (POST /v3/jobs/{action}) all run it, and it runs the same handlers as
// the swarm_* job tools. Identity and agent name are trusted run inputs,
// never model arguments. No owner credential is created: the internal
// request carries only the job operations' scopes. With machine in args the
// action runs on that linked machine's own ManageJobs and its answer comes
// back as it came.
func (s *Server) ManageJobs(ctx context.Context, p identity.Principal, sessionID, agent string, args map[string]any) (any, error) {
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("manage_jobs requires a session principal")
	}
	action := controlMCPString(args, "action")
	if _, ok := manageJobsAction(action); !ok {
		return nil, errors.New("unknown manage_jobs action")
	}
	mutate := nativeJobMutations[action]
	machine := controlMCPString(args, "machine")
	held, control := tool.ControlScopesFromContext(ctx)
	if control {
		return s.manageJobsAs(ctx, p, held, control, store.SessionSnapshot{}, action, machine, args)
	}
	current, found, err := s.sessions.Store().GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !found || current.AccountScopeID != p.AccountScopeID || current.UserID != p.UserID {
		return nil, errors.New("manage_jobs session principal mismatch")
	}
	if mutate {
		// Worker identity comes from the account-owned record, not the base
		// system agent or model-supplied arguments.
		if workerID, _ := current.Metadata["worker_id"].(string); workerID != "" {
			worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, errors.New("manage_jobs acting worker not found")
			}
			agent = strings.TrimSpace(worker.Agent)
			if agent == "" || strings.HasPrefix(agent, "system-worker-") {
				agent = "worker_" + worker.ID
			}
		}
		if s.perm == nil {
			return nil, fmt.Errorf("owner must allow manage_jobs.%s.%s in account permission rules", agent, action)
		}
		policy, err := s.perm.CurrentPolicyForAccount(p.AccountScopeID)
		if err != nil {
			return nil, err
		}
		if !nativeJobActionGranted(policy, agent, action) {
			return nil, fmt.Errorf("owner must allow manage_jobs.%s.%s in account permission rules", agent, action)
		}
	}
	return s.manageJobsAs(ctx, p, nil, false, current, action, machine, args)
}

// manageJobsAs runs one job action for a caller already checked: a session
// caller (current, its grant checked) or a Swarm Control or linked caller
// (control, holding held).
func (s *Server) manageJobsAs(ctx context.Context, p identity.Principal, held []string, control bool, current store.SessionSnapshot, action, machine string, args map[string]any) (any, error) {
	mutate := nativeJobMutations[action]
	if mutate && controlMCPInt(args, "page_offset", 0) != 0 {
		return nil, errors.New("mutations cannot be replayed for paging")
	}
	if machine != "" {
		return s.manageJobsLinked(ctx, p, control, current, action, machine, args)
	}
	scopes := append([]string(nil), nativeJobReadScopes...)
	if mutate {
		scopes = append(scopes, nativeJobWriteScopes...)
	}
	if control {
		// A Swarm Control or linked caller acts only within its key.
		scopes = nativeJobScopesHeld(scopes, held)
	}
	req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	req = requestWithScopedToken(req, &store.ScopedTokenRecord{Scopes: scopes})
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
	clean := map[string]any{}
	for _, key := range nativeJobInputs {
		if v, ok := args[key]; ok {
			clean[key] = v
		}
	}
	clean["workspace"] = controlMCPString(args, "folder")
	limit := controlMCPInt(args, "limit", 50)
	if limit < 1 || limit > 50 {
		return nil, errors.New("limit must be between 1 and 50")
	}
	clean["limit"] = float64(limit)
	if mutate && !control {
		if err := s.refuseNativeJobSelfTarget(c, current, action, clean); err != nil {
			return nil, err
		}
	}
	var out any
	switch action {
	case "list_runs":
		out, err = controlMCPListRuns(c, clean)
	case "get_job":
		out, err = controlMCPGetJob(c, clean)
	case "give_job":
		out, err = controlMCPGiveJob(c, clean)
	case "message_job":
		out, err = controlMCPMessageJob(c, clean)
	case "answer_job":
		out, err = controlMCPAnswerJob(c, clean)
	case "deliver_job":
		out, err = controlMCPDeliverJob(c, clean)
	case "stop_job":
		out, err = controlMCPStopJob(c, clean)
	case "schedule_job":
		out, err = controlMCPScheduleJob(c, clean)
	case "rerun_failed_steps":
		clean["automation"] = true
		out, err = controlMCPDeliverJob(c, clean)
	case "deliveries":
		if !nativeJobScopeHeld(scopes, "automations:read") {
			return nil, controlMCPToolFailure("this connection lacks the automations:read permission")
		}
		out, err = s.nativeJobDeliveries(p, args, limit)
	default:
		return nil, errors.New("unknown manage_jobs action")
	}
	if err != nil {
		return nil, err
	}
	return nativeJobPage(out, args, mutate)
}

// nativeJobDeliveries lists a workflow's (id or name) or a job's deliveries.
func (s *Server) nativeJobDeliveries(p identity.Principal, args map[string]any, limit int) (any, error) {
	ds, err := s.jobAutomationStore()
	if err != nil {
		return nil, err
	}
	automationID := controlMCPString(args, "id")
	if automationID != "" {
		automation, lookupErr := ds.Get(p.AccountScopeID, automationID)
		if errors.Is(lookupErr, jobautomation.ErrNotFound) {
			automation, lookupErr = ds.FindByName(p.AccountScopeID, automationID)
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("workflow %q: %w", automationID, lookupErr)
		}
		automationID = automation.ID
	}
	jobID := controlMCPString(args, "job_id")
	deliveries, err := ds.ListDeliveries(p.AccountScopeID, jobautomation.DeliveryFilter{AutomationID: automationID, JobID: jobID, Limit: limit})
	if err != nil {
		return nil, err
	}
	views := []map[string]any{}
	for _, d := range deliveries {
		views = append(views, jobDeliveryView(d))
	}
	answer := map[string]any{"deliveries": views}
	if note := deliveryJobIDNote(jobID, len(deliveries)); note != "" {
		answer["note"] = note
	}
	return answer, nil
}

// manageJobsLinked runs the action on a linked machine's own ManageJobs
// (POST /v3/jobs/{action}) and returns its answer as it came. Linked work is
// the home Orchestrator's (or a Swarm Control caller's): a task's own
// session never reaches it. Under linked.{name}.work ask, an action that
// starts or continues agent work there waits for the person: a session
// caller has no approval prompt for it here, so it is refused rather than
// sent; a Swarm Control caller's person approves in their own AI's chat.
func (s *Server) manageJobsLinked(ctx context.Context, p identity.Principal, control bool, current store.SessionSnapshot, action, machine string, args map[string]any) (any, error) {
	if !control {
		if project, _ := current.Metadata["task_id"].(string); project != "" {
			return nil, errors.New("linked work is only available to the home Orchestrator")
		}
		if task, _ := current.Metadata["project_task_id"].(string); task != "" {
			return nil, errors.New("linked work is only available to the home Orchestrator")
		}
		if tool.FleetTaskStart(tool.ManageJobsToolName, action) {
			access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
			if err != nil {
				return nil, err
			}
			if access.Link(machine).Work == store.RuleAsk {
				return nil, fmt.Errorf("linked.%s.work is ask: manage_jobs %s starts work on %s only with the person's approval, and manage_jobs has no approval prompt for it; the person can set the rule to auto on the Permissions page", machine, action, machine)
			}
		}
	}
	body := map[string]any{}
	for key, value := range args {
		if key != "machine" && key != "action" {
			body[key] = value
		}
	}
	return s.CallFleetWork(ctx, p, machine, tool.ManageJobsToolName, action, body)
}

// nativeJobScopesHeld keeps the scopes the caller's key holds.
func nativeJobScopesHeld(scopes, held []string) []string {
	var out []string
	for _, scope := range scopes {
		if nativeJobScopeHeld(held, scope) {
			out = append(out, scope)
		}
	}
	return out
}

func nativeJobScopeHeld(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// manageJobsAction is the declared manage_jobs action.
func manageJobsAction(name string) (tool.CatalogAction, bool) {
	catalog, ok := tool.CatalogToolNamed(tool.ManageJobsToolName)
	if !ok {
		return tool.CatalogAction{}, false
	}
	return catalog.Action(name)
}

func nativeJobActionGranted(policy permission.Policy, agent, action string) bool {
	key := "manage_jobs." + agent + "." + action
	allowed := false
	for _, r := range policy.Rules {
		if r.Kind != permission.PolicyRuleKindTool {
			continue
		}
		if r.Decision == permission.PolicyDecisionDeny && (r.Tool == key || r.Tool == "manage_jobs") {
			return false
		}
		if r.Tool == key && r.Decision == permission.PolicyDecisionAllow {
			allowed = true
		}
	}
	return allowed
}

func (s *Server) refuseNativeJobSelfTarget(c *controlMCPCall, current store.SessionSnapshot, action string, args map[string]any) error {
	ownWorker, _ := current.Metadata["worker_id"].(string)
	ownRun, _ := current.Metadata["worker_execution_run_id"].(string)
	ownJob := "worker/" + ownWorker + "/" + ownRun
	// give_job and schedule_job name a worker, not a job: never the caller's
	// own worker.
	if action == "give_job" || action == "schedule_job" {
		worker, err := c.resolveWorker(controlMCPString(args, "worker"))
		if err != nil {
			return err
		}
		if ownWorker != "" && worker["id"] == ownWorker {
			return errors.New("manage_jobs cannot start its own worker (self-loop)")
		}
		return nil
	}
	target := controlMCPString(args, "job_id")
	if ownWorker != "" && target == ownJob {
		return errors.New("manage_jobs cannot act on its own job")
	}
	var refs controlMCPJobRefs
	var err error
	if project, task, ok := parseTaskJobID(target); ok {
		var t map[string]any
		t, err = c.task(project, task)
		refs.sessionID, _ = t["session_id"].(string)
	} else {
		var kind, a, b string
		kind, a, b, err = controlMCPParseJobID(args)
		if err == nil {
			refs, err = c.jobRefs(kind, a, b)
		}
	}
	if err != nil {
		return err
	}
	if refs.sessionID == current.ID {
		return errors.New("manage_jobs cannot act on its own job")
	}
	// A workflow child must not restart/message the job whose delivery owns it.
	ds, err := s.jobAutomationStore()
	if err != nil {
		return err
	}
	deliveries, err := ds.JobDeliveries(current.AccountScopeID, target)
	if err != nil {
		return err
	}
	ownProject, _ := current.Metadata["project_id"].(string)
	ownTask, _ := current.Metadata["task_id"].(string)
	ownTaskJob := "task/" + ownProject + "/" + ownTask
	for _, d := range deliveries {
		for _, step := range d.Steps {
			job, _ := step.Outcome["job_id"].(string)
			if job == ownJob || job == ownTaskJob {
				return errors.New("manage_jobs cannot act on its own workflow delivery")
			}
			for _, item := range step.Items {
				if item.JobID == ownJob || item.JobID == ownTaskJob {
					return errors.New("manage_jobs cannot act on its own workflow delivery")
				}
			}
		}
	}
	return nil
}

// Pages are lossless JSON text, bounded even under JSON escaping. A digest pins
// continuation reads to the same value instead of silently joining stale pages.
func nativeJobPage(out any, args map[string]any, mutation bool) (any, error) {
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	offset := controlMCPInt(args, "page_offset", 0)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if offset < 0 || offset > len(data) || (offset < len(data) && !utf8.RuneStart(data[offset])) {
		return nil, errors.New("invalid page_offset")
	}
	if offset > 0 && controlMCPString(args, "page_digest") != digest {
		return nil, errors.New("result changed or page_digest missing; restart paging at zero")
	}
	if len(data) <= 60*1024 && offset == 0 {
		return out, nil
	}
	if mutation {
		return map[string]any{"completed": true, "job_id": controlMCPMap(out)["job_id"], "result_omitted": true, "next": "Read get_job or deliveries to inspect the completed operation; do not repeat the mutation."}, nil
	}
	end := offset + 8*1024
	if end > len(data) {
		end = len(data)
	}
	for end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	page := map[string]any{"json_page": string(data[offset:end]), "page_offset": offset, "page_digest": digest, "total_bytes": len(data)}
	if end < len(data) {
		page["next_page_offset"] = end
	}
	return page, nil
}
