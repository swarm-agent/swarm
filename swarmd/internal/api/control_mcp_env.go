package api

import (
	"net/http"
	"net/url"
	"strconv"
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
		out["next"] = "jobs in this folder now run in this environment"
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
	if checks, ok := args["checks"]; ok {
		body["checks"] = checks
	}
	if timeout, ok := args["check_timeout_minutes"]; ok {
		body["check_timeout_minutes"] = timeout
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

func controlMCPEnvironmentTools() []controlMCPTool {
	project := controlMCPStringProp("The project (id or name from swarm_list_projects). May be left out when there is only one project.", 0, 256)
	workspace := controlMCPStringProp("Which of the project's folders (an absolute path from swarm_list_projects). May be left out when the project has one folder.", 0, 4096)
	waitProp := map[string]any{"type": "integer", "minimum": 0, "maximum": controlMCPMaxWaitSeconds, "description": "Wait up to this many seconds (at most 110) while Swarm builds it. Default 0."}
	return []controlMCPTool{
		{
			Name: "swarm_set_environment", Title: "Define the environment a folder's jobs run in",
			Description: "Define what a project folder's sandbox starts from: base_image (an image name such as ubuntu:24.04, node:22, golang:1.24 or one the person already uses) plus setup commands (run once as root while Swarm builds it, for example apt-get install -y ...) and environment variables (not secrets: those go through the secret system). Swarm builds it in an isolated gVisor container with internet access but no host folders, saves it as an image named after the definition (the same definition is reused, never rebuilt), and checks that bash and git run in it. Jobs in that folder then run on it, with the same sandbox protections. While it builds, and if it fails, jobs in the folder are refused; reset goes back to Swarm's default environment (Ubuntu with git, Python 3, Node.js, build-essential, curl and jq).",
			InputSchema: controlMCPObject(nil, map[string]any{
				"project":               project,
				"workspace":             workspace,
				"base_image":            controlMCPStringProp("The image to start from.", 0, 255),
				"checks":                map[string]any{"type": "array", "maxItems": 10, "items": map[string]any{"type": "string", "maxLength": 4000}, "description": "Shell checks on the rebased head, as the sandbox user; never include secrets. Not part of image identity."},
				"check_timeout_minutes": map[string]any{"type": "integer", "minimum": 0, "maximum": 60, "description": "Total check time limit; 0 means 20 minutes."},
				"setup":                 map[string]any{"type": "array", "maxItems": 40, "items": map[string]any{"type": "string", "maxLength": 4000}, "description": "Shell commands run in order as root while building; each must succeed."},
				"env":                   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Environment variables for every command in the folder (not secrets)."},
				"reset":                 map[string]any{"type": "boolean", "description": "Remove the folder's environment: it runs on Swarm's default again."},
				"wait_seconds":          waitProp,
			}),
			Annotations: controlMCPWrites, call: controlMCPSetEnvironment,
		},
		{
			Name: "swarm_get_environment", Title: "Read a folder's environment",
			Description: "A project folder's environment: its definition (base image, setup, env), status (building, ready or failed, with the error), what the folder's sandbox runs on now, and with include_log the build log (always included when it failed). A folder without one runs on Swarm's default environment.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"project":      project,
				"workspace":    workspace,
				"include_log":  map[string]any{"type": "boolean"},
				"wait_seconds": waitProp,
			}),
			Annotations: controlMCPReadOnly, call: controlMCPGetEnvironment,
		},
	}
}
