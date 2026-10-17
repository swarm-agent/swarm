package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

const runnerConfig = "/etc/swarm-runner"
const runnerUnit = "swarm-runner.service"

// Dependencies keep privileged installation tests off the host filesystem.
type runnerCLI struct {
	in            io.Reader
	out           io.Writer
	request       func(string, string, any, any) error
	run           func(string, ...string) error
	write         func(string, string, os.FileMode) error
	hostname      func() (string, error)
	root          func([]string) error
	config, units string
	client        *http.Client
	stateFile     string
}

func cmdRunner(args []string) error {
	return newRunnerCLI().command(args)
}

func newRunnerCLI() runnerCLI {
	return runnerCLI{os.Stdin, os.Stdout, machineRequest, machineRun, machineWrite, os.Hostname, machineRoot, runnerConfig, "/etc/systemd/system",
		&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("runner join redirects are not allowed") }}, "/run/swarm-runner/state"}
}

var runnerCLIName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (c runnerCLI) command(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarmctl runner token [--name NAME] | list | remove NAME | join HUB_URL [--name NAME] | unit | status")
	}
	switch args[0] {
	case "token":
		name, err := c.hostname()
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("runner token", flag.ContinueOnError)
		fs.StringVar(&name, "name", name, "runner hostname or join --name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || !runnerCLIName.MatchString(name) {
			return fmt.Errorf("valid runner --name required")
		}
		var result struct {
			Token string `json:"token"`
		}
		if err := c.request("POST", "/v3/runners/join-tokens", map[string]string{"name": name}, &result); err != nil {
			return err
		}
		if result.Token == "" {
			return fmt.Errorf("hub returned no token")
		}
		_, err = fmt.Fprintln(c.out, result.Token)
		return err
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: runner list")
		}
		var result struct {
			Runners []struct {
				Name, State, Engine string
				LastSeen            int64  `json:"last_seen"`
				Landing             string `json:"current_landing"`
				Revoked             bool
			} `json:"runners"`
		}
		if err := c.request("GET", "/v3/runners", nil, &result); err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tENGINE\tLAST SEEN\tLANDING")
		for _, r := range result.Runners {
			if r.Revoked {
				continue
			}
			if r.State == "" {
				r.State = "unhealthy"
			}
			if r.Engine == "" {
				r.Engine = "unknown"
			}
			seen := "never"
			if r.LastSeen > 0 {
				seen = time.UnixMilli(r.LastSeen).UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.State, r.Engine, seen, r.Landing)
		}
		return w.Flush()
	case "remove":
		if len(args) != 2 || !runnerCLIName.MatchString(args[1]) {
			return fmt.Errorf("usage: runner remove NAME")
		}
		return c.request("DELETE", "/v3/runners/"+url.PathEscape(args[1]), nil, nil)
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: runner status")
		}
		if err := c.run("systemctl", "show", runnerUnit, "--property=ActiveState,SubState,StatusText"); err != nil {
			return err
		}
		env, err := os.ReadFile(filepath.Join(c.config, "runner.env"))
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(env), "\n") {
			for key, label := range map[string]string{"SWARM_RUNNER_HUB=": "Hub", "SWARM_RUNNER_NAME=": "Name"} {
				if strings.HasPrefix(line, key) {
					fmt.Fprintf(c.out, "%s: %s\n", label, strings.Trim(strings.TrimPrefix(line, key), "\""))
				}
			}
		}
		state, err := os.ReadFile(c.stateFile)
		if err != nil {
			return fmt.Errorf("runner state unavailable: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(state)), "\n") {
			if _, err := fmt.Fprintf(c.out, "Landing state: %s\n", line); err != nil {
				return err
			}
		}
		return nil
	case "unit":
		if len(args) != 1 {
			return fmt.Errorf("usage: runner unit")
		}
		if err := c.root([]string{"runner", "unit"}); err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(c.config, "credential"))
		if os.IsNotExist(err) {
			return fmt.Errorf("runner is not joined")
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runner credential is not a regular file")
		}
		return c.writeUnit()
	case "join":
		return c.join(args)
	default:
		return fmt.Errorf("unknown runner command %q", args[0])
	}
}

func (c runnerCLI) join(args []string) error {
	if err := c.root(append([]string{"runner"}, args...)); err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: runner join HUB_URL [--name NAME]")
	}
	hub, err := url.Parse(args[1])
	if err != nil || hub.Host == "" || (hub.Scheme != "https" && hub.Scheme != "http") || hub.User != nil || hub.RawQuery != "" || hub.Fragment != "" || (hub.Path != "" && hub.Path != "/") || strings.ContainsAny(args[1], "\r\n\t\"\\%$") {
		return fmt.Errorf("hub must be an HTTP(S) origin without credentials or query")
	}
	name, err := c.hostname()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("runner join", flag.ContinueOnError)
	fs.StringVar(&name, "name", name, "runner name (must match token)")
	if err = fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !runnerCLIName.MatchString(name) {
		return fmt.Errorf("valid runner --name required")
	}
	// Refuse accidental overwrite before spending the one-time token.
	if _, err = os.Lstat(filepath.Join(c.config, "credential")); err == nil {
		return fmt.Errorf("runner credential already exists; remove the runner on the hub before re-enrolling")
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(c.in, 4097))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(raw) > 4096 {
		return fmt.Errorf("expected join token on stdin")
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	req, err := http.NewRequest("POST", strings.TrimRight(args[1], "/")+"/v3/runners/join", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("runner join rejected (HTTP %d)", resp.StatusCode)
	}
	var result struct{ Credential, Name string }
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result); err != nil {
		return err
	}
	if result.Credential == "" {
		return fmt.Errorf("hub returned no credential")
	}
	// Persist immediately: a subsequent installation failure must not lose the spent token.
	if err = c.write(filepath.Join(c.config, "credential"), result.Credential+"\n", 0600); err != nil {
		return err
	}
	// Keep the directory root-controlled while allowing the service account to
	// traverse it and read only its own private credential.
	if err = c.run("chown", "root:swarm-runner", c.config); err != nil {
		return err
	}
	if err = c.run("chmod", "0750", c.config); err != nil {
		return err
	}
	if err = c.run("chown", "swarm-runner:swarm-runner", filepath.Join(c.config, "credential")); err != nil {
		return err
	}
	if result.Name != name {
		return fmt.Errorf("token belongs to runner %q, not %q; credential saved, revoke it on the hub before retrying", result.Name, name)
	}
	if err = c.write(filepath.Join(c.config, "runner.env"), fmt.Sprintf("SWARM_RUNNER_HUB=\"%s\"\nSWARM_RUNNER_NAME=\"%s\"\n", strings.TrimRight(args[1], "/"), name), 0600); err != nil {
		return err
	}
	if err = c.writeUnit(); err != nil {
		return err
	}
	return c.run("systemctl", "enable", "--now", runnerUnit)
}

// writeUnit owns only the join-generated unit, not installer-owned drop-ins.
func (c runnerCLI) writeUnit() error {
	const unit = `[Unit]
Description=Swarm runner
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
[Service]
User=swarm-runner
Group=swarm-runner
SupplementaryGroups=docker
EnvironmentFile=/etc/swarm-runner/runner.env
Environment=LD_LIBRARY_PATH=/opt/swarm-runner/current/lib
Environment=SWARM_RUNNER_SLOTS=1
EnvironmentFile=-/etc/swarm-runner/slots.env
ExecStart=/opt/swarm-runner/current/bin/swarmd runner --slots ${SWARM_RUNNER_SLOTS} --hub ${SWARM_RUNNER_HUB} --name ${SWARM_RUNNER_NAME} --credential-file /etc/swarm-runner/credential --state-file /run/swarm-runner/state
RuntimeDirectory=swarm-runner
TimeoutStopSec=30min
KillMode=mixed
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
`
	path := filepath.Join(c.units, runnerUnit)
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	status := "unchanged"
	if string(previous) != unit {
		if err := c.write(path, unit, 0644); err != nil {
			return err
		}
		status = "changed"
	}
	// Reload even on an unchanged retry after a previous reload failure.
	if err := c.run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.out, "%s %s\n", runnerUnit, status)
	return err
}
