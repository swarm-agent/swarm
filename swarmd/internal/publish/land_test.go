package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Load owns administrator rule compatibility; legacy TSV rules must
// remain PR rules and unknown options must fail closed. This parser-level test
// is the narrowest boundary that proves mode selection without running a daemon.
func TestLoadLandMode(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	config := filepath.Join(t.TempDir(), "rules")
	writeFile(t, config, "/srv/old\t/usr/local/bin/pr\n/srv/new\t/usr/local/bin/land\tland,auto,sync\n", 0644)
	rules, err := Load(config)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules: %+v, %v", rules, err)
	}
	if rules[0].Mode != "pr" || rules[1].Mode != "land" || !rules[1].Auto || !rules[1].Sync {
		t.Fatalf("modes: %+v", rules)
	}
}

// Purpose: Land owns the privileged command protocol. Only an exact LANDED
// receipt for the requested head, or MOVED, can succeed; malformed receipts,
// missing base evidence and command failures must not be reported as landed.
// A temporary owner-controlled executable exercises the real process boundary.
func TestLandProtocol(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	dir := t.TempDir()
	cmd := filepath.Join(dir, "land")
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	req := Request{Branch: "agent/job", Head: head, BaseBranch: "main", BaseCommit: base}
	rule := Rule{Folder: dir, Command: cmd, Mode: "land"}
	for _, tc := range []struct {
		name, output string
		moved, fail  bool
	}{
		{"landed", "LANDED " + head, false, false},
		{"moved", "MOVED", true, false},
		{"wrong head", "LANDED " + base, false, true},
		{"malformed", "LANDED HEAD", false, true},
		{"pr receipt", "https://github.com/example/repo/pull/1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, cmd, "#!/bin/sh\nset -e\ntest \"$SWARM_PUBLISH_ACTION\" = land\ntest \"$SWARM_PUBLISH_HEAD\" = "+head+"\ntest \"$SWARM_PUBLISH_BASE_BRANCH\" = main\ntest \"$SWARM_PUBLISH_BASE_COMMIT\" = "+base+"\nprintf '%s\\n' '"+tc.output+"'\n", 0755)
			res, err := Land(context.Background(), rule, req)
			if (err != nil) != tc.fail {
				t.Fatalf("result: %+v, %v", res, err)
			}
			if !tc.fail && (res.Moved != tc.moved || (!tc.moved && res.Commit != head)) {
				t.Fatalf("receipt: %+v", res)
			}
		})
	}
	writeFile(t, cmd, "#!/bin/sh\necho secret >&2\nexit 1\n", 0755)
	if _, err := Land(context.Background(), rule, req); err == nil {
		t.Fatal("command failure accepted")
	}
	req.BaseCommit = ""
	if _, err := Land(context.Background(), rule, req); err == nil {
		t.Fatal("missing base accepted")
	}
}
