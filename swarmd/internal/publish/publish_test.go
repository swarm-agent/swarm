package publish

import (
	"context"
	"os"
	"path/filepath"
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

func TestLoadAndFor(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	dir := t.TempDir()
	config := filepath.Join(dir, "publish")
	writeFile(t, config, "# rules\n/srv/models\t/usr/local/bin/models-pr\tauto\n\n/srv/site/\t/usr/local/bin/site-pr\n", 0o644)
	rules, err := Load(config)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	if rule, ok := For(rules, "/srv/models"); !ok || !rule.Auto || rule.Command != "/usr/local/bin/models-pr" {
		t.Fatalf("models rule = %+v %v", rule, ok)
	}
	if rule, ok := For(rules, "/srv/site"); !ok || rule.Auto {
		t.Fatalf("site rule (trailing slash cleaned, not auto) = %+v %v", rule, ok)
	}
	if _, ok := For(rules, "/srv/other"); ok {
		t.Fatal("a folder without a rule has none")
	}
	if rules, err := Load(filepath.Join(dir, "missing")); err != nil || rules != nil {
		t.Fatal("a missing file is no rules")
	}
	writeFile(t, config, "/srv/models\trelative-command\n", 0o644)
	if _, err := Load(config); err == nil {
		t.Fatal("a relative command is refused")
	}
	writeFile(t, config, "/srv/models\t/usr/local/bin/models-pr\n", 0o666)
	if _, err := Load(config); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("a config others can write is refused: %v", err)
	}
}

func TestRunPassesTheJobAndReadsTheURL(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	dir := t.TempDir()
	out := filepath.Join(dir, "seen")
	command := filepath.Join(dir, "open-pr")
	writeFile(t, command, "#!/bin/sh\nset -e\n{ echo \"$SWARM_PUBLISH_BRANCH $SWARM_PUBLISH_HEAD $SWARM_PUBLISH_BASE_BRANCH $SWARM_PUBLISH_TITLE\"; cat \"$SWARM_PUBLISH_BODY_FILE\"; echo; pwd; } > "+out+"\necho pushing\necho https://github.com/example/models/pull/7\n", 0o755)
	rule := Rule{Folder: dir, Command: command}
	head := strings.Repeat("a", 40)
	res, err := Run(context.Background(), rule, Request{Folder: dir, JobID: "worker/w/r", Branch: "agent/worker-r", Head: head, BaseBranch: "main", Title: "Refresh\nmodels", Body: "added two models"})
	if err != nil || res.URL != "https://github.com/example/models/pull/7" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	seen, _ := os.ReadFile(out)
	if want := "agent/worker-r " + head + " main Refresh models\nadded two models\n" + dir + "\n"; string(seen) != want {
		t.Fatalf("command saw %q, want %q", seen, want)
	}

	writeFile(t, command, "#!/bin/sh\necho done\n", 0o755)
	if _, err := Run(context.Background(), rule, Request{Branch: "agent/x", Head: head}); err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("no URL is an error: %v", err)
	}
	writeFile(t, command, "#!/bin/sh\necho 'not allowed' >&2\nexit 3\n", 0o755)
	if _, err := Run(context.Background(), rule, Request{Branch: "agent/x", Head: head}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a failing command reports its reason: %v", err)
	}
	if _, err := Run(context.Background(), rule, Request{Branch: "agent/x", Head: "HEAD"}); err == nil {
		t.Fatal("a head that is not a commit id is refused")
	}
	writeFile(t, command, "#!/bin/sh\necho https://github.com/x/y/pull/1\n", 0o775)
	if _, err := Run(context.Background(), rule, Request{Branch: "agent/x", Head: head}); err == nil {
		t.Fatal("a command others can change is refused")
	}
}
