package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/sandbox"
)

func executeBash(parent context.Context, scope WorkspaceScope, args map[string]any, onDelta func(string)) (string, error) {
	command, err := validateBashArguments(args)
	if err != nil {
		return "", err
	}
	return executeBashCommand(parent, scope, args, command, onDelta)
}

// executeBashCommand runs one command in the foreground: at its limit it is
// stopped and the result reports timed_out. Committed-source validation and
// other daemon-internal callers use this path; it never moves to the
// background, so a validation can never be reported before it ends.
func executeBashCommand(parent context.Context, scope WorkspaceScope, args map[string]any, command string, onDelta func(string)) (string, error) {
	prepared, timeout, capture, streamWriter, err := prepareBashExec(parent, scope, args, command, onDelta)
	if err != nil {
		return "", err
	}
	outcome := prepared.run(parent, timeout, func(chunk []byte) { _, _ = streamWriter.Write(chunk) })
	streamWriter.Flush()
	return foregroundOutcomeResponse(scope, strings.TrimSpace(command), capture, streamWriter, outcome, prepared.mode)
}

// bashTimeout is the call's limit from timeout_ms, bounded by maxBashTimeout.
func bashTimeout(args map[string]any) time.Duration {
	timeout := time.Duration(asInt(args["timeout_ms"], int(defaultBashTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	return timeout
}

// prepareBashExec validates the call and selects its execution path (lease,
// sandbox or host) without starting the command. The returned exec runs the
// command once on the context and budget its caller chooses.
func prepareBashExec(parent context.Context, scope WorkspaceScope, args map[string]any, command string, onDelta func(string)) (bashExec, time.Duration, *cappedBuffer, *bashStreamWriter, error) {
	if scope.ReadOnlyBash {
		if _, err := sandbox.ReadOnlyCommand(command); err != nil {
			return bashExec{}, 0, nil, nil, err
		}
		if sandbox.Default() == nil || !sandbox.Default().Active() {
			return bashExec{}, 0, nil, nil, fmt.Errorf("read-only bash requires an active sandbox")
		}
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return bashExec{}, 0, nil, nil, errors.New("bash requires command")
	}
	timeout := bashTimeout(args)
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		return bashExec{}, 0, nil, nil, errors.New("bash workspace path is required")
	}
	if scope.ReviewCheckout != "" && (sandbox.Default() == nil || !sandbox.Default().Active()) {
		return bashExec{}, 0, nil, nil, sandbox.ErrSandboxRequired
	}
	capture := newBashCapture()
	streamWriter := newBashStreamWriter(capture, maxBashOutputViewerBytes, onDelta)
	if leaseID, err := bashLease(parent, scope, args, timeout, onDelta); err != nil {
		return bashExec{}, 0, nil, nil, err
	} else if leaseID != "" {
		return leasedBashExec(sandbox.Leases(), leaseID, scope, command), timeout, capture, streamWriter, nil
	}
	if manager := sandbox.Default(); manager != nil && (manager.Active() || manager.Enforced()) {
		prepared, err := sandboxedBashExec(manager, scope, command)
		if err != nil {
			return bashExec{}, 0, nil, nil, err
		}
		return prepared, timeout, capture, streamWriter, nil
	}
	return hostBashExec(scope, command), timeout, capture, streamWriter, nil
}

// bashChunkWriter adapts an output callback to io.Writer. It is a pointer so
// os/exec sees one writer for stdout and stderr and keeps their order.
type bashChunkWriter struct{ write func([]byte) }

func (w *bashChunkWriter) Write(p []byte) (int, error) {
	w.write(p)
	return len(p), nil
}

// hostBashExec runs the command as a host process (explicit sandbox off).
func hostBashExec(scope WorkspaceScope, command string) bashExec {
	return bashExec{mode: "host", run: func(parent context.Context, budget time.Duration, write func([]byte)) bashOutcome {
		// This is disposable command scratch, not daemon state. Let Go select the
		// platform temp root (honoring TMPDIR when supplied) so direct, desktop,
		// container, and service launches all work without a service-manager contract.
		tempDir, err := os.MkdirTemp("", "swarm-command-")
		if err != nil {
			return bashOutcome{fail: fmt.Errorf("create private command temp directory: %w", err)}
		}
		defer os.RemoveAll(tempDir)

		ctx, cancel := context.WithTimeout(parent, budget)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bash", "-lc", command)
		cmd.Dir = scope.PrimaryPath
		cmd.Env = hostCommandEnvironment(scope, commandEnvironment(os.Environ(), tempDir))
		cmd.WaitDelay = time.Second
		prepareCommandForCancellation(cmd)

		out := &bashChunkWriter{write: write}
		cmd.Stdout = out
		cmd.Stderr = out

		stopWatchingCancel := watchCommandCancellation(ctx, cmd)
		defer stopWatchingCancel()

		err = cmd.Run()

		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		return bashOutcome{exitCode: exitCode, timedOut: timedOut, execErr: err}
	}}
}

// sandboxBashScript merges stderr into stdout (preserving order, as the host
// path does) and gives the command private disposable scratch. It must preserve
// the shared-group umask established by Manager.Exec for shells and descendants.
const sandboxBashScript = `exec 2>&1
d=$(mktemp -d "${TMPDIR:-/tmp}/swarm-command-XXXXXX") || exit 1
export TMPDIR="$d" TMP="$d" TEMP="$d"
bash -lc "$1"
rc=$?
rm -rf "$d"
exit $rc`

// sandboxedBashExec runs the command in the project's sandbox. The daemon's
// environment is not passed in; the working directory is the same path as on
// the host because the project is mounted at its own path.
func sandboxedBashExec(manager *sandbox.Manager, scope WorkspaceScope, command string) (bashExec, error) {
	sandboxScope, err := manager.CommandScope(scope.PrimaryPath)
	if err != nil {
		return bashExec{}, fmt.Errorf("bash sandbox: %w", err)
	}
	argv := []string{"bash", "-c", sandboxBashScript, "swarm-bash", command}
	if scope.ReadOnlyBash {
		argv, err = sandbox.ReadOnlyCommand(command)
		if err != nil {
			return bashExec{}, err
		}
	}
	execute := manager.Exec
	if scope.ReadOnlyBash {
		execute = func(ctx context.Context, sc sandbox.Scope, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			return manager.ExecReadOnly(ctx, sc, command, req)
		}
	}
	executionMode := "docker"
	if manager.Local() {
		executionMode = "bubblewrap"
	}
	return bashExec{mode: executionMode, run: func(ctx context.Context, budget time.Duration, write func([]byte)) bashOutcome {
		result, runErr := execute(ctx, sandboxScope, sandbox.ExecRequest{
			Command:    argv,
			WorkingDir: scope.PrimaryPath,
			Timeout:    budget,
			MaxTimeout: maxDuration(budget, maxBashTimeout),
			MaxOutput:  maxBashOutputViewerBytes,
			OnOutput:   write,
		})
		return sandboxOutcome(ctx, result, runErr, func(err error) error { return fmt.Errorf("bash sandbox execution failed: %w", err) })
	}}, nil
}

// sandboxOutcome maps a sandbox or lease exec result to a bash outcome.
func sandboxOutcome(ctx context.Context, result *provider.ExecResult, runErr error, wrap func(error) error) bashOutcome {
	exitCode := 0
	if result != nil {
		exitCode = result.ExitCode
	}
	switch {
	case errors.Is(runErr, provider.ErrOperationTimedOut):
		return bashOutcome{exitCode: exitCode, timedOut: true}
	case errors.Is(runErr, provider.ErrOperationCancelled):
		return bashOutcome{exitCode: -1, fail: fmt.Errorf("bash execution cancelled: %w", ctx.Err())}
	case runErr != nil:
		return bashOutcome{exitCode: -1, fail: wrap(runErr)}
	}
	return bashOutcome{exitCode: exitCode}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// bashLease is the lease a command runs in: the one named by environment,
// else the agent's only lease, else a new lease Swarm takes for the agent
// when its folder's environment is set up for leases (autoLease). Otherwise
// the command runs as before.
func bashLease(ctx context.Context, scope WorkspaceScope, args map[string]any, wait time.Duration, onDelta func(string)) (string, error) {
	id := strings.TrimSpace(asString(args["environment"]))
	if scope.ReadOnlyBash || (scope.ReviewCheckout != "" && !sandbox.BorrowedReviewSession(scope.SessionID)) {
		if id != "" {
			return "", errors.New("a review cannot run in a lease")
		}
		return "", nil
	}
	manager := sandbox.Leases()
	if manager == nil || strings.TrimSpace(scope.SessionID) == "" {
		if id != "" {
			return "", errors.New("leases are unavailable on this machine")
		}
		return "", nil
	}
	if id != "" {
		return id, nil
	}
	held := manager.OwnerLeases(scope.SessionID)
	switch len(held) {
	case 0:
		return autoLease(ctx, manager, scope, wait, onDelta)
	case 1:
		return held[0].ID, nil
	}
	ids := make([]string, 0, len(held))
	for _, l := range held {
		ids = append(ids, l.ID)
	}
	return "", fmt.Errorf("you hold %d leases (%s): pass environment with the lease to run in", len(held), strings.Join(ids, ", "))
}

// leaseWaitPoll is how often a command waiting for a free lease retries.
var leaseWaitPoll = 2 * time.Second

// autoLease takes a lease for an agent without one when its folder's
// environment is set up for leases (max_leases above 0), so the agent's bash
// runs in it without the agent asking. When every lease is in use it waits up
// to wait for one to free up. A folder without that runs as before; one whose
// environment is set up for leases but not ready is refused, never run on the
// host instead.
func autoLease(ctx context.Context, manager *sandbox.Manager, scope WorkspaceScope, wait time.Duration, onDelta func(string)) (string, error) {
	info, err := manager.LeaseInfo(scope.PrimaryPath)
	if err != nil {
		return "", fmt.Errorf("this folder's environment cannot be leased: %w", err)
	}
	if info.Max <= 0 {
		return "", nil
	}
	deadline := time.Now().Add(wait)
	announced := false
	for {
		lease, err := manager.TakeLease(ctx, scope.PrimaryPath, strings.TrimSpace(scope.SessionID))
		if err == nil {
			return lease.ID, nil
		}
		var full *sandbox.LeasesFullError
		if !errors.As(err, &full) {
			return "", err
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("%w (waited %s)", err, wait.Round(time.Second))
		}
		if !announced && onDelta != nil {
			onDelta(fmt.Sprintf("waiting for a free lease of this folder's environment (%d of %d in use)\n", full.InUse, full.Max))
			announced = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(leaseWaitPoll):
		}
	}
}

// leasedBashExec runs the command in one of the agent's leases. The folder
// is mounted at its own path there, so the working directory is unchanged.
func leasedBashExec(manager *sandbox.Manager, leaseID string, scope WorkspaceScope, command string) bashExec {
	return bashExec{mode: "lease " + leaseID, run: func(ctx context.Context, budget time.Duration, write func([]byte)) bashOutcome {
		result, runErr := manager.ExecLease(ctx, leaseID, strings.TrimSpace(scope.SessionID), sandbox.ExecRequest{
			Command:    []string{"bash", "-c", sandboxBashScript, "swarm-bash", command},
			WorkingDir: scope.PrimaryPath,
			Timeout:    budget,
			MaxTimeout: maxDuration(budget, maxBashTimeout),
			MaxOutput:  maxBashOutputViewerBytes,
			OnOutput:   write,
		})
		return sandboxOutcome(ctx, result, runErr, func(err error) error { return fmt.Errorf("bash in lease %s failed: %w", leaseID, err) })
	}}
}

// newBashCapture is a command's in-memory capture: the first
// maxBashOutputViewerBytes and, past that, the end of the output, so the
// capped result still shows how the command ended.
func newBashCapture() *cappedBuffer {
	return newCappedBuffer(maxBashOutputViewerBytes).keepTail(ToolOutputCapBytes())
}

// bashResponse is a finished command's result. Output over the tool output
// cap keeps its head and tail; the full captured output is saved in the
// scope's workspace for read/search (capToolOutputText).
func bashResponse(scope WorkspaceScope, command string, capture *cappedBuffer, streamWriter *bashStreamWriter, exitCode int, timedOut bool, err error, executionMode ...string) (string, error) {
	capOutput := func(text string) cappedToolText { return capToolOutputText(scope, "bash", text, true) }
	return bashResponseCapped(capOutput, command, capture, streamWriter, exitCode, timedOut, err, executionMode...)
}

// managedFullOutputUnavailable is why a managed command's capped output is
// not saved: it ran in the lease checkout, and the host workspace the
// daemon could save into is not where the agent's read runs (search is
// refused there), so a saved path would lead nowhere.
const managedFullOutputUnavailable = "the command ran in a managed lease and its full output is not kept; rerun it with narrower output (for example | tail, | grep, or redirect to a file in the checkout and read that)"

func bashResponseCapped(capOutput func(string) cappedToolText, command string, capture *cappedBuffer, streamWriter *bashStreamWriter, exitCode int, timedOut bool, err error, executionMode ...string) (string, error) {
	rawOutput := capture.Bytes()
	binarySuppressed := streamWriter.BinarySuppressed() || isLikelyBinary(rawOutput)
	combined := ""
	var capped cappedToolText
	if !binarySuppressed {
		capped = capOutput(sanitizeForToolOutput(capture.String()))
		combined = capped.Text
	}
	wasTruncated := capture.Truncated() || capped.Capped
	detailsTruncated := wasTruncated || timedOut || binarySuppressed

	response := map[string]any{
		"command":              command,
		"exit_code":            exitCode,
		"timed_out":            timedOut,
		"truncated":            wasTruncated,
		"binary_suppressed":    binarySuppressed,
		"output":               combined,
		"path_id":              toolPathID("bash"),
		"summary":              bashSummary(command, exitCode, timedOut, wasTruncated, binarySuppressed),
		"details_truncated":    detailsTruncated,
		"safety":               buildUntrustedSafety(combined),
		"prompt_injection_tag": "tool_output_untrusted",
	}
	capped.annotate(response)
	if len(executionMode) > 0 {
		response["execution_mode"] = executionMode[0]
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil && exitCode == -1 {
		return string(encoded), fmt.Errorf("bash execution failed: %w", err)
	}
	return string(encoded), nil
}

func commandEnvironment(env []string, tempDir string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if sensitiveCommandEnvironment(name) {
			continue
		}
		out = append(out, item)
	}
	return privateTempEnvironment(out, tempDir)
}

// privateTempEnvironment gives one tool invocation exclusive disposable scratch
// without discarding unrelated environment such as Git signing or credential-agent
// configuration. The caller owns creation and removal of tempDir.
func privateTempEnvironment(env []string, tempDir string) []string {
	out := make([]string, 0, len(env)+3)
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if name == "TMPDIR" || name == "TMP" || name == "TEMP" {
			continue
		}
		out = append(out, item)
	}
	return append(out, "TMPDIR="+tempDir, "TMP="+tempDir, "TEMP="+tempDir)
}

func sensitiveCommandEnvironment(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, marker := range []string{
		"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "API_KEY",
		"PRIVATE_KEY", "ACCESS_KEY", "AUTH_SOCK", "BEARER", "COOKIE",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// hostCommandEnvironment is the host-mode command environment: Git in the
// command trusts the folders the command is authorized to run in, by exact
// path, which belong to the person and not to the daemon's user (the
// sandboxed path does the same for its mounts). Nothing else is added.
func hostCommandEnvironment(scope WorkspaceScope, env []string) []string {
	seen := map[string]struct{}{}
	dirs := make([]string, 0, 1+len(scope.Roots))
	for _, dir := range append([]string{scope.PrimaryPath}, scope.Roots...) {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return sandbox.AppendSafeDirectoryEnv(env, dirs)
}
