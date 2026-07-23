package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	capture := newCappedBuffer(maxBashOutputViewerBytes)
	streamWriter := newBashStreamWriter(capture, maxBashOutputViewerBytes, onDelta)
	supervised := newCommandSupervisor().run(ctx, scope, command, streamWriter, streamWriter)
	streamWriter.Flush()

	wasTruncated := capture.Truncated()
	rawOutput := capture.Bytes()
	binarySuppressed := streamWriter.BinarySuppressed() || isLikelyBinary(rawOutput)
	combined := ""
	if !binarySuppressed {
		combined = sanitizeForToolOutput(capture.String())
	}
	detailsTruncated := wasTruncated || supervised.TimedOut || binarySuppressed

	response := map[string]any{
		"command":                command,
		"exit_code":              supervised.ExitCode,
		"timed_out":              supervised.TimedOut,
		"termination_reason":     supervised.TerminationReason,
		"containment":            supervised.Containment,
		"temp_guarantee":         supervised.TempGuarantee,
		"disk_reserve_guarantee": supervised.DiskGuarantee,
		"truncated":              wasTruncated,
		"binary_suppressed":      binarySuppressed,
		"output":                 combined,
		"path_id":                toolPathID("bash"),
		"summary":                bashSummary(command, supervised.ExitCode, supervised.TimedOut, wasTruncated, binarySuppressed),
		"details_truncated":      detailsTruncated,
		"safety":                 buildUntrustedSafety(combined),
		"prompt_injection_tag":   "tool_output_untrusted",
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if supervised.Err != nil && supervised.ExitCode == -1 {
		return string(encoded), fmt.Errorf("bash execution failed: %w", supervised.Err)
	}
	return string(encoded), nil
}
