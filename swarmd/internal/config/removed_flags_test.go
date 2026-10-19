package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: service units written by older installers pass
// --runner-gateway-port=7784 and --landing-max-concurrent; after the runner
// gateway and landing were removed, an update must still start. Threat: an
// unknown-flag error stops the daemon on every existing machine. Owner:
// parse's removedFlags handling. Parsing real arguments against an isolated
// startup config is the narrowest layer that proves acceptance, the single
// log line, and that an unrelated unknown flag still fails.
func TestParseAcceptsRemovedFlags(t *testing.T) {
	dir := writeTestStartupConfig(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("CONFIGURATION_DIRECTORY", dir)
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for _, parse := range []func([]string) (Config, error){Parse, ParseHeadless} {
		logs.Reset()
		if _, err := parse([]string{"--cwd", workspace, "--runner-gateway-port=7784"}); err != nil {
			t.Fatalf("--runner-gateway-port=7784 rejected: %v", err)
		}
		if _, err := parse([]string{"--cwd", workspace, "--runner-gateway-port", "7784", "--landing-max-concurrent=2"}); err != nil {
			t.Fatalf("removed flags rejected: %v", err)
		}
		if got := strings.Count(logs.String(), "ignoring removed flags"); got != 2 {
			t.Fatalf("want one log line per parse, got %d: %q", got, logs.String())
		}
		if !strings.Contains(logs.String(), "--runner-gateway-port --landing-max-concurrent") {
			t.Fatalf("log does not name the ignored flags: %q", logs.String())
		}
		logs.Reset()
		if _, err := parse([]string{"--cwd", workspace}); err != nil {
			t.Fatal(err)
		}
		if logs.Len() != 0 {
			t.Fatalf("logged without removed flags: %q", logs.String())
		}
		if _, err := parse([]string{"--cwd", workspace, "--not-a-swarmd-flag=1"}); err == nil {
			t.Fatal("unknown flag accepted")
		}
	}
}
