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

// swarmctl secret manages secret slots over the private daemon socket, as the
// machine owner. The value is read from stdin only, never argv, and is never
// printed back.
func cmdSecret(args []string) error {
	return runSecret(args, os.Stdin, os.Stdout)
}

func runSecret(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: swarmctl secret <list|create|set-value|grant|revoke|grants|uses|delete> --help")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("secret "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "daemon local-transport socket path")
	var (
		name       = fs.String("name", "", "secret name (e.g. STRIPE_KEY)")
		desc       = fs.String("description", "", "what this secret is")
		hosts      = fs.String("hosts", "", "comma-separated allowed websites (e.g. api.stripe.com)")
		workspace  = fs.String("workspace", "", "absolute workspace path for a grant")
		days       = fs.Int("days", 0, "grant lifetime in days")
		hours      = fs.Int("hours", 0, "grant lifetime in hours (added to --days)")
		valueStdin = fs.Bool("value-stdin", false, "read the secret value from redirected stdin")
		grantID    = fs.String("grant-id", "", "grant id to revoke")
	)
	if err := fs.Parse(rest); err != nil {
		return errors.New("invalid flags; see --help")
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		return secretGet(client, "/v1/secrets", output)
	case "uses":
		return secretGet(client, "/v1/secrets/uses", output)
	case "create":
		if err := requireName(*name); err != nil {
			return err
		}
		hostList := splitHosts(*hosts)
		if len(hostList) == 0 {
			return errors.New("--hosts is required: the exact websites this secret may be sent to")
		}
		return secretJSON(client, http.MethodPost, "/v1/secrets", map[string]any{
			"name": *name, "description": *desc, "hosts": hostList,
		}, output)
	case "set-value":
		if err := requireName(*name); err != nil {
			return err
		}
		if !*valueStdin {
			return errors.New("pass --value-stdin and pipe the secret, e.g. printf %s \"$KEY\" | swarmctl secret set-value --name NAME --value-stdin")
		}
		value, err := readStdinSecret(input)
		if err != nil {
			return err
		}
		return secretRaw(client, "/v1/secrets/"+*name+"/value", value, output)
	case "grant":
		if err := requireName(*name); err != nil {
			return err
		}
		if strings.TrimSpace(*workspace) == "" {
			return errors.New("--workspace is required")
		}
		seconds := int64(*days)*86400 + int64(*hours)*3600
		if seconds <= 0 {
			return errors.New("set --days and/or --hours for the grant lifetime")
		}
		return secretJSON(client, http.MethodPost, "/v1/secrets/"+*name+"/grants", map[string]any{
			"workspace_path": *workspace, "expires_seconds": seconds,
		}, output)
	case "grants":
		if err := requireName(*name); err != nil {
			return err
		}
		return secretGet(client, "/v1/secrets/"+*name+"/grants", output)
	case "revoke":
		if err := requireName(*name); err != nil {
			return err
		}
		if strings.TrimSpace(*grantID) == "" {
			return errors.New("--grant-id is required")
		}
		return secretJSON(client, http.MethodDelete, "/v1/secrets/"+*name+"/grants?grant_id="+*grantID, nil, output)
	case "delete":
		if err := requireName(*name); err != nil {
			return err
		}
		return secretJSON(client, http.MethodDelete, "/v1/secrets/"+*name, nil, output)
	default:
		return fmt.Errorf("unknown secret subcommand %q", sub)
	}
}

func requireName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("--name is required")
	}
	return nil
}

func splitHosts(csv string) []string {
	var out []string
	for _, h := range strings.Split(csv, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func readStdinSecret(input io.Reader) ([]byte, error) {
	if file, ok := input.(*os.File); ok {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return nil, errors.New("redirect a secret file or pipe to stdin; interactive entry is not supported")
		}
	}
	value, err := io.ReadAll(io.LimitReader(input, 16<<10+1))
	if err != nil {
		return nil, errors.New("cannot read secret from stdin")
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 || len(value) > 16<<10 {
		return nil, errors.New("secret value must be 1 to 16384 bytes")
	}
	return value, nil
}

func secretGet(client *http.Client, path string, output io.Writer) error {
	return secretDo(client, http.MethodGet, path, "", nil, output)
}

func secretJSON(client *http.Client, method, path string, payload any, output io.Writer) error {
	var body []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return errors.New("cannot encode request")
		}
		body = b
	}
	return secretDo(client, method, path, "application/json", body, output)
}

func secretRaw(client *http.Client, path string, value []byte, output io.Writer) error {
	return secretDo(client, http.MethodPut, path, "application/octet-stream", value, output)
}

func secretDo(client *http.Client, method, path, contentType string, body []byte, output io.Writer) error {
	req, err := http.NewRequest(method, "http://swarm-local-transport"+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach the private daemon socket; run as the swarm service user on the server")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Slot and grant errors are safe to show; they never echo a value.
		msg := strings.TrimSpace(string(payload))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("request rejected (HTTP %d): %s", resp.StatusCode, msg)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, payload, "", "  ") == nil {
		fmt.Fprintln(output, pretty.String())
	} else {
		fmt.Fprintln(output, string(payload))
	}
	return nil
}
