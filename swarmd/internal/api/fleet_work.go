package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// CallFleetWork deliberately accepts operations rather than remote URLs.
func (s *Server) CallFleetWork(ctx context.Context, p identity.Principal, machine, name, action string, args map[string]any) (json.RawMessage, error) {
	if !tool.FleetWorkActionAllowed(name, action) {
		return nil, errors.New("operation is not available on a linked machine")
	}
	if rotate, _ := args["rotate_key"].(bool); rotate {
		return nil, errors.New("remote key rotation is forbidden")
	}
	if s.sessions == nil {
		return nil, errors.New("session service unavailable")
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return nil, err
	}
	if level := access.Link(machine).Work; level != "ask" && level != "allowed" {
		return nil, fmt.Errorf("linked.%s.work is off", machine)
	}
	path, method := "/v3/workers", http.MethodGet
	if name == "manage_workflows" {
		path = JobAutomationsPath
	}
	id := controlMCPString(args, "id")
	if id == "" {
		id = controlMCPString(args, "worker_id")
	}
	query := url.Values{}
	for _, key := range []string{"limit", "cursor", "lifecycle_state"} {
		if v, ok := args[key]; ok {
			query.Set(key, fmt.Sprint(v))
		}
	}
	switch action {
	case "list":
	case "propose":
		path += "/propose"
		method = http.MethodPost
	case "history", "deliveries":
		if action == "history" {
			path += "/history"
			query.Set("id", id)
		} else {
			path = JobDeliveriesPath
			query.Set("automation_id", id)
			query.Set("job_id", controlMCPString(args, "job_id"))
			query.Set("limit", "50")
		}
	default:
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#%") {
			return nil, errors.New("a valid remote record id is required")
		}
		path += "/" + url.PathEscape(id)
		switch action {
		case "get", "inspect":
		case "runs":
			path += "/runs"
		case "pause", "resume", "archive", "enable", "disable":
			path += "/" + action
			method = http.MethodPost
		case "delete":
			method = http.MethodDelete
		default:
			return nil, errors.New("this remote mutation requires a proposal-preserving HTTP route; unavailable")
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	body := map[string]any{}
	for _, key := range []string{"expected_revision", "client_request_id"} {
		if v, ok := args[key]; ok {
			body[key] = v
		}
	}
	if action == "propose" {
		for _, key := range []string{"id", "name", "project", "workspace", "scope", "when", "enabled", "retries", "revision", "result", "steps", "on", "inputs", "with"} {
			if v, ok := args[key]; ok {
				body[key] = v
			}
		}
	}
	client, linked, _, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		return nil, errors.New("linked machine connection unavailable")
	}
	var input any
	if method != http.MethodGet {
		input = body
	}
	var result json.RawMessage
	err = client.JSON(ctx, method, path, input, &result)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, fmt.Errorf("linked machine %s refused %s: %w", machine, action, err)
	}
	if action == "list" {
		if name == "manage_workers" || name == "manage_automation" {
			return compactFleetWorkers(result, func(id, cursor string) (json.RawMessage, error) {
				var page json.RawMessage
				err := client.JSON(ctx, http.MethodGet, WorkersPath+"/"+url.PathEscape(id)+"/runs?limit=100&cursor="+url.QueryEscape(cursor), nil, &page)
				s.noteFleetContact(linked, err)
				return page, err
			})
		}
		return compactFleetList(result)
	}
	return result, nil
}

// Whitelist list fields: definitions, instructions and step outcomes stay in get.
func compactFleetList(raw json.RawMessage) (json.RawMessage, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, key := range []string{"next_cursor", "stopped"} {
		if v, ok := data[key]; ok {
			out[key] = v
		}
	}
	for _, collection := range []string{"automations", "workers", "projects"} {
		rows, ok := data[collection].([]any)
		if !ok {
			continue
		}
		items := []any{}
		for _, row := range rows {
			record, ok := row.(map[string]any)
			if !ok {
				return nil, errors.New("invalid linked list record")
			}
			item := map[string]any{}
			for _, key := range []string{"id", "name", "enabled", "revision", "lifecycle_state", "created_at", "updated_at"} {
				if v, ok := record[key]; ok {
					if text, ok := v.(string); ok && len(text) > 256 {
						v = text[:256]
					}
					item[key] = v
				}
			}
			if collection == "automations" {
				trigger := "when matching work ends"
				if on, ok := record["on"].(map[string]any); ok {
					if _, ok := on["schedule"]; ok {
						trigger = "on schedule"
					}
					if _, ok := on["webhook"]; ok {
						trigger = "on webhook"
					}
				}
				item["trigger"] = trigger
			}
			if steps, ok := record["steps"].([]any); ok {
				item["step_count"] = len(steps)
			}
			if deliveries, ok := record["latest_deliveries"].([]any); ok {
				item["recent_delivery_count"] = len(deliveries)
				if len(deliveries) > 0 {
					if d, ok := deliveries[0].(map[string]any); ok {
						last := map[string]any{}
						for _, key := range []string{"state", "created_at", "updated_at", "finished_at"} {
							if v, ok := d[key]; ok {
								last[key] = v
							}
						}
						item["last_delivery"] = last
					}
				}
			}
			items = append(items, item)
		}
		out[collection] = items
		out["count"] = len(items)
	}
	return json.Marshal(out)
}

// Run pages are ordered by opaque ID, not time. Scan bounded history rather than
// claiming that the first run is the latest. Refuse incomplete summaries.
func compactFleetWorkers(raw json.RawMessage, runs func(string, string) (json.RawMessage, error)) (json.RawMessage, error) {
	var list struct {
		Workers []pebblestore.WorkerRecord `json:"workers"`
		Next    string                     `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	if len(list.Workers) > 100 {
		return nil, errors.New("linked worker list exceeds summary budget")
	}
	items := []any{}
	requests := 0
	for _, w := range list.Workers {
		if w.ID == "" || w.ID == "." || w.ID == ".." || strings.ContainsAny(w.ID, "/\\?#%") {
			return nil, errors.New("invalid linked worker id")
		}
		if len(w.ID) > 256 || len(w.Name) > 256 || len(w.Role) > 256 || (w.ModelProfile != nil && len(w.ModelProfile.Action.Model) > 256) {
			return nil, errors.New("linked worker summary field exceeds budget")
		}
		role := w.Role
		if role == "" {
			role = "swarm"
		}
		var model any
		if w.ModelProfile != nil {
			model = w.ModelProfile.Action.Model
		}
		var latest *pebblestore.WorkerRunRecord
		cursor := ""
		seen := map[string]bool{}
		for {
			requests++
			if requests > 200 {
				return nil, errors.New("linked worker history exceeds summary budget")
			}
			page, err := runs(w.ID, cursor)
			if err != nil {
				return nil, fmt.Errorf("linked worker history: %w", err)
			}
			var history struct {
				Runs []pebblestore.WorkerRunRecord `json:"runs"`
				Next string                        `json:"next_cursor"`
			}
			if err := json.Unmarshal(page, &history); err != nil {
				return nil, err
			}
			for _, run := range history.Runs {
				if latest == nil || run.CreatedAt > latest.CreatedAt || (run.CreatedAt == latest.CreatedAt && run.ID > latest.ID) {
					copy := run
					latest = &copy
				}
			}
			if history.Next == "" {
				break
			}
			if seen[history.Next] {
				return nil, errors.New("linked worker history repeated cursor")
			}
			seen[history.Next] = true
			cursor = history.Next
		}
		var last any
		if latest != nil {
			last = map[string]any{"state": latest.Status, "created_at": latest.CreatedAt, "started_at": latest.StartedAt, "completed_at": latest.CompletedAt}
		}
		items = append(items, map[string]any{"id": w.ID, "name": w.Name, "role": role, "model": model, "enabled": w.LifecycleState == pebblestore.WorkerLifecycleStateIdle || w.LifecycleState == pebblestore.WorkerLifecycleStateActive, "lifecycle_state": w.LifecycleState, "last_run": last})
	}
	return json.Marshal(map[string]any{"workers": items, "count": len(items), "next_cursor": list.Next})
}
