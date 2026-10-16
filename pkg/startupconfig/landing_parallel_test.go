package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: parseEntries, Format and missingKeyLines must preserve the new
// landing_parallel key without weakening unknown-key or removed-key handling.
// Parser-level tests are the narrowest reproduction of the startup rejection.
func TestLandingParallelConfig(t *testing.T) {
	defaults := Default("")
	for _, value := range []string{"0", "1", "8", "32"} {
		got, seen, err := parseEntries("landing_parallel = "+value, defaults)
		if err != nil {
			t.Fatalf("valid parallelism %s: %v", value, err)
		}
		if _, ok := seen["landing_parallel"]; !ok {
			t.Fatal("key not recorded")
		}
		text := Format(got)
		if !strings.Contains(text, "landing_parallel = "+value+"\n") {
			t.Fatalf("round trip lost value: %s", value)
		}
		if _, _, err := parseEntries(text, defaults); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(missingKeyLines(got, seen), "\n"), "landing_parallel") {
			t.Fatal("migration duplicates configured key")
		}
	}
	for _, value := range []string{"-1", "1.5", "abc", "999999999999999999999", "1\nlanding_parallel = 2"} {
		if _, _, err := parseEntries("landing_parallel = "+value, defaults); err == nil {
			t.Fatalf("accepted invalid value %q", value)
		}
	}
	if !strings.Contains(strings.Join(missingKeyLines(defaults, map[string]struct{}{}), "\n"), "landing_parallel = 0") {
		t.Fatal("migration omitted default")
	}
	if _, _, err := parseEntries("landing_paralell = 8", defaults); err == nil {
		t.Fatal("unknown key accepted")
	}
}

// Purpose: ResolvedLandingParallel must keep local defaults serial and resolve
// remote defaults without changing the stored zero sentinel. This pure policy
// test isolates provider selection from environment acquisition or Git effects.
func TestLandingParallelDefaults(t *testing.T) {
	for _, tc := range []struct {
		provider, project, bucket string
		want                      int
	}{
		{"local", "", "", 1}, {"runners", "", "", 8}, {"gcp-cloudbuild", "p", "b", 8},
		{"auto", "", "", 1}, {"auto", "p", "", 1}, {"auto", "", "b", 1}, {"auto", "p", "b", 8},
	} {
		cfg := Default("")
		cfg.EnvironmentProvider, cfg.CloudBuildProject, cfg.CloudBuildBucket = tc.provider, tc.project, tc.bucket
		if got := cfg.ResolvedLandingParallel(); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
		if cfg.LandingParallel != 0 {
			t.Fatal("resolution mutated sentinel")
		}
		for _, explicit := range []int{1, 3, 16} {
			cfg.LandingParallel = explicit
			if got := cfg.ResolvedLandingParallel(); got != explicit {
				t.Fatalf("explicit %d: got %d", explicit, got)
			}
		}
	}
}
