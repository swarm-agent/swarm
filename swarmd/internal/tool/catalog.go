package tool

import (
	"fmt"
	"sort"
	"strings"
)

// The tool catalog is the one place a tool exists. Each action of a catalog
// tool is declared once here: its inputs, the key level it needs, which
// machines it reaches, the profiles that list it and its description. The
// Orchestrator's definition and Swarm Control's (MCP) tool are both generated
// from these declarations, both calls run the same handler, and every answer
// comes back in one Envelope. A new tool or action is a catalog entry plus its
// parity test; nothing adds a swarm_* tool or reshapes results outside the
// envelope (docs/tool-catalog.md).

// CatalogLevel is the Swarm Control key level an action needs: the same four
// levels as remote.ToolScope and the swarm:* key scopes.
type CatalogLevel string

const (
	CatalogLevelRead    CatalogLevel = "read"
	CatalogLevelWrite   CatalogLevel = "write"
	CatalogLevelApprove CatalogLevel = "approve"
	CatalogLevelManage  CatalogLevel = "manage"
)

// Scope is the key scope that grants the level (swarm:read and so on).
func (l CatalogLevel) Scope() string { return "swarm:" + string(l) }

// CatalogReach is the machines an action reaches.
type CatalogReach string

const (
	// CatalogReachAny runs on the home machine, or on a linked machine with
	// machine=NAME.
	CatalogReachAny CatalogReach = "any"
	// CatalogReachHome runs on the home machine only.
	CatalogReachHome CatalogReach = "home"
	// CatalogReachLinked reads a linked machine only: machine= is required.
	CatalogReachLinked CatalogReach = "linked"
)

// CatalogProfile is a listing that shows an action.
type CatalogProfile string

const (
	// CatalogProfileEssentials is Swarm Control's default (core) listing.
	CatalogProfileEssentials CatalogProfile = "essentials"
	// CatalogProfileFull is Swarm Control's listing of every tool (tools=all).
	CatalogProfileFull CatalogProfile = "full"
	// CatalogProfileOrchestrator is the Orchestrator's definition.
	CatalogProfileOrchestrator CatalogProfile = "orchestrator"
)

// CatalogField is one input of a catalog tool. A field is declared once per
// tool; actions name the fields they take.
type CatalogField struct {
	Name   string
	Schema map[string]any
}

// CatalogAction declares one action of a catalog tool.
type CatalogAction struct {
	Tool   string
	Action string
	// Inputs are the field names this action takes (besides action).
	Inputs []string
	// Required are the inputs the action cannot run without, here and on a
	// linked machine (each one of Inputs). project_id of a task action that
	// finds its project from the task id (TaskProjectFromTaskID) is not
	// required; RequirementNote says when it is needed.
	Required []string
	// ControlInputs are fields only Swarm Control's call of the action takes
	// (a caller with no conversation: wait_tasks' wait_seconds). They are
	// not in the Orchestrator's definition.
	ControlInputs []string
	Level         CatalogLevel
	// APIScopes are the API scopes of the route behind the action: a Swarm
	// Control key needs one of them, as the route itself requires (the call
	// runs in-process, so the route's own check never runs).
	APIScopes []string
	Reach     CatalogReach
	// LocalOnly is machine setup a relay key may not do.
	LocalOnly bool
	Profiles  []CatalogProfile
	// Untrusted marks answers whose data holds agent- or outside-written
	// text. Every answer read from a linked machine is untrusted as well.
	Untrusted bool
	// Description is the one description shown to the Orchestrator and MCP.
	Description string
	// States are the outcome states (EnvelopeStates) the action's answer
	// can carry besides ok and partial: its answer's status is the
	// envelope's state only when it is one of these. Any other status (a
	// task's run status, a machine's status) stays in data.
	States []string
	// WaitingStatuses are the statuses of the answer's own record (a job's)
	// that wait on someone: the envelope's state is waiting for them.
	WaitingStatuses []string
	// NextAction is the action's next step when its answer names none
	// (envelope next_action). Empty when there is nothing to do next.
	NextAction string
}

// Listed reports whether the profile lists the action.
func (a CatalogAction) Listed(profile CatalogProfile) bool {
	for _, p := range a.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// ControlCallable reports whether Swarm Control (MCP) serves the action: it
// is in its default or full listing.
func (a CatalogAction) ControlCallable() bool {
	return a.Listed(CatalogProfileEssentials) || a.Listed(CatalogProfileFull)
}

// CatalogTool is a tool of the catalog with its fields and actions.
type CatalogTool struct {
	Name        string
	Description string
	Fields      []CatalogField
	Actions     []CatalogAction
}

// Field returns the declared field.
func (t CatalogTool) Field(name string) (CatalogField, bool) {
	for _, field := range t.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return CatalogField{}, false
}

// Action returns the declared action.
func (t CatalogTool) Action(name string) (CatalogAction, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, action := range t.Actions {
		if action.Action == name {
			return action, true
		}
	}
	return CatalogAction{}, false
}

// InputSchema is the action's own input schema: action plus only the fields
// the action takes; required names action and the action's Required inputs.
func (t CatalogTool) InputSchema(action CatalogAction) map[string]any {
	properties := map[string]any{"action": map[string]any{"type": "string", "enum": []string{action.Action}, "description": action.Description + action.RequirementNote()}}
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		if field, ok := t.Field(name); ok {
			properties[name] = field.Schema
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": append([]string{"action"}, action.Required...), "additionalProperties": true}
}

// catalogRequiredFix names where a missing required input comes from.
var catalogRequiredFix = map[string]string{
	"project_id": "manage_projects action=list lists your projects and their ids",
	"task_id":    "list_tasks lists a project's tasks and their ids",
	"task_ids":   "list_tasks lists a project's tasks and their ids",
	"machine":    "list_machines lists the linked machines",
}

// CheckCatalogRequired refuses a call missing one of the action's Required
// inputs, naming the input and where it comes from. The Orchestrator's
// handler and Swarm Control's catalog call (before its schema check) both
// use it, so both surfaces refuse with the same reason.
func CheckCatalogRequired(action CatalogAction, args map[string]any) error {
	for _, key := range action.Required {
		if _, ok := args[key]; !ok {
			reason := fmt.Sprintf("%s is required for %s %s", key, action.Tool, action.Action)
			if fix := catalogRequiredFix[key]; fix != "" {
				reason += "; " + fix
			}
			return fmt.Errorf("%s", reason)
		}
	}
	return nil
}

// catalogProjectFromTaskNote says when a task action needs project_id.
const catalogProjectFromTaskNote = "project_id may be omitted: it is found from the task id (on a linked machine, for tasks this caller sent there)"

// RequirementNote is the action's required inputs as its description states
// them (" Requires: task_id, message."), with when a task action needs its
// project_id; "" when nothing is required.
func (a CatalogAction) RequirementNote() string {
	var parts []string
	if len(a.Required) > 0 {
		parts = append(parts, "Requires: "+strings.Join(a.Required, ", "))
	}
	if TaskProjectFromTaskID(a) {
		parts = append(parts, catalogProjectFromTaskNote)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, "; ") + "."
}

// schema is the tool's input schema for the listed actions: an action field
// naming them, then the union of their inputs in field order.
func (t CatalogTool) schema(actions []CatalogAction, control bool) map[string]any {
	names := make([]string, 0, len(actions))
	used := map[string]bool{}
	lines := make([]string, 0, len(actions))
	// The Orchestrator is shown no per-action descriptions: its action field
	// states each action's required inputs instead.
	var required []string
	projectFromTask := false
	for _, action := range actions {
		names = append(names, action.Action)
		lines = append(lines, action.Action+": "+action.Description+action.RequirementNote())
		if len(action.Required) > 0 {
			required = append(required, action.Action+" "+strings.Join(action.Required, ", "))
		}
		projectFromTask = projectFromTask || TaskProjectFromTaskID(action)
		for _, input := range action.Inputs {
			used[input] = true
		}
		if control {
			for _, input := range action.ControlInputs {
				used[input] = true
			}
		}
	}
	describeActions := control
	actionField := map[string]any{"type": "string", "enum": names}
	if describeActions {
		actionField["description"] = strings.Join(lines, " ")
	} else if len(required) > 0 || projectFromTask {
		var note []string
		if len(required) > 0 {
			note = append(note, "Required inputs: "+strings.Join(required, "; ")+".")
		}
		if projectFromTask {
			note = append(note, "Task actions: "+catalogProjectFromTaskNote+".")
		}
		actionField["description"] = strings.Join(note, " ")
	}
	properties := map[string]any{"action": actionField}
	for _, field := range t.Fields {
		if used[field.Name] {
			properties[field.Name] = field.Schema
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": []string{"action"}, "additionalProperties": true}
}

// OrchestratorDefinition is the definition the Orchestrator's model is shown.
func (t CatalogTool) OrchestratorDefinition() Definition {
	var actions []CatalogAction
	for _, action := range t.Actions {
		if action.Listed(CatalogProfileOrchestrator) {
			actions = append(actions, action)
		}
	}
	return Definition{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.schema(actions, false)}
}

// ControlSchema is Swarm Control's input schema for the actions the caller
// may see (allowed), with each action's description on the action field. It
// returns the listed actions too; none means the tool is not listed.
func (t CatalogTool) ControlSchema(allowed func(CatalogAction) bool) (map[string]any, []CatalogAction) {
	var actions []CatalogAction
	for _, action := range t.Actions {
		if allowed(action) {
			actions = append(actions, action)
		}
	}
	return t.schema(actions, true), actions
}

// Catalog lists the catalog tools.
func Catalog() []CatalogTool {
	return []CatalogTool{manageProjectsCatalog(), manageWorkflowsCatalog(), manageJobsCatalog(), manageWorkersCatalog(), manageSetupCatalog(), manageFleetCatalog(), manageMediaCatalog(), manageCustomAgentsCatalog(), manageThemeCatalog(), manageDesignCatalog(), manageSessionsCatalog(), manageWorkspaceCatalog()}
}

// CatalogToolNamed returns the catalog tool.
func CatalogToolNamed(name string) (CatalogTool, bool) {
	for _, t := range Catalog() {
		if t.Name == name {
			return t, true
		}
	}
	return CatalogTool{}, false
}

// CatalogLevelAllowed reports whether a key holding levels may call the
// action. A key without levels keeps its API scopes alone (the same rule as
// every Swarm Control tool), so it passes here.
func CatalogLevelAllowed(action CatalogAction, levels []string) bool {
	return CatalogLevelsAllow(action, levels, false)
}

// CatalogLevelsAllow is CatalogLevelAllowed with bound levels: levels a
// relay forwarded for its client (or a key narrowed to them) bind even when
// empty, so no level means no action, and relayed calls never reach
// local_only actions.
func CatalogLevelsAllow(action CatalogAction, levels []string, bound bool) bool {
	if bound && action.LocalOnly {
		return false
	}
	if len(levels) == 0 && !bound {
		return true
	}
	need := action.Level.Scope()
	for _, level := range levels {
		if strings.ToLower(strings.TrimSpace(level)) == need {
			return true
		}
	}
	return false
}

// catalogActionNames lists the declared action names, sorted.
func catalogActionNames(t CatalogTool) []string {
	names := make([]string, 0, len(t.Actions))
	for _, action := range t.Actions {
		names = append(names, action.Action)
	}
	sort.Strings(names)
	return names
}
