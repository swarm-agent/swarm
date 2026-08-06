package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func executeBash(parent context.Context, scope WorkspaceScope, args map[string]any, onDelta func(string)) (string, error) {
	command, err := validateBashArguments(args)
	if err != nil {
		return "", err
	}
	return executeBashCommand(parent, scope, args, command, onDelta)
}

func executeBashCommand(parent context.Context, scope WorkspaceScope, args map[string]any, command string, onDelta func(string)) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("bash requires command")
	}
	timeout := time.Duration(asInt(args["timeout_ms"], int(defaultBashTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		return "", errors.New("bash workspace path is required")
	}

	// This is disposable command scratch, not daemon state. Let Go select the
	// platform temp root (honoring TMPDIR when supplied) so direct, desktop,
	// container, and service launches all work without a service-manager contract.
	tempDir, err := os.MkdirTemp("", "swarm-command-")
	if err != nil {
		return "", fmt.Errorf("create private command temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = scope.PrimaryPath
	cmd.Env = commandEnvironment(os.Environ(), tempDir)
	cmd.WaitDelay = time.Second
	prepareCommandForCancellation(cmd)

	capture := newCappedBuffer(maxBashOutputViewerBytes)
	streamWriter := newBashStreamWriter(capture, maxBashOutputViewerBytes, onDelta)
	cmd.Stdout = streamWriter
	cmd.Stderr = streamWriter

	stopWatchingCancel := watchCommandCancellation(ctx, cmd)
	defer stopWatchingCancel()

	err = cmd.Run()
	streamWriter.Flush()

	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	wasTruncated := capture.Truncated()
	rawOutput := capture.Bytes()
	binarySuppressed := streamWriter.BinarySuppressed() || isLikelyBinary(rawOutput)
	combined := ""
	if !binarySuppressed {
		combined = sanitizeForToolOutput(capture.String())
	}
	detailsTruncated := wasTruncated || timedOut || binarySuppressed

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

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
	out := make([]string, 0, len(env)+3)
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if name == "TMPDIR" || name == "TMP" || name == "TEMP" || sensitiveCommandEnvironment(name) {
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
