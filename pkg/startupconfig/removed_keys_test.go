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
