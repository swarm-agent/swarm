package voice

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWhisperLocalAdapterDoesNotProbeHomeOrXDGDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("SWARMD_WHISPER_BIN", "")
	t.Setenv("SWARMD_WHISPER_MODEL_DIR", "")
	t.Setenv("PATH", "")

	adapter := NewWhisperLocalAdapter().(*whisperLocalAdapter)
	_, err := adapter.resolveWhisperBin(nil)
	if err == nil {
		t.Fatal("resolveWhisperBin unexpectedly found a binary")
	}
	if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "XDG") {
		t.Fatalf("resolveWhisperBin error leaks forbidden default path: %v", err)
	}
	if dirs := adapter.modelDirs(nil); len(dirs) != 0 {
		t.Fatalf("modelDirs defaulted to forbidden roots: %#v", dirs)
	}
	if got := defaultWhisperModelDir(); got != "" {
		t.Fatalf("defaultWhisperModelDir = %q, want empty", got)
	}
}
