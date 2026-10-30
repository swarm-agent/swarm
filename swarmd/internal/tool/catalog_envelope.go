package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/identity"
)

// Envelope is the one shape every catalog answer takes, for the Orchestrator
// and for Swarm Control alike.
type Envelope struct {
	Tool   string `json:"tool"`
	Action string `json:"action"`
	// Machine is where the action ran: the home machine's own name, or the
	// linked machine's.
	Machine string `json:"machine"`
	// State is the call's outcome, one of EnvelopeStates. A task's, run's or
	// machine's own status is in Data, never here.
	State string `json:"state"`
	Data  any    `json:"data"`
	// NextAction is the next step when there is one, starting with the
	// action to call when there is one; omitted when there is nothing to do.
	NextAction string `json:"next_action,omitempty"`
	// Next is set whenever the answer was cut: the exact arguments of the
	// manage_projects call that reads the rest (action plus its cursor or
	// offset fields).
	Next any `json:"next,omitempty"`
	// UntrustedContent is true when Data holds agent- or outside-written text.
	UntrustedContent bool `json:"untrusted_content"`
}

// EnvelopeParts are the inputs of NewEnvelope.
type EnvelopeParts struct {
	Tool, Action, Machine, State string
	Data                         any
	NextAction                   string
	Next                         any
	Untrusted                    bool
}

// HomeMachineFallback names the home machine when it has no Swarm name.
const HomeMachineFallback = "home"

// Envelope states: the outcome of a call that ran. A call that is refused or
// fails is a tool error carrying the reason (MCP isError), not an envelope.
const (
	// EnvelopeStateOK: the call did what it was asked.
	EnvelopeStateOK = "ok"
	// EnvelopeStateWatching: a watch was recorded; a new turn brings the
	// outcomes (the Orchestrator's wait_tasks).
	EnvelopeStateWatching = "watching"
	// EnvelopeStateCancelled: watches were ended (cancel_watch).
	EnvelopeStateCancelled = "cancelled"
	// EnvelopeStatePartial: the call ran but part of its answer is missing
	// (data_error, linked_record_error).
	EnvelopeStatePartial = "partial"
)

// EnvelopeStates is the documented set of envelope states
// (docs/tool-catalog.md).
var EnvelopeStates = []string{EnvelopeStateOK, EnvelopeStateWatching, EnvelopeStateCancelled, ControlTaskWaitMilestone, ControlTaskWaitSettled, ControlTaskWaitTimedOut, EnvelopeStatePartial}

// EnvelopeStateKnown reports whether state is in EnvelopeStates.
func EnvelopeStateKnown(state string) bool {
	for _, known := range EnvelopeStates {
		if state == known {
			return true
		}
	}
	return false
}

// NewEnvelope is the only function that builds a catalog answer. Machine and
// State are always set.
func NewEnvelope(p EnvelopeParts) Envelope {
	machine := strings.TrimSpace(p.Machine)
	if machine == "" {
		machine = HomeMachineFallback
	}
	state := strings.TrimSpace(p.State)
	if state == "" {
		state = "ok"
	}
	return Envelope{Tool: p.Tool, Action: p.Action, Machine: machine, State: state, Data: p.Data, NextAction: strings.TrimSpace(p.NextAction), Next: p.Next, UntrustedContent: p.Untrusted}
}

// HomeMachineNamer is implemented by the daemon's API server: the home
// machine's own name for the envelope.
type HomeMachineNamer interface {
	HomeMachineName() string
}

func (r *Runtime) homeMachineName() string {
	if r != nil {
		if namer, ok := r.getProjectTaskLifecycleService().(HomeMachineNamer); ok {
			if name := strings.TrimSpace(namer.HomeMachineName()); name != "" {
				return name
			}
		}
	}
	return HomeMachineFallback
}

var (
	catalogDeclaredOnce sync.Once
	catalogDeclaredMemo map[string]CatalogTool
)

// catalogDeclared is a catalog tool's declaration, built once for lookups.
func catalogDeclared(name string) CatalogTool {
	catalogDeclaredOnce.Do(func() {
		catalogDeclaredMemo = map[string]CatalogTool{}
		for _, t := range Catalog() {
			catalogDeclaredMemo[t.Name] = t
		}
	})
	return catalogDeclaredMemo[name]
}

// manageProjectsDeclared is manage_projects' declaration.
func manageProjectsDeclared() CatalogTool { return catalogDeclared(manageProjectsToolName) }

// manageProjectsWrapsData reports whether the handler's answer carries
// another service's answer under data (a linked machine's project answer,
// agents at work, folder environments): that answer is the envelope's data.
func manageProjectsWrapsData(action, machine string) bool {
	if action == FleetAgentsAtWorkAction || FolderEnvironmentAction(action) {
		return true
	}
	return machine != "" && FleetProjectActionAllowed(action)
}

// manageProjectsHomeNotes are the home's own notes on a linked answer.
var manageProjectsHomeNotes = []string{"linked_task_id", "linked_task", "linked_record_error", "data_error"}

// manageProjectsEnvelope puts one manage_projects answer into the envelope.
// The fields the answer held are kept in data; only tool, action, machine and
// untrusted_content move to the envelope.
//
// Values are carried as their raw JSON, so a nested view keeps its own field
// order (a task view's state first).
func (r *Runtime) manageProjectsEnvelope(action string, args map[string]any, answer string) (string, error) {
	machine := strings.TrimSpace(asString(args["machine"]))
	return r.catalogEnvelope(manageProjectsDeclared(), action, args, answer, manageProjectsWrapsData(action, machine))
}

// catalogEnvelope puts one catalog tool's answer into the envelope. wraps
// says the answer carries another service's answer under data (a linked
// machine's answer): that answer is the envelope's data, with the home's
// notes.
func (r *Runtime) catalogEnvelope(catalog CatalogTool, action string, args map[string]any, answer string, wraps bool) (string, error) {
	machine := strings.TrimSpace(asString(args["machine"]))
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		return "", fmt.Errorf("%s %s answer is not a JSON object: %w", catalog.Name, action, err)
	}
	declared, _ := catalog.Action(action)
	untrusted := declared.Untrusted || machine != "" || strings.TrimSpace(string(decoded["untrusted_content"])) == "true"
	var data json.RawMessage
	var object map[string]json.RawMessage
	if wraps {
		data = decoded["data"]
		var notes []string
		for _, key := range manageProjectsHomeNotes {
			if _, ok := decoded[key]; ok {
				notes = append(notes, key)
			}
		}
		if json.Unmarshal(data, &object) != nil {
			object = nil
		}
		if len(notes) > 0 {
			if object == nil {
				object = map[string]json.RawMessage{}
				if len(data) > 0 && string(data) != "null" {
					object["result"] = data
				}
			}
			// The home's notes are its own: the machine's answer cannot
			// stand in for them.
			for _, key := range manageProjectsHomeNotes {
				delete(object, key)
			}
			for _, key := range notes {
				object[key] = decoded[key]
			}
			encoded, err := json.Marshal(object)
			if err != nil {
				return "", err
			}
			data = encoded
		}
	} else {
		object = make(map[string]json.RawMessage, len(decoded))
		for key, value := range decoded {
			if envelopeOwnedKey(key, value) {
				continue
			}
			object[key] = value
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return "", err
		}
		data = encoded
	}
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	parts := EnvelopeParts{Tool: catalog.Name, Action: action, Machine: machine, Data: data, Untrusted: untrusted}
	if machine == "" {
		parts.Machine = r.homeMachineName()
	}
	parts.State = envelopeState(declared, object, decoded)
	next := envelopeNext(catalog, declared, args, machine, object)
	if next != nil {
		parts.Next = next
	}
	parts.NextAction = envelopeNextAction(declared, object, next != nil)
	out, err := json.Marshal(NewEnvelope(parts))
	return string(out), err
}

// envelopeText reads a string field of an answer object.
func envelopeText(object map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := object[key]; ok && json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

// envelopeObject reads an object field of an answer object.
func envelopeObject(object map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	if raw, ok := object[key]; ok && json.Unmarshal(raw, &value) == nil {
		return value
	}
	return nil
}

// envelopeState is the call's outcome: partial when the home noted that
// part of the answer is missing, the answer's status when the action
// declares it as an outcome (CatalogAction.States), else ok. A task's, run's
// or machine's own status stays in data.
func envelopeState(declared CatalogAction, object, decoded map[string]json.RawMessage) string {
	for _, note := range []string{"data_error", "linked_record_error"} {
		if _, ok := decoded[note]; ok {
			return EnvelopeStatePartial
		}
	}
	status := envelopeText(object, "status")
	for _, state := range declared.States {
		if status == state {
			return state
		}
	}
	return EnvelopeStateOK
}

// envelopeNextAction is the next step: the answer's own (next_action or
// next_step, or its task's), else reading the rest of a cut answer, else the
// action's declared next step. "none" is never a next step.
func envelopeNextAction(declared CatalogAction, object map[string]json.RawMessage, cut bool) string {
	step := func(value string) string {
		if strings.EqualFold(value, "none") {
			return ""
		}
		return value
	}
	for _, candidate := range []string{envelopeText(object, "next_action"), envelopeText(object, "next_step"), envelopeText(envelopeObject(object, "task"), "next_action")} {
		if value := step(candidate); value != "" {
			return value
		}
	}
	if cut {
		return "Call next to read the rest."
	}
	return declared.NextAction
}

// envelopeNext is the call that reads the rest of a cut answer, in one
// shape: the exact arguments of that manage_projects call. A bare cursor
// (next_cursor) becomes this call again at that cursor; a continuation
// object (a cut message's next, a cut task result's next) is checked against
// the catalog and completed with the machine. nil when nothing was cut.
func envelopeNext(catalog CatalogTool, declared CatalogAction, args map[string]any, machine string, object map[string]json.RawMessage) map[string]any {
	if object == nil {
		return nil
	}
	var candidates []json.RawMessage
	for _, key := range []string{"next", "next_cursor"} {
		if raw, ok := object[key]; ok {
			candidates = append(candidates, raw)
		}
	}
	list := func(raw json.RawMessage) []map[string]json.RawMessage {
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		return items
	}
	// A cut message (read_task_session).
	for _, message := range list(object["messages"]) {
		if raw, ok := message["next"]; ok {
			candidates = append(candidates, raw)
		}
	}
	// A cut task result (get_task; wait_tasks' task views, here and from a
	// linked machine, where the view is that machine's get_task data).
	resultNext := func(task map[string]json.RawMessage) {
		if raw, ok := envelopeObject(task, "result")["next"]; ok {
			candidates = append(candidates, raw)
		}
	}
	resultNext(envelopeObject(object, "task"))
	for _, row := range list(object["tasks"]) {
		view := envelopeObject(row, "task")
		resultNext(view)
		resultNext(envelopeObject(view, "task"))
	}
	for _, raw := range candidates {
		var value any
		if json.Unmarshal(raw, &value) != nil || !envelopeContinuation(value) {
			continue
		}
		if next := envelopeNextCall(catalog, declared, args, machine, value); next != nil {
			return next
		}
	}
	return nil
}

// envelopeContinuationInputs are the inputs a continuation replaces; the
// call's other arguments carry over to it.
var envelopeContinuationInputs = map[string]bool{"cursor": true, "since_cursor": true, "message_id": true, "message_seq": true, "content_offset": true}

// envelopeNextCall turns one continuation into the arguments of the call
// that reads on, or nil when it is not a call the catalog declares.
func envelopeNextCall(catalog CatalogTool, declared CatalogAction, args map[string]any, machine string, value any) map[string]any {
	next := map[string]any{}
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			next[key] = item
		}
		if _, ok := next["action"]; !ok {
			for key, item := range envelopeCarriedArgs(declared, args) {
				if _, set := next[key]; !set {
					next[key] = item
				}
			}
			next["action"] = declared.Action
		}
	case string, float64:
		cursor, ok := envelopeCursor(v)
		if !ok {
			return nil
		}
		next = envelopeCarriedArgs(declared, args)
		next["action"], next["cursor"] = declared.Action, cursor
	default:
		return nil
	}
	name, _ := next["action"].(string)
	action, ok := catalog.Action(name)
	// Reading on is a read: a continuation never names a change.
	if !ok || name != action.Action || action.Level != CatalogLevelRead {
		return nil
	}
	// A call on a linked machine reads on there.
	if _, set := next["machine"]; !set && machine != "" && envelopeTakes(action, "machine") {
		next["machine"] = machine
	}
	for key := range next {
		if key != "action" && !envelopeTakes(action, key) {
			return nil
		}
	}
	return next
}

// envelopeCarriedArgs are the call's own declared arguments a continuation
// keeps (its filters, project, task, machine), without the continuation's.
func envelopeCarriedArgs(declared CatalogAction, args map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range args {
		if key != "action" && !envelopeContinuationInputs[key] && envelopeTakes(declared, key) {
			out[key] = value
		}
	}
	return out
}

// envelopeTakes reports whether the action declares the input.
func envelopeTakes(action CatalogAction, input string) bool {
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		if name == input {
			return true
		}
	}
	return false
}

// envelopeCursor reads a cursor as the integer the cursor input takes; a
// linked machine's answer may carry it as a string.
func envelopeCursor(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v > 0 && v == float64(int(v)) {
			return int(v), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// CheckEnvelope checks a catalog answer against the envelope contract
// (docs/tool-catalog.md): tool, action and machine set; state one of
// EnvelopeStates; next_action never "none"; next, when set, the arguments
// of a declared call of the same tool; and an answer whose task result was
// cut carries next.
func CheckEnvelope(answer string) error {
	var envelope struct {
		Tool       string          `json:"tool"`
		Action     string          `json:"action"`
		Machine    string          `json:"machine"`
		State      string          `json:"state"`
		Data       json.RawMessage `json:"data"`
		NextAction *string         `json:"next_action"`
		Next       json.RawMessage `json:"next"`
	}
	if err := json.Unmarshal([]byte(answer), &envelope); err != nil {
		return fmt.Errorf("not an envelope: %w", err)
	}
	catalog, ok := CatalogToolNamed(envelope.Tool)
	if !ok {
		return fmt.Errorf("tool %q is not a catalog tool", envelope.Tool)
	}
	if _, ok := catalog.Action(envelope.Action); !ok || envelope.Machine == "" {
		return fmt.Errorf("action %q or machine %q missing", envelope.Action, envelope.Machine)
	}
	if !EnvelopeStateKnown(envelope.State) {
		return fmt.Errorf("state %q is not one of %s", envelope.State, strings.Join(EnvelopeStates, ", "))
	}
	if envelope.NextAction != nil && (strings.TrimSpace(*envelope.NextAction) == "" || strings.EqualFold(strings.TrimSpace(*envelope.NextAction), "none")) {
		return fmt.Errorf("next_action %q: omit it when there is nothing to do", *envelope.NextAction)
	}
	if len(envelope.Next) > 0 && string(envelope.Next) != "null" {
		var next map[string]any
		if err := json.Unmarshal(envelope.Next, &next); err != nil {
			return fmt.Errorf("next is not the arguments of a call: %s", envelope.Next)
		}
		name, _ := next["action"].(string)
		action, ok := catalog.Action(name)
		if !ok || action.Action != name || action.Level != CatalogLevelRead {
			return fmt.Errorf("next names no declared read action: %s", envelope.Next)
		}
		for key := range next {
			if key != "action" && !envelopeTakes(action, key) {
				return fmt.Errorf("next passes %q, which %s does not take", key, name)
			}
		}
		return nil
	}
	if bytes.Contains(envelope.Data, []byte(`"result_truncated":true`)) || bytes.Contains(envelope.Data, []byte(`"next_cursor":`)) && envelopeDataCut(envelope.Data) {
		return errors.New("the answer was cut but carries no next")
	}
	return nil
}

// envelopeDataCut reports whether data names a further page (a next_cursor
// that continues).
func envelopeDataCut(data json.RawMessage) bool {
	var object map[string]any
	if json.Unmarshal(data, &object) != nil {
		return false
	}
	_, ok := envelopeCursor(object["next_cursor"])
	return ok
}

// envelopeOwnedKey reports whether an answer's key is metadata the envelope
// now carries: the tool and action names, the machine's name and the
// untrusted flag. The same key holding anything else (a machine status's
// machine object) is the answer's own data and stays.
func envelopeOwnedKey(key string, value json.RawMessage) bool {
	var text string
	var flag bool
	switch key {
	case "tool", "action", "machine":
		return json.Unmarshal(value, &text) == nil
	case "untrusted_content":
		return json.Unmarshal(value, &flag) == nil
	}
	return false
}

// EnvelopeData returns the data of a catalog answer, after checking the
// answer is an envelope (tool, action, machine and state set).
func EnvelopeData(answer string) (json.RawMessage, error) {
	var envelope struct {
		Tool    string          `json:"tool"`
		Action  string          `json:"action"`
		Machine string          `json:"machine"`
		State   string          `json:"state"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(answer), &envelope); err != nil {
		return nil, fmt.Errorf("catalog answer is not an envelope: %w", err)
	}
	if envelope.Tool == "" || envelope.Action == "" || envelope.Machine == "" || envelope.State == "" {
		return nil, fmt.Errorf("catalog answer is not an envelope: %s", answer)
	}
	return envelope.Data, nil
}

// envelopeContinuation reports whether value says there is more to read.
func envelopeContinuation(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case float64:
		return v != 0
	case bool:
		return v
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	}
	return true
}

// ControlCatalogCall is one Swarm Control (MCP) call of a catalog tool: the
// caller's verified principal and key levels, the tool and its arguments.
type ControlCatalogCall struct {
	Principal identity.Principal
	// Levels are the key's swarm:* levels; none keeps its API scopes alone,
	// which the caller checked.
	Levels []string
	// LevelsBound: Levels bind even when empty (a relay client's forwarded
	// levels); local_only actions are refused.
	LevelsBound bool
	// KeyID is the caller's AI key record; it names the caller as a sender.
	KeyID     string
	Tool      string
	Arguments map[string]any
}

// ExecuteControlCatalog runs a catalog tool for a Swarm Control caller: the
// same handler the Orchestrator runs, with the caller's identity and account,
// after the key's level is checked for the action. A Swarm Control caller is
// the person's own AI: it has no conversation in Swarm, so actions that wake
// a conversation say so instead of pretending.
func (r *Runtime) ExecuteControlCatalog(ctx context.Context, call ControlCatalogCall) (string, error) {
	if r == nil {
		return "", errors.New("tool catalog is not configured")
	}
	catalog, ok := CatalogToolNamed(call.Tool)
	if !ok {
		return "", fmt.Errorf("%s is not a catalog tool", call.Tool)
	}
	args := make(map[string]any, len(call.Arguments))
	for key, value := range call.Arguments {
		args[key] = value
	}
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
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
		return "", fmt.Errorf("%s has no action %q for Swarm Control; use one of: %s", catalog.Name, name, strings.Join(served, ", "))
	}
	if !CatalogLevelsAllow(action, call.Levels, call.LevelsBound) {
		return "", fmt.Errorf("this key is limited to %s; %s %s needs %s", strings.Join(call.Levels, ", "), catalog.Name, action.Action, action.Level.Scope())
	}
	p := call.Principal
	if !p.Valid() || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", fmt.Errorf("%s requires an authenticated user identity", catalog.Name)
	}
	args["action"] = action.Action
	sender := "user:" + strings.TrimSpace(p.UserID)
	if key := strings.TrimSpace(call.KeyID); key != "" {
		sender = "key:" + key
	}
	scope := WorkspaceScope{Principal: p, ControlCaller: true, ControlSender: sender}
	switch catalog.Name {
	case manageProjectsToolName:
		return r.executeManageProjects(identity.ContextWithPrincipal(ctx, p), scope, args)
	case manageWorkflowsToolName:
		return r.executeManageWorkflows(identity.ContextWithPrincipal(ctx, p), scope, args)
	}
	return "", fmt.Errorf("%s has no handler", catalog.Name)
}
