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
)

// ManageJobs is the native adapter to the existing control handlers. Identity and
// agent name are trusted run inputs, never model arguments. No owner credential
// is created: the internal request carries only the job operations' scopes.
func (s *Server) ManageJobs(ctx context.Context, p identity.Principal, sessionID, agent string, args map[string]any) (any, error) {
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("manage_jobs requires a session principal")
	}
	current, found, err := s.sessions.Store().GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !found || current.AccountScopeID != p.AccountScopeID || current.UserID != p.UserID {
		return nil, errors.New("manage_jobs session principal mismatch")
	}
	action := controlMCPString(args, "action")
	mutate := action == "give_job" || action == "message_job" || action == "rerun_failed_steps"
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
		if controlMCPInt(args, "page_offset", 0) != 0 {
			return nil, errors.New("mutations cannot be replayed for paging")
		}
	}
	scopes := []string{"sessions:read", "automations:read", "swarm:read", "signals:read", "projects:read"}
	if mutate {
		scopes = append(scopes, "sessions:write", "automations:write")
	}
	req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	req = requestWithScopedToken(req, &store.ScopedTokenRecord{Scopes: scopes})
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
	// Only the documented inputs reach handlers; callers cannot smuggle then,
	// model overrides, integration flags or a different authority through this tool.
	clean := map[string]any{}
	for _, key := range []string{"job_id", "worker", "brief", "message", "project", "since", "until", "status", "cursor", "client_request_id", "idempotency_key"} {
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
	if mutate {
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
	case "rerun_failed_steps":
		clean["automation"] = true
		out, err = controlMCPDeliverJob(c, clean)
	case "deliveries":
		var ds *jobautomation.Store
		ds, err = s.jobAutomationStore()
		if err == nil {
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
			var deliveries []jobautomation.Delivery
			jobID := controlMCPString(args, "job_id")
			deliveries, err = ds.ListDeliveries(p.AccountScopeID, jobautomation.DeliveryFilter{AutomationID: automationID, JobID: jobID, Limit: limit})
			views := []map[string]any{}
			for _, d := range deliveries {
				views = append(views, jobDeliveryView(d))
			}
			answer := map[string]any{"deliveries": views}
			if note := deliveryJobIDNote(jobID, len(deliveries)); note != "" && err == nil {
				answer["note"] = note
			}
			out = answer
		}
	default:
		return nil, errors.New("unknown manage_jobs action")
	}
	if err != nil {
		return nil, err
	}
	return nativeJobPage(out, args, mutate)
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
	if action == "give_job" {
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
