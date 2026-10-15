package publish

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: Load is the owner policy boundary; proof is opt-in and invalid
// non-land combinations must fail closed. Parsing the actual rule file is the
// narrowest layer proving the installer option reaches policy.
func TestLoadRequireProof(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	path := filepath.Join(t.TempDir(), "publish")
	for _, tc := range []struct {
		options       string
		want, invalid bool
	}{
		{"land,require-proof", true, false}, {"land", false, false}, {"require-proof", false, true},
	} {
		writeFile(t, path, "/srv/project\t/usr/local/bin/publish\t"+tc.options+"\n", 0600)
		rules, err := Load(path)
		if tc.invalid {
			if err == nil || len(rules) != 0 {
				t.Fatal("invalid policy accepted")
			}
			continue
		}
		if err != nil || len(rules) != 1 || rules[0].RequireProof != tc.want {
			t.Fatalf("%s: %+v %v", tc.options, rules, err)
		}
	}
}
