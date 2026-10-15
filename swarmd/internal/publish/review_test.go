package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Load is the root-owned publish policy boundary. Optional review
// gating must round-trip and default off; require-review without land must
// fail closed rather than imply PR review enforcement. Pipeline additionally
// requires all three gates; test the parser, not a duplicate policy model.
func TestLoadRequireReview(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	path := filepath.Join(t.TempDir(), "publish")
	for _, tc := range []struct {
		options       string
		want, invalid bool
	}{
		{"land,require-review,auto,pipeline", true, false}, {"pipeline", false, true}, {"land,auto,pipeline", false, true}, {"land,require-review,pipeline", false, true},
		{"land,require-review,auto", true, false}, {"land", false, false}, {"auto", false, false}, {"require-review", false, true},
	} {
		writeFile(t, path, "/srv/project\t/usr/local/bin/publish\t"+tc.options+"\n", 0600)
		rules, err := Load(path)
		if tc.invalid {
			if err == nil {
				t.Fatal("invalid policy accepted")
			}
			continue
		}
		if err != nil || len(rules) != 1 || rules[0].RequireReview != tc.want || rules[0].Pipeline != strings.Contains(tc.options, "pipeline") {
			t.Fatalf("%s: %+v %v", tc.options, rules, err)
		}
	}
}
