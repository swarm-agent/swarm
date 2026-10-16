package main

import (
	"os"
	"path/filepath"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"testing"
)

// Purpose: Cloud Build startup keys must survive the canonical config parser,
// including blank optional values in generated defaults. This parser-level test
// prevents an unusable daemon configuration without starting a daemon or ADC.
func TestCloudBuildConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	data := "environment_provider = gcp-cloudbuild\ncloudbuild_project = project\ncloudbuild_bucket = bucket\ncloudbuild_region = global\ncloudbuild_machine_type = \ncloudbuild_service_account = \ncloudbuild_timeout_minutes = 60\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := startupconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloudBuildProject != "project" || cfg.CloudBuildBucket != "bucket" || cfg.CloudBuildTimeoutMinutes != 60 || cfg.EnvironmentProvider != "gcp-cloudbuild" {
		t.Fatalf("lost config: %+v", cfg)
	}
}
