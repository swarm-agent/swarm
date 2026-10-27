package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
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
		return nil, fmt.Errorf("%s is not available on a linked machine", action)
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
	if err != nil {
		// The machine's own refusal (its status and message) tells the agent
		// what to change; a call that never got an answer says only that.
		var refused *fleet.Error
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("%s refused %s: %s", machine, action, refused.Error())
		}
		return nil, fmt.Errorf("could not reach linked machine %s", machine)
	}
	if action == "list" {
		return compactFleetList(result)
	}
	return result, nil
}
