package startupconfig

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Load must tolerate retired settings without tolerating typos. This
// file-level test exercises the startup boundary, including its warning output.
func TestLoadRemovedStartupKey(t *testing.T) {
	t.Setenv(childStartupConfigEnv, "")
	path := filepath.Join(t.TempDir(), "swarm.conf")
	var warnings bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&warnings)
	defer log.SetOutput(previous)
	text := Format(Default(path)) + "\nsandbox_cpu_slots = 0\nsandbox_cpu_slots =\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	want := "ignoring removed startup key sandbox_cpu_slots (removed in a3f19d808)"
	if strings.Count(warnings.String(), want) != 1 {
		t.Fatalf("warnings: %s", warnings.String())
	}
	if err := os.WriteFile(path, []byte(text+"never_valid_key = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("unknown key error: %v", err)
	}
	if strings.Contains(Format(Default(path)), "sandbox_cpu_slots") {
		t.Fatal("formatter writes removed key")
	}
}

// Purpose: machines installed before landing was removed have swarm.conf
// files carrying every landing, runner and Cloud Build key, including values
// the old parser rejected only for those features. An update must never make
// them fail to start. Load is the startup boundary the daemon uses, so it is
// the narrowest layer proving each key is ignored with one log line, the file
// still loads, the formatter no longer writes them and RemovedKeys (the
// installer's migration list) names all of them.
func TestLoadIgnoresRemovedStartupKeys(t *testing.T) {
	t.Setenv(childStartupConfigEnv, "")
	path := filepath.Join(t.TempDir(), "swarm.conf")
	var warnings bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&warnings)
	defer log.SetOutput(previous)
	old := map[string]string{
		"environment_provider":            "gcp-cloudbuild",
		"environment_runner_wait_seconds": "120",
		"environment_runner_wake_command": "/usr/local/bin/wake runners",
		"landing_parallel":                "8",
		"landing_max_concurrent":          "2",
		"cloudbuild_project":              "",
		"cloudbuild_region":               "global",
		"cloudbuild_bucket":               "",
		"cloudbuild_machine_type":         "e2-highcpu-8",
		"cloudbuild_timeout_minutes":      "0",
		"cloudbuild_service_account":      "",
		"cloudbuild_cache":                "true",
	}
	text := Format(Default(path))
	for key, value := range old {
		text += key + " = " + value + "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("swarm.conf with removed keys no longer loads: %v", err)
	}
	if cfg.Port != Default(path).Port {
		t.Fatalf("other settings lost: %+v", cfg)
	}
	removed := map[string]bool{}
	for _, key := range RemovedKeys() {
		removed[key.Name] = true
	}
	formatted := Format(Default(path))
	for key := range old {
		if !removed[key] {
			t.Errorf("RemovedKeys is missing %s", key)
		}
		if n := strings.Count(warnings.String(), "ignoring removed startup key "+key+" "); n != 1 {
			t.Errorf("%s logged %d times: %s", key, n, warnings.String())
		}
		if strings.Contains(formatted, key) {
			t.Errorf("formatter still writes %s", key)
		}
	}
}
