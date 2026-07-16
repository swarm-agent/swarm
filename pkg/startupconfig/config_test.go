package startupconfig

import (
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

func TestLegacySwarmRoleIsIgnoredBeforeEmptyValueValidation(t *testing.T) {
	cfg, _, err := parseEntries("swarm_role =\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if cfg.Child {
		t.Fatalf("Child = true, want false; legacy swarm_role must not control topology")
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
