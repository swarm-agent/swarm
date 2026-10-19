package publish

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// Requirement: rule files written before landing was removed still list
// land, auto, require-review, require-proof and pipeline; they must keep
// loading (the plain pull-request rule and sync survive), with one log line
// per rule, while a genuinely unknown option still fails closed. Load is the
// owner policy boundary the daemon reads, so parsing a real rule file is the
// narrowest layer that proves it.
func TestLoadIgnoresRemovedOptions(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	path := filepath.Join(t.TempDir(), "publish")
	writeFile(t, path, "/srv/a\t/usr/local/bin/publish\tland,auto,require-review,pipeline\n/srv/b\t/usr/local/bin/publish\tland,require-proof,sync\n/srv/c\t/usr/local/bin/publish\n", 0600)
	rules, err := Load(path)
	if err != nil {
		t.Fatalf("rule file with removed options no longer loads: %v", err)
	}
	want := []Rule{{Folder: "/srv/a", Command: "/usr/local/bin/publish"}, {Folder: "/srv/b", Command: "/usr/local/bin/publish", Sync: true}, {Folder: "/srv/c", Command: "/usr/local/bin/publish"}}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %+v, want %+v", rules, want)
	}
	if got := strings.Count(logs.String(), "ignoring removed options"); got != 2 {
		t.Fatalf("want one log line per rule with removed options, got %d: %q", got, logs.String())
	}
	if !strings.Contains(logs.String(), "line 1: ignoring removed options land,auto,require-review,pipeline") {
		t.Fatalf("log does not name the options: %q", logs.String())
	}
	writeFile(t, path, "/srv/a\t/usr/local/bin/publish\tland,typo\n", 0600)
	if rules, err := Load(path); err == nil || len(rules) != 0 {
		t.Fatalf("unknown option accepted: %+v", rules)
	}
}
