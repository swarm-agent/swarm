package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Provider credentials after first setup, for boxes with no shell (a sealed
// container): list them, add a Vertex AI credential next to an existing
// provider, and remove one. `setup credential` stays the first-provider
// onboarding step. Nothing secret is read or printed here: the listing shows
// only provider, ID, type and whether it is active, and only Vertex (which
// uses the machine's Google identity) can be added this way.
var credentialIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func runSetupCredentials(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket (defaults to canonical data root)")
	var provider, id, project, location *string
	switch args[0] {
	case "credentials":
	case "add-credential":
		provider = fs.String("provider", "", "only vertex (the machine's Google identity; no key)")
		project = fs.String("project", "", "Google Cloud project")
		location = fs.String("location", "global", "Vertex AI location: global or a region such as us-central1")
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
	if fs.NArg() != 0 {
		return errors.New("unexpected setup arguments; use --help")
	}
	var method, route string
	var payload any
	switch args[0] {
	case "credentials":
		method, route = http.MethodGet, "/v1/auth/credentials"
	case "add-credential":
		if !strings.EqualFold(strings.TrimSpace(*provider), "vertex") {
			return errors.New("add-credential supports only --provider vertex; keys go through `setup credential --api-key-stdin`")
		}
		if strings.TrimSpace(*project) == "" {
			return errors.New("--project is required")
		}
		method, route = http.MethodPost, "/v1/auth/credentials"
		payload = map[string]any{"provider": "vertex", "type": "adc", "google_project": strings.TrimSpace(*project), "google_location": strings.TrimSpace(*location), "active": true}
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
			ID string `json:"id"`
		}
		if err := setupRequestShowingRejection(client, method, route, payload, &status); err != nil {
			return err
		}
		fmt.Fprintf(output, "Vertex AI credential saved (id %s).\n", status.ID)
	default:
		var ignored map[string]any
		if err := setupRequest(client, method, route, payload, &ignored); err != nil {
			return err
		}
		fmt.Fprintln(output, "Credential removed.")
	}
	return nil
}
