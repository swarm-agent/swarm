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
	for _, value := range []string{"local", "runners", "auto"} {
		got, _, err := parseEntries("environment_provider = "+value, cfg)
		if err != nil || got.EnvironmentProvider != value {
			t.Fatalf("provider %q: %+v %v", value, got, err)
		}
	}
	for _, value := range []string{"remote", ""} {
		_, _, err := parseEntries("environment_provider = "+value, Default(""))
		if err == nil || !strings.Contains(err.Error(), "local") {
			t.Fatalf("value %q: %v", value, err)
		}
	}
}

// Purpose: the runner wait is a canonical, backwards-compatible startup key.
// parseEntries, Format and missingKeyLines own its validation and persistence;
// parser-level cases prevent invalid/overflowing durations and duplicate values
// without requiring a daemon or runner transport.
func TestEnvironmentRunnerWaitConfig(t *testing.T) {
	defaults := Default("")
	if defaults.EnvironmentRunnerWaitSeconds != 120 {
		t.Fatalf("default wait = %d", defaults.EnvironmentRunnerWaitSeconds)
	}
	for _, value := range []int{0, 1, 120, 3600} {
		cfg := defaults
		cfg.EnvironmentRunnerWaitSeconds = value
		got, _, err := parseEntries(Format(cfg), defaults)
		if err != nil || got.EnvironmentRunnerWaitSeconds != value {
			t.Fatalf("round trip %d: %+v %v", value, got, err)
		}
	}
	for _, value := range []string{"-1", "1.5", "abc", "9223372037", "999999999999999999999"} {
		if _, _, err := parseEntries("environment_runner_wait_seconds = "+value, defaults); err == nil {
			t.Fatalf("accepted invalid wait %q", value)
		}
	}
	if _, _, err := parseEntries("environment_runner_wait_seconds = 1\nenvironment_runner_wait_seconds = 2", defaults); err == nil {
		t.Fatal("accepted duplicate wait")
	}
	got, _, err := parseEntries("environment_provider = local", defaults)
	if err != nil || got.EnvironmentRunnerWaitSeconds != 120 {
		t.Fatalf("old config: %+v %v", got, err)
	}
	lines := missingKeyLines(defaults, map[string]struct{}{})
	if !strings.Contains(strings.Join(lines, "\n"), "environment_runner_wait_seconds = 120") {
		t.Fatal("missing-key migration omitted runner wait")
	}
	lines = missingKeyLines(defaults, map[string]struct{}{"environment_runner_wait_seconds": {}})
	if strings.Contains(strings.Join(lines, "\n"), "environment_runner_wait_seconds") {
		t.Fatal("missing-key migration overwrites existing runner wait")
	}
}
