package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: parseEntries, Format and missingKeyLines must preserve the optional
// cache switch, reject invalid/duplicate values, and migrate old configuration.
// This parser-level test is the narrowest startup persistence boundary.
func TestCloudBuildCacheStartupSpeed(t *testing.T) {
	defaults := Default("")
	if !strings.Contains(Format(defaults), "cloudbuild_cache = true") {
		t.Fatal("cache default absent")
	}
	cfg, _, err := parseEntries("cloudbuild_cache = false", defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Format(cfg), "cloudbuild_cache = false") {
		t.Fatal("cache opt-out lost")
	}
	if !strings.Contains(strings.Join(missingKeyLines(defaults, map[string]struct{}{}), "\n"), "cloudbuild_cache = true") {
		t.Fatal("migration omitted cache")
	}
	for _, text := range []string{"cloudbuild_cache = invalid", "cloudbuild_cache = true\ncloudbuild_cache = false", "cloudbuild_unknown = true"} {
		if _, _, err := parseEntries(text, defaults); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}
