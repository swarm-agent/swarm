package api

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"

	agentruntime "swarm/packages/swarmd/internal/agent"
)

// Building blocks for an AI that sets up this box for someone: custom agents,
// keys for the client apps it builds, and ChatGPT sign-in. Every tool fixes
// what it sends (agent mode and tools, key scopes, sign-in method), so an MCP
// caller cannot widen them; the daemon handlers still check scopes and
// ownership on each sub-request.

// Custom agent names: lowercase, one path segment, never a system agent.
var controlMCPAgentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Tool sets a custom agent may get. read_only and read_write are the daemon's
// presets; build lists its tools explicitly because no preset lets an agent
// both edit files and run commands. Commands still need approval under the
// account's permission policy. bash_git_only is not offered: its command
// prefixes are not enforced in V3 sessions.
var controlMCPAgentToolSets = []string{"read_only", "read_write", "build"}

var controlMCPBuildAgentTools = []string{
	"read", "search", "list", "find", "write", "edit", "bash",
	"git_status", "git_diff", "git_add", "git_commit",
	"plan_manage", "ask_user", "compact", "exit_plan_mode", "skill_use", "websearch", "webfetch",
}

// Client keys may start, read and drive sessions and nothing else.
var controlMCPClientKeyScopes = []string{"sessions:read", "sessions:write", clientAppScope}

var controlMCPClientKeyDays = []int{1, 7, 30, 90, 365}

func controlMCPAgentToolContract(toolSet string) map[string]any {
	if toolSet != "build" {
		return map[string]any{"preset": toolSet}
	}
	tools := make(map[string]any, len(controlMCPBuildAgentTools))
	for _, name := range controlMCPBuildAgentTools {
		tools[name] = map[string]any{"enabled": true}
	}
	return map[string]any{"preset": "custom", "tools": tools}
}

func controlMCPIsSystemAgent(name string) bool {
	if slices.Contains(controlMCPSessionAgents, name) {
		return true
	}
	_, system := agentruntime.CanonicalSystemAgentID(name)
	return system
}

// customAgent admits a user-defined agent for start_session: it must exist on
// this account, be enabled and run as a sub-agent.
func (c *controlMCPCall) customAgent(name string) error {
	if !controlMCPAgentName.MatchString(name) || controlMCPReservedIDs[name] || controlMCPIsSystemAgent(name) {
		return controlMCPToolFailure("agent %q is not a session agent (see list_agents)", name)
	}
	response, err := c.dispatch(http.MethodGet, "/v2/agents/"+name, nil, nil)
	if err != nil {
		return controlMCPToolFailure("agent %q is not available (see list_agents): %s", name, err.Error())
	}
	profile := controlMCPMap(response["profile"])
	if enabled, _ := profile["enabled"].(bool); !enabled {
		return controlMCPToolFailure("agent %q is disabled", name)
	}
	if mode, _ := profile["mode"].(string); mode != agentruntime.ModeSubagent {
		return controlMCPToolFailure("agent %q is not a session agent (see list_agents)", name)
	}
	return nil
}

func controlMCPListAgents(c *controlMCPCall, _ map[string]any) (any, error) {
	response, err := c.dispatch(http.MethodGet, "/v2/agents", nil, nil)
	if err != nil {
		// The full view resolves each agent's model and fails before a
		// provider is connected; the summary view (no descriptions) does not.
		if response, err = c.dispatch(http.MethodGet, "/v2/agents", url.Values{"view": {"summary"}}, nil); err != nil {
			return nil, err
		}
	}
	custom := []map[string]any{}
	for _, item := range controlMCPList(controlMCPMap(response["state"])["profiles"]) {
		profile := controlMCPMap(item)
		name, _ := profile["name"].(string)
		// Only agents start_session admits (see customAgent).
		if mode, _ := profile["mode"].(string); mode != agentruntime.ModeSubagent || !controlMCPAgentName.MatchString(name) || controlMCPReservedIDs[name] || controlMCPIsSystemAgent(name) {
			continue
		}
		entry := controlMCPPick(profile, "name", "enabled")
		if description, _ := profile["description"].(string); description != "" {
			entry["description"] = controlMCPTruncate(description, 200)
		}
		if len(custom) >= controlMCPMaxListItems {
			break
		}
		custom = append(custom, entry)
	}
	return map[string]any{"built_in": controlMCPSessionAgents, "custom": custom}, nil
}

func controlMCPDefineAgent(c *controlMCPCall, args map[string]any) (any, error) {
	name := controlMCPString(args, "name")
	if !controlMCPAgentName.MatchString(name) || controlMCPReservedIDs[name] {
		return nil, controlMCPToolFailure("name must be lowercase letters, digits and dashes, starting with a letter")
	}
	if controlMCPIsSystemAgent(name) {
		return nil, controlMCPToolFailure("%q is a built-in agent; choose another name", name)
	}
	instructions := controlMCPString(args, "instructions")
	if instructions == "" {
		return nil, controlMCPToolFailure("instructions are required")
	}
	toolSet := controlMCPString(args, "tools")
	if !slices.Contains(controlMCPAgentToolSets, toolSet) {
		return nil, controlMCPToolFailure("tools must be one of %v", controlMCPAgentToolSets)
	}
	// Never replace a protected (built-in) profile under a name the system
	// registry does not know.
	if existing, err := c.dispatch(http.MethodGet, "/v2/agents/"+name, nil, nil); err == nil {
		if protected, _ := controlMCPMap(existing["profile"])["protected"].(bool); protected {
			return nil, controlMCPToolFailure("%q is a built-in agent; choose another name", name)
		}
	}
	// Empty provider/model/thinking clear any pin left on an earlier agent of
	// this name, so custom agents always run on the account default model.
	response, err := c.dispatch(http.MethodPut, "/v2/agents/"+name, nil, map[string]any{
		"mode":          agentruntime.ModeSubagent,
		"description":   controlMCPString(args, "description"),
		"prompt":        instructions,
		"provider":      "",
		"model":         "",
		"thinking":      "",
		"tool_contract": controlMCPAgentToolContract(toolSet),
		"enabled":       true,
	})
	if err != nil {
		return nil, err
	}
	agent := controlMCPPick(controlMCPMap(response["profile"]), "name", "enabled")
	agent["tools"] = toolSet
	return map[string]any{"agent": agent, "next": fmt.Sprintf("start_session with agent %q", name)}, nil
}

func controlMCPCreateClientKey(c *controlMCPCall, args map[string]any) (any, error) {
	name := controlMCPString(args, "name")
	if name == "" || len(name) > 64 {
		return nil, controlMCPToolFailure("name is required (at most 64 characters)")
	}
	days := controlMCPInt(args, "days", 30)
	if !slices.Contains(controlMCPClientKeyDays, days) {
		return nil, controlMCPToolFailure("days must be one of %v", controlMCPClientKeyDays)
	}
	response, err := c.dispatch(http.MethodPost, "/v3/auth/tokens", nil, map[string]any{
		"name":               name,
		"scopes":             controlMCPClientKeyScopes,
		"expires_in_seconds": days * 24 * 60 * 60,
	})
	if err != nil {
		return nil, err
	}
	token, _ := response["token"].(string)
	if token == "" {
		return nil, controlMCPToolFailure("Swarm did not return a key")
	}
	record := controlMCPPick(controlMCPMap(response["record"]), "id", "name", "scopes", "expires_at")
	return map[string]any{
		"key":    token,
		"record": record,
		"use":    "Shown once. Send it as 'Authorization: Bearer <key>' to this box's HTTP API from a server process (for example the Node SDK: new SwarmClient({ baseUrl, token })). Never put it in browser code.",
	}, nil
}

// ChatGPT sign-in: only the device flow, so the person finishes it in their
// own browser; the result never carries tokens.
func controlMCPConnectChatGPT(c *controlMCPCall, args map[string]any) (any, error) {
	var (
		response map[string]any
		err      error
	)
	switch controlMCPString(args, "action") {
	case "start":
		response, err = c.dispatch(http.MethodPost, "/v1/auth/codex/oauth/start", nil, map[string]any{"provider": "codex", "method": "device", "active": true})
	case "status":
		loginID, idErr := controlMCPID(args, "login_id")
		if idErr != nil {
			return nil, controlMCPToolFailure("status needs login_id from action start")
		}
		response, err = c.dispatch(http.MethodGet, "/v1/auth/codex/oauth/status", url.Values{"session_id": {loginID}}, nil)
	default:
		return nil, controlMCPToolFailure("action must be start or status")
	}
	if err != nil {
		return nil, err
	}
	out := controlMCPPick(response, "status", "verification_url", "user_code", "expires_at", "error")
	if id, _ := response["session_id"].(string); id != "" {
		out["login_id"] = id
	}
	switch out["status"] {
	case "waiting", "authorizing":
		if out["user_code"] != nil {
			out["tell_the_person"] = fmt.Sprintf("Open %v and enter the code %v, then check back with action status.", out["verification_url"], out["user_code"])
		}
	case "error":
		// The code is spent or expired; never send the person to it.
		delete(out, "user_code")
		delete(out, "verification_url")
		out["next"] = "This sign-in did not finish; call action start for a new code."
	}
	return out, nil
}
