package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// sealedAgentFile is what `swarmctl setup sealed-agent --stdin` reads: the
// client tools an application answers and one agent that may use only them.
type sealedAgentFile struct {
	Tools []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
		Effect      string         `json:"effect,omitempty"`
		TimeoutMS   int            `json:"timeout_ms,omitempty"`
	} `json:"tools"`
	Agent struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Prompt      string   `json:"prompt"`
		Provider    string   `json:"provider,omitempty"`
		Model       string   `json:"model,omitempty"`
		Thinking    string   `json:"thinking,omitempty"`
		Tools       []string `json:"tools"`
		// Limits bound one message's cost; unset fields use sealed defaults.
		Limits *struct {
			MaxSteps           int `json:"max_steps,omitempty"`
			MaxOutputTokens    int `json:"max_output_tokens,omitempty"`
			MaxHistoryMessages int `json:"max_history_messages,omitempty"`
			RunTimeoutMS       int `json:"run_timeout_ms,omitempty"`
		} `json:"limits,omitempty"`
	} `json:"agent"`
}

var sealedName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func runSetupSealedAgent(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("setup sealed-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	stdin := fs.Bool("stdin", false, "read the sealed agent definition (JSON) from stdin")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid sealed-agent flags; use --help")
	}
	if !*stdin || fs.NArg() != 0 {
		return errors.New("usage: swarmctl setup sealed-agent --stdin < agent.json")
	}
	var file sealedAgentFile
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("invalid sealed agent definition: %w", err)
	}
	declared := map[string]bool{}
	for _, tool := range file.Tools {
		if !sealedName.MatchString(tool.Name) {
			return fmt.Errorf("tool name %q must be lowercase letters, digits and underscores", tool.Name)
		}
		declared[tool.Name] = true
	}
	if !sealedName.MatchString(file.Agent.Name) || strings.TrimSpace(file.Agent.Prompt) == "" {
		return errors.New("agent needs a lowercase name and a prompt")
	}
	contract := map[string]any{}
	for _, name := range file.Agent.Tools {
		if !declared[name] {
			return fmt.Errorf("agent tool %q is not one of the client tools in this file", name)
		}
		contract[name] = map[string]any{"enabled": true}
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	for _, tool := range file.Tools {
		body := map[string]any{"kind": "client", "description": tool.Description, "input_schema": tool.InputSchema, "effect": tool.Effect, "timeout_ms": tool.TimeoutMS}
		if err := setupRequest(client, http.MethodPut, "/v2/custom-tools/"+url.PathEscape(tool.Name), body, &map[string]any{}); err != nil {
			return fmt.Errorf("tool %s: %w", tool.Name, err)
		}
	}
	agent := map[string]any{
		"mode": "subagent", "description": file.Agent.Description, "prompt": file.Agent.Prompt,
		"tool_contract": map[string]any{"preset": "custom", "tools": contract},
	}
	if file.Agent.Limits != nil {
		agent["limits"] = file.Agent.Limits
	}
	for key, value := range map[string]string{"provider": file.Agent.Provider, "model": file.Agent.Model, "thinking": file.Agent.Thinking} {
		if value != "" {
			agent[key] = value
		}
	}
	if err := setupRequest(client, http.MethodPut, "/v2/agents/"+url.PathEscape(file.Agent.Name), agent, &map[string]any{}); err != nil {
		return fmt.Errorf("agent %s: %w", file.Agent.Name, err)
	}
	_, err = fmt.Fprintf(output, "Sealed agent %s saved with %d client tool(s).\n", file.Agent.Name, len(file.Agent.Tools))
	return err
}
