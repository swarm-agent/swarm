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
	if tool.FleetSourceInspectionAction(action) {
		// Source discovery and inspection read the machine's own project
		// source routes (project_sources.go) with only their declared
		// selectors: never a path of this home, never another key.
		id, err := segment(str("project_id"))
		if err != nil {
			return nil, err
		}
		var query url.Values
		path, method, query, body, err = fleetSourceRequest(action, ProjectsPath+"/"+id+"/"+projectSourcesSegment, args)
		if err != nil {
			return nil, err
		}
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
	} else {
		for key, value := range args {
			switch key {
			case "action", "machine", "project_id", "task_id", "origin_session_id":
				continue
			}
			body[key] = value
		}
		if err := mapFleetTaskBody(action, args, body); err != nil {
			return nil, err
		}
	}
	if action != "list" && !tool.FleetSourceInspectionAction(action) {
		id := str("project_id")
		if action == "get" {
			// get names its project by id, or by project_id as on this
			// machine (the tool's own get reads id, then project_id).
			if named := str("id"); named != "" {
				id = named
			}
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
	// Source reads name no task record.
	if !tool.FleetSourceInspectionAction(action) {
		s.observeFleetLinkedTasks(p.AccountScopeID, machine, action, args, result, err)
	}
	if err != nil {
		// The machine's own refusal (its status and message) tells the agent
		// what to change; a call that never got an answer says only that.
		var refused *fleet.Error
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("%s refused %s: %w", machine, action, refused)
		}
		return nil, fleetUnreachableError(machine, linked)
	}
	if action == "list" {
		return compactFleetList(result)
	}
	if action == "list_tasks" {
		return compactFleetTaskList(result)
	}
	if err := verifyFleetSourceAnswer(machine, action, args, result); err != nil {
		return nil, err
	}
	return result, nil
}

// fleetSourceRequest maps list_sources, inspect_source and inspect_files to
// the machine's project source route under base (/v3/projects/{id}/sources):
// the method, path, query and body, from the action's declared selectors
// only. Selectors are checked here as the route checks them, so a malformed
// one never crosses the link.
func fleetSourceRequest(action, base string, args map[string]any) (string, string, url.Values, map[string]any, error) {
	text := func(key string) (string, error) {
		switch v := args[key].(type) {
		case nil:
			return "", nil
		case string:
			return strings.TrimSpace(v), nil
		}
		return "", fmt.Errorf("%s must be text", key)
	}
	generation, err := fleetSourceGeneration(args)
	if err != nil {
		return "", "", nil, nil, err
	}
	query := url.Values{}
	switch action {
	case "list_sources":
		return base, http.MethodGet, query, nil, nil
	case "inspect_source":
		for _, key := range []string{"workspace_path", "workspace_id"} {
			value, err := text(key)
			if err != nil {
				return "", "", nil, nil, err
			}
			if value != "" {
				query.Set(key, value)
			}
		}
		if generation > 0 {
			query.Set("workspace_generation", strconv.FormatInt(generation, 10))
		}
		return base + "/" + projectSourceInspectSegment, http.MethodGet, query, nil, nil
	case "inspect_files":
		body := map[string]any{}
		for _, key := range []string{"workspace_path", "workspace_id", "task_id", "attempt_id", "source_session_id", "head_commit"} {
			value, err := text(key)
			if err != nil {
				return "", "", nil, nil, err
			}
			if value != "" {
				body[key] = value
			}
		}
		if generation > 0 {
			body["workspace_generation"] = generation
		}
		if inspection, ok := args["inspection"]; ok && inspection != nil {
			body["inspection"] = inspection
		}
		return base + "/" + projectSourceInspectFilesSegmt, http.MethodPost, query, body, nil
	}
	return "", "", nil, nil, tool.FleetActionUnavailableError(action)
}

// fleetSourceGeneration reads workspace_generation: omitted, or a
// non-negative integer.
func fleetSourceGeneration(args map[string]any) (int64, error) {
	const key = "workspace_generation"
	switch v := args[key].(type) {
	case nil:
		return 0, nil
	case int:
		if v >= 0 {
			return int64(v), nil
		}
	case int64:
		if v >= 0 {
			return v, nil
		}
	case float64:
		if v >= 0 && v == float64(int64(v)) {
			return int64(v), nil
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n >= 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s must be a non-negative integer", key)
}

// verifyFleetSourceAnswer refuses a linked source answer that does not name
// exactly what was asked: inspect_source's source must be the selected one,
// and inspect_files' target.reference must keep the requested project,
// source and (for a task result) the exact task, attempt, session and
// head_commit. A machine can therefore never stand in its current branch
// for a pinned committed result, or another source for the selected one.
func verifyFleetSourceAnswer(machine, action string, args map[string]any, raw json.RawMessage) error {
	if action != "inspect_source" && action != "inspect_files" {
		return nil
	}
	asked := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	generation, _ := fleetSourceGeneration(args)
	mismatch := func(what string) error {
		return fmt.Errorf("%s answered %s for another %s than was asked; nothing was read from it", machine, action, what)
	}
	if action == "inspect_source" {
		var answer struct {
			Source *struct {
				Path                string `json:"path"`
				WorkspaceID         string `json:"workspace_id"`
				WorkspaceGeneration int64  `json:"workspace_generation"`
			} `json:"source"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil || answer.Source == nil || answer.Source.Path == "" || answer.Source.WorkspaceID == "" {
			return fmt.Errorf("%s returned an inspect_source answer without its source", machine)
		}
		if path := asked("workspace_path"); path != "" && path != answer.Source.Path {
			return mismatch("workspace_path")
		}
		if id := asked("workspace_id"); id != "" && id != answer.Source.WorkspaceID {
			return mismatch("workspace_id")
		}
		if generation > 0 && generation != answer.Source.WorkspaceGeneration {
			return mismatch("workspace_generation")
		}
		return nil
	}
	var answer struct {
		Target *struct {
			Reference tool.ProjectInspectionRequest `json:"reference"`
		} `json:"target"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Target == nil {
		return fmt.Errorf("%s returned an inspect_files answer without its target", machine)
	}
	ref := answer.Target.Reference
	if ref.ProjectID != asked("project_id") {
		return mismatch("project")
	}
	if path := asked("workspace_path"); path != "" && path != ref.WorkspacePath {
		return mismatch("workspace_path")
	}
	if id := asked("workspace_id"); id != "" && id != ref.WorkspaceID {
		return mismatch("workspace_id")
	}
	if generation > 0 && generation != ref.WorkspaceGeneration {
		return mismatch("workspace_generation")
	}
	if ref.WorkspacePath == "" || ref.WorkspaceID == "" || ref.WorkspaceGeneration <= 0 {
		return fmt.Errorf("%s returned an inspect_files target without its source identity", machine)
	}
	task := asked("task_id")
	if ref.TaskID != task || ref.AttemptID != asked("attempt_id") || ref.SessionID != asked("source_session_id") {
		return mismatch("task, attempt or session")
	}
	if task != "" {
		if ref.HeadCommit == "" {
			return fmt.Errorf("%s returned a task result without its head_commit", machine)
		}
		if head := asked("head_commit"); head != "" && head != ref.HeadCommit {
			return mismatch("head_commit")
		}
	} else if ref.HeadCommit != "" {
		return mismatch("head_commit")
	}
	return nil
}

// mapFleetTaskBody renames the tool's arguments to the fields the machine's
// task route reads, with the types it reads. The tool names a task revision
// expected_revision (an integer) for every task action; the machine's
// reopen and archive routes read it as revision, while message and refine
// read expected_revision. A revision is checked here as the tool checks it
// on this machine, so it never reaches the machine as text or under a name
// the route ignores. refine_task with a plan document has no linked route:
// the machine's refine route would drop the document, so it is refused.
func mapFleetTaskBody(action string, args, body map[string]any) error {
	switch action {
	case "reopen_task", "archive_task":
		revision, err := fleetTaskRevision(args)
		if err != nil {
			return err
		}
		delete(body, "expected_revision")
		body["revision"] = revision
	case "message_task", "refine_task":
		if action == "refine_task" && (args["plan_document"] != nil || args["document"] != nil) {
			return errors.New("refine_task with a plan document is not available on a linked machine; send feedback (and expected_revision) without plan_document")
		}
		if _, ok := args["expected_revision"]; ok {
			revision, err := fleetTaskRevision(args)
			if err != nil {
				return err
			}
			body["expected_revision"] = revision
		}
	}
	return nil
}

// fleetTaskRevision reads expected_revision under the tool's own rule
// (projectTaskInteger): required, an integer from 1 to 2^30.
func fleetTaskRevision(args map[string]any) (int, error) {
	const key, limit = "expected_revision", 1 << 30
	raw, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	var n int64
	switch v := raw.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		n = int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		n = parsed
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < 1 || n > limit {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, limit)
	}
	return int(n), nil
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
			if key != "status" && key != "exclude_status" {
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
	for _, key := range []string{"status", "exclude_status", "worker_id", "query", "agent", "limit", "cursor", "scope"} {
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

// clipFleetTaskText bounds text a linked machine reported, on a rune boundary.
func clipFleetTaskText(text string, runes int) string {
	if r := []rune(text); len(r) > runes {
		return string(r[:runes])
	}
	return text
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
		if reason, _ := record["limit_reason"].(string); stateTask.Status == pebblestore.ProjectTaskStatusLimitWaiting {
			if r := []rune(reason); len(r) > 300 {
				reason = string(r[:300])
			}
			stateTask.LimitReason = reason
		}
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
			// A stall is the linked machine's own decision (its stall check,
			// docs/task-progress.md "Stalled tasks"): pass its reason through.
			if reason, _ := progress["task_state_reason"].(string); strings.HasPrefix(reason, "stalled: ") {
				if r := []rune(reason); len(r) > 300 {
					reason = string(r[:300])
				}
				item["task_state_reason"] = reason
			}
			// The machine's own state and reason (its task progress, which
			// reads its live records: a pending request, an update drain),
			// carried through as it gave them.
			if state, _ := progress["task_state"].(string); state != "" {
				item["state"] = clipFleetTaskText(state, 32)
			}
			// As on this machine's rows, the reason is shown when the run's
			// live state decides the state (waiting on a person, or held).
			if live := pebblestore.ProjectTaskProgressLive(progress); live != nil {
				if reason, _ := progress["task_state_reason"].(string); strings.TrimSpace(reason) != "" {
					item["state_reason"] = clipFleetTaskText(reason, 300)
				}
				if live.Request != nil {
					request := *live.Request
					request.PermissionID, request.SessionID, request.Kind, request.Tool = clipFleetTaskText(request.PermissionID, 200), clipFleetTaskText(request.SessionID, 200), clipFleetTaskText(request.Kind, 32), clipFleetTaskText(request.Tool, 80)
					request.Summary = clipFleetTaskText(request.Summary, 300)
					item["pending_request"] = request
				}
			}
		}
		if _, given := item["state"]; !given && stateTask.ID != "" {
			// A machine that reports no state: the same rule over what it reported.
			item["state"], _ = pebblestore.ProjectTaskState(stateTask, stateOutcome, stateStale, stateIdle)
		}
		if stateTask.ID != "" && stateTask.Status == pebblestore.ProjectTaskStatusLimitWaiting {
			// Waiting on the provider's limit: say until when, in the machine's
			// own words when it gave them. A live or stall reason stays.
			_, hasReason := item["state_reason"]
			_, stalled := item["task_state_reason"]
			if !hasReason && !stalled {
				reason := ""
				if progress, ok := record["progress"].(map[string]any); ok {
					reason, _ = progress["task_state_reason"].(string)
				}
				if strings.TrimSpace(reason) == "" {
					_, reason = pebblestore.ProjectTaskState(stateTask, stateOutcome, stateStale, stateIdle)
				}
				item["state_reason"] = clipFleetTaskText(reason, 300)
			}
		}
		tasks = append(tasks, item)
	}
	out["tasks"] = tasks
	out["count"] = len(tasks)
	return json.Marshal(out)
}
