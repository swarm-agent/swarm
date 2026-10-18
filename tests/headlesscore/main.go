// Command headlesscore is an isolated real-package smoke probe, not an agent
// benchmark. Requirement: packaged Headless starts and initializes without UI.
// Threat: package omissions, browser-auth coupling, leaked credentials or a
// second storage authority. Owners: core package, NewHeadless, swarmctl setup,
// canonical identity/workspace/token APIs. Real processes in an externally
// isolated filesystem/network namespace are the narrowest end-to-end layer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const base = "http://127.0.0.1:7781"

var client = &http.Client{Timeout: 3 * time.Second}

type daemon struct {
	command *exec.Cmd
	done    chan error
	log     *os.File
}

func start(args ...string) (*daemon, error) {
	log, err := os.OpenFile("/var/log/swarmd/probe.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/opt/swarm/current/bin/swarmd", append([]string{"--cwd=/var/lib/swarm"}, args...)...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	d := &daemon{command: cmd, done: make(chan error, 1), log: log}
	go func() { d.done <- cmd.Wait() }()
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		select {
		case err := <-d.done:
			log.Close()
			return nil, fmt.Errorf("packaged daemon exited before readiness: %v", err)
		default:
		}
		resp, err := client.Get(base + "/readyz")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return d, nil
			}
		}
	}
	d.stop()
	return nil, errors.New("packaged daemon readiness timeout")
}
func (d *daemon) stop() error {
	if d == nil || d.command == nil {
		return nil
	}
	_ = d.command.Process.Signal(syscall.SIGTERM)
	defer d.log.Close()
	defer func() { d.command = nil }()
	select {
	case err := <-d.done:
		return err
	case <-time.After(20 * time.Second):
		_ = d.command.Process.Kill()
		<-d.done
		return errors.New("daemon did not shut down cleanly")
	}
}
func command(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/ctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// Never echo backend errors or token output. Callers assert typed results.
	return stdout.Bytes(), err
}
func required(args ...string) ([]byte, error) {
	data, err := command(args...)
	if err != nil {
		return nil, fmt.Errorf("swarmctl %s failed", strings.Join(args[:min(2, len(args))], " "))
	}
	return data, nil
}
func request(method, path, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if len(resp.Cookies()) > 0 {
		return 0, nil, errors.New("Headless issued browser cookies")
	}
	return resp.StatusCode, data, err
}
func expect(method, path, token string, body []byte, want int) ([]byte, error) {
	code, data, err := request(method, path, token, body)
	if err != nil {
		return nil, err
	}
	if code != want {
		return nil, fmt.Errorf("%s %s status %d, wanted %d", method, path, code, want)
	}
	return data, nil
}

type tokenReceipt struct {
	Token string `json:"token"`
	ID    string `json:"id"`
}

func mint(args ...string) (tokenReceipt, error) {
	data, err := required(append([]string{"setup", "sdk-token"}, args...)...)
	if err != nil {
		return tokenReceipt{}, err
	}
	var receipt tokenReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Token == "" || receipt.ID == "" {
		return receipt, errors.New("invalid token receipt")
	}
	return receipt, nil
}
func run() error {
	if os.Getenv("SWARM_PACKAGE_PROBE_ISOLATED") != "1" {
		return errors.New("run only through scripts/test-headless-core.sh isolation")
	}
	for _, dir := range []string{"/etc/swarmd", "/var/lib/swarmd", "/var/cache/swarmd", "/var/log/swarmd", "/run/swarmd", "/var/lib/swarm", "/home/swarm", "/project"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := os.WriteFile("/etc/passwd", []byte(fmt.Sprintf("swarm:x:%d:%d:Swarm:/var/lib/swarm:/bin/bash\n", os.Getuid(), os.Getgid())), 0644); err != nil {
		return err
	}
	if err := os.WriteFile("/project/README.md", []byte("Isolated Headless package verification.\n"), 0600); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-b", "main", "/project"}, {"-C", "/project", "add", "README.md"}, {"-C", "/project", "-c", "user.name=Package verification", "-c", "user.email=package-test@example.invalid", "commit", "-m", "Initial fixture"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			return errors.New("cannot initialize isolated repository")
		}
	}
	searchCtx, cancelSearch := context.WithTimeout(context.Background(), 15*time.Second)
	search := exec.CommandContext(searchCtx, "/opt/swarm/current/bin/swarm-fff-search")
	search.Stdin = strings.NewReader(`{"protocol_version":1,"request_id":"package-search","index_root":"/project","target_path":"/project","operation":"content","queries":["Headless"],"max_results":10,"page_limit":10,"timeout_ms":8000}` + "\n")
	searchOutput, searchErr := search.Output()
	cancelSearch()
	var searchResult struct {
		Completed bool `json:"completed"`
		Content   struct {
			Matches []json.RawMessage `json:"matches"`
		} `json:"content"`
	}
	if searchErr != nil || json.Unmarshal(searchOutput, &searchResult) != nil || !searchResult.Completed || len(searchResult.Content.Matches) == 0 {
		return errors.New("packaged FFF search could not find fixture content")
	}
	fmt.Println("PASS: packaged search helper and FFF library find actual repository content")
	d, err := start()
	if err != nil {
		return err
	}
	defer func() {
		if d != nil {
			_ = d.stop()
		}
	}()
	if err := executionState(!missingBubblewrap(), "required", false); err != nil {
		return err
	}
	fmt.Println("PASS: packaged Headless default execution readiness matches Bubblewrap availability; no Docker or host state is accessible")
	initial, err := required("setup", "status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(initial), "identity=false") || !strings.Contains(string(initial), "credentials=0") {
		return errors.New("initial state was not fresh")
	}
	if _, err := required("setup", "identity", "--username", "package-owner", "--name", "package-test"); err != nil {
		return err
	}
	wantWorkspaces := "workspaces=1"
	if missingBubblewrap() {
		wantWorkspaces = "workspaces=0"
		if _, err := command("setup", "workspace", "--path", "/project", "--name", "package-fixture"); err == nil {
			return errors.New("workspace Git ran despite missing required isolation")
		}
	} else if _, err := required("setup", "workspace", "--path", "/project", "--name", "package-fixture"); err != nil {
		return err
	}
	status, err := required("setup", "status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(status), "identity=true") || !strings.Contains(string(status), wantWorkspaces) {
		return errors.New("CLI did not persist identity/workspace")
	}
	fmt.Println("PASS: packaged CLI initialized canonical owner; workspace setup follows actual execution availability")
	if _, err := command("setup", "complete"); err == nil {
		return errors.New("incomplete provider setup was reported complete")
	}
	if _, err := command("setup", "credential", "--provider", "openai", "--api-key-stdin"); err == nil {
		return errors.New("empty provider credential accepted")
	}
	status, err = required("setup", "status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(status), "credentials=0") || !strings.Contains(string(status), "onboarding_required=true") {
		return errors.New("failed setup changed provider/completion state")
	}
	fmt.Println("PASS: missing provider/empty credential fails closed; onboarding stays incomplete")
	if _, err := expect("GET", "/v3/sessions", "", nil, 401); err != nil {
		return err
	}
	if _, err := expect("POST", "/v1/onboarding", "", []byte(`{"username":"unauthorized-change","swarm_name":"bad"}`), 401); err != nil {
		return err
	}
	token, err := mint("--name", "isolated-api", "--expires-in-seconds", "600")
	if err != nil {
		return err
	}
	if _, err := expect("GET", "/v3/sessions", token.Token, nil, 200); err != nil {
		return err
	}
	if _, err := expect("GET", "/v3/box/summary", token.Token, nil, 403); err != nil {
		return err
	}
	monitor, err := mint("--name", "isolated-monitor", "--monitor", "--expires-in-seconds", "600")
	if err != nil {
		return err
	}
	summary, err := expect("GET", "/v3/box/summary", monitor.Token, nil, 200)
	if err != nil {
		return err
	}
	var box struct {
		Sandbox struct {
			Active  bool   `json:"active"`
			Runtime string `json:"runtime"`
		} `json:"sandbox"`
	}
	if json.Unmarshal(summary, &box) != nil || box.Sandbox.Active == missingBubblewrap() {
		return errors.New("authenticated summary misreports execution")
	}
	if !missingBubblewrap() && box.Sandbox.Runtime != "bubblewrap" {
		return errors.New("headless default did not select Bubblewrap")
	}
	if missingBubblewrap() && (!strings.Contains(string(summary), "required sandbox unavailable") || strings.Contains(string(summary), "agent sandbox is off")) {
		return errors.New("missing Bubblewrap was not reported as blocked")
	}
	if _, err := required("setup", "revoke-sdk-token", "--id", monitor.ID); err != nil {
		return err
	}
	fmt.Println("PASS: scoped monitoring reports the actual default isolation backend or blocked execution; generic token denied")
	for _, path := range []string{"/", "/index.html", "/v1/auth/desktop/session", "/v1/feedback", "/v1/update/status", "/ws"} {
		if _, err := expect("GET", path, token.Token, nil, 404); err != nil {
			return err
		}
	}
	ai, err := mint("--name", "isolated-mcp", "--ai-access", "read", "--expires-in-seconds", "600")
	if err != nil {
		return err
	}
	data, err := expect("POST", "/mcp", ai.Token, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"package-probe","version":"1"}}}`), 200)
	if err != nil {
		return err
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &rpc) != nil || len(rpc.Result) == 0 || len(rpc.Error) > 0 {
		return errors.New("MCP initialization failed")
	}
	fmt.Println("PASS: authenticated API and MCP work; unauthenticated access and Desktop routes are rejected")
	if _, err := required("setup", "revoke-sdk-token", "--id", ai.ID); err != nil {
		return err
	}
	if _, err := expect("POST", "/mcp", ai.Token, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), 401); err != nil {
		return err
	}
	if err := d.stop(); err != nil {
		return err
	}
	d = nil
	unit, err := os.ReadFile("/unit/swarm-headless.service")
	if err != nil {
		return err
	}
	var unitArgs []string
	for _, line := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			argv := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
			if len(argv) < 2 || argv[0] != "/opt/swarm/current/bin/swarmd" {
				return errors.New("unexpected unit command")
			}
			unitArgs = argv[1:]
		}
	}
	if !strings.Contains(string(unit), "--sandbox=required") || len(unitArgs) == 0 {
		return errors.New("packaged unit does not require isolation")
	}
	// Bypass is effective only when isolation is active. Missing Bubblewrap must not enable it.
	d, err = start(append(unitArgs, "--bypass-permissions")...)
	if err != nil {
		return err
	}
	if err := executionState(!missingBubblewrap(), "required", !missingBubblewrap()); err != nil {
		return err
	}
	status, err = required("setup", "status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(status), "identity=true") || !strings.Contains(string(status), wantWorkspaces) {
		return errors.New("identity/workspace lost across restart")
	}
	if _, err := expect("GET", "/v3/sessions", token.Token, nil, 200); err != nil {
		return err
	}
	if _, err := expect("POST", "/mcp", ai.Token, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), 401); err != nil {
		return err
	}
	if _, err := required("setup", "revoke-sdk-token", "--id", token.ID); err != nil {
		return err
	}
	if _, err := expect("GET", "/v3/sessions", token.Token, nil, 401); err != nil {
		return err
	}
	for file, mode := range map[string]os.FileMode{"/etc/swarmd/swarm.conf": 0600, "/var/lib/swarmd/local-transport": 0700, "/var/lib/swarmd/local-transport/api.sock": 0600} {
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != mode {
			return fmt.Errorf("private path permission mismatch: %s", filepath.Base(file))
		}
	}
	fmt.Println("PASS: service arguments preserve execution and bypass policy across restart; identity/workspace/token state, revocation and private file modes hold")
	if err := d.stop(); err != nil {
		return err
	}
	d = nil
	d, err = start("--sandbox=off", "--bypass-permissions")
	if err != nil {
		return err
	}
	if err := executionState(true, "off", false); err != nil {
		return err
	}
	if err := d.stop(); err != nil {
		return err
	}
	d = nil
	fmt.Println("PASS: explicit off is ready for host execution while permission bypass stays disabled")
	fmt.Println("PASS: clean shutdown; no provider task, live host data or service was used")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

// Requirement: default Bubblewrap is active when available, fails closed when
// unavailable, and explicit off alone selects host execution. Threat: silent
// fallback or bypass while unconfined. Owners: NewHeadless, readyz, permission
// bypass gate and box summary. Real isolated packaged startup is the boundary.
func missingBubblewrap() bool { return os.Getenv("SWARM_PACKAGE_PROBE_MISSING_BWRAP") == "1" }
func executionState(wantReady bool, mode string, wantBypass bool) error {
	data, err := expect("GET", "/readyz", "", nil, 200)
	if err != nil {
		return err
	}
	var ready struct {
		OK             bool   `json:"ok"`
		ExecutionReady bool   `json:"agent_execution_ready"`
		Mode           string `json:"agent_execution_mode"`
		Reason         string `json:"agent_execution_reason"`
	}
	reason := ""
	if !wantReady {
		reason = "sandbox_unavailable"
	}
	if json.Unmarshal(data, &ready) != nil || !ready.OK || ready.ExecutionReady != wantReady || ready.Mode != mode || ready.Reason != reason {
		return fmt.Errorf("execution readiness mismatch: ready=%v mode=%s reason=%s", ready.ExecutionReady, ready.Mode, ready.Reason)
	}
	health, err := expect("GET", "/healthz", "", nil, 200)
	if err != nil {
		return err
	}
	var state struct {
		Bypass bool `json:"bypass_permissions"`
	}
	if json.Unmarshal(health, &state) != nil || state.Bypass != wantBypass {
		return errors.New("execution mode has incorrect permission bypass")
	}
	return nil
}
