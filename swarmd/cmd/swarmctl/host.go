package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// swarmctl host lists and decides host change requests over the private
// daemon socket, as the machine owner: an AI setting the machine up may ask
// Swarm to see a folder; only the owner, here or in the app, approves it.
func cmdHost(args []string) error {
	return runHost(args, os.Stdout)
}

func runHost(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: swarmctl host <list [--status pending]|approve ID [--reason TEXT]|deny ID [--reason TEXT]> [--socket PATH]")
	}
	sub, rest := args[0], args[1:]
	id := ""
	if (sub == "approve" || sub == "deny") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		id, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("host "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "daemon local-transport socket path")
	status := fs.String("status", "", "list only this status (pending, applying, applied, failed, denied)")
	reason := fs.String("reason", "", "why the change is approved or denied")
	if err := fs.Parse(rest); err != nil {
		return errors.New("invalid flags; see --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch sub {
	case "list":
		path := "/v1/host/changes"
		if s := strings.TrimSpace(*status); s != "" {
			path += "?status=" + url.QueryEscape(s)
		}
		client, err := newSetupClient(*socket)
		if err != nil {
			return err
		}
		return secretGet(client, path, output)
	case "approve", "deny":
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, "/?#") {
			return errors.New("a host change ID is required, e.g. swarmctl host approve hc_...")
		}
		client, err := newSetupClient(*socket)
		if err != nil {
			return err
		}
		payload := map[string]any{}
		if r := strings.TrimSpace(*reason); r != "" {
			payload["reason"] = r
		}
		return secretJSON(client, http.MethodPost, "/v1/host/changes/"+url.PathEscape(id)+"/"+sub, payload, output)
	default:
		return fmt.Errorf("unknown host subcommand %q", sub)
	}
}
