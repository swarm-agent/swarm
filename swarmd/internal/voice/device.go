package voice

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type inputDevice struct {
	ID      string
	Name    string
	Default bool
	Backend string
}

func listInputDevices(ctx context.Context) ([]inputDevice, error) {
	switch runtime.GOOS {
	case "linux":
		return listLinuxInputDevices(ctx)
	default:
		return nil, fmt.Errorf("voice device listing is not supported on %s yet", runtime.GOOS)
	}
}

func recordInputAudio(ctx context.Context, deviceID string, seconds int) (capturedAudio, error) {
	switch runtime.GOOS {
	case "linux":
		return recordLinuxInputAudio(ctx, strings.TrimSpace(deviceID), seconds)
	default:
		return capturedAudio{}, fmt.Errorf("voice recording is not supported on %s yet", runtime.GOOS)
	}
}

func listLinuxInputDevices(ctx context.Context) ([]inputDevice, error) {
	if devices, err := listLinuxWPCTLSources(ctx); err == nil && len(devices) > 0 {
		return devices, nil
	}
	if devices, err := listLinuxPactlSources(ctx); err == nil && len(devices) > 0 {
		return devices, nil
	}
	return nil, errors.New("no input devices found (install PipeWire/PulseAudio tooling)")
}

func listLinuxWPCTLSources(ctx context.Context) ([]inputDevice, error) {
	if !commandExists("wpctl") {
		return nil, errors.New("wpctl not found")
	}
	output, err := runCommand(ctx, 3*time.Second, "wpctl", "status")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(output, "\n")
	inSources := false
	reSource := regexp.MustCompile(`^\s*(\*)?\s*([0-9]+)\.\s+(.+?)(?:\s+\[vol:.*)?$`)
	devices := make([]inputDevice, 0, 8)
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripTreeGlyphs(line))
		if trimmed == "" {
			if inSources && len(devices) > 0 {
				break
			}
			continue
		}
		if strings.HasSuffix(trimmed, "Sources:") {
			inSources = true
			continue
		}
		if !inSources {
			continue
		}
		if strings.HasSuffix(trimmed, "Sinks:") || strings.HasSuffix(trimmed, "Filters:") || strings.HasSuffix(trimmed, "Streams:") {
			break
		}
		matches := reSource.FindStringSubmatch(trimmed)
		if len(matches) < 4 {
			continue
		}
		id := strings.TrimSpace(matches[2])
		name := strings.TrimSpace(matches[3])
		if id == "" || name == "" {
			continue
		}
		devices = append(devices, inputDevice{
			ID:      id,
			Name:    name,
			Default: strings.TrimSpace(matches[1]) == "*",
			Backend: "wpctl",
		})
	}
	if len(devices) == 0 {
		return nil, errors.New("no devices parsed from wpctl")
	}
	return devices, nil
}

func listLinuxPactlSources(ctx context.Context) ([]inputDevice, error) {
	if !commandExists("pactl") {
		return nil, errors.New("pactl not found")
	}
	output, err := runCommand(ctx, 3*time.Second, "pactl", "list", "short", "sources")
	if err != nil {
		return nil, err
	}
	defaultSource := ""
	if rawDefault, defaultErr := runCommand(ctx, 2*time.Second, "pactl", "get-default-source"); defaultErr == nil {
		defaultSource = strings.TrimSpace(rawDefault)
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	devices := make([]inputDevice, 0, 8)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSpace(parts[1])
		name := id
		if id == "" {
			continue
		}
		devices = append(devices, inputDevice{
			ID:      id,
			Name:    name,
			Default: defaultSource != "" && strings.EqualFold(id, defaultSource),
			Backend: "pactl",
		})
	}
	if len(devices) == 0 {
		return nil, errors.New("no devices parsed from pactl")
	}
	return devices, nil
}

func recordLinuxInputAudio(ctx context.Context, deviceID string, seconds int) (capturedAudio, error) {
	if seconds <= 0 {
		seconds = 4
	}
	tmpFile, err := os.CreateTemp("", "swarm-voice-*.wav")
	if err != nil {
		return capturedAudio{}, err
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	var backend string
	switch {
	case commandExists("pw-record"):
		backend = "pw-record"
		if err := recordWithPWRecord(ctx, tmpPath, deviceID, seconds); err != nil {
			return capturedAudio{}, err
		}
	case commandExists("arecord"):
		backend = "arecord"
		if err := recordWithARecord(ctx, tmpPath, deviceID, seconds); err != nil {
			return capturedAudio{}, err
		}
	default:
		return capturedAudio{}, errors.New("no recorder found (install pw-record or arecord)")
	}

	audio, err := os.ReadFile(tmpPath)
	if err != nil {
		return capturedAudio{}, err
	}
	if len(audio) < 1024 {
		return capturedAudio{}, errors.New("recorded audio is too small; check microphone permissions and selected device")
	}
	return capturedAudio{
		Audio:   audio,
		Backend: backend,
	}, nil
}

func recordWithPWRecord(ctx context.Context, outputPath, deviceID string, seconds int) error {
	args := []string{"--rate", "16000", "--channels", "1", "--format", "s16"}
	if strings.TrimSpace(deviceID) != "" {
		args = append(args, "--target", strings.TrimSpace(deviceID))
	}
	args = append(args, outputPath)

	cmd := exec.CommandContext(ctx, "pw-record", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pw-record: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()

	select {
	case err := <-waitCh:
		if err == nil {
			return nil
		}
		return fmt.Errorf("pw-record failed: %s", sanitizeCommandError(err, stderr.String()))
	case <-timer.C:
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-waitCh:
			if err == nil {
				return nil
			}
			if exitedWithSignal(err, os.Interrupt) || exitedWithSignal(err, syscall.SIGTERM) || exitedWithCode(err, 130) {
				return nil
			}
			// Some pw-record builds exit non-zero on intentional stop but still flush a valid WAV.
			if fileHasAudioData(outputPath) {
				return nil
			}
			return fmt.Errorf("pw-record stop failed: %s", sanitizeCommandError(err, stderr.String()))
		case <-time.After(1500 * time.Millisecond):
			_ = cmd.Process.Kill()
			<-waitCh
			return nil
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-waitCh
			return ctx.Err()
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waitCh
		return ctx.Err()
	}
}

func recordWithARecord(ctx context.Context, outputPath, deviceID string, seconds int) error {
	args := []string{"-f", "S16_LE", "-c", "1", "-r", "16000", "-d", strconv.Itoa(seconds)}
	if strings.TrimSpace(deviceID) != "" {
		args = append(args, "-D", strings.TrimSpace(deviceID))
	}
	args = append(args, outputPath)
	output, err := runCommand(ctx, time.Duration(seconds+4)*time.Second, "arecord", args...)
	if err != nil {
		msg := strings.TrimSpace(output)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("arecord failed: %s", msg)
	}
	return nil
}

func runCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return string(output), fmt.Errorf("%s timed out", name)
		}
		return string(output), err
	}
	return string(output), nil
}

func commandExists(name string) bool {
	path, err := exec.LookPath(name)
	return err == nil && strings.TrimSpace(path) != ""
}

func stripTreeGlyphs(raw string) string {
	raw = strings.TrimPrefix(raw, "│")
	raw = strings.TrimPrefix(raw, "├")
	raw = strings.TrimPrefix(raw, "└")
	raw = strings.TrimPrefix(raw, "─")
	return strings.TrimSpace(raw)
}

func sanitizeCommandError(err error, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	// Keep stderr metadata short and safe.
	lines := strings.Split(output, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, " | ")
	return strings.TrimSpace(out)
}

func exitedWithSignal(err error, signal os.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return ws.Signaled() && ws.Signal() == signal
}

func exitedWithCode(err error, code int) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return ws.Exited() && ws.ExitStatus() == code
}

func fileHasAudioData(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	if err != nil {
		return false
	}
	// WAV header is typically 44 bytes.
	return info.Size() > 64
}
