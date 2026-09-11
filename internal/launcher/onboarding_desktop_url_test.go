package launcher

import (
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

// Requirement: the privileged Desktop redirect must use installed system config
// without HOME/XDG, while missing or invalid config must never yield a success
// URL. OnboardingDesktopURL is the narrow boundary; no accounts/services needed.
func TestOnboardingDesktopURLWithoutHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONFIGURATION_DIRECTORY", root)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	path := filepath.Join(root, "swarm.conf")
	if got, err := OnboardingDesktopURL(); err == nil || got != "" {
		t.Fatalf("missing config accepted: %q, %v", got, err)
	}
	cfg := startupconfig.Default(path)
	cfg.DesktopPort = 5567
	content := []byte(startupconfig.Format(cfg))
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := OnboardingDesktopURL(); err != nil || got != "http://127.0.0.1:5567" {
		t.Fatalf("system destination = %q, %v", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(content) {
		t.Fatal("redirect mutated system configuration")
	}
	if err := os.WriteFile(path, []byte("desktop_port = invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := OnboardingDesktopURL(); err == nil || got != "" {
		t.Fatalf("invalid config accepted: %q, %v", got, err)
	}
}
