package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func recordLocalVoiceAudio(ctx context.Context, deviceID string) ([]byte, string, error) {
	if runtime.GOOS != "linux" {
		return nil, "", fmt.Errorf("voice capture is not supported on %s", runtime.GOOS)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tmpFile, err := os.CreateTemp("", "swarmtui-voice-*.wav")
	if err != nil {
		return nil, "", err
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	deviceID = strings.TrimSpace(deviceID)
	backend := ""
	switch {
	case commandExists("pw-record"):
		backend = "pw-record"
		if err := recordWithPWRecordUntilStop(ctx, tmpPath, deviceID); err != nil {
			return nil, "", err
		}
	case commandExists("arecord"):
		backend = "arecord"
		if err := recordWithARecordUntilStop(ctx, tmpPath, deviceID); err != nil {
			return nil, "", err
		}
	default:
		return nil, "", errors.New("no recorder found (install pw-record or arecord)")
	}

	audio, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, "", err
	}
	if len(audio) < 1024 {
		return nil, "", errors.New("recorded audio is too small; check microphone/device selection")
	}
	return audio, backend, nil
}

func recordWithPWRecordUntilStop(ctx context.Context, outputPath, deviceID string) error {
	args := []string{"--rate", "16000", "--channels", "1", "--format", "s16"}
	if deviceID != "" {
		args = append(args, "--target", deviceID)
	}
	args = append(args, outputPath)

	cmd := exec.Command("pw-record", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pw-record: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		if err == nil {
			return nil
		}
		if fileHasAudioData(outputPath) {
			return nil
		}
		return fmt.Errorf("pw-record failed: %s", sanitizeCommandError(err, stderr.String()))
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case err := <-waitCh:
			if err == nil {
				return nil
			}
			if exitedWithSignal(err, os.Interrupt) || exitedWithSignal(err, syscall.SIGTERM) || exitedWithCode(err, 130) {
				return nil
			}
			if fileHasAudioData(outputPath) {
				return nil
			}
			return fmt.Errorf("pw-record stop failed: %s", sanitizeCommandError(err, stderr.String()))
		case <-time.After(1500 * time.Millisecond):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitCh
			if fileHasAudioData(outputPath) {
				return nil
			}
			return ctx.Err()
		}
	}
}

func recordWithARecordUntilStop(ctx context.Context, outputPath, deviceID string) error {
	args := []string{"-f", "S16_LE", "-c", "1", "-r", "16000"}
	if deviceID != "" && acceptsALSADevice(deviceID) {
		args = append(args, "-D", deviceID)
	}
	args = append(args, outputPath)

	cmd := exec.Command("arecord", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start arecord: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		if err == nil {
			return nil
		}
		if fileHasAudioData(outputPath) {
			return nil
		}
		return fmt.Errorf("arecord failed: %s", sanitizeCommandError(err, stderr.String()))
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case err := <-waitCh:
			if err == nil {
				return nil
			}
			if exitedWithSignal(err, os.Interrupt) || exitedWithSignal(err, syscall.SIGTERM) || exitedWithCode(err, 130) {
				return nil
			}
			if fileHasAudioData(outputPath) {
				return nil
			}
			return fmt.Errorf("arecord stop failed: %s", sanitizeCommandError(err, stderr.String()))
		case <-time.After(1500 * time.Millisecond):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitCh
			if fileHasAudioData(outputPath) {
				return nil
			}
			return ctx.Err()
		}
	}
}

func acceptsALSADevice(deviceID string) bool {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return false
	}
	if strings.Contains(deviceID, ":") || strings.Contains(deviceID, ",") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(deviceID), "hw") || strings.HasPrefix(strings.ToLower(deviceID), "plughw") {
		return true
	}
	for _, r := range deviceID {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

func sanitizeCommandError(err error, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	lines := strings.Split(output, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}

func commandExists(name string) bool {
	path, err := exec.LookPath(name)
	return err == nil && strings.TrimSpace(path) != ""
}

func exitedWithSignal(err error, signal os.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return status.Signaled() && status.Signal() == signal
}

func exitedWithCode(err error, code int) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return status.Exited() && status.ExitStatus() == code
}

func fileHasAudioData(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	if err != nil {
		return false
	}
	return info.Size() > 64
}
