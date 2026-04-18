package voice

import (
	"path/filepath"
	"testing"
)

func TestDefaultWhisperModelDir_UsesXDGDataHome(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "xdg-data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	got := defaultWhisperModelDir()
	want := filepath.Join(dataHome, "swarm", "models", "whisper.cpp")
	if got != want {
		t.Fatalf("defaultWhisperModelDir() = %q, want %q", got, want)
	}
}

func TestResolveXDGCacheHome_FallsBackToHomeCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)

	got := resolveXDGCacheHome()
	want := filepath.Join(home, ".cache")
	if got != want {
		t.Fatalf("resolveXDGCacheHome() = %q, want %q", got, want)
	}
}
