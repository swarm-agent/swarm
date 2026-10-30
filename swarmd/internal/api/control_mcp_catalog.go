package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/remote"
	"swarm/packages/swarmd/internal/tool"
)

// Swarm Control serves the tool catalog (tool.Catalog, docs/tool-catalog.md):
// each catalog tool is one MCP tool generated from its declaration, filtered
// by the caller's key level per action, and a call runs the same handler the
// Orchestrator runs (tool.Runtime.ExecuteControlCatalog), not an HTTP round
// trip. The answer is the catalog envelope as it is. The swarm_* tools are a
// separate, frozen set (TestControlMCPSwarmToolNamesFrozen).

// controlCatalogRuntime is the tool runtime as Swarm Control uses it.
type controlCatalogRuntime interface {
	ExecuteControlCatalog(context.Context, tool.ControlCatalogCall) (string, error)
}

// SetToolCatalogRuntime wires the tool runtime that serves catalog tools to
// Swarm Control. Without it no catalog tool is listed or callable.
func (s *Server) SetToolCatalogRuntime(runtime controlCatalogRuntime) {
	if s == nil {
		return
	}
	s.toolCatalog = runtime
}

// HomeMachineName is this machine's own name in catalog answers: its Swarm
// name, or "" when it has none (the envelope then says home).
func (s *Server) HomeMachineName() string {
	if s == nil {
		return ""
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.SwarmName)
}

// catalogActionAllowed reports whether this caller may see and call the
// action: the key's level for the action and, as every Swarm Control tool,
// the API scopes behind it (projects or sessions, read or write). Agent-bound
// and application keys never drive the person's projects.
func (c *controlMCPCall) catalogActionAllowed(action tool.CatalogAction) bool {
	if c == nil || c.request == nil || fleetActivityGatewayCaller(c.request) {
		return false
	}
	record, ok := ScopedTokenFromRequest(c.request)
	if !ok {
		return false
	}
	levels, bound := c.catalogLevels()
	if !tool.CatalogLevelsAllow(action, levels, bound) {
		return false
	}
	return catalogScopesHeld(record.HasScope, action)
}

// catalogScopesHeld reports whether the key holds one of the API scopes of
// the route behind the action (CatalogAction.APIScopes). An action that
// declares none is refused.
func catalogScopesHeld(has func(string) bool, action tool.CatalogAction) bool {
	for _, scope := range action.APIScopes {
		if has(scope) {
			return true
		}
	}
	return false
}

// ControlRelayLevelsHeader carries the Swarm Control levels a relay client
// holds on this machine (its scopes within the device's ceiling), set by
// the relay (remote.Service) on each call it forwards with the device token.
const ControlRelayLevelsHeader = remote.ClientLevelsHeader

// catalogLevels are the levels a catalog action is checked against: the
// key's swarm:* levels, narrowed to the relay client's when the call came
// through the relay. Forwarded levels only ever narrow: a key without levels
// takes the forwarded ones, a key with levels keeps those it shares with
// them. bound reports that the levels bind even when empty.
func (c *controlMCPCall) catalogLevels() (levels []string, bound bool) {
	key := c.levels()
	values := c.request.Header.Values(ControlRelayLevelsHeader)
	if len(values) == 0 {
		return key, false
	}
	forwarded := map[string]bool{}
	for _, value := range values {
		for _, level := range strings.Split(value, ",") {
			if level = strings.ToLower(strings.TrimSpace(level)); strings.HasPrefix(level, "swarm:") {
				forwarded[level] = true
			}
		}
	}
	if len(key) == 0 {
		for level := range forwarded {
			levels = append(levels, level)
		}
	} else {
		for _, level := range key {
			if forwarded[level] {
				levels = append(levels, level)
			}
		}
	}
	sort.Strings(levels)
	return levels, true
}

// catalogTools are the catalog tools this caller sees: the default listing
// (essentials) or every Swarm Control action (tools=all), only actions the
// caller may call.
func (c *controlMCPCall) catalogTools() []controlMCPTool {
	if c == nil || c.server == nil || c.server.toolCatalog == nil {
		return nil
	}
	everything := c.allTools()
	var tools []controlMCPTool
	for _, catalog := range tool.Catalog() {
		schema, actions := catalog.ControlSchema(func(action tool.CatalogAction) bool {
			listed := action.Listed(tool.CatalogProfileEssentials) || (everything && action.ControlCallable())
			return listed && c.catalogActionAllowed(action)
		})
		if len(actions) == 0 {
			continue
		}
		annotations := controlMCPReadOnly
		for _, action := range actions {
			if action.Level != tool.CatalogLevelRead {
				annotations = controlMCPWrites
				break
			}
		}
		tools = append(tools, controlMCPTool{Name: catalog.Name, Title: "Projects and tasks", Description: catalog.Description, InputSchema: schema, Annotations: annotations})
	}
	return tools
}

// callCatalogTool runs one catalog tool call for this caller.
func (c *controlMCPCall) callCatalogTool(catalog tool.CatalogTool, args map[string]any) (any, error) {
	if c.server == nil || c.server.toolCatalog == nil {
		return nil, controlMCPToolFailure("%s is not available on this machine", catalog.Name)
	}
	name, _ := args["action"].(string)
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "list"
	}
	action, ok := catalog.Action(name)
	if !ok || !action.ControlCallable() {
		var served []string
		for _, candidate := range catalog.Actions {
			if candidate.ControlCallable() {
				served = append(served, candidate.Action)
			}
		}
		return nil, controlMCPToolFailure("%s has no action %q for Swarm Control; use one of: %s", catalog.Name, name, strings.Join(served, ", "))
	}
	levels, bound := c.catalogLevels()
	if !tool.CatalogLevelsAllow(action, levels, bound) {
		if bound && action.LocalOnly {
			return nil, controlMCPToolFailure("%s %s is machine setup a relay client may not do", catalog.Name, action.Action)
		}
		return nil, controlMCPToolFailure("this key is limited to %s; %s %s needs %s", strings.Join(levels, ", "), catalog.Name, action.Action, action.Level.Scope())
	}
	if !c.catalogActionAllowed(action) {
		return nil, controlMCPToolFailure("this connection cannot call %s %s; it needs %s", catalog.Name, action.Action, strings.Join(action.APIScopes, " or "))
	}
	// The declared inputs are checked for type, enum and bounds; other
	// arguments pass to the handler as they do for the Orchestrator, which
	// re-validates everything.
	schema := catalog.InputSchema(action)
	properties, _ := schema["properties"].(map[string]any)
	declared := map[string]any{}
	for key, value := range args {
		if _, ok := properties[key]; ok {
			declared[key] = value
		}
	}
	declared["action"] = action.Action
	if err := controlMCPValidateArgs(schema, declared); err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || !principal.Valid() {
		return nil, controlMCPToolFailure("%s requires an authenticated user identity", catalog.Name)
	}
	// The caller is named by its AI key where a session would be.
	keyID := ""
	if record, ok := ScopedTokenFromRequest(c.request); ok && record != nil {
		keyID = record.ID
	}
	answer, err := c.server.toolCatalog.ExecuteControlCatalog(c.request.Context(), tool.ControlCatalogCall{Principal: principal, Levels: levels, LevelsBound: bound, KeyID: keyID, Tool: catalog.Name, Arguments: args})
	if err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	return json.RawMessage(answer), nil
}
