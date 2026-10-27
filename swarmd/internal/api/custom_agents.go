package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
)

var customAgentAllowedTools = []string{"manage_jobs", "read", "search", "list", "find", "write", "edit", "bash", "websearch", "webfetch", "webdownload", "media_inspect", "manage_media", "git_status", "git_diff", "git_add", "git_commit", "task_progress"}

// ManageCustomAgents is the single account-scoped boundary for native and HTTP callers.
func (s *Server) ManageCustomAgents(account string, args map[string]any) (any, error) {
	if s.agents == nil || strings.TrimSpace(account) == "" {
		return nil, errors.New("account agent service required")
	}
	action, name := controlMCPString(args, "action"), controlMCPString(args, "name")
	if action == "list" {
		state, err := s.agents.ListStateForAccount(account, 2000)
		if err != nil {
			return nil, err
		}
		profiles := []publicAgentProfileDTO{}
		for _, p := range state.Profiles {
			if !p.Protected && !controlMCPIsSystemAgent(p.Name) && !controlMCPReservedIDs[p.Name] && controlMCPAgentName.MatchString(p.Name) && p.Mode == agentruntime.ModeSubagent {
				profiles = append(profiles, publicAgentProfile(p))
			}
		}
		return map[string]any{"profiles": profiles}, nil
	}
	if !controlMCPAgentName.MatchString(name) || controlMCPReservedIDs[name] {
		return nil, errors.New("name must be lowercase letters, digits and dashes, starting with a letter")
	}
	if controlMCPIsSystemAgent(name) {
		return nil, fmt.Errorf("%q is a built-in agent; choose another name", name)
	}
	existing, found, err := s.agents.GetProfileForAccount(account, name)
	if err != nil {
		return nil, err
	}
	if found && (existing.Protected || existing.Mode != agentruntime.ModeSubagent) {
		return nil, fmt.Errorf("%q is a built-in agent; choose another name", name)
	}
	switch action {
	case "get":
		if !found {
			return nil, errors.New("custom agent not found")
		}
		return map[string]any{"profile": publicAgentProfile(existing)}, nil
	case "disable":
		if !found {
			return nil, errors.New("custom agent not found")
		}
		enabled := false
		p, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: name, Enabled: &enabled})
		return map[string]any{"profile": publicAgentProfile(p)}, err
	case "define":
		instructions := controlMCPString(args, "instructions")
		if instructions == "" {
			return nil, errors.New("instructions are required")
		}
		names := []string{}
		switch v := args["tools"].(type) {
		case string:
			if !slices.Contains(controlMCPAgentToolSets, v) {
				return nil, fmt.Errorf("tools must be one of %v or an explicit list", controlMCPAgentToolSets)
			}
			names = append(names, "read", "search", "list", "find", "websearch", "webfetch")
			if v != "read_only" {
				names = append(names, "write", "edit")
			}
			if v == "build" {
				names = append(names, "bash", "git_status", "git_diff", "git_add", "git_commit")
			}
		case []any:
			for _, n := range v {
				text, ok := n.(string)
				if !ok {
					return nil, errors.New("tools requires tool names")
				}
				names = append(names, text)
			}
		case []string:
			names = append(names, v...)
		default:
			return nil, errors.New("tools requires an explicit list or preset")
		}
		tools := map[string]store.AgentToolConfig{}
		for _, key := range []string{"tools", "tools_on", "tools_off"} {
			values := names
			if key != "tools" {
				values = nil
				switch v := args[key].(type) {
				case []any:
					for _, n := range v {
						text, ok := n.(string)
						if !ok {
							return nil, errors.New("tools requires tool names")
						}
						values = append(values, text)
					}
				case []string:
					values = v
				case nil:
				default:
					return nil, errors.New("tools requires tool names")
				}
			}
			for _, n := range values {
				if !slices.Contains(customAgentAllowedTools, n) {
					return nil, fmt.Errorf("tool %q is not allowed; allowed tools: %s", n, strings.Join(customAgentAllowedTools, ", "))
				}
				tools[n] = store.AgentToolConfig{Enabled: store.BoolPtr(key != "tools_off")}
			}
		}
		p, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: name, Mode: agentruntime.ModeSubagent, Description: controlMCPString(args, "description"), Prompt: instructions, ProviderSet: true, ModelSet: true, ThinkingSet: true, Enabled: store.BoolPtr(true), ToolContract: &store.AgentToolContract{Preset: "custom", Tools: tools}})
		return map[string]any{"profile": publicAgentProfile(p)}, err
	default:
		return nil, errors.New("action must be list, get, define or disable")
	}
}

func (s *Server) handleCustomAgents(w http.ResponseWriter, r *http.Request) {
	read := r.Method == http.MethodGet
	if read {
		if !s.requireScopeAny(w, r, "agents:read", "swarm:read") {
			return
		}
	} else if !s.requireScopeAny(w, r, "agents:write", "swarm:write") {
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, 401, errors.New("product identity required"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v3/agents")
	args := map[string]any{}
	switch {
	case read && path == "":
		args["action"] = "list"
	case read && strings.Count(path, "/") == 1:
		args["action"] = "get"
		args["name"] = strings.TrimPrefix(path, "/")
	case r.Method == http.MethodPut && strings.Count(path, "/") == 1:
		if err := decodeJSON(r, &args); err != nil {
			writeError(w, 400, err)
			return
		}
		args["action"] = "define"
		args["name"] = strings.TrimPrefix(path, "/")
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/disable") && strings.Count(path, "/") == 2:
		args["action"] = "disable"
		args["name"] = strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/disable")
	default:
		methodNotAllowed(w)
		return
	}
	result, err := s.ManageCustomAgents(principal.AccountScopeID, args)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, result)
}
