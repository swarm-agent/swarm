package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	State   string `json:"state"`
	Data    any    `json:"data"`
	// NextAction is one sentence when there is a next step.
	NextAction string `json:"next_action,omitempty"`
	// Next is the cursor or continuation of an answer that was cut.
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
	manageProjectsCatalogOnce sync.Once
	manageProjectsCatalogMemo CatalogTool
)

// manageProjectsDeclared is the declaration, built once for lookups.
func manageProjectsDeclared() CatalogTool {
	manageProjectsCatalogOnce.Do(func() { manageProjectsCatalogMemo = manageProjectsCatalog() })
	return manageProjectsCatalogMemo
}

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
func (r *Runtime) manageProjectsEnvelope(action, machine, answer string) (string, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		return "", fmt.Errorf("manage_projects %s answer is not a JSON object: %w", action, err)
	}
	declared, _ := manageProjectsDeclared().Action(action)
	untrusted := declared.Untrusted || machine != "" || strings.TrimSpace(string(decoded["untrusted_content"])) == "true"
	var data json.RawMessage
	var object map[string]json.RawMessage
	if manageProjectsWrapsData(action, machine) {
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
	parts := EnvelopeParts{Tool: manageProjectsToolName, Action: action, Machine: machine, Data: data, Untrusted: untrusted}
	if machine == "" {
		parts.Machine = r.homeMachineName()
	}
	text := func(key string) string {
		var value string
		if raw, ok := object[key]; ok && json.Unmarshal(raw, &value) == nil {
			return strings.TrimSpace(value)
		}
		return ""
	}
	if object != nil {
		parts.State = text("status")
		if parts.NextAction = text("next_action"); parts.NextAction == "" {
			parts.NextAction = text("next_step")
		}
		for _, key := range []string{"next", "next_cursor"} {
			var value any
			if raw, ok := object[key]; ok && json.Unmarshal(raw, &value) == nil && envelopeContinuation(value) {
				parts.Next = raw
				break
			}
		}
		// A cut message (read_task_session) carries its own next; the
		// envelope names the first one, so a cut is never silent.
		var messages []map[string]json.RawMessage
		if raw, ok := object["messages"]; parts.Next == nil && ok && json.Unmarshal(raw, &messages) == nil {
			for _, message := range messages {
				var value any
				if raw, ok := message["next"]; ok && json.Unmarshal(raw, &value) == nil && envelopeContinuation(value) {
					parts.Next = raw
					break
				}
			}
		}
	}
	out, err := json.Marshal(NewEnvelope(parts))
	return string(out), err
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
	}
	return "", fmt.Errorf("%s has no handler", catalog.Name)
}
