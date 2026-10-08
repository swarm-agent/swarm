package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

// Setup deliberately uses only the daemon's private Unix transport. Container
// exec as the daemon UID is the authority; no TCP fallback is used.
func cmdSetup(args []string) error {
	return runSetup(args, os.Stdin, os.Stdout)
}

func runSetup(args []string, input io.Reader, output io.Writer) error {
	if len(args) > 0 && (args[0] == "sdk-token" || args[0] == "revoke-sdk-token") {
		return runSetupSDKToken(args, output)
	}
	if len(args) > 0 && args[0] == "sealed-agent" {
		return runSetupSealedAgent(args, input, output)
	}
	if len(args) == 0 {
		return errors.New("usage: swarmctl setup <status|identity|credential|model|workspace|complete|sealed-agent|sdk-token|revoke-sdk-token> --help")
	}
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	// Do not echo unknown arguments: a mistaken --api-key SECRET must not leak.
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket (defaults to canonical data root)")
	var username, name, provider, model, thinking, role, tier, contextMode, path *string
	var stdin *bool
	switch args[0] {
	case "status", "complete":
	case "identity":
		username = fs.String("username", "", "new owner name")
		name = fs.String("name", "", "Swarm name")
	case "credential":
		provider = fs.String("provider", "", "provider ID")
		stdin = fs.Bool("api-key-stdin", false, "read first provider API key from redirected stdin, never argv")
	case "model":
		role = fs.String("role", "", "action, plan, compact, finder, coder, designer, or router")
		provider = fs.String("provider", "", "provider ID (required)")
		model = fs.String("model", "", "model ID (required)")
		thinking = fs.String("thinking", "", "supported thinking level (required)")
		tier = fs.String("service-tier", "", "supported service tier")
		contextMode = fs.String("context-mode", "", "supported context mode")
	case "workspace":
		path = fs.String("path", "", "absolute path to selected, clean, committed repository")
		name = fs.String("name", "", "workspace display name")
	default:
		return errors.New("unknown setup operation")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(output, "Usage: swarmctl setup %s [flags]\n", args[0])
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid setup flags; use --help (credentials are accepted only through stdin)")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected setup arguments; use --help")
	}
	method, route := http.MethodPost, "/v1/onboarding"
	var payload any
	switch args[0] {
	case "status":
		method = http.MethodGet
	case "identity":
		if strings.TrimSpace(*username) == "" || strings.TrimSpace(*name) == "" {
			return errors.New("--username and --name are required")
		}
		payload = map[string]string{"username": strings.TrimSpace(*username), "swarm_name": strings.TrimSpace(*name)}
	case "credential":
		if strings.TrimSpace(*provider) == "" || !*stdin {
			return errors.New("--provider and --api-key-stdin are required")
		}
		// A terminal would echo the key. Require a pipe or file, e.g. a secret
		// manager stream passed to docker exec -i (without -t).
		if file, ok := input.(*os.File); ok {
			info, err := file.Stat()
			if err != nil || info.Mode()&os.ModeCharDevice != 0 {
				return errors.New("redirect a secret file or pipe to stdin; interactive key entry is not supported")
			}
		}
		key, err := io.ReadAll(io.LimitReader(input, 16385))
		if err != nil || len(key) > 16384 || strings.TrimSpace(string(key)) == "" {
			return errors.New("API key input must be nonempty and at most 16384 bytes")
		}
		payload = map[string]any{"provider": strings.TrimSpace(*provider), "type": "api", "api_key": strings.TrimSpace(string(key)), "active": true}
		route = "/v1/onboarding/provider/credential"
	case "model":
		if strings.TrimSpace(*provider) == "" || strings.TrimSpace(*model) == "" || strings.TrimSpace(*thinking) == "" {
			return errors.New("--provider, --model and --thinking are required; setup never chooses a model")
		}
		group := "system_agents"
		switch *role {
		case "action", "plan":
			group = "swarm"
		case "compact", "finder", "coder", "designer", "router":
		default:
			return errors.New("--role must name action, plan, compact, finder, coder, designer, or router")
		}
		assignment := map[string]string{"provider": *provider, "model": *model, "thinking": *thinking, "service_tier": *tier, "context_mode": *contextMode}
		payload = map[string]any{group: map[string]any{*role: assignment}}
		method, route = http.MethodPatch, "/v1/agent-model-settings"
	case "workspace":
		if !filepath.IsAbs(*path) {
			return errors.New("--path must be an absolute repository path inside the container")
		}
		payload = map[string]any{"path": *path, "name": *name, "make_current": true}
		route = "/v1/workspace/add"
	case "complete":
		payload = map[string]bool{"desktop_onboarding_complete": true}
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if args[0] == "complete" {
		var status setupStatus
		if err := setupRequest(client, http.MethodGet, "/v1/onboarding", nil, &status); err != nil {
			return err
		}
		if !status.Identity.Bootstrapped || status.Heuristics.CredentialCount < 1 || status.Heuristics.WorkspaceCount < 1 {
			return errors.New("setup incomplete: identity, provider credential and saved workspace are required")
		}
		var settings setupModels
		if err := setupRequest(client, http.MethodGet, "/v1/agent-model-settings", nil, &settings); err != nil {
			return err
		}
		for _, assignment := range []setupAssignment{settings.Settings.Swarm.Action, settings.Settings.Swarm.Plan, settings.Settings.SystemAgents.Compact, settings.Settings.SystemAgents.Finder, settings.Settings.SystemAgents.Coder, settings.Settings.SystemAgents.Designer, settings.Settings.SystemAgents.Router} {
			if assignment.Provider == "" || assignment.Model == "" || assignment.Thinking == "" {
				return errors.New("setup incomplete: all canonical agent model assignments are required")
			}
		}
	}
	// Never print raw responses, cookies or backend errors (provider failures may
	// contain credentials). Only a typed, non-secret status is shown.
	var status setupStatus
	if err := setupRequest(client, method, route, payload, &status); err != nil {
		return err
	}
	if args[0] == "status" {
		fmt.Fprintf(output, "identity=%t onboarding_required=%t credentials=%d workspaces=%d\n", status.Identity.Bootstrapped, status.NeedsOnboarding, status.Heuristics.CredentialCount, status.Heuristics.WorkspaceCount)
		return nil
	}
	fmt.Fprintln(output, "Setup operation saved.")
	return nil
}

type setupStatus struct {
	NeedsOnboarding bool `json:"needs_onboarding"`
	Identity        struct {
		Bootstrapped bool `json:"bootstrapped"`
	} `json:"identity"`
	Heuristics struct {
		CredentialCount int `json:"credential_count"`
		WorkspaceCount  int `json:"saved_workspace_count"`
	} `json:"heuristics"`
}

type setupAssignment struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}
type setupModels struct {
	Settings struct {
		Swarm        struct{ Action, Plan setupAssignment }                             `json:"swarm"`
		SystemAgents struct{ Compact, Finder, Coder, Designer, Router setupAssignment } `json:"system_agents"`
	} `json:"agent_model_settings"`
}

func newSetupClient(socket string) (*http.Client, error) {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		socket = strings.TrimSpace(os.Getenv(localTransportSocketEnv))
	}
	if socket == "" {
		root, err := storagecontract.ResolveRoot(storagecontract.RootData, storagecontract.Options{OverrideRoots: map[storagecontract.RootKind]string{storagecontract.RootData: os.Getenv("DATA_DIR")}})
		if err != nil {
			return nil, errors.New("cannot resolve canonical data root; supply --socket")
		}
		socket = filepath.Join(root, "local-transport", "api.sock")
	}
	if !filepath.IsAbs(socket) {
		return nil, errors.New("setup socket must be an absolute path")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func setupRequest(client *http.Client, method, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("cannot encode setup request")
	}
	req, err := http.NewRequest(method, "http://swarm-local-transport"+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid setup request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach private daemon socket; start the container and run setup as its daemon user")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("setup request rejected (HTTP %d); check prerequisites and selected inputs; response withheld to protect credentials", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("invalid setup response; operation may have been saved, inspect setup status before retrying")
	}
	return nil
}
