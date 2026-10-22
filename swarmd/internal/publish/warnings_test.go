package publish

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: LoadWithWarnings must preserve ignored-policy diagnostics without
// changing executable Rule values, and logs must name the migration. The parser
// owns this boundary; real files are the narrowest test, including rejection of
// unknown options without returning partial rules or warnings.
func TestLoadWithWarnings(t *testing.T) {
	t.Cleanup(AllowOwnerForTests(os.Getuid()))
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	path := filepath.Join(t.TempDir(), "publish")
	writeFile(t, path, "/srv/a\t/owner/command\tauto,sync\n/srv/b\t/owner/command\tsync\n", 0600)
	rules, warnings, err := LoadWithWarnings(path)
	if err != nil || len(rules) != 2 || len(warnings) != 1 {
		t.Fatalf("load: %v %v %v", rules, warnings, err)
	}
	if !rules[0].Sync || warnings[0].Folder != "/srv/a" || len(warnings[0].Options) != 1 || warnings[0].Options[0] != "auto" {
		t.Fatalf("policy: %v %v", rules, warnings)
	}
	for _, text := range []string{warnings[0].Message, logs.String()} {
		for _, want := range []string{"/srv/a", "auto", "swarm_set_workflow", "deliver to: publish", "folder or worker"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "/owner/command") {
			t.Fatalf("command exposed: %s", text)
		}
	}
	writeFile(t, path, "/srv/a\t/owner/command\tauto\n/srv/b\t/owner/command\ttypo\n", 0600)
	if rules, warnings, err := LoadWithWarnings(path); err == nil || len(rules) != 0 || len(warnings) != 0 {
		t.Fatalf("partial invalid config: %v %v %v", rules, warnings, err)
	}
}
