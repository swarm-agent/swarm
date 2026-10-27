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
