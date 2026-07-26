package startupconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV3DiagnosticsConfigParsesAndFormats(t *testing.T) {
	cfg, _, err := parseEntries("v3_diagnostics = true\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if !cfg.V3Diagnostics {
		t.Fatalf("V3Diagnostics = false, want true")
	}
	if !containsLine(Format(cfg), "v3_diagnostics = true") {
		t.Fatalf("formatted config missing v3_diagnostics = true")
	}
}

func TestProviderAPIDiagnosticsConfigParsesAndFormats(t *testing.T) {
	cfg, _, err := parseEntries("provider_api_diagnostics = true\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if !cfg.ProviderAPIDiagnostics {
		t.Fatalf("ProviderAPIDiagnostics = false, want true")
	}
	if cfg.V3Diagnostics {
		t.Fatalf("V3Diagnostics = true, want false")
	}
	if !containsLine(Format(cfg), "provider_api_diagnostics = true") {
		t.Fatalf("formatted config missing provider_api_diagnostics = true")
	}
}

func TestLongSessionDiagnosticsConfigParsesFormatsAndDefaultsFalse(t *testing.T) {
	defaults := Default(t.TempDir() + "/swarm.conf")
	if defaults.LongSessionDiagnostics {
		t.Fatalf("LongSessionDiagnostics default = true, want false")
	}
	cfg, _, err := parseEntries("long_session_diagnostics = true\n", defaults)
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if !cfg.LongSessionDiagnostics {
		t.Fatalf("LongSessionDiagnostics = false, want true")
	}
	if !containsLine(Format(cfg), "long_session_diagnostics = true") {
		t.Fatalf("formatted config missing long_session_diagnostics = true")
	}
	if cfg.V3Diagnostics || cfg.ProviderAPIDiagnostics {
		t.Fatalf("long-session diagnostics must not enable payload diagnostics")
	}
}

func TestLongSessionDiagnosticsMissingKeyMigration(t *testing.T) {
	cfg := Default(t.TempDir() + "/swarm.conf")
	lines := missingKeyLines(cfg, map[string]struct{}{
		"bypass_permissions": {}, "retain_tool_output_history": {},
		"v3_diagnostics": {}, "provider_api_diagnostics": {},
		devModeKey: {}, devRootKey: {}, "advertise_host": {},
		"advertise_port": {}, "swarm_name": {}, "child": {},
		"tailscale_url": {}, "peer_transport_port": {},
	})
	joined := strings.Join(lines, "\n")
	if !containsLine(joined, "long_session_diagnostics = false") {
		t.Fatalf("missing-key migration omitted long_session_diagnostics: %q", joined)
	}
}

func TestLegacySwarmRoleIsIgnoredBeforeEmptyValueValidation(t *testing.T) {
	cfg, _, err := parseEntries("swarm_role =\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if cfg.Child {
		t.Fatalf("Child = true, want false; legacy swarm_role must not control topology")
	}
}

func TestWriteAtomicallyPreservesModeAndRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := Default(path)
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "swarm.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg.Path = link
	if err := Write(cfg); err == nil {
		t.Fatal("Write succeeded for symlink config")
	}
	payload, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "unchanged" {
		t.Fatalf("symlink target changed: %q", payload)
	}
}

func containsLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}
