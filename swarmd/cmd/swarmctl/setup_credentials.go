package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Provider credentials after first setup, for boxes with no shell (a sealed
// container): list, add and remove credentials. `setup credential` stays the
// first-provider onboarding step. API keys are accepted only through redirected
// stdin; the listing shows only provider, ID, type and whether it is active.
var credentialIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func runSetupCredentials(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket (defaults to canonical data root)")
	var provider, id, project, location *string
	var stdin *bool
	switch args[0] {
	case "credentials":
	case "add-credential":
		provider = fs.String("provider", "", "provider ID (vertex uses the machine's Google identity; no key)")
		stdin = fs.Bool("api-key-stdin", false, "read provider API key from redirected stdin, never argv")
		project = fs.String("project", "", "Google Cloud project")
		location = fs.String("location", "global", "Vertex AI location: global or a region such as us-central1")
	case "credential-order":
		provider = fs.String("provider", "", "provider ID (claudecode)")
	case "remove-credential":
		provider = fs.String("provider", "", "provider ID")
		id = fs.String("id", "", "credential ID from `setup credentials`")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(output, "Usage: swarmctl setup %s [flags]\n", args[0])
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid setup flags; use --help")
	}
	if args[0] == "credential-order" {
		if *provider != "claudecode" || fs.NArg() != 1 {
			return errors.New("usage: setup credential-order --provider claudecode a,b,c")
		}
		client, err := newSetupClient(*socket)
		if err != nil {
			return err
		}
		defer client.CloseIdleConnections()
		var ignored map[string]any
		if err := setupRequest(client, http.MethodPost, "/v1/claudecode/accounts", map[string]any{"order": strings.Split(fs.Arg(0), ",")}, &ignored); err != nil {
			return err
		}
		fmt.Fprintln(output, "Claude Code account order saved.")
		return nil
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected setup arguments; use --help")
	}
	var method, route string
	var payload any
	switch args[0] {
	case "credentials":
		method, route = http.MethodGet, "/v1/auth/credentials"
	case "add-credential":
		method, route = http.MethodPost, "/v1/auth/credentials"
		*provider = strings.ToLower(strings.TrimSpace(*provider))
		if *provider == "claudecode" {
			if *stdin {
				return errors.New("Claude Code sign-in is performed by the person; drop --api-key-stdin")
			}
			method, route = http.MethodPost, "/v1/claudecode/accounts"
			payload = map[string]any{}
			break
		}
		if *provider == "vertex" {
			if *stdin {
				return errors.New("--provider vertex reads no key; drop --api-key-stdin")
			}
			if strings.TrimSpace(*project) == "" {
				return errors.New("--project is required")
			}
			payload = map[string]any{"provider": "vertex", "type": "adc", "google_project": strings.TrimSpace(*project), "google_location": strings.TrimSpace(*location), "active": true}
			break
		}
		vertexFlags := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "project" || f.Name == "location" {
				vertexFlags = true
			}
		})
		if vertexFlags {
			return errors.New("--project and --location apply only to --provider vertex")
		}
		if *provider == "" || !*stdin {
			return errors.New("--provider and --api-key-stdin are required")
		}
		if file, ok := input.(*os.File); ok {
			info, err := file.Stat()
			if err != nil || info.Mode()&os.ModeCharDevice != 0 {
				return errors.New("redirect a secret file or pipe to stdin; interactive key entry is not supported")
			}
		}
		key, err := io.ReadAll(io.LimitReader(input, 16385))
		defer clear(key)
		if err != nil || len(key) > 16384 || strings.TrimSpace(string(key)) == "" {
			return errors.New("API key input must be nonempty and at most 16384 bytes")
		}
		fields := map[string]any{"provider": *provider, "type": "api", "api_key": strings.TrimSpace(string(key)), "active": true}
		defer delete(fields, "api_key")
		payload = fields
	case "remove-credential":
		if strings.TrimSpace(*provider) == "" || !credentialIDPattern.MatchString(strings.TrimSpace(*id)) {
			return errors.New("--provider and a valid --id are required")
		}
		method, route = http.MethodPost, "/v1/auth/credentials/delete"
		payload = map[string]string{"provider": strings.ToLower(strings.TrimSpace(*provider)), "id": strings.TrimSpace(*id)}
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	switch args[0] {
	case "credentials":
		var list struct {
			Records []struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
				Active   bool   `json:"active"`
				AuthType string `json:"auth_type"`
			} `json:"records"`
		}
		if err := setupRequest(client, method, route, nil, &list); err != nil {
			return err
		}
		type item struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Type     string `json:"type"`
			Active   bool   `json:"active"`
		}
		items := make([]item, 0, len(list.Records))
		for _, record := range list.Records {
			items = append(items, item{ID: record.ID, Provider: record.Provider, Type: record.AuthType, Active: record.Active})
		}
		return json.NewEncoder(output).Encode(map[string]any{"credentials": items})
	case "add-credential":
		var status struct {
			ID           string `json:"id"`
			LoginCommand string `json:"login_command"`
		}
		if err := setupRequestShowingRejection(client, method, route, payload, &status); err != nil {
			return err
		}
		if *provider == "claudecode" {
			fmt.Fprintf(output, "Claude Code account slot added (id %s). Run this on the machine to sign in:\n%s\n", status.ID, status.LoginCommand)
		} else if *provider == "vertex" {
			fmt.Fprintf(output, "Vertex AI credential saved (id %s).\n", status.ID)
		} else {
			fmt.Fprintf(output, "API credential saved (id %s).\n", status.ID)
		}
	default:
		var ignored map[string]any
		if err := setupRequest(client, method, route, payload, &ignored); err != nil {
			return err
		}
		fmt.Fprintln(output, "Credential removed.")
	}
	return nil
}
