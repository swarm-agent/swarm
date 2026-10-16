package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: parseEntries must reject unsupported landing providers at startup,
// rather than silently accepting a configuration whose isolation cannot be supplied.
// The parser is the narrowest boundary for validating the public config key.
func TestEnvironmentProviderConfig(t *testing.T) {
	cfg := Default("")
	if cfg.EnvironmentProvider != "local" {
		t.Fatal(cfg.EnvironmentProvider)
	}
	got, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || got.EnvironmentProvider != "local" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, _, err := parseEntries("environment_provider = local\nenvironment_provider = local", cfg); err == nil {
		t.Fatal("duplicate provider accepted")
	}
	for _, value := range []string{"remote", ""} {
		_, _, err := parseEntries("environment_provider = "+value, Default(""))
		if err == nil || !strings.Contains(err.Error(), "local") {
			t.Fatalf("value %q: %v", value, err)
		}
	}
}
