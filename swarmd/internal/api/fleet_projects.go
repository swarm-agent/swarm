package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ListFleetMachines exposes link status, never addresses or credentials.
func (s *Server) ListFleetMachines(ctx context.Context, p identity.Principal) ([]tool.FleetMachineInfo, error) {
	out := []tool.FleetMachineInfo{}
	if s.fleet == nil || s.fleetTokens == nil {
		return out, nil
	}
	machines, err := s.fleet.ListMachines()
	if err != nil {
		return nil, errors.New("cannot list linked machines")
	}
	for _, machine := range machines {
		_, owned, err := s.fleetTokens.GetFleetLinkToken(p.AccountScopeID, machine.Name)
		if err != nil {
			return nil, errors.New("cannot read linked machine ownership")
		}
		if !owned {
			continue
		}
		info := tool.FleetMachineInfo{Name: machine.Name, Online: machine.LastContactAt > 0 && machine.LastError == ""}
		if machine.LastError != "" {
			info.LastError = "last contact failed"
		}
		out = append(out, info)
	}
	return out, nil
}

// CallFleetProject maps only the allowed operations to the machine's existing
// project API. Task models, workspaces and lifecycle remain owned by that API.
func (s *Server) CallFleetProject(ctx context.Context, p identity.Principal, machine, action string, args map[string]any) (json.RawMessage, error) {
	if !tool.FleetProjectActionAllowed(action) {
		return nil, tool.FleetActionUnavailableError(action)
	}
	if s.fleet == nil || s.fleetTokens == nil {
		return nil, errors.New("fleet service unavailable")
	}
	str := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	segment := func(value string) (string, error) {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\?#%") {
			return "", errors.New("a valid project or task ID is required")
		}
		return url.PathEscape(value), nil
	}
	path, method := ProjectsPath, http.MethodGet
	body := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "action", "machine", "project_id", "task_id", "origin_session_id":
			continue
		}
		body[key] = value
	}
	if action != "list" {
		id := str("project_id")
		if action == "get" {
			id = str("id")
		}
		id, err := segment(id)
		if err != nil {
			return nil, err
		}
		path += "/" + id
		if action != "get" {
			path += "/tasks"
			switch action {
			case "list_tasks":
				query, err := fleetTaskListQuery(args)
				if err != nil {
					return nil, err
				}
				path += "?" + query.Encode()
			case "propose_task", "create_task":
				method = http.MethodPost
				// The remote service decides whether execution may begin.
			default:
				taskID, err := segment(str("task_id"))
				if err != nil {
					return nil, err
				}
				path += "/" + taskID
				if action != "get_task" {
					method = http.MethodPost
					path += "/" + strings.TrimSuffix(action, "_task")
				}
			}
		}
	}
	client, linked, _, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		return nil, errors.New("linked machine connection unavailable")
	}
	var result json.RawMessage
	var input any
	if method != http.MethodGet {
		input = body
	}
	err = client.JSON(ctx, method, path, input, &result)
	s.noteFleetContact(linked, err)
	// What the link answered about tasks refreshes the home's linked-task
	// records (fleet_linked_tasks.go); the linked wait reads through here too.
	s.observeFleetLinkedTasks(p.AccountScopeID, machine, action, args, result, err)
	if err != nil {
		// The machine's own refusal (its status and message) tells the agent
		// what to change; a call that never got an answer says only that.
		var refused *fleet.Error
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("%s refused %s: %w", machine, action, refused)
		}
		return nil, fmt.Errorf("could not reach linked machine %s", machine)
	}
	if action == "list" {
		return compactFleetList(result)
	}
	if action == "list_tasks" {
		return compactFleetTaskList(result)
	}
	return result, nil
}

// fleetTaskListQuery forwards the list filters to the machine's task list
// route. It asks for current tasks, one page of 25 (at most 50), without
// workflow tasks, unless told otherwise, so the machine filters and pages and
// never returns its whole board.
func fleetTaskListQuery(args map[string]any) (url.Values, error) {
	q := url.Values{"scope": {pebblestore.ProjectTaskScopeCurrent}, "limit": {strconv.Itoa(pebblestore.ProjectTaskListDefaultLimit)}, "include_workflow_tasks": {"false"}}
	switch v := args["include_workflow_tasks"].(type) {
	case nil:
	case bool:
		q.Set("include_workflow_tasks", strconv.FormatBool(v))
	default:
		return nil, errors.New("include_workflow_tasks must be true or false")
	}
	text := func(key string) (string, error) {
		switch v := args[key].(type) {
		case nil:
			return "", nil
		case string:
			return strings.TrimSpace(v), nil
		case float64:
			if v != float64(int64(v)) {
				return "", fmt.Errorf("%s must be an integer", key)
			}
			return strconv.FormatInt(int64(v), 10), nil
		case int:
			return strconv.Itoa(v), nil
		case json.Number:
			return v.String(), nil
		case []any:
			if key != "status" {
				return "", fmt.Errorf("%s must be a single value", key)
			}
			parts := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return "", errors.New("status must be text")
				}
				parts = append(parts, strings.TrimSpace(s))
			}
			return strings.Join(parts, ","), nil
		}
		return "", fmt.Errorf("%s has an unsupported type", key)
	}
	for _, key := range []string{"status", "query", "agent", "limit", "cursor", "scope"} {
		value, err := text(key)
		if err != nil {
			return nil, err
		}
		if value != "" {
			q.Set(key, value)
		}
	}
	if _, err := pebblestore.NormalizeProjectTaskScope(q.Get("scope")); err != nil {
		return nil, err
	}
	if limit, err := pebblestore.ParseProjectTaskListLimit(q.Get("limit")); err != nil || limit > pebblestore.ProjectTaskAgentListMaxLimit {
		return nil, fmt.Errorf("limit must be an integer from 1 to %d", pebblestore.ProjectTaskAgentListMaxLimit)
	}
	if _, err := pebblestore.ParseProjectTaskListCursor(q.Get("cursor")); err != nil {
		return nil, err
	}
	return q, nil
}

// compactFleetTaskList keeps the page facts and the list-row fields of each
// task (projectTaskListRow's identity fields), with the outcome, stale and
// needs_attention flags the machine computed and its count of reviewer
// tasks; full records
// stay in get_task. The counts are the machine's outcome_counts, the same
// buckets as list_tasks on this machine; raw status counts are left out
// (needs_review only says a run ended, not that anyone must review it).
func compactFleetTaskList(raw json.RawMessage) (json.RawMessage, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, key := range []string{"total", "next_cursor", "cursor", "limit", "scope", "outcome_counts", "workflow_tasks"} {
		if v, ok := data[key]; ok {
			out[key] = v
		}
	}
	// The hint is this machine's text, written from the machine's count.
	if hidden, _ := out["workflow_tasks"].(float64); hidden > 0 {
		out["workflow_tasks_hint"] = tool.WorkflowTasksHint
	}
	rows, _ := data["tasks"].([]any)
	if len(rows) > pebblestore.ProjectTaskListMaxLimit {
		return nil, errors.New("linked machine returned more tasks than one page")
	}
	tasks := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		record, ok := row.(map[string]any)
		if !ok {
			return nil, errors.New("invalid linked task record")
		}
		item := map[string]any{}
		for _, key := range []string{"id", "title", "status", "agent", "session_id", "updated_at"} {
			if v, ok := record[key]; ok {
				if text, ok := v.(string); ok && len([]rune(text)) > 256 {
					v = string([]rune(text)[:256])
				}
				if key == "status" {
					// The run's status, as list_tasks on this machine names it.
					key = "run_status"
				}
				item[key] = v
			}
		}
		// The task's state from the same rule as this machine's list rows
		// (pebblestore.ProjectTaskState), over the record and progress the
		// linked machine reported.
		stateTask := pebblestore.ProjectTaskRecord{}
		stateTask.ID, _ = record["id"].(string)
		stateTask.Status, _ = record["status"].(string)
		stateTask.AutomationID, _ = record["automation_id"].(string)
		stateTask.WorktreeBranch, _ = record["worktree_branch"].(string)
		stateTask.Archived, _ = record["archived"].(bool)
		stateOutcome, stateStale, stateIdle := "", false, false
		if archived, _ := record["archived"].(bool); archived {
			item["archived"] = true
		}
		if reviews, _ := record["reviews"].(float64); reviews > 0 && reviews == float64(int(reviews)) {
			item["reviews"] = int(reviews)
		}
		if progress, ok := record["progress"].(map[string]any); ok {
			if outcome, _ := progress["outcome"].(string); strings.TrimSpace(outcome) != "" {
				if r := []rune(outcome); len(r) > 300 {
					outcome = string(r[:300])
				}
				item["outcome"] = outcome
			}
			if stale, _ := progress["stale"].(bool); stale {
				item["stale"] = true
			}
			// The machine's own decision (pebblestore.ProjectTaskNeedsAttention).
			if attention, _ := progress["needs_attention"].(bool); attention {
				item["needs_attention"] = true
			}
			stateOutcome, _ = progress["outcome"].(string)
			stateStale, _ = progress["stale"].(bool)
			stateIdle, _ = progress["idle"].(bool)
		}
		if stateTask.ID != "" {
			item["state"], _ = pebblestore.ProjectTaskState(stateTask, stateOutcome, stateStale, stateIdle)
		}
		tasks = append(tasks, item)
	}
	out["tasks"] = tasks
	out["count"] = len(tasks)
	return json.Marshal(out)
}
