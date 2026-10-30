package api

import (
	"context"
	"encoding/json"
	"strings"

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
	if !tool.CatalogLevelAllowed(action, c.levels()) {
		return false
	}
	if action.Level == tool.CatalogLevelRead {
		return record.HasScope("projects:read") || record.HasScope("sessions:read")
	}
	return record.HasScope("projects:write") || record.HasScope("sessions:write")
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
	if !tool.CatalogLevelAllowed(action, c.levels()) {
		return nil, controlMCPToolFailure("this key is limited to %s; %s %s needs %s", strings.Join(c.levels(), ", "), catalog.Name, action.Action, action.Level.Scope())
	}
	if !c.catalogActionAllowed(action) {
		need := "projects:write or sessions:write"
		if action.Level == tool.CatalogLevelRead {
			need = "projects:read or sessions:read"
		}
		return nil, controlMCPToolFailure("this connection cannot call %s %s; it needs %s", catalog.Name, action.Action, need)
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
	answer, err := c.server.toolCatalog.ExecuteControlCatalog(c.request.Context(), tool.ControlCatalogCall{Principal: principal, Levels: c.levels(), Tool: catalog.Name, Arguments: args})
	if err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	return json.RawMessage(answer), nil
}
