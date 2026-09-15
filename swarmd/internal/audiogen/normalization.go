package audiogen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CommandRunner defines the execution boundary for external CLI binaries like ffmpeg.
type CommandRunner interface {
	LookPath(file string) (string, error)
	RunCommand(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osCommandRunner struct{}

func (r *osCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r *osCommandRunner) RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("%s failed", name)
		}
		return nil, fmt.Errorf("%s failed: %s", name, detail)
	}
	return stdout.Bytes(), nil
}

// TrimAudioToDuration trims an audio stream to the exact target duration in seconds with a smooth fade-out.
// If CommandRunner is nil or ffmpeg is not available, it gracefully returns the original audio bytes without error.
func TrimAudioToDuration(ctx context.Context, runner CommandRunner, audioBytes []byte, targetSeconds float64, fadeOutSeconds float64) ([]byte, bool, error) {
	if runner == nil || len(audioBytes) == 0 || targetSeconds <= 0 {
		return audioBytes, false, nil
	}
	if _, err := runner.LookPath("ffmpeg"); err != nil {
		// FFmpeg is not installed on PATH; gracefully return original audio
		return audioBytes, false, nil
	}

	tempDir := os.TempDir()
	inFile, err := os.CreateTemp(tempDir, "swarm-audiogen-in-*.mp3")
	if err != nil {
		return audioBytes, false, fmt.Errorf("create temp audio input file: %w", err)
	}
	inPath := inFile.Name()
	defer os.Remove(inPath)

	if _, err := inFile.Write(audioBytes); err != nil {
		_ = inFile.Close()
		return audioBytes, false, fmt.Errorf("write temp audio input file: %w", err)
	}
	_ = inFile.Close()

	outPath := filepath.Join(tempDir, fmt.Sprintf("swarm-audiogen-out-%d.mp3", time.Now().UnixNano()))
	defer os.Remove(outPath)

	fadeOut := fadeOutSeconds
	if fadeOut <= 0 {
		fadeOut = 0.5
	}
	if fadeOut >= targetSeconds {
		fadeOut = targetSeconds / 2.0
	}
	fadeStart := targetSeconds - fadeOut

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-i", inPath,
		"-t", fmt.Sprintf("%.3f", targetSeconds),
		"-af", fmt.Sprintf("afade=t=out:st=%.3f:d=%.3f", fadeStart, fadeOut),
		"-c:a", "libmp3lame",
		"-b:a", "192k",
		outPath,
	}

	if _, err := runner.RunCommand(ctx, "ffmpeg", args...); err != nil {
		return audioBytes, false, fmt.Errorf("ffmpeg audio trim: %w", err)
	}

	trimmed, err := os.ReadFile(outPath)
	if err != nil {
		return audioBytes, false, fmt.Errorf("read trimmed audio output file: %w", err)
	}
	if len(trimmed) == 0 {
		return audioBytes, false, errors.New("trimmed audio file is empty")
	}

	return trimmed, true, nil
}

// ProbeAudioDurationMs probes the exact duration in milliseconds using ffprobe if available.
func ProbeAudioDurationMs(ctx context.Context, runner CommandRunner, audioBytes []byte) (int, error) {
	if runner == nil || len(audioBytes) == 0 {
		return 0, errors.New("cannot probe empty audio or nil runner")
	}
	if _, err := runner.LookPath("ffprobe"); err != nil {
		return 0, err
	}

	inFile, err := os.CreateTemp("", "swarm-audioprobe-*.mp3")
	if err != nil {
		return 0, err
	}
	defer os.Remove(inFile.Name())

	if _, err := inFile.Write(audioBytes); err != nil {
		_ = inFile.Close()
		return 0, err
	}
	_ = inFile.Close()

	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		inFile.Name(),
	}

	out, err := runner.RunCommand(ctx, "ffprobe", args...)
	if err != nil {
		return 0, err
	}
	secStr := strings.TrimSpace(string(out))
	sec, err := strconv.ParseFloat(secStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse ffprobe duration %q: %w", secStr, err)
	}
	return int(sec * 1000), nil
}
