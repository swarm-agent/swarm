package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func printRemoteUsage() {
	fmt.Println(`Usage: swarmctl remote <command> [flags]

Remote access is off until you initialize and enable it. The relay is a
service you deploy (for example packages/swarm-relay on Cloudflare).

  status                       show relay, device and pending authorization requests
  init --relay URL --name NAME [--allow-write] [--allow-approve] [--allow-manage]
                               create this machine's device key (does not connect)
  enable | disable             connect to / disconnect from the relay
  approve CODE [--scopes s,..] approve an AI client's authorization request
  deny CODE                    deny an AI client's authorization request
  reset                        disconnect, revoke the device token, delete the device key

All commands use the private daemon socket (--socket to override).`)
}

func cmdRemote(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRemoteUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	fs := flag.NewFlagSet("remote "+command, flag.ContinueOnError)
	socket := fs.String("socket", "", "private daemon Unix socket (defaults to canonical data root)")
	relay := fs.String("relay", "", "relay origin, e.g. https://swarm-relay.example.workers.dev")
	name := fs.String("name", "", "device name shown to AI clients")
	allowWrite := fs.Bool("allow-write", false, "allow remote clients to create sessions, send messages and stop runs")
	allowApprove := fs.Bool("allow-approve", false, "allow remote clients to approve or deny pending tool calls once")
	allowManage := fs.Bool("allow-manage", false, "allow remote clients to create/update/delete workers and change daily usage limits")
	scopes := fs.String("scopes", "", "comma-separated scopes to grant (default: everything requested that this machine allows)")
	positional := []string{}
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	var out map[string]any
	switch command {
	case "status":
		err = remoteRequest(client, http.MethodGet, "/v1/remote", nil, &out)
	case "init":
		err = remoteRequest(client, http.MethodPost, "/v1/remote/init", map[string]any{"relay_url": *relay, "device_name": *name, "allow_write": *allowWrite, "allow_approve": *allowApprove, "allow_manage": *allowManage}, &out)
	case "enable", "disable", "reset":
		err = remoteRequest(client, http.MethodPost, "/v1/remote/"+command, map[string]any{}, &out)
	case "approve", "deny":
		if len(positional) != 1 {
			return fmt.Errorf("usage: swarmctl remote %s CODE", command)
		}
		body := map[string]any{"code": positional[0]}
		if *scopes != "" {
			body["scopes"] = strings.Split(*scopes, ",")
		}
		path := "/v1/remote/consents/deny"
		if command == "approve" {
			path = "/v1/remote/consents/approve"
		}
		err = remoteRequest(client, http.MethodPost, path, body, &out)
	default:
		printRemoteUsage()
		return fmt.Errorf("unknown remote command %q", command)
	}
	if err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(os.Stdout, string(encoded))
	return nil
}

// Remote administration responses never contain the device key or token, so
// error messages are shown to the owner.
func remoteRequest(client *http.Client, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errors.New("cannot encode remote request")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, "http://swarm-local-transport"+path, body)
	if err != nil {
		return errors.New("invalid remote request")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach private daemon socket; run as the daemon user")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("invalid remote response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error == "" {
			failure.Error = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("remote request rejected (HTTP %d): %s", resp.StatusCode, failure.Error)
	}
	return json.Unmarshal(raw, out)
}
