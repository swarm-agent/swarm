package voice

import (
	"os"
	"os/exec"
	"testing"
)

func TestExitedWithCode(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 130")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-nil error from exit 130")
	}
	if !exitedWithCode(err, 130) {
		t.Fatalf("exitedWithCode(err, 130) = false, want true (err=%v)", err)
	}
	if exitedWithCode(err, 1) {
		t.Fatalf("exitedWithCode(err, 1) = true, want false (err=%v)", err)
	}
}

func TestFileHasAudioData(t *testing.T) {
	small, err := os.CreateTemp("", "voice-small-*.wav")
	if err != nil {
		t.Fatalf("create small file: %v", err)
	}
	smallPath := small.Name()
	t.Cleanup(func() { _ = os.Remove(smallPath) })
	if _, err := small.Write(make([]byte, 44)); err != nil {
		t.Fatalf("write small file: %v", err)
	}
	if err := small.Close(); err != nil {
		t.Fatalf("close small file: %v", err)
	}
	if fileHasAudioData(smallPath) {
		t.Fatalf("fileHasAudioData(%q) = true, want false for header-only data", smallPath)
	}

	large, err := os.CreateTemp("", "voice-large-*.wav")
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	largePath := large.Name()
	t.Cleanup(func() { _ = os.Remove(largePath) })
	if _, err := large.Write(make([]byte, 256)); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	if err := large.Close(); err != nil {
		t.Fatalf("close large file: %v", err)
	}
	if !fileHasAudioData(largePath) {
		t.Fatalf("fileHasAudioData(%q) = false, want true for audio-like data", largePath)
	}
}
