package api

import (
	"net/http"
	"net/url"
	"strconv"

	"swarm/packages/swarmd/internal/sandbox"
)

// Environments: the AI defines what a project folder's sandbox starts from
// (a base image plus setup commands and environment variables); Swarm builds
// it under gVisor and the folder's jobs then run on it.

// controlMCPEnvironmentLogChars bounds the build log a tool returns.
const controlMCPEnvironmentLogChars = 3000

func (c *controlMCPCall) environment(path string, wait int) (map[string]any, error) {
	query := url.Values{"workspace_path": {path}}
	if wait > 0 {
		query.Set("wait_seconds", strconv.Itoa(min(wait, controlMCPMaxWaitSeconds)))
	}
	response, err := c.dispatch(http.MethodGet, SandboxEnvironmentsPath, query, nil)
	if err != nil {
		return nil, err
	}
	return controlMCPMap(response["environment"]), nil
}

// controlMCPEnvironmentView trims the build log unless asked for (or failed)
// and says what to do next.
func controlMCPEnvironmentView(env map[string]any, place controlMCPJobPlace, includeLog bool) map[string]any {
	out := map[string]any{}
	for key, value := range env {
		out[key] = value
	}
	out["project"] = map[string]any{"id": place.projectID, "name": place.projectName}
	status, _ := env["status"].(string)
	if log, _ := env["log"].(string); log != "" {
		if includeLog || status == "failed" {
			if len(log) > controlMCPEnvironmentLogChars {
				log = "…" + log[len(log)-controlMCPEnvironmentLogChars:]
			}
			out["log"] = log
		} else {
			delete(out, "log")
		}
	}
	switch status {
	case "building":
		out["next"] = "Swarm is building it: swarm_get_environment with wait_seconds waits; jobs in this folder start once it is ready"
	case "ready":
		out["next"] = "with max_leases set, every agent in this folder runs its bash in a lease of it, up to max_leases at once"
		if manager := sandbox.Default(); manager != nil && manager.Active() {
			out["next"] = "jobs in this folder now run in this environment; with max_leases set, every agent also runs its bash in a lease of it, up to max_leases at once"
		}
	case "failed":
		out["next"] = "the build failed (see error and log): fix the definition with swarm_set_environment, or reset to Swarm's default"
	}
	return out
}

func controlMCPSetEnvironment(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	if reset, _ := args["reset"].(bool); reset {
		response, err := c.dispatch(http.MethodDelete, SandboxEnvironmentsPath, url.Values{"workspace_path": {place.path}}, nil)
		if err != nil {
			return nil, err
		}
		return controlMCPEnvironmentView(controlMCPMap(response["environment"]), place, false), nil
	}
	if rebuild, _ := args["rebuild"].(bool); rebuild {
		body := map[string]any{"workspace_path": place.path, "wait_seconds": min(max(controlMCPInt(args, "wait_seconds", 0), 0), controlMCPMaxWaitSeconds)}
		response, err := c.dispatch(http.MethodPost, SandboxEnvironmentsPath+"/rebuild", nil, body)
		if err != nil {
			return nil, err
		}
		out := controlMCPEnvironmentView(controlMCPMap(response["environment"]), place, false)
		out["build_started"] = response["build_started"]
		return out, nil
	}
	if _, appendSetup := args["append_setup"]; appendSetup || args["unset_env"] != nil {
		return controlMCPMergeEnvironment(c, place, args)
	}
	setup := []string{}
	for _, raw := range controlMCPList(args["setup"]) {
		if command, ok := raw.(string); ok {
			setup = append(setup, command)
		}
	}
	env := map[string]string{}
	for name, raw := range controlMCPMap(args["env"]) {
		value, ok := raw.(string)
		if !ok {
			return nil, controlMCPToolFailure("env values must be strings (%s)", name)
		}
		env[name] = value
	}
	body := map[string]any{"workspace_path": place.path, "base_image": controlMCPString(args, "base_image"), "setup": setup, "env": env}
	if _, ok := args["max_leases"]; ok {
		body["max_leases"] = controlMCPInt(args, "max_leases", 0)
	}
	response, err := c.dispatch(http.MethodPut, SandboxEnvironmentsPath, nil, body)
	if err != nil {
		return nil, err
	}
	view := controlMCPMap(response["environment"])
	if wait := controlMCPInt(args, "wait_seconds", 0); wait > 0 && view["status"] == "building" {
		if view, err = c.environment(place.path, wait); err != nil {
			return nil, err
		}
	}
	out := controlMCPEnvironmentView(view, place, false)
	out["build_started"] = response["build_started"]
	return out, nil
}

// controlMCPMergeEnvironment changes only what the call names (PATCH):
// append_setup adds commands, unset_env removes variables, env sets them,
// setup replaces the commands and base_image the image.
func controlMCPMergeEnvironment(c *controlMCPCall, place controlMCPJobPlace, args map[string]any) (any, error) {
	body := map[string]any{"workspace_path": place.path}
	for _, key := range []string{"base_image", "setup", "append_setup", "env", "unset_env", "max_leases"} {
		if value, ok := args[key]; ok && value != nil && value != "" {
			body[key] = value
		}
	}
	if wait := controlMCPInt(args, "wait_seconds", 0); wait > 0 {
		body["wait_seconds"] = min(wait, controlMCPMaxWaitSeconds)
	}
	response, err := c.dispatch(http.MethodPatch, SandboxEnvironmentsPath, nil, body)
	if err != nil {
		return nil, err
	}
	out := controlMCPEnvironmentView(controlMCPMap(response["environment"]), place, false)
	out["build_started"] = response["build_started"]
	return out, nil
}

func controlMCPGetEnvironment(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	view, err := c.environment(place.path, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		return nil, err
	}
	includeLog, _ := args["include_log"].(bool)
	return controlMCPEnvironmentView(view, place, includeLog), nil
}

// environmentBlocksJob refuses a job while the folder's environment is
// building or failed, so a job never runs on an environment the AI did not
// mean (a folder without one runs on Swarm's default).
func (c *controlMCPCall) environmentBlocksJob(place controlMCPJobPlace) error {
	env, err := c.environment(place.path, 0)
	if err != nil || env == nil {
		return nil // an unreadable environment never blocks; the sandbox decides
	}
	switch env["status"] {
	case "building":
		return controlMCPToolFailure("the environment of %s is still building: wait with swarm_get_environment (wait_seconds), then give the job", place.path)
	case "failed":
		message, _ := env["error"].(string)
		return controlMCPToolFailure("the environment of %s failed to build (%s): fix it with swarm_set_environment or reset it, then give the job", place.path, controlMCPTruncate(message, 300))
	}
	return nil
}
