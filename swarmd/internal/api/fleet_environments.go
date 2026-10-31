package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Folder environments for the Orchestrator's manage_projects actions
// (list_environments, get_environment, update_environment,
// rebuild_environment). Without machine they call this Swarm's service
// functions (sandbox_environments.go); with machine they call the same routes
// on the linked machine through the link, which runs the same functions
// there. The home's linked work rule for the machine gates every linked call
// (off refuses; ask and auto were already decided by the permission gate).

// ManageFolderEnvironment runs one folder environment action here or on a
// linked machine and returns its JSON answer.
func (s *Server) ManageFolderEnvironment(ctx context.Context, p identity.Principal, machine, action string, args map[string]any) (json.RawMessage, error) {
	if !tool.FolderEnvironmentAction(action) {
		return nil, fmt.Errorf("%s is not a folder environment action; use one of: %s", action, strings.Join(tool.FolderEnvironmentActions(), ", "))
	}
	where := ""
	if machine = strings.TrimSpace(machine); machine != "" {
		where = machine + " itself"
	}
	if err := refuseSandboxEnvironmentFields(args, where); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(controlMCPString(args, "workspace_path"))
	wait, err := environmentArgInt(args, "wait_seconds")
	if err != nil {
		return nil, err
	}
	wait = min(max(wait, 0), sandboxEnvironmentMaxWait)
	var change SandboxEnvironmentChange
	if action == tool.FolderEnvironmentUpdate {
		if change, err = sandboxEnvironmentChangeFromArgs(args); err != nil {
			return nil, err
		}
		change.WaitSeconds = wait
	}
	if action != tool.FolderEnvironmentList && path == "" {
		return nil, fmt.Errorf("%s needs workspace_path: the absolute path of the folder (list_environments shows them)", action)
	}
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return s.localFolderEnvironment(ctx, p, action, path, wait, change)
	}
	return s.linkedFolderEnvironment(ctx, p, machine, action, path, wait, change)
}

func (s *Server) localFolderEnvironment(ctx context.Context, p identity.Principal, action, path string, wait int, change SandboxEnvironmentChange) (json.RawMessage, error) {
	out := map[string]any{"ok": true}
	switch action {
	case tool.FolderEnvironmentList:
		views, err := s.ListSandboxEnvironments(ctx, p)
		if err != nil {
			return nil, err
		}
		out["environments"], out["count"] = views, len(views)
	case tool.FolderEnvironmentGet:
		view, err := s.GetSandboxEnvironment(ctx, p, path, wait)
		if err != nil {
			return nil, err
		}
		out["environment"] = view
	case tool.FolderEnvironmentUpdate:
		update := s.UpdateSandboxEnvironment
		if change.Replace {
			update = s.replaceSandboxEnvironment
		}
		view, build, err := update(ctx, p, change)
		if err != nil {
			return nil, err
		}
		out["environment"], out["build_started"] = view, build
	case tool.FolderEnvironmentRebuild:
		view, err := s.RebuildSandboxEnvironment(ctx, p, path, wait)
		if err != nil {
			return nil, err
		}
		out["environment"], out["build_started"] = view, true
	}
	return json.Marshal(out)
}

func (s *Server) linkedFolderEnvironment(ctx context.Context, p identity.Principal, machine, action, path string, wait int, change SandboxEnvironmentChange) (json.RawMessage, error) {
	if s.fleet == nil || s.fleetTokens == nil {
		return nil, errors.New("fleet service unavailable")
	}
	if s.sessions == nil {
		return nil, errors.New("session service unavailable")
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return nil, err
	}
	if !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return nil, fmt.Errorf("linked work for machine %q is off, so its folder environments cannot be read or changed from here; the person turns it on for that machine on the Permissions page", machine)
	}
	client, linked, _, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		return nil, fmt.Errorf("linked machine %s: %w", machine, err)
	}
	method, route := http.MethodGet, SandboxEnvironmentsPath
	var body any
	switch action {
	case tool.FolderEnvironmentList:
		route += "/list"
	case tool.FolderEnvironmentGet:
		query := url.Values{"workspace_path": {path}}
		if wait > 0 {
			query.Set("wait_seconds", strconv.Itoa(wait))
		}
		route += "?" + query.Encode()
	case tool.FolderEnvironmentUpdate:
		method, body = http.MethodPatch, change
		if change.Replace {
			// The machine's held wait runs on the merge only; a replace
			// answers at once there.
			method, body = http.MethodPut, replaceSandboxEnvironmentBody(change)
		}
	case tool.FolderEnvironmentRebuild:
		method, route = http.MethodPost, route+"/rebuild"
		body = map[string]any{"workspace_path": path, "wait_seconds": wait}
	}
	var result json.RawMessage
	callErr := client.JSON(ctx, method, route, body, &result)
	s.noteFleetContact(linked, callErr)
	if callErr != nil {
		var refused *fleet.Error
		if errors.As(callErr, &refused) {
			return nil, fmt.Errorf("%s refused %s: %w", machine, action, refused)
		}
		return nil, fleetUnreachableError(machine, linked)
	}
	return result, nil
}

// fleetUnreachableError says a linked machine did not answer, when, and when
// the home last reached it, instead of a bare "could not reach". The
// transport error is not repeated: it names the machine's private address.
func fleetUnreachableError(machine string, linked pebblestore.FleetMachine) error {
	last := "it has never answered this home"
	if linked.LastContactAt > 0 {
		last = "last successful contact at " + time.UnixMilli(linked.LastContactAt).UTC().Format(time.RFC3339)
	}
	return fmt.Errorf("could not reach linked machine %s: %s unreachable, the call failed at %s; %s. The machine may be off or its link address unreachable; try again later", machine, machine, time.Now().UTC().Format(time.RFC3339), last)
}

// sandboxEnvironmentChangeFromArgs reads update_environment's arguments.
func sandboxEnvironmentChangeFromArgs(args map[string]any) (SandboxEnvironmentChange, error) {
	change := SandboxEnvironmentChange{WorkspacePath: strings.TrimSpace(controlMCPString(args, "workspace_path"))}
	if err := environmentReplaceConflict(args); err != nil {
		return change, err
	}
	change.Replace, _ = args["replace"].(bool)
	if raw, ok := args["base_image"]; ok && raw != nil {
		image, isText := raw.(string)
		if !isText {
			return change, errors.New("base_image must be text")
		}
		change.BaseImage = strings.TrimSpace(image)
	}
	if raw, ok := args["setup"]; ok && raw != nil {
		setup, err := environmentArgStrings(raw, "setup")
		if err != nil {
			return change, err
		}
		change.Setup = &setup
	}
	if raw, ok := args["append_setup"]; ok && raw != nil {
		commands, err := environmentArgStrings(raw, "append_setup")
		if err != nil {
			return change, err
		}
		change.AppendSetup = commands
	}
	if raw, ok := args["unset_env"]; ok && raw != nil {
		names, err := environmentArgStrings(raw, "unset_env")
		if err != nil {
			return change, err
		}
		change.UnsetEnv = names
	}
	if raw, ok := args["env"]; ok && raw != nil {
		values, isMap := raw.(map[string]any)
		if !isMap {
			return change, errors.New("env must be an object of NAME: value text")
		}
		change.Env = make(map[string]string, len(values))
		for name, value := range values {
			text, isText := value.(string)
			if !isText {
				return change, fmt.Errorf("env values must be text (%s)", name)
			}
			change.Env[name] = text
		}
	}
	if raw, ok := args["max_leases"]; ok && raw != nil { // null is "not given", like the other fields
		n, err := environmentArgInt(args, "max_leases")
		if err != nil {
			return change, err
		}
		change.MaxLeases = &n
	}
	return change, nil
}

func environmentArgStrings(raw any, name string) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		if typed, isTyped := raw.([]string); isTyped {
			return append([]string{}, typed...), nil
		}
		return nil, fmt.Errorf("%s must be a list of text", name)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, isText := item.(string)
		if !isText {
			return nil, fmt.Errorf("%s must be a list of text", name)
		}
		out = append(out, text)
	}
	return out, nil
}

func environmentArgInt(args map[string]any, name string) (int, error) {
	switch v := args[name].(type) {
	case nil:
		return 0, nil
	case float64:
		if v != math.Trunc(v) || v < math.MinInt32 || v > math.MaxInt32 {
			return 0, fmt.Errorf("%s must be a whole number", name)
		}
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, fmt.Errorf("%s must be a whole number", name)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%s must be a whole number", name)
}

// environmentReplaceConflict refuses replace: true with an input that only
// means something to a merge (append_setup, unset_env) or with another
// operation (rebuild, reset), and without base_image (an environment always
// has one; the definition route reads an empty one as "keep the current
// definition"). update_environment merges by default: only the fields given
// change; replace: true sets the whole definition, so setup and env not given
// are cleared.
func environmentReplaceConflict(args map[string]any) error {
	if replace, _ := args["replace"].(bool); !replace {
		return nil
	}
	if image, _ := args["base_image"].(string); strings.TrimSpace(image) == "" {
		return errors.New("replace sets the whole definition and needs base_image")
	}
	for _, key := range []string{"append_setup", "unset_env", "rebuild", "reset"} {
		if value, ok := args[key]; ok && value != nil && value != false {
			return fmt.Errorf("replace sets the whole definition (base_image, setup, env, max_leases); %s does not go with it", key)
		}
	}
	return nil
}

// replaceSandboxEnvironment is update_environment's replace: true here: the
// whole definition, then the held wait (as the merge holds it).
func (s *Server) replaceSandboxEnvironment(ctx context.Context, p identity.Principal, change SandboxEnvironmentChange) (map[string]any, bool, error) {
	setup := []string{}
	if change.Setup != nil {
		setup = *change.Setup
	}
	view, build, err := s.DefineSandboxEnvironment(ctx, p, change.WorkspacePath, pebblestore.SandboxEnvironmentDefinition{BaseImage: change.BaseImage, Setup: setup, Env: change.Env}, change.MaxLeases)
	if err != nil || change.WaitSeconds <= 0 {
		return view, build, err
	}
	view, err = s.GetSandboxEnvironment(ctx, p, change.WorkspacePath, change.WaitSeconds)
	return view, build, err
}

// replaceSandboxEnvironmentBody is the PUT body of a replace sent to a
// linked machine.
func replaceSandboxEnvironmentBody(change SandboxEnvironmentChange) map[string]any {
	setup := []string{}
	if change.Setup != nil {
		setup = *change.Setup
	}
	env := change.Env
	if env == nil {
		env = map[string]string{}
	}
	body := map[string]any{"workspace_path": change.WorkspacePath, "base_image": change.BaseImage, "setup": setup, "env": env}
	if change.MaxLeases != nil {
		body["max_leases"] = *change.MaxLeases
	}
	return body
}
