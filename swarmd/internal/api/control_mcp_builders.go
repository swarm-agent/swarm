package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

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

// Client keys may start, read and drive sessions and nothing else.
var controlMCPClientKeyScopes = []string{"sessions:read", "sessions:write", clientAppScope}

var controlMCPClientKeyDays = []int{1, 7, 30, 90, 365}

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
	response, err := c.dispatch(http.MethodPut, "/v3/agents/"+url.PathEscape(name), nil, args)
	if err != nil {
		return nil, err
	}
	result := controlMCPPick(controlMCPMap(response["profile"]), "name", "enabled")
	result["tools"] = args["tools"]
	return map[string]any{"agent": result, "next": fmt.Sprintf("start_session with agent %q", name)}, nil
}

func controlMCPCreateClientKey(c *controlMCPCall, args map[string]any) (any, error) {
	name := controlMCPString(args, "name")
	if name == "" || len(name) > 64 {
		return nil, controlMCPToolFailure("name is required (at most 64 characters)")
	}
	body := map[string]any{"name": name, "scopes": controlMCPClientKeyScopes}
	if days := controlMCPInt(args, "days", 0); days != 0 {
		if !slices.Contains(controlMCPClientKeyDays, days) {
			return nil, controlMCPToolFailure("days must be one of %v, or left out (the key then works until 90 days unused)", controlMCPClientKeyDays)
		}
		body["expires_in_seconds"] = days * 24 * 60 * 60
	}
	response, err := c.dispatch(http.MethodPost, "/v3/auth/tokens", nil, body)
	if err != nil {
		return nil, err
	}
	token, _ := response["token"].(string)
	if token == "" {
		return nil, controlMCPToolFailure("Swarm did not return a key")
	}
	record := controlMCPPick(controlMCPMap(response["record"]), "id", "name", "scopes", "expires_at", "lapses_at", "lapse_reason")
	return map[string]any{
		"key":    token,
		"record": record,
		"use":    "Shown once. Send it as 'Authorization: Bearer <key>' to this box's HTTP API from a server process (for example the Node SDK: new SwarmClient({ baseUrl, token })). Never put it in browser code.",
	}, nil
}

// controlMCPChatGPTMaxWaitSeconds caps swarm_connect_chatgpt status
// wait_seconds, well under any client's tool timeout.
const controlMCPChatGPTMaxWaitSeconds = 25

// ChatGPT sign-in: only the device flow, so the person finishes it in their
// own browser; the result never carries tokens. The sign-in routes are
// owner-only (admin), but connecting the person's own ChatGPT plan is the
// first step of the setup a write AI key does for them, and only the person
// can finish it. So the tool calls the start and status handlers itself: a
// key with Swarm Control levels is held to its level (swarm:write, checked
// before any tool runs), any other scoped token still needs admin. It is
// local only: the relay never lists or forwards it (remote.localOnlyTools).
func controlMCPConnectChatGPT(c *controlMCPCall, args map[string]any) (any, error) {
	if record, ok := ScopedTokenFromRequest(c.request); ok && len(c.levels()) == 0 && !record.HasScope("admin") {
		return nil, controlMCPToolFailure("this connection lacks the admin permission")
	}
	if c.server == nil {
		return nil, controlMCPToolFailure("ChatGPT sign-in is unavailable")
	}
	var (
		response map[string]any
		err      error
	)
	switch controlMCPString(args, "action") {
	case "start":
		response, err = c.dispatchTo(http.HandlerFunc(c.server.handleCodexOAuthStart), http.MethodPost, "/v1/auth/codex/oauth/start", nil, map[string]any{"provider": "codex", "method": "device", "active": true})
	case "status":
		loginID, idErr := controlMCPID(args, "login_id")
		if idErr != nil {
			return nil, controlMCPToolFailure("status needs login_id from action start")
		}
		response, err = c.chatGPTStatus(loginID, min(controlMCPInt(args, "wait_seconds", 0), controlMCPChatGPTMaxWaitSeconds))
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
			return map[string]any{"status": "lost", "message": "this sign-in is gone (Swarm restarted or it expired); start a new one with action start"}, nil
		}
	default:
		return nil, controlMCPToolFailure("action must be start or status")
	}
	if err != nil {
		return nil, err
	}
	out := controlMCPPick(response, "status", "verification_url", "user_code", "expires_at", "error", "message")
	if credential, ok := response["credential"].(map[string]any); ok {
		if defaults, ok := credential["auto_defaults"]; ok {
			out["auto_defaults"] = defaults
		}
	}
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

// chatGPTStatus reads a sign-in's state and, with seconds, waits while the
// person has not finished it yet. It wakes on sign-in changes (success,
// failure or the code's expiry); there is no polling.
func (c *controlMCPCall) chatGPTStatus(loginID string, seconds int) (map[string]any, error) {
	deadline := time.NewTimer(time.Duration(max(seconds, 0)) * time.Second)
	defer deadline.Stop()
	for {
		changed := c.server.codexOAuthChanges()
		response, err := c.dispatchTo(http.HandlerFunc(c.server.handleCodexOAuthStatus), http.MethodGet, "/v1/auth/codex/oauth/status", url.Values{"session_id": {loginID}}, nil)
		if err != nil || seconds <= 0 {
			return response, err
		}
		if status := response["status"]; status != "waiting" && status != "authorizing" {
			return response, nil
		}
		select {
		case <-changed:
		case <-deadline.C:
			return response, nil
		case <-c.request.Context().Done():
			return response, nil
		}
	}
}
