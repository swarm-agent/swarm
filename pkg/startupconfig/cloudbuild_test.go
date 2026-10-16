package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: Cloud Build configuration must round-trip without dropping other
// startup keys, and reject missing prerequisites before daemon startup.
// parseEntries/Format/missingKeyLines are the narrowest persistence boundary;
// these cases prevent silently accepting incomplete or duplicate configuration.
func TestCloudBuildConfig(t *testing.T) {
	defaults := Default("")
	if defaults.CloudBuildRegion != "global" || defaults.CloudBuildMachineType != "e2-highcpu-8" || defaults.CloudBuildTimeoutMinutes != 0 {
		t.Fatalf("unexpected Cloud Build defaults")
	}
	text := "environment_provider = gcp-cloudbuild\ncloudbuild_project = example-project\ncloudbuild_bucket = example-bucket\ncloudbuild_region = us-central1\ncloudbuild_machine_type = e2-highcpu-32\ncloudbuild_timeout_minutes = 30\ncloudbuild_service_account = projects/example-project/serviceAccounts/build@example-project.iam.gserviceaccount.com"
	cfg, _, err := parseEntries(text, defaults)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := parseEntries(Format(cfg), defaults)
	if err != nil || got.CloudBuildProject != cfg.CloudBuildProject || got.CloudBuildBucket != cfg.CloudBuildBucket || got.CloudBuildRegion != cfg.CloudBuildRegion || got.CloudBuildMachineType != cfg.CloudBuildMachineType || got.CloudBuildTimeoutMinutes != cfg.CloudBuildTimeoutMinutes || got.CloudBuildServiceAccount != cfg.CloudBuildServiceAccount || got.EnvironmentProvider != "gcp-cloudbuild" {
		t.Fatalf("Cloud Build round trip failed: %v", err)
	}
	for _, invalid := range []string{
		"environment_provider = gcp-cloudbuild",
		"environment_provider = gcp-cloudbuild\ncloudbuild_project = project",
		"environment_provider = gcp-cloudbuild\ncloudbuild_bucket = bucket",
		"cloudbuild_timeout_minutes = -1", "cloudbuild_timeout_minutes = abc", "cloudbuild_timeout_minutes = 999999999999999999999",
	} {
		if _, _, err := parseEntries(invalid, defaults); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	for _, key := range []string{"cloudbuild_project", "cloudbuild_region", "cloudbuild_bucket", "cloudbuild_machine_type", "cloudbuild_timeout_minutes", "cloudbuild_service_account"} {
		value := "example"
		if key == "cloudbuild_timeout_minutes" {
			value = "10"
		}
		if _, _, err := parseEntries(key+" = "+value+"\n"+key+" = "+value, defaults); err == nil {
			t.Fatalf("duplicate %s accepted", key)
		}
		if !strings.Contains(strings.Join(missingKeyLines(defaults, map[string]struct{}{}), "\n"), key+" = ") {
			t.Fatalf("migration omitted %s", key)
		}
		if strings.Contains(strings.Join(missingKeyLines(defaults, map[string]struct{}{key: {}}), "\n"), key+" = ") {
			t.Fatalf("migration overwrites %s", key)
		}
	}
	if _, _, err := parseEntries("environment_provider = auto", defaults); err != nil {
		t.Fatal(err)
	}
}
